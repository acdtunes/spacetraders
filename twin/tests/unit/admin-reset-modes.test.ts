import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of POST /_twin/reset dispatching on `mode` (cold / income-entry /
// gate-entry) and yielding the right /_twin/state superset. Runs under vitest.unit.config (no live
// stack). Mirrors the harness helpers: coldStart()/incomeEntry()/gateEntry() bodies are POSTed to
// /_twin/reset, then GET /_twin/state is asserted field-for-field.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const DEFAULT_HUBS = ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4', 'X1-PZ28-H5'];
const REAL_JUMP_GATE = 'X1-PZ28-I67'; // the ONLY JUMP_GATE in fixtures/era2-X1-PZ28/waypoints.json

/** A fully registered world (TWINAGENT + the 2 starting hulls) built from the committed fixture. */
function seededWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: 'jwt-preserve-me' });
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

async function reset(body?: Record<string, unknown>): Promise<void> {
  const res = await app.inject({ method: 'POST', url: '/_twin/reset', payload: body ?? {} });
  expect(res.statusCode).toBe(200); // 2xx; body itself is ignored by the harness
}
async function getState(): Promise<Record<string, any>> {
  const res = await app.inject({ method: 'GET', url: '/_twin/state' });
  expect(res.statusCode).toBe(200);
  return res.json();
}

describe('POST /_twin/reset — cold / DATA mode (no `mode`)', () => {
  it('defaults: re-materializes agent + frigate/probe, 175k credits, blank control-plane, frozen clock', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({ credits: 175000, probes: 1, frigates: 1 }); // == harness coldStart()
    const s = await getState();

    expect(s.agent.symbol).toBe('TWINAGENT');           // identity preserved across reset
    expect(s.agent.credits).toBe(175000);
    expect(s.ships).toHaveLength(2);
    expect(s.ships.map((x: any) => x.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
    expect(s.coverage).toBe(0);
    expect(s.markets).toEqual([]);
    expect(s.mutationLog).toEqual([]);                  // reset never seeds log entries
    expect(s.clock.mode).toBe('frozen');
    // INCOME/GATE views remain at their cold defaults
    expect(s.haulers).toEqual([]);
    expect(s.frigateContractTagged).toBe(false);
    expect(s.creditsPerHour).toBe(0);
    expect(s.hubs).toEqual([]);
    expect(s.construction).toEqual({ site: '', percent: 0, started: false, adopted: false });
    expect(s.gateWorkers).toEqual([]);
    expect(s.done).toBe(false);
  });

  it('applies the optional fields: credits, coverage, preScoutedMarkets, extra probes', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({
      credits: 500000, probes: 2, frigates: 1,
      probePrice: 30000, coverage: 0.5,
      preScoutedMarkets: ['X1-PZ28-A1', 'X1-PZ28-B6'],
    });
    const s = await getState();

    expect(s.agent.credits).toBe(500000);
    expect(s.coverage).toBe(0.5);
    // preScoutedMarkets -> {scouted:true, fresh:true}
    const byWp = Object.fromEntries(s.markets.map((m: any) => [m.waypoint, m]));
    expect(byWp['X1-PZ28-A1']).toEqual({ waypoint: 'X1-PZ28-A1', scouted: true, fresh: true });
    expect(byWp['X1-PZ28-B6']).toEqual({ waypoint: 'X1-PZ28-B6', scouted: true, fresh: true });
    // probes:2 -> 2 SATELLITE hulls (+ the 1 COMMAND frigate)
    expect(s.ships.filter((x: any) => x.role === 'SATELLITE')).toHaveLength(2);
    expect(s.ships.filter((x: any) => x.role === 'COMMAND')).toHaveLength(1);
  });
});

describe('POST /_twin/reset — income-entry mode', () => {
  it('defaults: 600k credits, hubs H1..H5, frigate tagged, $/hr 0, EMPTY haulers (daemon buys them)', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({
      mode: 'income-entry', credits: 600000, haulerPrice: 300000,
      hubs: DEFAULT_HUBS, frigateContractTagged: true, creditsPerHour: 0,
    });
    const s = await getState();

    expect(s.agent.symbol).toBe('TWINAGENT');            // identity preserved
    expect(s.agent.credits).toBe(600000);
    expect(s.hubs).toEqual(DEFAULT_HUBS);
    expect(s.frigateContractTagged).toBe(true);
    expect(s.batchContractRunning).toBe(false);
    expect(s.creditsPerHour).toBe(0);
    expect(s.haulers).toEqual([]);                       // haulers are BOUGHT by the daemon, not pre-seeded
    expect(s.mutationLog).toEqual([]);
    expect(s.clock.mode).toBe('frozen');
    // gate view untouched
    expect(s.construction).toEqual({ site: '', percent: 0, started: false, adopted: false });
    expect(s.done).toBe(false);
  });

  it('honors overrides: fewer hubs, higher credits, seeded $/hr', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({ mode: 'income-entry', hubs: ['X1-PZ28-H1'], credits: 400000, creditsPerHour: 12345 });
    const s = await getState();
    expect(s.hubs).toEqual(['X1-PZ28-H1']);
    expect(s.agent.credits).toBe(400000);
    expect(s.creditsPerHour).toBe(12345);
    expect(s.frigateContractTagged).toBe(true);          // default when omitted
  });
});

describe('POST /_twin/reset — gate-entry mode', () => {
  it('defaults: 1.5M credits, $/hr 50k (from incomePerHour), construction at the REAL jump-gate, executor up', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({
      mode: 'gate-entry', credits: 1_500_000, haulers: 4, incomePerHour: 50000,
      gateMaterialChains: 3, constructionPercent: 0, workerPrice: 300000, executorRunning: true,
    }); // gateSite omitted -> defaults to the real JUMP_GATE from the fixture
    const s = await getState();

    expect(s.agent.symbol).toBe('TWINAGENT');
    expect(s.agent.credits).toBe(1_500_000);
    expect(s.creditsPerHour).toBe(50000);                // incomePerHour mapped onto the ONE $/hr var
    expect(s.construction).toEqual({ site: REAL_JUMP_GATE, percent: 0, started: false, adopted: false });
    expect(s.gateWorkers).toEqual([]);                   // filled by repurpose/buy DURING the run
    expect(s.executorRunning).toBe(true);
    expect(s.autosizerRunning).toBe(false);
    expect(s.standingCoordinators).toEqual({ siting: false, workerRebalancer: false });
    expect(s.done).toBe(false);
    expect(s.haulers).toHaveLength(4);                   // 4 idle income haulers available to repurpose
    for (const h of s.haulers) { expect(h.role).toBe('HAULER'); expect(h.parkedHub).toBeNull(); }
    expect(s.mutationLog).toEqual([]);
    expect(s.clock.mode).toBe('frozen');
  });

  it('honors overrides: explicit gateSite echoes, constructionPercent/haulers/executorRunning applied', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({
      mode: 'gate-entry', gateSite: 'X1-PZ28-I57', incomePerHour: 60000,
      constructionPercent: 40, haulers: 2, executorRunning: false, credits: 3_000_000,
    });
    const s = await getState();
    expect(s.construction.site).toBe('X1-PZ28-I57');     // body value echoed verbatim
    expect(s.construction.percent).toBe(40);
    expect(s.creditsPerHour).toBe(60000);
    expect(s.agent.credits).toBe(3_000_000);
    expect(s.haulers).toHaveLength(2);
    expect(s.executorRunning).toBe(false);
  });
});

describe('POST /_twin/reset — invariants across modes', () => {
  it('re-freezes the world clock after a reset (running -> frozen)', async () => {
    app = buildServer({ world: seededWorld() });
    await app.inject({ method: 'POST', url: '/_twin/clock', payload: { mode: 'running' } });
    expect((await getState()).clock.mode).toBe('running');
    await reset({ credits: 175000 });
    expect((await getState()).clock.mode).toBe('frozen');
  });

  it('reset is a clean rebuild: a prior gate seed does not bleed into a later cold reset', async () => {
    app = buildServer({ world: seededWorld() });
    await reset({ mode: 'gate-entry', constructionPercent: 40, haulers: 4 });
    expect((await getState()).construction.percent).toBe(40);
    await reset({ credits: 175000, probes: 1, frigates: 1 }); // cold
    const s = await getState();
    expect(s.construction).toEqual({ site: '', percent: 0, started: false, adopted: false });
    expect(s.gateWorkers).toEqual([]);
    expect(s.haulers).toEqual([]);
    expect(s.done).toBe(false);
    expect(s.creditsPerHour).toBe(0);
  });
});

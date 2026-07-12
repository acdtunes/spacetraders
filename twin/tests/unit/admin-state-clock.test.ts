import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of the reshaped GET /_twin/state (FROZEN superset) and the
// new POST /_twin/clock. Runs under vitest.unit.config (no live stack). The world-clock is a
// module singleton, so each test pins it via resetClock()+setNow() for determinism.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';

/** A fully registered world (TWINAGENT + the 2 starting hulls) built from the committed fixture. */
function seededWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: 'tok-1' });
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

async function getState(): Promise<Record<string, any>> {
  const res = await app.inject({ method: 'GET', url: '/_twin/state' });
  expect(res.statusCode).toBe(200);
  return res.json();
}

describe('GET /_twin/state — FROZEN superset (BASE + INCOME + GATE, one object)', () => {
  it('emits every BASE / INCOME / GATE field with the projected values', async () => {
    const w = seededWorld();
    // BASE control-plane
    w.coverage = 0.5;
    w.marketScouting.set('X1-PZ28-A1', { scouted: true, fresh: false });
    w.marketScouting.set('X1-PZ28-B6', { scouted: true, fresh: true });
    w.mutationLog.push({ seq: 1, call: 'PurchaseShip', detail: { shipType: 'LIGHT_HAULER' }, at: FROZEN_NOW });
    // INCOME view
    w.haulers.push({ symbol: 'TWINAGENT-3', role: 'HAULER', parkedHub: 'X1-PZ28-H1' });
    w.frigateContractTagged = true;
    w.batchContractRunning = true;
    w.creditsPerHour = 50000;
    w.hubs = ['X1-PZ28-H1', 'X1-PZ28-H2'];
    // GATE view
    w.construction = { site: 'X1-PZ28-I57', percent: 25, started: true, adopted: true };
    w.gateWorkers.push({ symbol: 'TWINAGENT-4', source: 'repurposed' });
    w.gateWorkers.push({ symbol: 'TWINAGENT-5', source: 'bought' });
    w.executorRunning = true;
    w.autosizerRunning = true;
    w.standingCoordinators = { siting: true, workerRebalancer: true };
    w.done = false;

    app = buildServer({ world: w });
    const s = await getState();

    // ── BASE ──────────────────────────────────────────────────────────────
    expect(s.agent.credits).toBe(175000);                       // harness reads agent.credits
    expect(s.coverage).toBe(0.5);
    expect(s.clock).toEqual({ now: FROZEN_NOW, mode: 'frozen' });
    // markets projects marketScouting -> array of {waypoint, scouted, fresh}
    expect(Array.isArray(s.markets)).toBe(true);
    const byWp = Object.fromEntries(s.markets.map((m: any) => [m.waypoint, m]));
    expect(byWp['X1-PZ28-A1']).toEqual({ waypoint: 'X1-PZ28-A1', scouted: true, fresh: false });
    expect(byWp['X1-PZ28-B6']).toEqual({ waypoint: 'X1-PZ28-B6', scouted: true, fresh: true });
    expect(s.mutationLog).toEqual([{ seq: 1, call: 'PurchaseShip', detail: { shipType: 'LIGHT_HAULER' }, at: FROZEN_NOW }]);

    // ── ships: base view {symbol, role, nav:{status, waypoint}, scoutAssignment} ──
    expect(Array.isArray(s.ships)).toBe(true);
    expect(s.ships).toHaveLength(2);
    for (const ship of s.ships) {
      expect(typeof ship.symbol).toBe('string');
      expect(typeof ship.role).toBe('string');
      expect(typeof ship.nav.status).toBe('string');
      expect(typeof ship.nav.waypoint).toBe('string');
      expect(ship.scoutAssignment).toBeNull();                  // string|null; no world field yet -> null
    }
    expect(s.ships.map((x: any) => x.role).sort()).toEqual(['COMMAND', 'SATELLITE']);

    // ── INCOME ────────────────────────────────────────────────────────────
    expect(s.haulers).toEqual([{ symbol: 'TWINAGENT-3', role: 'HAULER', parkedHub: 'X1-PZ28-H1' }]);
    expect(s.frigateContractTagged).toBe(true);
    expect(s.batchContractRunning).toBe(true);
    expect(s.creditsPerHour).toBe(50000);
    expect(s.hubs).toEqual(['X1-PZ28-H1', 'X1-PZ28-H2']);

    // ── GATE ──────────────────────────────────────────────────────────────
    expect(s.construction).toEqual({ site: 'X1-PZ28-I57', percent: 25, started: true, adopted: true });
    expect(s.gateWorkers).toEqual([
      { symbol: 'TWINAGENT-4', source: 'repurposed' },
      { symbol: 'TWINAGENT-5', source: 'bought' },
    ]);
    expect(s.executorRunning).toBe(true);
    expect(s.autosizerRunning).toBe(true);
    expect(s.standingCoordinators).toEqual({ siting: true, workerRebalancer: true });
    expect(s.done).toBe(false);
  });

  it('keeps DATA acceptance green: agent is the FULL Agent + ships retain full Ship fields', async () => {
    app = buildServer({ world: seededWorld() });
    const s = await getState();
    // tests/agent.test.ts asserts GET /my/agent .data toEqual(state.agent) — full Agent required
    expect(s.agent).toMatchObject({
      symbol: 'TWINAGENT', credits: 175000, headquarters: 'X1-PZ28-A1', startingFaction: 'COSMIC',
    });
    expect(typeof s.agent.accountId).toBe('string');
    // tests/ships/*.test.ts read registration.role + full nav — ships must stay full Ship objects
    const cmd = s.ships.find((x: any) => x.symbol === 'TWINAGENT-1');
    expect(cmd.registration.role).toBe('COMMAND');
    expect(cmd.nav.systemSymbol).toBe('X1-PZ28');
    expect(cmd.nav.waypointSymbol).toBe('X1-PZ28-A1');   // preserved alongside the new nav.waypoint
    expect(cmd.nav.waypoint).toBe('X1-PZ28-A1');         // harness alias
    expect(cmd.nav.flightMode).toBe('CRUISE');
    expect(cmd.nav.status).toBe('DOCKED');
    expect(Array.isArray(cmd.cargo.inventory)).toBe(true);
    expect(typeof cmd.frame.symbol).toBe('string');
  });

  it('cold-start (no agent) yields agent:null + empty projections, still valid JSON', async () => {
    app = buildServer({ world: loadColdStartWorld() });
    const s = await getState();
    expect(s.agent).toBeNull();
    expect(s.ships).toEqual([]);
    expect(s.markets).toEqual([]);
    expect(s.mutationLog).toEqual([]);
    expect(s.coverage).toBe(0);
    expect(s.clock.mode).toBe('frozen');
  });
});

describe('POST /_twin/clock — the T1 world-clock control', () => {
  it('advanceMs moves now AND flips an in-transit ship to IN_ORBIT at its destination', async () => {
    const w = seededWorld();
    // Put the COMMAND hull in transit A1 -> B6, arriving at FROZEN_NOW + 100s.
    w.transits.set('TWINAGENT-1', {
      shipSymbol: 'TWINAGENT-1',
      originWaypoint: 'X1-PZ28-A1',
      destinationWaypoint: 'X1-PZ28-B6',
      departureTime: FROZEN_NOW,
      arrival: '2026-07-11T00:01:40.000Z', // +100s
    });
    app = buildServer({ world: w });

    // Before advancing: IN_TRANSIT at the ORIGIN.
    const before = await getState();
    const shipBefore = before.ships.find((x: any) => x.symbol === 'TWINAGENT-1');
    expect(shipBefore.nav.status).toBe('IN_TRANSIT');
    expect(shipBefore.nav.waypoint).toBe('X1-PZ28-A1');
    expect(before.clock.now).toBe(FROZEN_NOW);

    // Advance exactly onto the arrival instant.
    const res = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { advanceMs: 100_000 } });
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({ now: '2026-07-11T00:01:40.000Z' });

    // After advancing: now moved + ship flipped to IN_ORBIT at the DESTINATION.
    const after = await getState();
    expect(after.clock.now).toBe('2026-07-11T00:01:40.000Z');
    const shipAfter = after.ships.find((x: any) => x.symbol === 'TWINAGENT-1');
    expect(shipAfter.nav.status).toBe('IN_ORBIT');
    expect(shipAfter.nav.waypoint).toBe('X1-PZ28-B6');
    expect(shipAfter.nav.waypointSymbol).toBe('X1-PZ28-B6');
  });

  it('the harness call {advanceMs:1000} returns the advanced now (rfc3339)', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { advanceMs: 1000 } });
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({ now: '2026-07-11T00:00:01.000Z' });
  });

  it('setNow pins an explicit instant; mode flips frozen<->running (both reflected in {now}/state.clock)', async () => {
    app = buildServer({ world: seededWorld() });
    const pinned = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { setNow: '2026-07-11T05:00:00.000Z' } });
    expect(pinned.statusCode).toBe(200);
    expect(pinned.json()).toEqual({ now: '2026-07-11T05:00:00.000Z' });

    const toRunning = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { mode: 'running' } });
    expect(toRunning.statusCode).toBe(200);
    expect((await getState()).clock.mode).toBe('running');
  });

  it('rejects a bad mode with the error envelope; empty body is a no-op that returns current now', async () => {
    app = buildServer({ world: seededWorld() });
    const bad = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { mode: 'sideways' } });
    expect(bad.statusCode).toBe(400);
    expect(bad.json().error.code).toBe(400);

    const noop = await app.inject({ method: 'POST', url: '/_twin/clock', payload: {} });
    expect(noop.statusCode).toBe(200);
    expect(noop.json()).toEqual({ now: FROZEN_NOW });
  });

  it('the retired POST /_twin/time-compression is gone (404)', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/time-compression', payload: { compression: 250 } });
    expect(res.statusCode).toBe(404);
  });
});

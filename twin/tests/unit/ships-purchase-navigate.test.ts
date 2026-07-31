import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of the TWIN-OBSERVABLE mutation-log wiring: a real /v2 buy
// (POST /my/ships) logs PurchaseShip{detail:{shipType}} and files the hull into the phase-correct
// projection (DATA -> ships[] only; INCOME -> haulers[]; GATE -> gateWorkers[] source:'bought'),
// and a real /v2 navigate logs `navigate` (and parks a hauler at a hub). countCall matches the
// harness helper's exact call-name strings.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  w.agent!.credits = 100_000_000; // ample funds — capital-gating is daemon-side, not the twin's
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

function buy(shipType: string, waypointSymbol?: string) {
  return app.inject({
    method: 'POST', url: '/v2/my/ships', headers: AUTH,
    payload: waypointSymbol ? { shipType, waypointSymbol } : { shipType },
  });
}
function navigate(symbol: string, waypointSymbol: string) {
  return app.inject({ method: 'POST', url: `/v2/my/ships/${symbol}/navigate`, headers: AUTH, payload: { waypointSymbol } });
}
async function state(): Promise<Record<string, any>> {
  const res = await app.inject({ method: 'GET', url: '/_twin/state' });
  expect(res.statusCode).toBe(200);
  return res.json();
}
function countCall(log: Array<{ call: string }>, call: string): number {
  return log.filter((e) => e.call === call).length; // mirrors the harness mutation-log helper
}

describe('POST /v2/my/ships — PurchaseShip mutation-log wiring', () => {
  it('DATA buy: logs PurchaseShip{detail:{shipType}} + files the hull into ships[] only', async () => {
    app = buildServer({ world: baseWorld() }); // cold: hubs [], construction.site '' => DATA
    expect((await buy('SHIP_PROBE')).statusCode).toBe(201);

    const s = await state();
    expect(s.ships).toHaveLength(3); // 2 starting hulls + 1 bought probe
    expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(1);
    expect(s.mutationLog.find((e: any) => e.call === 'PurchaseShip'))
      .toMatchObject({ call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: FROZEN_NOW });
    expect(s.haulers).toEqual([]);
    expect(s.gateWorkers).toEqual([]);

    expect((await buy('SHIP_PROBE')).statusCode).toBe(201);
    const s2 = await state();
    expect(countCall(s2.mutationLog, 'PurchaseShip')).toBe(2); // countCall == exact buy count
    expect(s2.ships).toHaveLength(4);
  });

  it('INCOME buy: each PurchaseShip appends a hauler (parkedHub null); countCall == buys', async () => {
    const w = baseWorld();
    w.hubs = ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3']; // INCOME phase discriminator
    app = buildServer({ world: w });

    for (let i = 0; i < 3; i++) expect((await buy('LIGHT_HAULER')).statusCode).toBe(201);
    const s = await state();
    expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(3);
    expect(s.haulers).toHaveLength(3);
    for (const h of s.haulers) { expect(h.parkedHub).toBeNull(); expect(typeof h.symbol).toBe('string'); }
    expect(s.gateWorkers).toEqual([]);
  });

  it('GATE buy: each PurchaseShip appends a bought gateWorker; bought == countCall, repurposed excluded', async () => {
    const w = baseWorld();
    w.construction = { site: 'X1-PZ28-I67', percent: 0, started: false, adopted: false }; // GATE discriminator
    w.gateWorkers = [{ symbol: 'TWINAGENT-repurp', source: 'repurposed' }];                // state-only repurpose (no log)
    app = buildServer({ world: w });

    for (let i = 0; i < 2; i++) expect((await buy('LIGHT_HAULER')).statusCode).toBe(201);
    const s = await state();
    const repurposed = s.gateWorkers.filter((x: any) => x.source === 'repurposed').length;
    const bought = s.gateWorkers.filter((x: any) => x.source === 'bought').length;
    expect(repurposed).toBe(1);
    expect(bought).toBe(2);
    expect(bought).toBe(s.gateWorkers.length - repurposed);        // harness gate-worker-sizing invariant
    expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(bought); // repurpose emits NO PurchaseShip
    expect(s.haulers).toEqual([]);
  });

  it('rejects an unauthenticated buy (401) and a missing shipType (400)', async () => {
    app = buildServer({ world: baseWorld() });
    expect((await app.inject({ method: 'POST', url: '/v2/my/ships', payload: { shipType: 'SHIP_PROBE' } })).statusCode).toBe(401);
    expect((await app.inject({ method: 'POST', url: '/v2/my/ships', headers: AUTH, payload: {} })).statusCode).toBe(400);
  });
});

describe('POST /v2/my/ships/:symbol/navigate — navigate mutation-log wiring', () => {
  it('logs a `navigate` entry (no detail) for a real /v2 navigate', async () => {
    app = buildServer({ world: baseWorld() });
    expect((await navigate('TWINAGENT-1', 'X1-PZ28-B6')).statusCode).toBe(200);
    const entry = (await state()).mutationLog.find((e: any) => e.call === 'navigate');
    expect(entry.call).toBe('navigate');
    expect('detail' in entry).toBe(false);
  });

  it('parks a hauler at a hub it navigates to; a non-hub navigate leaves it unparked', async () => {
    const w = baseWorld();
    w.hubs = ['X1-PZ28-H1', 'X1-PZ28-H2'];
    app = buildServer({ world: w });
    await buy('LIGHT_HAULER');
    const sym = (await state()).haulers[0].symbol;

    await navigate(sym, 'X1-PZ28-B6');                       // non-hub -> stays unparked
    expect((await state()).haulers[0].parkedHub).toBeNull();

    await navigate(sym, 'X1-PZ28-H1');                       // hub -> parked there
    const parked = await state();
    expect(parked.haulers[0].parkedHub).toBe('X1-PZ28-H1');
    expect(countCall(parked.mutationLog, 'navigate')).toBe(2);
  });

  it('404s navigating an unknown ship', async () => {
    app = buildServer({ world: baseWorld() });
    expect((await navigate('NOPE-9', 'X1-PZ28-B6')).statusCode).toBe(404);
  });
});

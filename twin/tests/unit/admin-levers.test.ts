import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';
import { resetFaults } from '../../src/world/faults';

// Hermetic Fastify-inject proof of the 5 scalar admin levers: POST /_twin/agent,
// /_twin/markets/coverage, /_twin/income, /_twin/construction, /_twin/fault. Runs under
// vitest.unit.config (no live stack, no daemon). The world-clock AND the fault queue are
// module-level singletons (mirrors admin-state-clock.test.ts's clock handling), so each test
// pins/clears both via beforeEach/afterEach for determinism.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';

function seededWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: 'tok-1' });
  return w;
}

let app: FastifyInstance;
beforeEach(() => {
  resetClock();
  setNow(FROZEN_NOW);
  setClockMode('frozen');
  resetFaults();
});
afterEach(async () => {
  if (app) await app.close();
  resetFaults();
});

async function state(): Promise<Record<string, any>> {
  const res = await app.inject({ method: 'GET', url: '/_twin/state' });
  expect(res.statusCode).toBe(200);
  return res.json();
}

describe('POST /_twin/agent — overwrite the treasury', () => {
  it('overwrites agent.credits (void 2xx)', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/agent', payload: { credits: 999_000 } });
    expect(res.statusCode).toBeGreaterThanOrEqual(200);
    expect(res.statusCode).toBeLessThan(300);
    expect((await state()).agent.credits).toBe(999_000);
  });

  it('rejects a non-numeric credits with the 400 error envelope', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/agent', payload: { credits: 'lots' } });
    expect(res.statusCode).toBe(400);
    expect(res.json().error.code).toBe(400);
  });
});

describe('POST /_twin/markets/coverage — set coverage / mark waypoints scouted+fresh', () => {
  it('fraction sets world.coverage and echoes {coverage} JSON', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/markets/coverage', payload: { fraction: 0.75 } });
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({ coverage: 0.75 });
    expect((await state()).coverage).toBe(0.75);
  });

  it('scoutWaypoints marks each waypoint scouted+fresh without touching coverage', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({
      method: 'POST',
      url: '/_twin/markets/coverage',
      payload: { scoutWaypoints: ['X1-PZ28-A1', 'X1-PZ28-B6'] },
    });
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({ coverage: 0 }); // unchanged — no fraction sent
    const s = await state();
    const byWp = Object.fromEntries(s.markets.map((m: any) => [m.waypoint, m]));
    expect(byWp['X1-PZ28-A1']).toEqual({ waypoint: 'X1-PZ28-A1', scouted: true, fresh: true });
    expect(byWp['X1-PZ28-B6']).toEqual({ waypoint: 'X1-PZ28-B6', scouted: true, fresh: true });
  });

  it('applies both fields together', async () => {
    app = buildServer({ world: seededWorld() });
    await app.inject({
      method: 'POST',
      url: '/_twin/markets/coverage',
      payload: { fraction: 0.4, scoutWaypoints: ['X1-PZ28-A1'] },
    });
    const s = await state();
    expect(s.coverage).toBe(0.4);
    expect(s.markets.find((m: any) => m.waypoint === 'X1-PZ28-A1')).toEqual({
      waypoint: 'X1-PZ28-A1', scouted: true, fresh: true,
    });
  });

  it('rejects a non-array scoutWaypoints with the 400 error envelope', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/markets/coverage', payload: { scoutWaypoints: 'nope' } });
    expect(res.statusCode).toBe(400);
  });
});

describe('POST /_twin/income — set the ONE $/hr var', () => {
  it('sets creditsPerHour (void 2xx)', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/income', payload: { creditsPerHour: 42_000 } });
    expect(res.statusCode).toBeGreaterThanOrEqual(200);
    expect(res.statusCode).toBeLessThan(300);
    expect((await state()).creditsPerHour).toBe(42_000);
  });

  it('rejects a missing/non-numeric creditsPerHour', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/income', payload: {} });
    expect(res.statusCode).toBe(400);
  });
});

describe('POST /_twin/construction — set construction.percent (never auto-advances)', () => {
  it('sets percent, leaves started/adopted untouched, and does not drift on a later read', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/construction', payload: { percent: 40 } });
    expect(res.statusCode).toBeGreaterThanOrEqual(200);
    expect(res.statusCode).toBeLessThan(300);
    const s1 = await state();
    expect(s1.construction).toEqual({ site: '', percent: 40, started: false, adopted: false });
    const s2 = await state(); // plain read later — must not have moved on its own
    expect(s2.construction.percent).toBe(40);
  });

  it('allows ->100', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/construction', payload: { percent: 100 } });
    expect(res.statusCode).toBeLessThan(300);
    expect((await state()).construction.percent).toBe(100);
  });

  it('rejects a non-numeric percent with the 400 error envelope', async () => {
    app = buildServer({ world: seededWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/construction', payload: { percent: 'done' } });
    expect(res.statusCode).toBe(400);
  });
});

describe('POST /_twin/fault — arm N matching /v2 requests to fail, then self-clear', () => {
  it('fires exactly `count` times on the matching endpoint, then the (count+1)th passes through', async () => {
    app = buildServer({ world: seededWorld() });
    const arm = await app.inject({
      method: 'POST',
      url: '/_twin/fault',
      payload: { endpoint: 'GET /my/ships', code: 500, count: 2 },
    });
    expect(arm.statusCode).toBeGreaterThanOrEqual(200);
    expect(arm.statusCode).toBeLessThan(300);

    const auth = { authorization: 'Bearer tok-1' };
    const first = await app.inject({ method: 'GET', url: '/v2/my/ships', headers: auth });
    expect(first.statusCode).toBe(500);
    expect(first.json().error).toBeDefined();

    const second = await app.inject({ method: 'GET', url: '/v2/my/ships', headers: auth });
    expect(second.statusCode).toBe(500);

    const third = await app.inject({ method: 'GET', url: '/v2/my/ships', headers: auth });
    expect(third.statusCode).toBe(200); // self-cleared after 2
    expect(Array.isArray(third.json().data)).toBe(true);
  });

  it('only faults the armed method+path — a different endpoint is unaffected', async () => {
    app = buildServer({ world: seededWorld() });
    await app.inject({
      method: 'POST',
      url: '/_twin/fault',
      payload: { endpoint: 'GET /my/ships', code: 503, count: 1 },
    });
    const auth = { authorization: 'Bearer tok-1' };
    const agentRes = await app.inject({ method: 'GET', url: '/v2/my/agent', headers: auth });
    expect(agentRes.statusCode).toBe(200); // untouched — different endpoint
    const shipsRes = await app.inject({ method: 'GET', url: '/v2/my/ships', headers: auth });
    expect(shipsRes.statusCode).toBe(503); // still armed
  });

  it('/_twin itself is never faulted — the preHandler is scoped to /v2 only', async () => {
    app = buildServer({ world: seededWorld() });
    await app.inject({
      method: 'POST',
      url: '/_twin/fault',
      payload: { endpoint: 'GET /state', code: 500, count: 5 }, // nonsensical outside /v2, proves scope
    });
    const res = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(res.statusCode).toBe(200);
  });

  it('rejects a malformed endpoint / bad count with the 400 error envelope', async () => {
    app = buildServer({ world: seededWorld() });
    const badEndpoint = await app.inject({
      method: 'POST', url: '/_twin/fault', payload: { endpoint: 'GARBAGE', code: 500, count: 1 },
    });
    expect(badEndpoint.statusCode).toBe(400);
    const badCount = await app.inject({
      method: 'POST', url: '/_twin/fault', payload: { endpoint: 'GET /my/ships', code: 500, count: 0 },
    });
    expect(badCount.statusCode).toBe(400);
  });
});

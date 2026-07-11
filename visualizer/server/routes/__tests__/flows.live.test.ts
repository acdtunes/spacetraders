import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));

async function makeApp() {
  const { default: flowsRouter } = await import('../flows.js');
  const app = express();
  app.use('/api/flows', flowsRouter);
  return app;
}

const daemonFlow = {
  containerId: 'tour-run-S1-abc',
  program: 'tour',
  ship: 'S1',
  tourId: 'tour-run-S1-abc',
  currentLeg: { from: 'X1-A-1', to: 'X1-A-2', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:05:00Z' },
  cargo: [{ good: 'FABRICS', units: 120 }],
  remainingHops: [{ waypoint: 'X1-A-2', tranches: [{ good: 'FABRICS', isBuy: false, units: 120, expectedUnitPrice: 1600 }] }],
  projected: { profit: 5000, ratePerHour: 9000 },
  plannedAt: '2026-07-11T00:00:00Z',
};

beforeEach(() => {
  connect.mockReset();
  vi.resetModules();
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('GET /api/flows/live', () => {
  it('proxies the daemon feed and enriches each flow with PG ship nav', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ flows: [daemonFlow], generatedAt: '2026-07-11T00:01:00Z' }),
    }));
    const query = vi.fn().mockResolvedValue({ rows: [
      { ship_symbol: 'S1', nav_status: 'IN_TRANSIT', system_symbol: 'X1-A', location_symbol: 'X1-A-1', location_x: 10, location_y: -4, arrival_time: '2026-07-11T00:05:00Z' },
    ] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    expect(res.status).toBe(200);
    expect(res.body.feedLost).toBe(false);
    expect(res.body.flows).toHaveLength(1);
    expect(res.body.flows[0].shipNav).toMatchObject({ status: 'IN_TRANSIT', systemSymbol: 'X1-A', x: 10, y: -4 });
    expect(res.body.lastPlanAt).toBe('2026-07-11T00:00:00Z');
  });

  it('reports feedLost with empty flows when the daemon is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('ECONNREFUSED')));
    // PG is healthy but there are no flows to join.
    connect.mockResolvedValue({ query: vi.fn().mockResolvedValue({ rows: [] }), release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    expect(res.status).toBe(200);
    expect(res.body).toMatchObject({ flows: [], feedLost: true, lastPlanAt: null });
  });

  it('degrades to 503 when the daemon is up but PG is down', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ flows: [daemonFlow], generatedAt: '2026-07-11T00:01:00Z' }),
    }));
    connect.mockRejectedValue(new Error('ECONNREFUSED'));

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');

    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });

  it('treats a non-200 daemon response as feed loss', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 502, json: async () => ({}) }));
    connect.mockResolvedValue({ query: vi.fn().mockResolvedValue({ rows: [] }), release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/live');
    expect(res.status).toBe(200);
    expect(res.body.feedLost).toBe(true);
  });
});

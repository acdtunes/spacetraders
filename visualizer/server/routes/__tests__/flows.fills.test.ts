import { describe, it, expect, vi, beforeEach } from 'vitest';
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

beforeEach(() => {
  connect.mockReset();
  vi.resetModules();
});

describe('GET /api/flows/fills', () => {
  it('merges telemetry + arb fills newest-first, capped by LIMIT $1 (default 30)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [
        { id: 11, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: true, realized_units: 40, realized_unit_price: 30, waypoint: 'X1-AA-P1', realized_at: '2026-07-17T12:00:00Z' },
        { id: 12, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: false, realized_units: 40, realized_unit_price: 55, waypoint: 'X1-BB-Q2', realized_at: '2026-07-17T12:10:00Z' },
      ] })                                   // telemetry query
      .mockResolvedValueOnce({ rows: [
        { id: 7, ship_symbol: 'SHIP-2', good_symbol: 'FUEL', units_sold: 20, actual_net_profit: 900, sell_market: 'X1-CC-R3', executed_at: '2026-07-17T12:05:00Z' },
      ] });                                  // arb query
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/fills');
    expect(res.status).toBe(200);
    expect(res.body.fills.map((f: any) => f.id)).toEqual(['t-12', 'a-7', 't-11']);
    expect(res.body.fills[0]).toMatchObject({ ship: 'SHIP-1', good: 'IRON', isBuy: false, credits: 2200 });
    expect(typeof res.body.generatedAt).toBe('string');
    // both queries are LIMIT $1 with the default limit
    expect(query.mock.calls[0][0]).toMatch(/LIMIT \$1/);
    expect(query.mock.calls[0][1]).toEqual([30]);
    expect(query.mock.calls[1][1]).toEqual([30]);
  });

  it('caps the limit to 100 and floors it to 1', async () => {
    const query = vi.fn().mockResolvedValue({ rows: [] });
    connect.mockResolvedValue({ query, release: vi.fn() });
    const app = await makeApp();

    await request(app).get('/api/flows/fills?limit=500');
    expect(query.mock.calls[0][1]).toEqual([100]);

    query.mockClear();
    await request(app).get('/api/flows/fills?limit=0');
    expect(query.mock.calls[0][1]).toEqual([30]); // 0 is falsy -> default 30
  });

  it('degrades to 503 when the DB is down', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/fills');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

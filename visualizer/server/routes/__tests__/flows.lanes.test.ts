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

describe('GET /api/flows/lanes', () => {
  it('defaults window to 6h and returns aggregated lanes', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [
        { tour_id: 't1', ship_symbol: 'S1', leg_index: 0, waypoint: 'X1-A-1', is_buy: true, realized_units: 100, realized_unit_price: 50, realized_at: new Date().toISOString() },
        { tour_id: 't1', ship_symbol: 'S1', leg_index: 1, waypoint: 'X1-A-2', is_buy: false, realized_units: 100, realized_unit_price: 80, realized_at: new Date().toISOString() },
      ] })                       // telemetry query
      .mockResolvedValueOnce({ rows: [] }); // arb query
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/lanes');
    expect(res.status).toBe(200);
    expect(res.body.window).toBe('6h');
    expect(res.body.lanes[0]).toMatchObject({ from: 'X1-A-1', to: 'X1-A-2', realizedProfit: 8000 });
  });

  it('rejects an invalid window with 400', async () => {
    const app = await makeApp();
    const res = await request(app).get('/api/flows/lanes?window=99h');
    expect(res.status).toBe(400);
  });

  it('degrades to 503 when the DB is down', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/lanes?window=1h');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

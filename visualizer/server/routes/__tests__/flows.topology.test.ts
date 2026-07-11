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
  app.use(express.json());
  app.use('/api/flows', flowsRouter);
  return app;
}

beforeEach(() => {
  connect.mockReset();
  vi.resetModules();
});

describe('GET /api/flows/topology', () => {
  it('returns systems (with coordinates) and only real edges', async () => {
    const query = vi.fn().mockResolvedValue({
      rows: [
        { system_symbol: 'X1-NK36', connected_system: 'X1-KA42', gate_waypoint: 'X1-NK36-I52', under_construction: false },
        { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-KA42-I52', under_construction: true },
      ],
    });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const symbols = res.body.systems.map((s: any) => s.symbol).sort();
    expect(symbols).toEqual(['X1-KA42', 'X1-NK36', 'X1-ZC66']);
    for (const s of res.body.systems) {
      expect(Number.isFinite(s.x)).toBe(true);
      expect(Number.isFinite(s.y)).toBe(true);
    }
    expect(res.body.edges).toHaveLength(2);
    // The SQL must exclude backoff markers (connected_system = '').
    const sql = query.mock.calls[0][0] as string;
    expect(sql).toMatch(/connected_system\s*<>\s*''/);
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

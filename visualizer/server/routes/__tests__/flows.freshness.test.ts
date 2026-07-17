import { describe, it, expect, vi, beforeEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));

// One mocked SpaceTradersClient serves BOTH concerns: /systems/{sym} coord
// fetches (lazy fill) and /my/agent (home system).
const stGet = vi.fn();
vi.mock('../../src/client.js', () => ({
  SpaceTradersClient: class {
    get(path: string) { return stGet(path); }
  },
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
  stGet.mockReset();
  vi.resetModules();
});

describe('GET /api/flows/freshness', () => {
  it('aggregates era-scoped market freshness and merges scout posts', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 3 }] }) // eras
      .mockResolvedValueOnce({ rows: [ // grouped market aggregation
        { system: 'X1-AA', total: '60', fresh: '41', freshest_at: '2026-07-17T12:03:11Z' },
      ] })
      .mockResolvedValueOnce({ rows: [ // scout_posts
        { system_symbol: 'X1-AA', assigned_hull: 'TORWIND-9', reposition_container_id: null, kind: 'standing' },
      ] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');

    expect(res.status).toBe(200);
    expect(res.body.staleAfterMinutes).toBe(75);
    expect(res.body.systems[0]).toMatchObject({ system: 'X1-AA', freshnessPct: 68, scoutPost: { status: 'manned' } });
    const marketSql = query.mock.calls[1][0] as string;
    expect(marketSql).toMatch(/JOIN waypoints/i);
    expect(marketSql).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(marketSql).toMatch(/GROUP BY/i);
    // cutoff param is a Date/ISO ~75min before now
    const cutoff = new Date(query.mock.calls[1][1][1]).getTime();
    expect(Date.now() - cutoff).toBeGreaterThan(74 * 60 * 1000);
    expect(Date.now() - cutoff).toBeLessThan(76 * 60 * 1000);
  });

  it('era-scopes the scout_posts read so dead-era posts are never resurrected', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 7 }] }) // eras
      .mockResolvedValueOnce({ rows: [] }) // market aggregation
      .mockResolvedValueOnce({ rows: [] }); // scout_posts (era-scoped => dead-era row excluded)
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');

    expect(res.status).toBe(200);
    const scoutSql = query.mock.calls[2][0] as string;
    expect(scoutSql).toMatch(/FROM scout_posts/i);
    expect(scoutSql).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(query.mock.calls[2][1]).toEqual([7]);
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

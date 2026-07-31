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

// Query order: [0] eras, [1] rotation probe (p95 + charted count), [2] grouped
// market aggregation, [3] scout_posts.
const ROTATION = 1, MARKETS = 2, SCOUTS = 3;

describe('GET /api/flows/freshness', () => {
  it('aggregates era-scoped market freshness and merges scout posts', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 3 }] })
      .mockResolvedValueOnce({ rows: [{ p95_minutes: '402.4', markets_known: '13525' }] })
      .mockResolvedValueOnce({ rows: [
        { system: 'X1-AA', total: '60', fresh: '41', freshest_at: '2026-07-17T12:03:11Z' },
      ] })
      .mockResolvedValueOnce({ rows: [
        { system_symbol: 'X1-AA', assigned_hull: 'TORWIND-9', reposition_container_id: null, kind: 'standing' },
      ] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');

    expect(res.status).toBe(200);
    expect(res.body.systems[0]).toMatchObject({ system: 'X1-AA', freshnessPct: 68, scoutPost: { status: 'manned' } });
    const marketSql = query.mock.calls[MARKETS][0] as string;
    expect(marketSql).toMatch(/JOIN waypoints/i);
    expect(marketSql).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(marketSql).toMatch(/GROUP BY/i);
  });

  // The regression this bead exists for. The cutoff used to be a literal 75 that
  // outlived the gobot constant it claimed to mirror (sp-k4z5b deleted it); the
  // same stale assumption in four daemon consumers cost ~87% of trade throughput.
  // Both the response scale AND the SQL cutoff must now follow the observed p95.
  it('derives the staleness cutoff from the observed rotation, never a constant', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 3 }] })
      .mockResolvedValueOnce({ rows: [{ p95_minutes: '402.4', markets_known: '13525' }] })
      .mockResolvedValueOnce({ rows: [] })
      .mockResolvedValueOnce({ rows: [] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const res = await request(await makeApp()).get('/api/flows/freshness');

    expect(res.body.rotationBoundMinutes).toBe(402);
    expect(res.body.rotationBoundBasis).toBe('observed');
    expect(res.body.staleAfterMinutes).toBe(402);
    expect(res.body.marketsKnown).toBe(13525);
    expect(res.body.staleAfterMinutes).not.toBe(75);

    // The SQL cutoff moves with it — a response that merely REPORTS the derived
    // bound while still counting `fresh` against 75 minutes would look correct
    // and mis-shade every system.
    const cutoff = new Date(query.mock.calls[MARKETS][1][1]).getTime();
    const agoMinutes = (Date.now() - cutoff) / 60_000;
    expect(agoMinutes).toBeGreaterThan(401);
    expect(agoMinutes).toBeLessThan(404);
  });

  it('reports basis "unknown" when there are no market rows to measure', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 3 }] })
      .mockResolvedValueOnce({ rows: [{ p95_minutes: null, markets_known: '0' }] })
      .mockResolvedValueOnce({ rows: [] })
      .mockResolvedValueOnce({ rows: [] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    const res = await request(await makeApp()).get('/api/flows/freshness');
    expect(res.body.rotationBoundBasis).toBe('unknown');
    expect(res.body.marketsKnown).toBe(0);
  });

  it('era-scopes the rotation probe so a dead era cannot widen the bound', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 7 }] })
      .mockResolvedValueOnce({ rows: [{ p95_minutes: '90', markets_known: '10' }] })
      .mockResolvedValueOnce({ rows: [] })
      .mockResolvedValueOnce({ rows: [] });
    connect.mockResolvedValue({ query, release: vi.fn() });

    await request(await makeApp()).get('/api/flows/freshness');
    const sql = query.mock.calls[ROTATION][0] as string;
    expect(sql).toMatch(/percentile_cont\(0\.95\)/);
    expect(sql).toMatch(/w\.era_id = \$1 OR w\.era_id IS NULL/);
    // The charted-market count uses gobot's own filter, so the denominator shown
    // is the denominator the scanner paces against.
    expect(sql).toMatch(/type <> 'FUEL_STATION'/);
    expect(sql).toMatch(/traits LIKE '%MARKETPLACE%'/);
    expect(query.mock.calls[ROTATION][1]).toEqual([7]);
  });

  it('era-scopes the scout_posts read so dead-era posts are never resurrected', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({ rows: [{ era_id: 7 }] })
      .mockResolvedValueOnce({ rows: [{ p95_minutes: '90', markets_known: '10' }] })
      .mockResolvedValueOnce({ rows: [] })
      .mockResolvedValueOnce({ rows: [] }); // scout_posts (era-scoped => dead-era row excluded)
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');

    expect(res.status).toBe(200);
    const scoutSql = query.mock.calls[SCOUTS][0] as string;
    expect(scoutSql).toMatch(/FROM scout_posts/i);
    expect(scoutSql).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(query.mock.calls[SCOUTS][1]).toEqual([7]);
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/freshness');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

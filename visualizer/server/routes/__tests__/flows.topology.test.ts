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

const GATE_ROWS = {
  rows: [
    { system_symbol: 'X1-NK36', connected_system: 'X1-KA42', gate_waypoint: 'X1-KA42-I52', under_construction: false },
    { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-ZC66-I52', under_construction: true },
  ],
};
const ERA_ROW = { rows: [{ era_id: 3 }] };

// Query order under test: eras -> gate_edges -> system_coords -> (inserts) -> players.
describe('GET /api/flows/topology', () => {
  it('serves real snapshot coordinates with layout=real', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)   // eras
      .mockResolvedValueOnce(GATE_ROWS) // gate_edges
      .mockResolvedValueOnce({          // system_coords: all known
        rows: [
          { symbol: 'X1-NK36', x: -100, y: 0 },
          { symbol: 'X1-KA42', x: 250, y: 40 },
          { symbol: 'X1-ZC66', x: 120, y: 380 },
        ],
      })
      .mockResolvedValueOnce({ rows: [] }); // players token (none)
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const nk = res.body.systems.find((s: any) => s.symbol === 'X1-NK36');
    expect(nk).toMatchObject({ x: -100, y: 0, layout: 'real' });
    expect(res.body.systems).toHaveLength(3);
    expect(res.body.edges).toHaveLength(2);
    expect(stGet).not.toHaveBeenCalledWith(expect.stringMatching(/^\/systems\//));
  });

  it('era-scopes the gate_edges query so dead-era rows never enter the topology', async () => {
    // Regression (review finding): gate_edges rows from a dead era persist in
    // PG after a universe reset (gobot only deletes per rescanned system). An
    // unscoped SELECT dragged those ghost systems into systemSet; they can
    // never exist in the current-era system_coords snapshot, so every cache
    // rebuild refetched them from the live API forever. The SELECT must carry
    // gobot's eraScopePredicate: era_id = current OR era_id IS NULL.
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({
        rows: [
          { symbol: 'X1-NK36', x: -100, y: 0 },
          { symbol: 'X1-KA42', x: 250, y: 40 },
          { symbol: 'X1-ZC66', x: 120, y: 380 },
        ],
      })
      .mockResolvedValueOnce({ rows: [] }); // players token

    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const gateCall = query.mock.calls.find((c) => /FROM gate_edges/.test(c[0]));
    expect(gateCall).toBeDefined();
    expect(gateCall![0]).toMatch(/era_id = \$1 OR era_id IS NULL/);
    expect(gateCall![1]).toEqual([3]);
  });

  it('serves an unscoped gate_edges query + force layout when era resolution fails (no 503)', async () => {
    // eras table missing/unreadable is the pre-AutoMigrate transition window:
    // topology must still render (unscoped gates, all-force layout), never 503.
    const query = vi.fn()
      .mockRejectedValueOnce(new Error('relation "eras" does not exist'))
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.systems).toHaveLength(3);
    for (const s of res.body.systems) {
      expect(s.layout).toBe('force');
      expect(Number.isFinite(s.x) && Number.isFinite(s.y)).toBe(true);
    }
    const gateCall = query.mock.calls.find((c) => /FROM gate_edges/.test(c[0]));
    expect(gateCall![0]).not.toMatch(/era_id/);
    // No system_coords read happened — the coord snapshot is era-keyed and
    // meaningless without an era.
    expect(query.mock.calls.some((c) => /FROM system_coords/.test(c[0]))).toBe(false);
  });

  it('lazily fetches a missing system from the live API and upserts it', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: -100, y: 0 },
        { symbol: 'X1-KA42', x: 250, y: 40 },
      ] })
      .mockResolvedValueOnce({ rows: [] })  // INSERT for X1-ZC66
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockImplementation(async (path: string) =>
      path === '/systems/X1-ZC66' ? { data: { symbol: 'X1-ZC66', x: 9, y: -4 } } : { data: {} },
    );

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const zc = res.body.systems.find((s: any) => s.symbol === 'X1-ZC66');
    expect(zc).toMatchObject({ x: 9, y: -4, layout: 'real' });
    const insert = query.mock.calls.find((c) => /INSERT INTO system_coords/.test(c[0]));
    expect(insert![1].slice(0, 4)).toEqual([3, 'X1-ZC66', 9, -4]);
  });

  it('force-places a system the live API cannot supply (still 200, finite coords)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: -100, y: 0 },
        { symbol: 'X1-KA42', x: 250, y: 40 },
      ] })
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockResolvedValue({ data: {} }); // no x/y -> null

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const zc = res.body.systems.find((s: any) => s.symbol === 'X1-ZC66');
    expect(zc.layout).toBe('force');
    expect(Number.isFinite(zc.x) && Number.isFinite(zc.y)).toBe(true);
  });

  it('degrades to an all-force layout when system_coords is unavailable (pre-AutoMigrate deploy order)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockRejectedValueOnce(new Error('relation "system_coords" does not exist'))
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.systems).toHaveLength(3);
    for (const s of res.body.systems) {
      expect(s.layout).toBe('force');
      expect(Number.isFinite(s.x) && Number.isFinite(s.y)).toBe(true);
    }
  });

  it('stamps homeSystem from players.token -> GET /my/agent headquarters', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce({ rows: [GATE_ROWS.rows[0]] })
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-NK36', x: 0, y: 0 },
        { symbol: 'X1-KA42', x: 100, y: 0 },
      ] })
      .mockResolvedValueOnce({ rows: [{ token: 'agent-jwt' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockImplementation(async (path: string) =>
      path === '/my/agent' ? { data: { symbol: 'TORWIND', headquarters: 'X1-KA42-A1' } } : { data: {} },
    );

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.homeSystem).toBe('X1-KA42');
    expect(stGet).toHaveBeenCalledWith('/my/agent');
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

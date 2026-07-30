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

  it('draws nothing (but never 503s) when era resolution fails, and says so in coverage', async () => {
    // eras table missing/unreadable is the pre-AutoMigrate transition window.
    // The coord snapshot is era-keyed, so without an era NOTHING is placeable —
    // and an unplaceable system is now omitted rather than force-placed. The
    // response must still be a 200 whose coverage explains the empty map.
    const query = vi.fn()
      .mockRejectedValueOnce(new Error('relation "eras" does not exist'))
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.systems).toEqual([]);
    expect(res.body.edges).toEqual([]); // every edge lost both endpoints
    expect(res.body.coverage).toEqual({ positioned: 0, known: 3, omittedEdges: 2, eraId: null });
    const gateCall = query.mock.calls.find((c) => /FROM gate_edges/.test(c[0]));
    expect(gateCall![0]).not.toMatch(/era_id/);
    // No system_coords read happened — the snapshot is meaningless without an era.
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

  it('OMITS a system the live API cannot supply, and drops the edge that reached it', async () => {
    // The honest-knowledge filter: X1-ZC66 exists (a gate read named it) but we
    // have no coordinates and the API will not give us any. It used to be
    // force-placed and shipped; now it is left out, and the KA42→ZC66 edge goes
    // with it so no line runs to nowhere.
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
    expect(res.body.systems.map((s: any) => s.symbol)).toEqual(['X1-KA42', 'X1-NK36']);
    expect(res.body.systems.find((s: any) => s.symbol === 'X1-ZC66')).toBeUndefined();
    // Only the edge with BOTH endpoints placeable survives.
    expect(res.body.edges).toHaveLength(1);
    expect(res.body.edges[0]).toMatchObject({ from: 'X1-NK36', to: 'X1-KA42' });
    expect(res.body.coverage).toEqual({ positioned: 2, known: 3, omittedEdges: 1, eraId: 3 });
  });

  it('draws nothing when system_coords is unavailable (pre-AutoMigrate deploy order)', async () => {
    // Same honest degradation as a failed era resolve: no coordinates, no map,
    // 200 + coverage rather than a hairball of simulated positions.
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockRejectedValueOnce(new Error('relation "system_coords" does not exist'))
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.systems).toEqual([]);
    expect(res.body.edges).toEqual([]);
    expect(res.body.coverage).toEqual({ positioned: 0, known: 3, omittedEdges: 2, eraId: 3 });
  });

  it('still resolves over the FULL system set, so the lazy backfill keeps growing coverage', async () => {
    // Directive (d): coverage must improve on its own. resolveSystemCoords is
    // also the bounded backfill, so it has to see every system the gate graph
    // names — narrowing it to the already-known ones would freeze coverage at
    // whatever it happens to be, forever.
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [{ symbol: 'X1-NK36', x: -100, y: 0 }] })
      .mockResolvedValueOnce({ rows: [] }) // INSERT for a freshly fetched system
      .mockResolvedValueOnce({ rows: [] }) // ...and the second one
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });
    stGet.mockImplementation(async (path: string) =>
      path === '/systems/X1-KA42' ? { data: { x: 5, y: 6 } }
      : path === '/systems/X1-ZC66' ? { data: { x: 7, y: 8 } }
      : { data: {} },
    );

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    const coordCall = query.mock.calls.find((c) => /FROM system_coords/.test(c[0]));
    expect(coordCall).toBeDefined();
    // All three symbols offered to the resolver, not just the one already known.
    expect([...(coordCall![1] as any[])[1]].sort()).toEqual(['X1-KA42', 'X1-NK36', 'X1-ZC66']);
    // ...and the two it fetched are on the map this same response.
    expect(res.body.coverage).toMatchObject({ positioned: 3, known: 3, omittedEdges: 0 });
  });

  it('sorts served systems deterministically (payload is cached and diffed)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce(ERA_ROW)
      .mockResolvedValueOnce(GATE_ROWS)
      .mockResolvedValueOnce({ rows: [
        { symbol: 'X1-ZC66', x: 120, y: 380 },
        { symbol: 'X1-NK36', x: -100, y: 0 },
        { symbol: 'X1-KA42', x: 250, y: 40 },
      ] })
      .mockResolvedValueOnce({ rows: [] }); // players token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.body.systems.map((s: any) => s.symbol)).toEqual(['X1-KA42', 'X1-NK36', 'X1-ZC66']);
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

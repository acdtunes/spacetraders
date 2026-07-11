import { describe, it, expect, vi, beforeEach } from 'vitest';
import express from 'express';
import request from 'supertest';

const connect = vi.fn();
vi.mock('pg', () => ({
  default: { Pool: class { on() {} connect() { return connect(); } } },
}));

// Mock the SpaceTraders client so the home-system GET /my/agent is controllable
// (and no real network happens). agentGet resolves the /my/agent envelope.
const agentGet = vi.fn();
vi.mock('../../src/client.js', () => ({
  SpaceTradersClient: class {
    get(path: string) { return agentGet(path); }
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
  agentGet.mockReset();
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

  it('stamps homeSystem from players.token -> GET /my/agent headquarters', async () => {
    // First query: gate_edges. Second query: the players token lookup.
    const query = vi.fn()
      .mockResolvedValueOnce({
        rows: [
          { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-KA42-I52', under_construction: false },
        ],
      })
      .mockResolvedValueOnce({ rows: [{ token: 'agent-jwt' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });
    agentGet.mockResolvedValue({ data: { symbol: 'TORWIND', headquarters: 'X1-KA42-A1' } });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body.homeSystem).toBe('X1-KA42');
    // The token lookup must be scoped to non-empty tokens.
    const tokenSql = query.mock.calls[1][0] as string;
    expect(tokenSql).toMatch(/FROM players/i);
    expect(agentGet).toHaveBeenCalledWith('/my/agent');
  });

  it('omits homeSystem when no player token is available (never guesses)', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({
        rows: [
          { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-KA42-I52', under_construction: false },
        ],
      })
      .mockResolvedValueOnce({ rows: [] }); // no players / no token
    connect.mockResolvedValue({ query, release: vi.fn() });

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body).not.toHaveProperty('homeSystem');
    expect(agentGet).not.toHaveBeenCalled();
  });

  it('omits homeSystem (still 200) when GET /my/agent throws', async () => {
    const query = vi.fn()
      .mockResolvedValueOnce({
        rows: [
          { system_symbol: 'X1-KA42', connected_system: 'X1-ZC66', gate_waypoint: 'X1-KA42-I52', under_construction: false },
        ],
      })
      .mockResolvedValueOnce({ rows: [{ token: 'agent-jwt' }] });
    connect.mockResolvedValue({ query, release: vi.fn() });
    agentGet.mockRejectedValue(new Error('429 rate limited'));

    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');

    expect(res.status).toBe(200);
    expect(res.body).not.toHaveProperty('homeSystem');
    expect(res.body.systems.length).toBeGreaterThan(0);
  });

  it('degrades to 503 db_unavailable when the pool cannot connect', async () => {
    connect.mockRejectedValue(new Error('ECONNREFUSED'));
    const app = await makeApp();
    const res = await request(app).get('/api/flows/topology');
    expect(res.status).toBe(503);
    expect(res.body).toEqual({ error: 'db_unavailable' });
  });
});

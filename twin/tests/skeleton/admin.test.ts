import { afterEach, describe, expect, it, vi } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World, Ship, Waypoint, System, Market, Shipyard } from '../../src/world/types';

vi.mock('../../src/world/loader', () => ({
  loadColdStartWorld: (): World => ({
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  }),
  registerAgent: (world: World, args: { symbol: string; faction: string; token: string }) => {
    const agent = { accountId: `twin-account-${args.symbol}`, symbol: args.symbol, headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: args.faction };
    world.agent = agent; world.agentToken = args.token;
    world.ships = new Map<string, Ship>([[`${args.symbol}-1`, { symbol: `${args.symbol}-1` } as unknown as Ship]]);
    world.shipCounter = 2;
    return { agent, ships: [...world.ships.values()] };
  },
}));

import { buildServer } from '../../src/server';

function registeredWorld(): World {
  const waypoints = new Map<string, Waypoint>([
    ['X1-PZ28-A1', { symbol: 'X1-PZ28-A1' } as unknown as Waypoint],
    ['X1-PZ28-A2', { symbol: 'X1-PZ28-A2' } as unknown as Waypoint],
  ]);
  const systems = new Map<string, System>([['X1-PZ28', { symbol: 'X1-PZ28', waypoints }]]);
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: { accountId: 'acct-TWINAGENT', symbol: 'TWINAGENT', headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: 'COSMIC' },
    agentToken: 'jwt-preserve-me',
    ships: new Map<string, Ship>([['TWINAGENT-1', { symbol: 'TWINAGENT-1' } as unknown as Ship]]),
    systems,
    markets: new Map<string, Market>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', exports: [], imports: [], exchange: [], tradeGoods: [] }]]),
    shipyards: new Map<string, Shipyard>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', shipTypes: [], ships: [], transactions: [], modificationsFee: 0 }]]),
    transits: new Map(), shipCounter: 2,
  };
}

let app: FastifyInstance;
afterEach(async () => { if (app) await app.close(); vi.clearAllMocks(); });

describe('/_twin admin routes', () => {
  it('GET /_twin/state returns the foundation TwinState shape', async () => {
    app = buildServer({ world: registeredWorld() });
    const res = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(res.statusCode).toBe(200);
    const s = res.json();
    expect(s.agent.symbol).toBe('TWINAGENT'); expect(s.agent.credits).toBe(175000);
    expect(Array.isArray(s.ships)).toBe(true); expect(s.ships).toHaveLength(1);
    expect(Array.isArray(s.transits)).toBe(true); expect(s.transits).toHaveLength(0);
    expect(s.markets['X1-PZ28-A1'].symbol).toBe('X1-PZ28-A1');
    expect(s.shipyards['X1-PZ28-A1'].symbol).toBe('X1-PZ28-A1');
    expect(s.waypointCount).toBe(2);
    expect(typeof s.compression).toBe('number'); expect(s.compression).toBeGreaterThan(0);
    expect(s.now).toMatch(/^\d{4}-\d{2}-\d{2}T/);
  });
  it('POST /_twin/time-compression validates >0 and updates the live factor', async () => {
    app = buildServer({ world: registeredWorld() });
    const ok = await app.inject({ method: 'POST', url: '/_twin/time-compression', payload: { compression: 250 } });
    expect(ok.statusCode).toBe(200);
    expect(ok.json()).toEqual({ ok: true, compression: 250 });
    const st = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(st.json().compression).toBe(250);
  });
  it('POST /_twin/time-compression rejects <= 0 with the error envelope', async () => {
    app = buildServer({ world: registeredWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/time-compression', payload: { compression: 0 } });
    expect(res.statusCode).toBe(400);
    expect(res.json().error.code).toBe(400);
  });
  it('POST /_twin/reset rebuilds cold-start but preserves agent symbol + token', async () => {
    const dirty = registeredWorld(); dirty.agent!.credits = 3; dirty.ships = new Map();
    app = buildServer({ world: dirty });
    const res = await app.inject({ method: 'POST', url: '/_twin/reset' });
    expect(res.statusCode).toBe(200);
    const body = res.json();
    expect(body.ok).toBe(true);
    expect(body.world.agent.symbol).toBe('TWINAGENT');
    expect(body.world.shipCount).toBe(1);
    const st = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(st.json().agent.credits).toBe(175000);
  });
});

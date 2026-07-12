import { afterEach, describe, expect, it, vi } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World, Ship, Waypoint, System, Market, Shipyard } from '../../src/world/types';

// A minimal but VALID ship (registration + nav) so the reshaped GET /_twin/state handler —
// which resolveNav's each ship and reads registration.role / nav.waypointSymbol — never crashes.
function stubShip(symbol: string): Ship {
  return {
    symbol, registration: { role: 'COMMAND' },
    nav: { systemSymbol: 'X1-PZ28', waypointSymbol: 'X1-PZ28-A1', status: 'DOCKED', flightMode: 'CRUISE', route: null },
  } as unknown as Ship;
}

vi.mock('../../src/world/loader', () => ({
  loadColdStartWorld: (): World => ({
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
    mutationLog: [], coverage: 0, marketScouting: new Map(), scoutAssigned: false,
    haulers: [], frigateContractTagged: false, batchContractRunning: false, creditsPerHour: 0, hubs: [],
    construction: { site: '', percent: 0, started: false, adopted: false }, gateWorkers: [],
    executorRunning: false, autosizerRunning: false, standingCoordinators: { siting: false, workerRebalancer: false }, done: false,
  }),
  registerAgent: (world: World, args: { symbol: string; faction: string; token: string }) => {
    const agent = { accountId: `twin-account-${args.symbol}`, symbol: args.symbol, headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: args.faction };
    world.agent = agent; world.agentToken = args.token;
    world.ships = new Map<string, Ship>([[`${args.symbol}-1`, stubShip(`${args.symbol}-1`)]]);
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
    ships: new Map<string, Ship>([['TWINAGENT-1', stubShip('TWINAGENT-1')]]),
    systems,
    markets: new Map<string, Market>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', exports: [], imports: [], exchange: [], tradeGoods: [] }]]),
    shipyards: new Map<string, Shipyard>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', shipTypes: [], ships: [], transactions: [], modificationsFee: 0 }]]),
    transits: new Map(), shipCounter: 2,
    mutationLog: [], coverage: 0, marketScouting: new Map(), scoutAssigned: false,
    haulers: [], frigateContractTagged: false, batchContractRunning: false, creditsPerHour: 0, hubs: [],
    construction: { site: '', percent: 0, started: false, adopted: false }, gateWorkers: [],
    executorRunning: false, autosizerRunning: false, standingCoordinators: { siting: false, workerRebalancer: false }, done: false,
  };
}

let app: FastifyInstance;
afterEach(async () => { if (app) await app.close(); vi.clearAllMocks(); });

describe('/_twin admin routes (HTTP smoke over a mock world)', () => {
  it('GET /_twin/state emits the reshaped superset (BASE + INCOME + GATE), one object', async () => {
    app = buildServer({ world: registeredWorld() });
    const res = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(res.statusCode).toBe(200);
    const s = res.json();
    // BASE
    expect(s.agent.symbol).toBe('TWINAGENT'); expect(s.agent.credits).toBe(175000);
    expect(Array.isArray(s.ships)).toBe(true); expect(s.ships).toHaveLength(1);
    expect(s.ships[0].role).toBe('COMMAND'); expect(s.ships[0].nav.waypoint).toBe('X1-PZ28-A1');
    expect(s.ships[0].scoutAssignment).toBeNull();
    expect(Array.isArray(s.markets)).toBe(true);
    expect(typeof s.coverage).toBe('number');
    expect(s.clock.mode).toBe('frozen'); expect(s.clock.now).toMatch(/^\d{4}-\d{2}-\d{2}T/);
    expect(Array.isArray(s.mutationLog)).toBe(true);
    // INCOME
    expect(Array.isArray(s.haulers)).toBe(true);
    expect(s.frigateContractTagged).toBe(false); expect(s.batchContractRunning).toBe(false);
    expect(s.creditsPerHour).toBe(0); expect(Array.isArray(s.hubs)).toBe(true);
    // GATE
    expect(s.construction).toEqual({ site: '', percent: 0, started: false, adopted: false });
    expect(Array.isArray(s.gateWorkers)).toBe(true);
    expect(s.executorRunning).toBe(false); expect(s.autosizerRunning).toBe(false);
    expect(s.standingCoordinators).toEqual({ siting: false, workerRebalancer: false });
    expect(s.done).toBe(false);
    // retired fields gone
    expect(s.compression).toBeUndefined(); expect(s.transits).toBeUndefined();
    expect(s.waypointCount).toBeUndefined(); expect(s.now).toBeUndefined();
  });

  it('POST /_twin/clock advances the world clock and returns {now}', async () => {
    app = buildServer({ world: registeredWorld() });
    const before = (await app.inject({ method: 'GET', url: '/_twin/state' })).json().clock.now;
    const res = await app.inject({ method: 'POST', url: '/_twin/clock', payload: { advanceMs: 5000 } });
    expect(res.statusCode).toBe(200);
    const now = res.json().now;
    expect(new Date(now).getTime()).toBe(new Date(before).getTime() + 5000);
    const st = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(st.json().clock.now).toBe(now);
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

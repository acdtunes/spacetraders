import { afterEach, describe, expect, it, vi } from 'vitest';
import type { World, Ship } from '../../src/world/types';

// Hermetic: stub the loader so this exercises ONLY reset/preserve, not fixture I/O.
vi.mock('../../src/world/loader', () => ({
  loadColdStartWorld: (): World => ({
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  }),
  registerAgent: (world: World, args: { symbol: string; faction: string; token: string }) => {
    const agent = { accountId: `twin-account-${args.symbol}`, symbol: args.symbol, headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: args.faction };
    world.agent = agent;
    world.agentToken = args.token;
    world.ships = new Map<string, Ship>([[`${args.symbol}-1`, { symbol: `${args.symbol}-1` } as unknown as Ship]]);
    world.shipCounter = 2;
    return { agent, ships: [...world.ships.values()] };
  },
}));

import { getWorld, setWorld, resetWorld } from '../../src/world/store';

function coldWorld(): World {
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  };
}
afterEach(() => vi.clearAllMocks());

describe('world store — reset preserves agent identity + token', () => {
  it('getWorld returns the injected world without touching the loader', () => {
    setWorld(coldWorld());
    const w = getWorld();
    expect(w.agent).toBeNull(); expect(w.agentToken).toBeNull(); expect(w.ships.size).toBe(0);
  });
  it('resetWorld rebuilds cold-start but re-materializes the SAME agent + token', () => {
    const dirty = coldWorld();
    dirty.agent = { accountId: 'acct-TWINAGENT', symbol: 'TWINAGENT', headquarters: 'X1-PZ28-A1', credits: 42, startingFaction: 'COSMIC' };
    dirty.agentToken = 'jwt-preserve-me'; dirty.ships = new Map();
    setWorld(dirty);
    resetWorld();
    const w = getWorld();
    expect(w.agent?.symbol).toBe('TWINAGENT');
    expect(w.agent?.startingFaction).toBe('COSMIC');
    expect(w.agentToken).toBe('jwt-preserve-me');
    expect(w.agent?.credits).toBe(175000);
    expect(w.ships.size).toBe(1);
  });
  it('resetWorld on a never-registered world stays cold', () => {
    setWorld(coldWorld()); resetWorld();
    const w = getWorld();
    expect(w.agent).toBeNull(); expect(w.agentToken).toBeNull(); expect(w.ships.size).toBe(0);
  });
});

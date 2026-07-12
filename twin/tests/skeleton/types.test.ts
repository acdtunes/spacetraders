import { describe, expect, it } from 'vitest';
import type { Agent, Ship, Market, Shipyard, World } from '../../src/world/types';

// Compile-only guard: constructs each shape so a field rename in types.ts is a red compile.
describe('world types compile to the Go decode-target shapes', () => {
  it('constructs an Agent / Ship / Market / Shipyard / World', () => {
    const agent: Agent = { accountId: 'a', symbol: 'S', headquarters: 'X1-PZ28-A1', credits: 1, startingFaction: 'COSMIC' };
    const market: Market = { symbol: 'X1-PZ28-A1', exports: [], imports: [], exchange: [], tradeGoods: [] };
    const yard: Shipyard = { symbol: 'X1-PZ28-A2', shipTypes: [], ships: [], transactions: [], modificationsFee: 0 };
    const world: World = {
      serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
      agent, agentToken: null, ships: new Map(), systems: new Map(),
      markets: new Map([[market.symbol, market]]), shipyards: new Map([[yard.symbol, yard]]),
      transits: new Map(), shipCounter: 0,
    };
    const ship: Ship | undefined = world.ships.get('X');
    expect(agent.credits).toBe(1);
    expect(world.markets.size).toBe(1);
    expect(ship).toBeUndefined();
  });
});

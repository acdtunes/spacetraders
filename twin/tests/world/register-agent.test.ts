import { describe, expect, it } from 'vitest';
import { loadColdStartWorld, mintToken, registerAgent } from '../../src/world/loader';

describe('registerAgent — materializes the cold-start agent + starting ships', () => {
  it('mutates the world into cold-start and returns the /register data', () => {
    const world = loadColdStartWorld();
    // pre-seed a ghost transit to prove registerAgent clears in-flight state
    world.transits.set('GHOST', {
      shipSymbol: 'GHOST', originWaypoint: 'X1-PZ28-A1', destinationWaypoint: 'X1-PZ28-B1',
      departureTime: '2026-07-11T00:00:00.000Z', arrival: '2026-07-11T00:10:00.000Z',
    });
    const token = mintToken('TWINAGENT');
    const { agent, ships } = registerAgent(world, { symbol: 'TWINAGENT', faction: 'COSMIC', token });

    expect(agent).toEqual({
      accountId: 'twin-account-TWINAGENT', symbol: 'TWINAGENT',
      headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: 'COSMIC',
    });
    expect(ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(world.agent).toBe(agent);
    expect(world.agentToken).toBe(token);
    expect([...world.ships.keys()].sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(world.ships.get('TWINAGENT-1')?.registration.role).toBe('COMMAND');
    expect(world.ships.get('TWINAGENT-2')?.registration.role).toBe('SATELLITE');
    expect(world.ships.get('TWINAGENT-1')?.nav.status).toBe('DOCKED');
    expect(world.transits.size).toBe(0); // ghost cleared
    expect(world.shipCounter).toBe(3);   // next purchased suffix after -1, -2
  });

  it('defaults faction to the template when the request faction is empty', () => {
    const world = loadColdStartWorld();
    const { agent } = registerAgent(world, { symbol: 'TWINAGENT', faction: '', token: mintToken('TWINAGENT') });
    expect(agent.startingFaction).toBe('COSMIC');
  });
});

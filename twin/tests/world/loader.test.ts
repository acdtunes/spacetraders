import { describe, expect, it } from 'vitest';
import { loadColdStartWorld, loadRegisterTemplate } from '../../src/world/loader';

const HOME_SYSTEM = 'X1-PZ28';

describe('loadColdStartWorld — captured X1-PZ28 snapshot, pre-register', () => {
  it('loads the full 90-waypoint topology with exactly one JUMP_GATE', () => {
    const world = loadColdStartWorld();
    const system = world.systems.get(HOME_SYSTEM);
    expect(system, `system ${HOME_SYSTEM} must be present`).toBeDefined();
    expect(system!.waypoints.size).toBe(90);
    const gates = [...system!.waypoints.values()].filter((w) => w.type === 'JUMP_GATE');
    expect(gates).toHaveLength(1);
  });

  it('exposes non-empty market/shipyard subsets keyed by real waypoint symbols', () => {
    const world = loadColdStartWorld();
    expect(world.markets.size).toBeGreaterThan(0);
    expect(world.shipyards.size).toBeGreaterThan(0);
    const system = world.systems.get(HOME_SYSTEM)!;
    for (const s of world.markets.keys()) expect(system.waypoints.has(s), `market ${s} is a known waypoint`).toBe(true);
    for (const s of world.shipyards.keys()) expect(system.waypoints.has(s), `shipyard ${s} is a known waypoint`).toBe(true);
  });

  it('includes a SHIP_PROBE shipyard listing with numeric engine.speed', () => {
    const world = loadColdStartWorld();
    const probe = [...world.shipyards.values()].flatMap((sy) => sy.ships).find((l) => l.type === 'SHIP_PROBE');
    expect(probe, 'a SHIP_PROBE listing must exist').toBeDefined();
    expect(typeof probe!.engine.speed).toBe('number');
    expect(probe!.engine.speed as number).toBeGreaterThan(0);
  });

  it('returns a PRE-register world (null agent, empty ships/transits, shipCounter 0)', () => {
    const world = loadColdStartWorld();
    expect(world.agent).toBeNull();
    expect(world.agentToken).toBeNull();
    expect(world.ships.size).toBe(0);
    expect(world.transits.size).toBe(0);
    expect(world.shipCounter).toBe(0);
    expect(world.serverStatus.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(typeof world.serverStatus.serverResets.next).toBe('string');
  });
});

describe('loadRegisterTemplate — captured cold-start template', () => {
  it('loads the golden values (2 starting ships with the {AGENT} placeholder)', () => {
    const tpl = loadRegisterTemplate();
    expect(tpl.startingCredits).toBe(175000);
    expect(tpl.headquarters).toBe('X1-PZ28-A1');
    expect(tpl.startingFaction).toBe('COSMIC');
    expect(tpl.ships).toHaveLength(2);
    expect(tpl.ships.map((s) => s.symbol).sort()).toEqual(['{AGENT}-1', '{AGENT}-2']);
    expect(tpl.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });
});

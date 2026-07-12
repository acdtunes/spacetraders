import { afterEach, describe, expect, it } from 'vitest';
import type { Ship, TransitState, Waypoint } from '../../src/world/types';
import {
  distance, fuelCost, getCompression, makeCooldownExpiration, makeTransit,
  parseCompression, realTravelSeconds, resolveNav, setCompression,
} from '../../src/clock';

function wp(symbol: string, x: number, y: number): Waypoint {
  return { symbol, type: 'PLANET', systemSymbol: 'X1-PZ28', x, y, traits: [], orbitals: [], isUnderConstruction: false };
}
function baseShip(): Ship {
  return {
    symbol: 'TWINAGENT-1', registration: { role: 'COMMAND' },
    nav: { systemSymbol: 'X1-PZ28', waypointSymbol: 'X1-PZ28-A1', status: 'IN_ORBIT', flightMode: 'CRUISE', route: null },
    fuel: { current: 400, capacity: 400 }, cargo: { capacity: 40, units: 0, inventory: [] }, cooldown: null,
    engine: { speed: 30 }, frame: { symbol: 'FRAME_FRIGATE', moduleSlots: 8, mountingPoints: 5 },
    reactor: { symbol: 'REACTOR_FISSION_I', name: 'Fission Reactor I', powerOutput: 31, requirements: { power: 0, crew: 8, slots: 1 } },
    crew: { current: 57, required: 57, capacity: 80 }, modules: [], mounts: [],
  };
}
afterEach(() => setCompression(100));

describe('distance', () => {
  it('is Euclidean', () => expect(distance({ x: 0, y: 0 }, { x: 3, y: 4 })).toBe(5));
  it('is 0 for the same point', () => expect(distance({ x: 7, y: -2 }, { x: 7, y: -2 })).toBe(0));
});

describe('realTravelSeconds', () => {
  it('is 0 when distance is 0', () => { expect(realTravelSeconds(0, 30, 'CRUISE')).toBe(0); expect(realTravelSeconds(0, 30, 'BURN')).toBe(0); });
  it('matches routing_engine.py per mode', () => {
    expect(realTravelSeconds(10, 30, 'CRUISE')).toBe(10);
    expect(realTravelSeconds(10, 30, 'DRIFT')).toBe(8);
    expect(realTravelSeconds(10, 30, 'BURN')).toBe(5);
    expect(realTravelSeconds(10, 30, 'STEALTH')).toBe(16);
  });
  it('defaults to CRUISE', () => expect(realTravelSeconds(10, 30)).toBe(10));
  it('floors to a minimum of 1s for any non-zero distance', () => expect(realTravelSeconds(1, 100, 'CRUISE')).toBe(1));
  it('clamps engine speed to a minimum of 1', () => expect(realTravelSeconds(10, 0, 'CRUISE')).toBe(310));
});

describe('fuelCost', () => {
  it('is 0 when distance is 0', () => { expect(fuelCost(0, 'CRUISE')).toBe(0); expect(fuelCost(0, 'BURN')).toBe(0); });
  it('matches routing_engine.py per mode', () => {
    expect(fuelCost(10, 'CRUISE')).toBe(10); expect(fuelCost(10, 'BURN')).toBe(20);
    expect(fuelCost(10, 'STEALTH')).toBe(10); expect(fuelCost(10, 'DRIFT')).toBe(1); expect(fuelCost(1000, 'DRIFT')).toBe(3);
  });
  it('defaults to CRUISE, never < 1 for non-zero', () => { expect(fuelCost(10)).toBe(10); expect(fuelCost(0.1, 'CRUISE')).toBe(1); });
});

describe('compression', () => {
  it('parseCompression: default 100 for unset/empty/invalid, honors positives', () => {
    expect(parseCompression(undefined)).toBe(100); expect(parseCompression('')).toBe(100);
    expect(parseCompression('abc')).toBe(100); expect(parseCompression('0')).toBe(100);
    expect(parseCompression('-5')).toBe(100); expect(parseCompression('50')).toBe(50); expect(parseCompression('2.5')).toBe(2.5);
  });
  it('get/set round-trips', () => { setCompression(5); expect(getCompression()).toBe(5); });
  it('setCompression rejects non-positive / non-finite', () => {
    expect(() => setCompression(0)).toThrow(RangeError);
    expect(() => setCompression(-1)).toThrow(RangeError);
    expect(() => setCompression(Number.NaN)).toThrow(RangeError);
  });
});

describe('makeTransit', () => {
  const now = new Date('2026-07-11T00:00:00.000Z');
  it('mints departure=now and arrival=now+realETA/compression', () => {
    setCompression(100);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300), engineSpeed: 30, mode: 'CRUISE', now });
    expect(t.originWaypoint).toBe('X1-PZ28-A1');
    expect(t.destinationWaypoint).toBe('X1-PZ28-B1');
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    expect(t.arrival).toBe('2026-07-11T00:00:03.100Z');
  });
  it('samples compression at call time', () => {
    const args = { shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300), engineSpeed: 30, mode: 'CRUISE' as const, now };
    setCompression(10); const slow = makeTransit(args);
    setCompression(100); const fast = makeTransit(args);
    expect(slow.arrival).toBe('2026-07-11T00:00:31.000Z');
    expect(fast.arrival).toBe('2026-07-11T00:00:03.100Z');
  });
  it('guards departure <= arrival for a zero-distance hop', () => {
    setCompression(100);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 5, 5), destination: wp('X1-PZ28-A2', 5, 5), engineSpeed: 30, now });
    expect(t.arrival).toBe(t.departureTime);
  });
});

describe('resolveNav', () => {
  function transit(arrival: string): TransitState {
    return { shipSymbol: 'TWINAGENT-1', originWaypoint: 'X1-PZ28-A1', destinationWaypoint: 'X1-PZ28-B1', departureTime: '2026-07-11T00:00:00.000Z', arrival };
  }
  it('returns the ship unchanged with no transit', () => {
    const out = resolveNav(baseShip(), undefined, new Date('2026-07-11T01:00:00.000Z'));
    expect(out.nav.status).toBe('IN_ORBIT'); expect(out.nav.waypointSymbol).toBe('X1-PZ28-A1');
  });
  it('before arrival: IN_TRANSIT at the ORIGIN', () => {
    const out = resolveNav(baseShip(), transit('2026-07-11T00:00:10.000Z'), new Date('2026-07-11T00:00:05.000Z'));
    expect(out.nav.status).toBe('IN_TRANSIT'); expect(out.nav.waypointSymbol).toBe('X1-PZ28-A1');
    expect(out.nav.route).toEqual({ departureTime: '2026-07-11T00:00:00.000Z', arrival: '2026-07-11T00:00:10.000Z' });
  });
  it('at/after arrival: IN_ORBIT at the DESTINATION', () => {
    const at = resolveNav(baseShip(), transit('2026-07-11T00:00:10.000Z'), new Date('2026-07-11T00:00:10.000Z'));
    expect(at.nav.status).toBe('IN_ORBIT'); expect(at.nav.waypointSymbol).toBe('X1-PZ28-B1');
  });
  it('is pure + idempotent post-arrival', () => {
    const ship = baseShip(); const t = transit('2026-07-11T00:00:10.000Z');
    const a = resolveNav(ship, t, new Date('2026-07-11T00:00:20.000Z'));
    const b = resolveNav(ship, t, new Date('2026-07-11T00:00:30.000Z'));
    expect(ship.nav.status).toBe('IN_ORBIT'); expect(a.nav).toEqual(b.nav);
  });
});

describe('makeCooldownExpiration', () => {
  it('is now + realSeconds/compression', () => {
    setCompression(100);
    expect(makeCooldownExpiration(500, new Date('2026-07-11T00:00:00.000Z'))).toBe('2026-07-11T00:00:05.000Z');
  });
});

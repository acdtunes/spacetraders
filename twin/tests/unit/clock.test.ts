import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Ship, TransitState, Waypoint } from '../../src/world/types';
import {
  advanceClock, distance, fuelCost, fuelRequired, getClockState, getCompression, getNow,
  makeCooldownExpiration, makeTransit, parseCompression, realTravelSeconds, resetClock,
  resolveNav, setClockMode, setCompression, setNow,
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

// Reset the world-clock singleton to a clean FROZEN state (with REAL timers) before every
// test so mode/now never bleed between cases.
beforeEach(() => { vi.useRealTimers(); resetClock(); setCompression(100); });
afterEach(() => { vi.useRealTimers(); });

describe('distance', () => {
  it('is Euclidean (raw, un-rounded)', () => expect(distance({ x: 0, y: 0 }, { x: 3, y: 4 })).toBe(5));
  it('is 0 for the same point', () => expect(distance({ x: 7, y: -2 }, { x: 7, y: -2 })).toBe(0));
});

describe('realTravelSeconds (real v2.3.0 formula: round(round(d)*(mult/speed)) + 15)', () => {
  it('always carries the +15 flat term — even at distance 0, every mode', () => {
    expect(realTravelSeconds(0, 30, 'CRUISE')).toBe(15);
    expect(realTravelSeconds(0, 30, 'BURN')).toBe(15);
    expect(realTravelSeconds(0, 30, 'DRIFT')).toBe(15);
    expect(realTravelSeconds(0, 30, 'STEALTH')).toBe(15);
  });
  it('matches the real-API multipliers per mode (d=300, speed=30)', () => {
    expect(realTravelSeconds(300, 30, 'CRUISE')).toBe(265);   // round(300*25/30)+15 = 250+15
    expect(realTravelSeconds(300, 30, 'BURN')).toBe(140);     // round(300*12.5/30)+15 = 125+15
    expect(realTravelSeconds(300, 30, 'DRIFT')).toBe(2515);   // round(300*250/30)+15 = 2500+15
    expect(realTravelSeconds(300, 30, 'STEALTH')).toBe(315);  // round(300*30/30)+15 = 300+15
  });
  it('rounds the distance before scaling', () => {
    // round(9.6)=10 -> round(10*30/30)+15 = 25
    expect(realTravelSeconds(9.6, 30, 'STEALTH')).toBe(25);
  });
  it('defaults to CRUISE', () => expect(realTravelSeconds(60, 30)).toBe(65)); // round(60*25/30)+15 = 50+15
  it('clamps engine speed to a minimum of 1 (no Infinity for a speed-0 hull)', () => {
    expect(realTravelSeconds(10, 0, 'CRUISE')).toBe(265); // round(10*25/1)+15 = 250+15
    expect(Number.isFinite(realTravelSeconds(10, 0, 'CRUISE'))).toBe(true);
  });
});

describe('fuelCost (real-API per-mode fuel)', () => {
  it('CRUISE/STEALTH = round(d), BURN = 2*round(d), DRIFT = 1 flat', () => {
    expect(fuelCost(10, 'CRUISE')).toBe(10);
    expect(fuelCost(10, 'STEALTH')).toBe(10);
    expect(fuelCost(10, 'BURN')).toBe(20);
    expect(fuelCost(10, 'DRIFT')).toBe(1);
    expect(fuelCost(1000, 'DRIFT')).toBe(1); // DRIFT is a flat 1, not proportional
  });
  it('rounds distance and defaults to CRUISE', () => {
    expect(fuelCost(9.4)).toBe(9);
    expect(fuelCost(9.6)).toBe(10);
  });
});

describe('fuelRequired (capacity-aware — probes travel free)', () => {
  it('equals fuelCost for a fuelled hull', () => {
    expect(fuelRequired(300, 'CRUISE', 400)).toBe(300);
    expect(fuelRequired(300, 'BURN', 400)).toBe(600);
  });
  it('is 0 when fuel capacity is 0 (probe/satellite) — navigate always allowed', () => {
    expect(fuelRequired(300, 'CRUISE', 0)).toBe(0);
    expect(fuelRequired(9999, 'BURN', 0)).toBe(0);
  });
});

describe('legacy time-compression knob (decoupled from the world clock)', () => {
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

describe('world clock — frozen (harness default)', () => {
  it('moves now ONLY on advanceClock — wall-clock is ignored while frozen', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('frozen');
    const before = getNow().toISOString();
    vi.advanceTimersByTime(10_000);                                  // 10s of REAL wall time
    expect(getNow().toISOString()).toBe(before);                    // frozen => unchanged
    expect(getNow().toISOString()).toBe('2026-07-11T00:00:00.000Z');
    const returned = advanceClock(5_000);                            // the only thing that moves it
    expect(returned).toBe('2026-07-11T00:00:05.000Z');              // advanceClock returns rfc3339
    expect(getNow().toISOString()).toBe('2026-07-11T00:00:05.000Z');
  });
  it('setNow pins an explicit instant; getClockState reflects now+mode', () => {
    setNow('2026-07-11T12:34:56.000Z');
    setClockMode('frozen');
    expect(getClockState()).toEqual({ now: '2026-07-11T12:34:56.000Z', mode: 'frozen' });
  });
  it('setNow rejects an unparseable instant', () => {
    expect(() => setNow('not-a-date')).toThrow(RangeError);
  });
});

describe('world clock — running', () => {
  it('tracks wall-clock elapsed since the last anchor', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('running');
    vi.advanceTimersByTime(3_000);
    expect(getNow().toISOString()).toBe('2026-07-11T00:00:03.000Z');
    expect(getClockState().mode).toBe('running');
  });
  it('setClockMode captures the current now WITHOUT jumping', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('running');
    vi.advanceTimersByTime(2_000);                                  // running: now = T0+2s
    setClockMode('frozen');                                          // freeze at T0+2s
    vi.advanceTimersByTime(10_000);                                 // wall keeps moving, frozen ignores
    expect(getNow().toISOString()).toBe('2026-07-11T00:00:02.000Z');
  });
});

describe('resetClock', () => {
  it('returns the clock to frozen mode', () => {
    setClockMode('running');
    resetClock();
    expect(getClockState().mode).toBe('frozen');
  });
});

describe('makeTransit (arrival = now + realTravelSeconds; a real future instant)', () => {
  it('mints departure=now and arrival=now+realETA seconds (no compression)', () => {
    const t = makeTransit({
      shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300),
      engineSpeed: 30, mode: 'CRUISE', now: new Date('2026-07-11T00:00:00.000Z'),
    });
    expect(t.originWaypoint).toBe('X1-PZ28-A1');
    expect(t.destinationWaypoint).toBe('X1-PZ28-B1');
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    // realTravelSeconds(300,30,CRUISE)=265 -> +265s
    expect(t.arrival).toBe('2026-07-11T00:04:25.000Z');
  });
  it('defaults departure to the world clock (getNow)', () => {
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('frozen');
    const t = makeTransit({
      shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 60), engineSpeed: 30,
    });
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    expect(t.arrival).toBe('2026-07-11T00:01:05.000Z'); // +65s
  });
  it('a probe (fuel capacity 0) still travels — a valid future arrival, and nav is fuel-free', () => {
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('frozen');
    const origin = wp('X1-PZ28-A1', 0, 0), dest = wp('X1-PZ28-C1', 0, 90);
    const t = makeTransit({ shipSymbol: 'PROBE-1', origin, destination: dest, engineSpeed: 3, mode: 'CRUISE' });
    expect(new Date(t.arrival).getTime()).toBeGreaterThan(new Date(t.departureTime).getTime());
    // capacity 0 => no fuel required => the navigate route always allows it
    expect(fuelRequired(distance(origin, dest), 'CRUISE', 0)).toBe(0);
  });
});

describe('resolveNav (reads THIS clock; single IN_TRANSIT->IN_ORBIT flip at arrival)', () => {
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
  it('defaults to the world clock: a navigated ship flips EXACTLY at arrival under advanceClock', () => {
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('frozen');
    const origin = wp('X1-PZ28-A1', 0, 0), dest = wp('X1-PZ28-B1', 0, 60);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin, destination: dest, engineSpeed: 30, mode: 'CRUISE' });
    expect(t.arrival).toBe('2026-07-11T00:01:05.000Z'); // +65s
    // at T0 -> in transit at origin (resolveNav with no now arg reads getNow())
    expect(resolveNav(baseShip(), t).nav.status).toBe('IN_TRANSIT');
    advanceClock(64_999);                                            // 1ms before arrival
    expect(resolveNav(baseShip(), t).nav.status).toBe('IN_TRANSIT');
    advanceClock(1);                                                 // exactly arrival
    const flipped = resolveNav(baseShip(), t);
    expect(flipped.nav.status).toBe('IN_ORBIT');
    expect(flipped.nav.waypointSymbol).toBe('X1-PZ28-B1');
  });
});

describe('makeCooldownExpiration (real seconds — no compression)', () => {
  it('is now + realSeconds', () => {
    expect(makeCooldownExpiration(500, new Date('2026-07-11T00:00:00.000Z'))).toBe('2026-07-11T00:08:20.000Z');
  });
  it('defaults now to the world clock', () => {
    setNow('2026-07-11T00:00:00.000Z');
    setClockMode('frozen');
    expect(makeCooldownExpiration(10)).toBe('2026-07-11T00:00:10.000Z');
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Ship, TransitState, Waypoint } from '../../src/world/types';
import {
  advanceClock, distance, fuelCost, fuelRequired, getClockState, getCompression, getMinTravelMs,
  getNow, makeCooldownExpiration, makeTransit, parseCompression, parseMinTravelMs,
  realTravelSeconds, resetClock, resolveNav, setClockMode, setCompression, setNow,
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
beforeEach(() => { vi.useRealTimers(); resetClock(); setCompression(20); });
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

describe('the unified time-compression knob (parse / get / set)', () => {
  it('parseCompression: default 20 for unset/empty/invalid, honors positives', () => {
    expect(parseCompression(undefined)).toBe(20); expect(parseCompression('')).toBe(20);
    expect(parseCompression('abc')).toBe(20); expect(parseCompression('0')).toBe(20);
    expect(parseCompression('-5')).toBe(20); expect(parseCompression('50')).toBe(50); expect(parseCompression('2.5')).toBe(2.5);
  });
  it('get/set round-trips', () => { setCompression(5); expect(getCompression()).toBe(5); });
  it('setCompression rejects non-positive / non-finite', () => {
    expect(() => setCompression(0)).toThrow(RangeError);
    expect(() => setCompression(-1)).toThrow(RangeError);
    expect(() => setCompression(Number.NaN)).toThrow(RangeError);
  });
});

describe('the configurable travel-time floor (TWIN_MIN_TRAVEL_MS)', () => {
  it('parseMinTravelMs: default 1000 for unset/empty/invalid/sub-1, honors integers >= 1', () => {
    expect(parseMinTravelMs(undefined)).toBe(1000); expect(parseMinTravelMs('')).toBe(1000);
    expect(parseMinTravelMs('abc')).toBe(1000); expect(parseMinTravelMs('0')).toBe(1000);
    expect(parseMinTravelMs('-5')).toBe(1000); expect(parseMinTravelMs('0.4')).toBe(1000);
    expect(parseMinTravelMs('50')).toBe(50); expect(parseMinTravelMs('1')).toBe(1); expect(parseMinTravelMs('250.9')).toBe(250);
  });
  it('the active floor defaults to 1000ms when the env is unset', () => {
    expect(getMinTravelMs()).toBe(1000);
  });
});

// The knob compresses the REAL v2.3.0 ETA INVERSELY (arrivalMs = realMs / factor), floored at
// getMinTravelMs(). makeCooldownExpiration(realSeconds, now) = now + compressedMs(realSeconds),
// so it is the cleanest probe of the compression math (no distance/engine indirection).
describe('time-compression drives travel INVERSELY (1x/10x/100x; floor; fidelity; live)', () => {
  const t0 = new Date('2026-07-11T00:00:00.000Z');
  const offset = (iso: string): number => Date.parse(iso) - t0.getTime();

  it('1x = TRUE real-API timing (fidelity): offset == realSeconds * 1000, un-compressed', () => {
    setCompression(1);
    expect(offset(makeCooldownExpiration(265, t0))).toBe(265_000); // exact real ETA, floor never bites
    expect(offset(makeCooldownExpiration(2515, t0))).toBe(2_515_000);
  });

  it('inverse proportionality across 1x/10x/100x — offset * factor is invariant', () => {
    setCompression(1);   const c1 = offset(makeCooldownExpiration(200, t0));   // 200000
    setCompression(10);  const c10 = offset(makeCooldownExpiration(200, t0));  // 20000
    setCompression(100); const c100 = offset(makeCooldownExpiration(200, t0)); // 2000
    expect(c1).toBe(200_000); expect(c10).toBe(20_000); expect(c100).toBe(2_000);
    expect(c1).toBe(c10 * 10); expect(c1).toBe(c100 * 100); // realMs constant regardless of factor
  });

  it('floor honored: a heavily-compressed short ETA never drops below getMinTravelMs() (1000)', () => {
    setCompression(1000);
    // realSeconds=15 (the flat +15s minimum) -> round(15000/1000)=15ms -> floored to 1000ms
    expect(offset(makeCooldownExpiration(15, t0))).toBe(1000);
    expect(offset(makeCooldownExpiration(15, t0))).toBeGreaterThanOrEqual(getMinTravelMs());
  });

  it('arrival is ALWAYS a real-future instant, even at extreme compression', () => {
    setCompression(1_000_000);
    const t = makeTransit({
      shipSymbol: 'S', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 3),
      engineSpeed: 30, mode: 'CRUISE', now: t0,
    });
    expect(Date.parse(t.arrival)).toBeGreaterThan(Date.parse(t.departureTime));
    expect(Date.parse(t.arrival) - Date.parse(t.departureTime)).toBeGreaterThanOrEqual(1000); // >= floor
  });

  it('setCompression takes effect LIVE for subsequent transits (the admin-lever seam)', () => {
    setCompression(20);  const slow = offset(makeCooldownExpiration(400, t0)); // 400000/20 = 20000
    setCompression(200); const fast = offset(makeCooldownExpiration(400, t0)); // 400000/200 = 2000
    expect(slow).toBe(20_000); expect(fast).toBe(2_000); expect(slow).toBe(fast * 10);
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

describe('makeTransit (arrival = now + COMPRESSED realTravelSeconds; a real future instant)', () => {
  it('mints departure=now and arrival=now+COMPRESSED realETA (compression=20)', () => {
    const t = makeTransit({
      shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300),
      engineSpeed: 30, mode: 'CRUISE', now: new Date('2026-07-11T00:00:00.000Z'),
    });
    expect(t.originWaypoint).toBe('X1-PZ28-A1');
    expect(t.destinationWaypoint).toBe('X1-PZ28-B1');
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    // realTravelSeconds(300,30,CRUISE)=265 -> compressed: round(265*1000/20)=13250ms
    expect(t.arrival).toBe('2026-07-11T00:00:13.250Z');
  });
  it('the compressed ETA floors at 1000ms (a short hop never fires below the daemon ClockDriftBuffer)', () => {
    // realTravelSeconds(0,30,CRUISE)=15 -> naive compression round(15*1000/20)=750ms, floored to 1000ms
    const t = makeTransit({
      shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-A1', 0, 0),
      engineSpeed: 30, mode: 'CRUISE', now: new Date('2026-07-11T00:00:00.000Z'),
    });
    expect(t.arrival).toBe('2026-07-11T00:00:01.000Z');
  });
  it('defaults departure to REAL wall-clock — decoupled from the frozen world clock', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));  // REAL wall clock, controlled
    setNow('2000-01-01T00:00:00.000Z');                        // world clock parked at a DIFFERENT instant
    setClockMode('frozen');
    const t = makeTransit({
      shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 60), engineSpeed: 30,
    });
    // departure tracks REAL wall time, NOT the frozen world-clock value (2000-01-01).
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    // realTravelSeconds(60,30,CRUISE)=65 -> compressed: round(65*1000/20)=3250ms
    expect(t.arrival).toBe('2026-07-11T00:00:03.250Z');
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
  it('defaults to REAL wall-clock, decoupled from the world clock — advanceClock does NOT drive the flip', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));  // REAL wall clock, controlled
    setNow('2099-01-01T00:00:00.000Z');                        // world clock parked at a far DIFFERENT instant
    setClockMode('frozen');
    const origin = wp('X1-PZ28-A1', 0, 0), dest = wp('X1-PZ28-B1', 0, 60);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin, destination: dest, engineSpeed: 30, mode: 'CRUISE' });
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');       // real wall clock, NOT the 2099 world-now
    // realTravelSeconds(60,30,CRUISE)=65 -> compressed: round(65*1000/20)=3250ms
    expect(t.arrival).toBe('2026-07-11T00:00:03.250Z');

    // at real-T0 -> in transit at origin (resolveNav with no now arg reads Date.now(), NOT getNow())
    expect(resolveNav(baseShip(), t).nav.status).toBe('IN_TRANSIT');

    // Advancing the WORLD clock (however far) has NO effect on ship motion — decoupled.
    advanceClock(999_999);
    expect(resolveNav(baseShip(), t).nav.status).toBe('IN_TRANSIT');

    // Only REAL wall time crossing arrival flips it.
    vi.setSystemTime(new Date('2026-07-11T00:00:03.249Z'));         // 1ms before arrival
    expect(resolveNav(baseShip(), t).nav.status).toBe('IN_TRANSIT');
    vi.setSystemTime(new Date('2026-07-11T00:00:03.250Z'));         // exactly arrival
    const flipped = resolveNav(baseShip(), t);
    expect(flipped.nav.status).toBe('IN_ORBIT');
    expect(flipped.nav.waypointSymbol).toBe('X1-PZ28-B1');
  });
});

describe('makeCooldownExpiration (REAL wall-clock expiry, COMPRESSED — same rationale as arrivals)', () => {
  it('is now + COMPRESSED realSeconds (compression=20)', () => {
    // compressedMs(500) = round(500*1000/20) = 25000ms = 25s
    expect(makeCooldownExpiration(500, new Date('2026-07-11T00:00:00.000Z'))).toBe('2026-07-11T00:00:25.000Z');
  });
  it('the compressed expiry floors at 1000ms for a short cooldown', () => {
    // compressedMs(10) = round(10*1000/20) = 500ms, floored to 1000ms
    expect(makeCooldownExpiration(10, new Date('2026-07-11T00:00:00.000Z'))).toBe('2026-07-11T00:00:01.000Z');
  });
  it('defaults now to REAL wall-clock — decoupled from the frozen world clock', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-11T00:00:00.000Z'));  // REAL wall clock
    setNow('2099-01-01T00:00:00.000Z');                        // world clock parked far away
    setClockMode('frozen');
    // compressedMs(10) floors at 1000ms: round(10*1000/20)=500 -> floored to 1000
    expect(makeCooldownExpiration(10)).toBe('2026-07-11T00:00:01.000Z');
  });
});

import { describe, it, expect } from 'vitest';
import { enginePulse, hasRimGlow, showsEngineGlow, ENGINE_GLOW_STEADY } from '../spriteGlow';

describe('enginePulse', () => {
  it('stays within [0.55, 0.9] for any timestamp', () => {
    for (const t of [0, 45, 90, 141, 10_000, 1_234_567, Date.now()]) {
      const p = enginePulse(t);
      expect(p).toBeGreaterThanOrEqual(0.55);
      expect(p).toBeLessThanOrEqual(0.9);
    }
  });

  it('pins to a steady mid opacity when reduced motion is requested', () => {
    for (const t of [0, 45, 90, 141, 10_000, Date.now()]) {
      expect(enginePulse(t, true)).toBe(ENGINE_GLOW_STEADY);
    }
    // Steady value sits inside the animated band so it reads the same.
    expect(ENGINE_GLOW_STEADY).toBeGreaterThanOrEqual(0.55);
    expect(ENGINE_GLOW_STEADY).toBeLessThanOrEqual(0.9);
  });

  it('is deterministic for a given timestamp', () => {
    expect(enginePulse(1000)).toBe(enginePulse(1000));
  });

  it('modulates over time between a trough and a peak', () => {
    const trough = enginePulse(0); // sin(0) = 0 -> 0.55
    const peak = enginePulse(90 * (Math.PI / 2)); // sin(pi/2) = 1 -> 0.9
    expect(trough).toBeCloseTo(0.55, 5);
    expect(peak).toBeCloseTo(0.9, 5);
    expect(peak - trough).toBeGreaterThan(0.3);
  });
});

describe('hasRimGlow', () => {
  it('is true for planets and gas giants', () => {
    expect(hasRimGlow('PLANET')).toBe(true);
    expect(hasRimGlow('GAS_GIANT')).toBe(true);
  });

  it('is false for stations, moons, asteroids, gates', () => {
    for (const t of ['ORBITAL_STATION', 'MOON', 'ASTEROID', 'ASTEROID_FIELD', 'FUEL_STATION', 'JUMP_GATE']) {
      expect(hasRimGlow(t)).toBe(false);
    }
  });
});

describe('showsEngineGlow', () => {
  it('glows only for in-transit ships that actually burn fuel', () => {
    expect(showsEngineGlow('IN_TRANSIT', 400)).toBe(true);
  });

  it('never glows for fuel-less probes (satellites fly BURN at zero cost)', () => {
    expect(showsEngineGlow('IN_TRANSIT', 0)).toBe(false);
  });

  it('never glows when not in transit', () => {
    expect(showsEngineGlow('DOCKED', 400)).toBe(false);
    expect(showsEngineGlow('IN_ORBIT', 400)).toBe(false);
  });
});

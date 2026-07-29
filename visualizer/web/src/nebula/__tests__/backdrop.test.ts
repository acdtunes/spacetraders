import { describe, it, expect } from 'vitest';
import { starSeeds } from '../layers/backdrop';

// Fixed input for the deterministic pins below.
const SYMBOLS = ['X1-B', 'X1-A'];
const BOUNDS = { minX: -100, minY: -50, maxX: 100, maxY: 50 };

describe('starSeeds', () => {
  it('respects the requested count', () => {
    expect(starSeeds(SYMBOLS, 5, BOUNDS)).toHaveLength(5);
    expect(starSeeds(SYMBOLS, 0, BOUNDS)).toHaveLength(0);
    expect(starSeeds(SYMBOLS, 600, BOUNDS)).toHaveLength(600);
  });

  it('keeps every seed in bounds with sane r/alpha', () => {
    for (const s of starSeeds(SYMBOLS, 600, BOUNDS)) {
      expect(s.x).toBeGreaterThanOrEqual(BOUNDS.minX);
      expect(s.x).toBeLessThanOrEqual(BOUNDS.maxX);
      expect(s.y).toBeGreaterThanOrEqual(BOUNDS.minY);
      expect(s.y).toBeLessThanOrEqual(BOUNDS.maxY);
      expect(s.r).toBeGreaterThan(0);
      expect(s.alpha).toBeGreaterThan(0);
      expect(s.alpha).toBeLessThanOrEqual(1);
    }
  });

  it('is deterministic: same input → identical output; symbol ORDER is irrelevant (set-seeded)', () => {
    const a = starSeeds(SYMBOLS, 50, BOUNDS);
    const b = starSeeds(SYMBOLS, 50, BOUNDS);
    expect(a).toEqual(b);
    // Poll ordering must not reshuffle the sky: seeding hashes the sorted set.
    expect(starSeeds(['X1-A', 'X1-B'], 50, BOUNDS)).toEqual(a);
    // ...but a different set of systems is a different sky.
    expect(starSeeds(['X1-A', 'X1-C'], 50, BOUNDS)).not.toEqual(a);
  });

  it('pins the first 3 seeds exactly for the fixed input (FNV-1a + mulberry32 spec)', () => {
    const [s0, s1, s2] = starSeeds(SYMBOLS, 5, BOUNDS);
    expect(s0.x).toBe(-63.94553938880563);
    expect(s0.y).toBe(-17.392010241746902);
    expect(s0.r).toBe(1.6379374387208374);
    expect(s0.alpha).toBe(0.33031897940672933);
    expect(s1.x).toBe(-59.41928941756487);
    expect(s1.y).toBe(13.150235661305487);
    expect(s1.r).toBe(1.538124348130077);
    expect(s1.alpha).toBe(0.6248700081137941);
    expect(s2.x).toBe(-40.47321816906333);
    expect(s2.y).toBe(48.24885556008667);
    expect(s2.r).toBe(1.8106853282544764);
    expect(s2.alpha).toBe(0.5301996715948918);
  });
});

import { describe, it, expect } from 'vitest';
import { starfield, parallaxOffset } from '../backdrop';

describe('backdrop', () => {
  it('starfield is deterministic for a seed and stays in bounds', () => {
    const a = starfield(42, 200, 1000, 800);
    const b = starfield(42, 200, 1000, 800);
    expect(a).toEqual(b);
    expect(a).toHaveLength(200);
    for (const s of a) {
      expect(s.x).toBeGreaterThanOrEqual(0);
      expect(s.x).toBeLessThanOrEqual(1000);
      expect(s.r).toBeGreaterThan(0);
      expect(s.a).toBeGreaterThan(0);
      expect(s.a).toBeLessThanOrEqual(1);
    }
  });
  it('parallax scales the screen-space pan at a given depth factor', () => {
    expect(parallaxOffset({ x: 100, y: -50 }, 0.1)).toEqual({ x: 10, y: -5 });
  });
});

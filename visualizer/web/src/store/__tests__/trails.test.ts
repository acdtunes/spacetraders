import { describe, it, expect } from 'vitest';
import { appendTrailPoint, trailOpacity, TRAIL_MAX_POINTS, TRAIL_FADE_MS } from '../trails';
import type { ShipTrailPoint } from '../../types/spacetraders';

const pt = (overrides: Partial<ShipTrailPoint>): ShipTrailPoint => ({
  shipSymbol: 'SHIP-1',
  x: 0,
  y: 0,
  timestamp: 0,
  flightMode: 'CRUISE',
  ...overrides,
});

describe('trail buffer', () => {
  it('caps the buffer at TRAIL_MAX_POINTS', () => {
    let pts: ShipTrailPoint[] = [];
    for (let i = 0; i < TRAIL_MAX_POINTS + 50; i++) {
      pts = appendTrailPoint(pts, pt({ x: i, timestamp: i }));
    }
    expect(pts.length).toBe(TRAIL_MAX_POINTS);
    expect(pts[0].x).toBe(50); // oldest dropped
  });
  it('drops points older than the fade window', () => {
    const now = 100_000;
    let pts = [pt({ x: 0, timestamp: now - TRAIL_FADE_MS - 1 }), pt({ x: 1, timestamp: now - 10 })];
    pts = appendTrailPoint(pts, pt({ x: 2, timestamp: now }));
    expect(pts.map((p) => p.x)).toEqual([1, 2]);
  });
  it('opacity fades with age, clamped to [0,1]', () => {
    const now = 50_000;
    expect(trailOpacity(pt({ timestamp: now }), now)).toBeCloseTo(1, 5);
    expect(trailOpacity(pt({ timestamp: now - TRAIL_FADE_MS }), now)).toBe(0);
    expect(trailOpacity(pt({ timestamp: now - TRAIL_FADE_MS / 2 }), now)).toBeCloseTo(0.5, 2);
  });
});

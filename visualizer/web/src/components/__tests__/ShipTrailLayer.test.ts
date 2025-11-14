import { describe, it, expect } from 'vitest';
import { filterActiveTrail, computeParticleCount, hexToRgb, boostColor, rgba, type TrailVisualSettings } from '../shipTrailUtils';
import type { ShipTrailPoint } from '../../types/spacetraders';

describe('shipTrailUtils', () => {
  const now = Date.now();
  const basePoint: ShipTrailPoint = {
    shipSymbol: 'TEST',
    x: 0,
    y: 0,
    timestamp: now - 3000,
    flightMode: 'CRUISE',
  };

  const config: Record<'CRUISE', TrailVisualSettings> = {
    CRUISE: {
      maxAgeMs: 5000,
      baseWidth: 1,
      baseAlpha: 0.5,
      tailAlpha: 0.1,
      glowBlur: 2,
      glowAlpha: 0.3,
      particleDensity: 0.5,
      particleSize: [0.2, 0.4],
      particleAlpha: 0.6,
      colorBoost: 0.2,
    },
  } as const;

  it('filters out stale trail points based on maxAge', () => {
    const trail: ShipTrailPoint[] = [
      { ...basePoint, timestamp: now - 1000 },
      { ...basePoint, timestamp: now - 4000 },
      { ...basePoint, timestamp: now - 7000 },
    ];

    const result = filterActiveTrail(trail, now, config as any);
    expect(result).toHaveLength(2);
    expect(result[0].timestamp).toBe(now - 1000);
    expect(result[1].timestamp).toBe(now - 4000);
  });

  it('returns zero particles when density is zero', () => {
    const zeroConfig: TrailVisualSettings = { ...config.CRUISE, particleDensity: 0 };
    const trail = [basePoint, { ...basePoint, x: 1, timestamp: now - 2000 }];
    expect(computeParticleCount(trail, zeroConfig)).toBe(0);
  });

  it('computes particle count relative to segments and density', () => {
    const trail = [
      { ...basePoint, x: 0 },
      { ...basePoint, x: 1, timestamp: now - 2000 },
      { ...basePoint, x: 2, timestamp: now - 1000 },
    ];
    const count = computeParticleCount(trail, config.CRUISE);
    // segments = 2, density 0.5 -> floor(1) = 1
    expect(count).toBe(1);
  });

  it('boosts colors and rgba formatting consistently', () => {
    const rgb = hexToRgb('#336699');
    expect(rgb).toEqual({ r: 51, g: 102, b: 153 });

    const boosted = boostColor(rgb, 0.5);
    expect(boosted.r).toBeGreaterThan(rgb.r);
    expect(boosted.g).toBeGreaterThan(rgb.g);
    expect(boosted.b).toBeGreaterThan(rgb.b);

    expect(rgba(boosted, 0.4)).toBe(`rgba(${boosted.r}, ${boosted.g}, ${boosted.b}, 0.4)`);
  });
});

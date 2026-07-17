import { describe, it, expect } from 'vitest';
import { freshnessColor, haloAlpha } from '../freshness';
import { NOIR, noirRgb } from '../../../theme/noir';

const rgb = (hex: string) => { const { r, g, b } = noirRgb(hex); return `rgb(${r}, ${g}, ${b})`; };

describe('freshnessColor', () => {
  it('hits the NOIR anchors at 0 / 50 / 100', () => {
    expect(freshnessColor(0)).toBe(rgb(NOIR.bad));
    expect(freshnessColor(50)).toBe(rgb(NOIR.warn));
    expect(freshnessColor(100)).toBe(rgb(NOIR.good));
  });
  it('interpolates piecewise and clamps out-of-range', () => {
    expect(freshnessColor(-10)).toBe(rgb(NOIR.bad));
    expect(freshnessColor(200)).toBe(rgb(NOIR.good));
    expect(freshnessColor(25)).not.toBe(rgb(NOIR.bad));
    expect(freshnessColor(25)).not.toBe(rgb(NOIR.warn));
  });
});

describe('haloAlpha', () => {
  it('is monotonic from smolder to glow', () => {
    expect(haloAlpha(0)).toBeCloseTo(0.18, 2);
    expect(haloAlpha(100)).toBeCloseTo(0.45, 2);
    expect(haloAlpha(50)).toBeGreaterThan(haloAlpha(0));
    expect(haloAlpha(50)).toBeLessThan(haloAlpha(100));
  });
});

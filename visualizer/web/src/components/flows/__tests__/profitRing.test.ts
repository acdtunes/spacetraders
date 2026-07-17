import { describe, it, expect } from 'vitest';
import { ringSpec } from '../profitRing';
import { NOIR } from '../../../theme/noir';

describe('ringSpec', () => {
  it('unknown or non-positive projection: empty dim track', () => {
    expect(ringSpec(1000, null)).toEqual({ fill: 0, color: NOIR.dim, underGlow: null, overshoot: false });
    expect(ringSpec(1000, 0)).toEqual({ fill: 0, color: NOIR.dim, underGlow: null, overshoot: false });
  });
  it('negative net: capital committed — empty ring, red under-glow', () => {
    expect(ringSpec(-42000, 100000)).toEqual({ fill: 0, color: NOIR.warn, underGlow: NOIR.bad, overshoot: false });
  });
  it('partial fill: amber below 0.6, green from 0.6', () => {
    expect(ringSpec(30000, 100000)).toMatchObject({ fill: 0.3, color: NOIR.warn });
    expect(ringSpec(70000, 100000)).toMatchObject({ fill: 0.7, color: NOIR.good });
  });
  it('overshoot: clamped full, flagged', () => {
    expect(ringSpec(120000, 100000)).toEqual({ fill: 1, color: NOIR.good, underGlow: null, overshoot: true });
  });
});

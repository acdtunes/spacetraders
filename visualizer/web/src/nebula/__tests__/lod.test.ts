import { describe, it, expect } from 'vitest';
import { bandFor } from '../lod';

describe('bandFor', () => {
  const fit = 0.1;
  it('maps a zoom sweep to GALAXY → REGION → SYSTEM', () => {
    expect(bandFor(0.1, fit, null)).toBe('GALAXY');      // z=1
    expect(bandFor(0.25, fit, null)).toBe('REGION');     // z=2.5
    expect(bandFor(1.0, fit, null)).toBe('SYSTEM');      // z=10
  });
  it('is sticky inside the hysteresis gap (no flicker)', () => {
    // z=2.0 sits between REGION_EXIT(1.8) and REGION_ENTER(2.2)
    expect(bandFor(0.2, fit, 'GALAXY')).toBe('GALAXY');  // was out → stays out
    expect(bandFor(0.2, fit, 'REGION')).toBe('REGION');  // was in  → stays in
    // z=8.0 sits between SYSTEM_EXIT(7.5) and SYSTEM_ENTER(9.0)
    expect(bandFor(0.8, fit, 'REGION')).toBe('REGION');
    expect(bandFor(0.8, fit, 'SYSTEM')).toBe('SYSTEM');
  });
  it('crosses thresholds decisively', () => {
    expect(bandFor(0.23, fit, 'GALAXY')).toBe('REGION'); // z=2.3 ≥ 2.2
    expect(bandFor(0.17, fit, 'REGION')).toBe('GALAXY'); // z=1.7 < 1.8
    expect(bandFor(0.95, fit, 'REGION')).toBe('SYSTEM'); // z=9.5 ≥ 9.0
    expect(bandFor(0.74, fit, 'SYSTEM')).toBe('REGION'); // z=7.4 < 7.5
  });
  it('guards degenerate fitScale', () => {
    expect(bandFor(1, 0, null)).toBe('GALAXY');
  });
});

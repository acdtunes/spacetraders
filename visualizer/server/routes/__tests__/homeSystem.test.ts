import { describe, it, expect } from 'vitest';
import { homeSystemFromHeadquarters } from '../../utils/homeSystem.js';

describe('homeSystemFromHeadquarters', () => {
  it('takes the first two segments of a headquarters waypoint', () => {
    expect(homeSystemFromHeadquarters('X1-KA42-A1')).toBe('X1-KA42');
    expect(homeSystemFromHeadquarters('X1-UQ16-FF5F')).toBe('X1-UQ16');
  });

  it('handles a bare system symbol (already two segments)', () => {
    expect(homeSystemFromHeadquarters('X1-KA42')).toBe('X1-KA42');
  });

  it('returns null for malformed / empty / missing input', () => {
    expect(homeSystemFromHeadquarters('')).toBeNull();
    expect(homeSystemFromHeadquarters(null)).toBeNull();
    expect(homeSystemFromHeadquarters(undefined)).toBeNull();
    expect(homeSystemFromHeadquarters('X1')).toBeNull();
    expect(homeSystemFromHeadquarters('-KA42-A1')).toBeNull();
  });
});

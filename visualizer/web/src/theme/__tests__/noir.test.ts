import { describe, it, expect } from 'vitest';
import { NOIR, noirAlpha } from '../noir';

describe('noir theme', () => {
  it('exposes the core palette', () => {
    expect(NOIR.bg0).toBe('#04060D');
    expect(NOIR.accent).toBe('#7DB1FF');
    expect(NOIR.star).toBe('#F5E9C8');
  });
  it('exposes a legible nebula core lighter than the base nebula', () => {
    expect(NOIR.nebulaCore).toBe('#2B4470');
    // The core must sit brighter than the base nebula tone so the 3-stop
    // gradient reads against bg0 instead of blending into it.
    expect(NOIR.nebula).toBe('#16223F');
  });
  it('converts hex + alpha to rgba', () => {
    expect(noirAlpha('#7DB1FF', 0.5)).toBe('rgba(125, 177, 255, 0.5)');
  });
});

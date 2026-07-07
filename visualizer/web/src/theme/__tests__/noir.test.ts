import { describe, it, expect } from 'vitest';
import { NOIR, noirAlpha } from '../noir';

describe('noir theme', () => {
  it('exposes the core palette', () => {
    expect(NOIR.bg0).toBe('#04060D');
    expect(NOIR.accent).toBe('#7DB1FF');
    expect(NOIR.star).toBe('#F5E9C8');
  });
  it('converts hex + alpha to rgba', () => {
    expect(noirAlpha('#7DB1FF', 0.5)).toBe('rgba(125, 177, 255, 0.5)');
  });
});

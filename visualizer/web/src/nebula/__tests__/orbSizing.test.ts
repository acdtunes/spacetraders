import { describe, it, expect } from 'vitest';
import { orbRadius } from '../layers/orbs';

// Brief-pinned contract: 4px floor, 18px cap, sqrt scaling, 1-decimal rounding.
describe('orbRadius', () => {
  it('pins the brief values exactly: floor, cap, and sqrt midpoint', () => {
    expect(orbRadius(0, 100)).toBe(4);
    expect(orbRadius(100, 100)).toBe(18);
    // sqrt(25/100) = 0.5 → round((4 + 14*0.5)*10)/10 = 11
    expect(orbRadius(25, 100)).toBe(11);
  });

  it('scales by sqrt of the activity share (1-decimal rounded)', () => {
    // sqrt(0.5) ≈ 0.7071 → 4 + 14*0.7071 = 13.8995 → 13.9
    expect(orbRadius(50, 100)).toBe(13.9);
  });

  it('caps at 18 when activity exceeds maxActivity', () => {
    expect(orbRadius(250, 100)).toBe(18);
  });

  it('sizes by magnitude: loss-heavy systems glow as large as gain-heavy ones', () => {
    expect(orbRadius(-25, 100)).toBe(11);
    expect(orbRadius(-100, 100)).toBe(18);
  });

  it('degrades to the 4px floor when maxActivity is zero, negative, or non-finite', () => {
    expect(orbRadius(0, 0)).toBe(4);
    expect(orbRadius(10, 0)).toBe(4);
    expect(orbRadius(10, -5)).toBe(4);
    expect(orbRadius(10, NaN)).toBe(4);
    expect(orbRadius(10, Infinity)).toBe(4);
  });
});

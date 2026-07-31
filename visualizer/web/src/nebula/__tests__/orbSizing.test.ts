import { describe, it, expect } from 'vitest';
import { AURA_STOPS, markRadiusPx, orbRadius, ORB_MAX_PX, ORB_MIN_PX } from '../layers/orbs';

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

// sp-9m0bd — the freshness annulus. Both priced and dark marks are sized from
// this one rule, and the labels are offset past it.
describe('markRadiusPx', () => {
  it('always clears the orb — a mark drawn inside the orb cannot be read', () => {
    // The whole reason the previous encoding measured 45.1 against the dark
    // ring's 80.9: its energy sat under the orb. Every orb size, not just the
    // ends, because the floor term could satisfy the ends alone.
    for (let orb = ORB_MIN_PX; orb <= ORB_MAX_PX; orb += 0.5) {
      expect(markRadiusPx(orb)).toBeGreaterThan(orb);
    }
  });

  it('scales with the orb rather than sitting at a constant', () => {
    expect(markRadiusPx(ORB_MAX_PX)).toBeGreaterThan(markRadiusPx(ORB_MIN_PX));
    expect(markRadiusPx(10)).toBe(20);
    expect(markRadiusPx(ORB_MAX_PX)).toBe(36);
  });

  it('floors at 8px for an orb below the 4px floor', () => {
    // Inert today (4 × 2 = 8 exactly), and kept as the guard it is: if
    // ORB_MIN_PX ever drops, a zero-activity system must still show a mark.
    expect(markRadiusPx(ORB_MIN_PX)).toBe(8);
    expect(markRadiusPx(1)).toBe(8);
    expect(markRadiusPx(0)).toBe(8);
  });
});

// sp-9m0bd — the priced mark's radial profile. Two properties do all the work,
// and the previous encoding violated the first by reusing the orb's own halo
// profile: its energy sat inside 35% of its radius, i.e. under the orb.
describe('AURA_STOPS', () => {
  const alphaAt = (offset: number) => {
    const stop = AURA_STOPS.find(([o]) => o === offset);
    if (stop == null) throw new Error(`no stop at ${offset}`);
    return Number(/rgba\(255,255,255,([\d.]+)\)/.exec(stop[1])![1]);
  };
  const offsets = AURA_STOPS.map(([o]) => o);

  it('puts NO energy under the orb — the mark begins where the orb ends', () => {
    // The orb reaches exactly half the mark's radius (MARK_SCALE is 2), so
    // every stop at or inside 0.5 must be fully transparent.
    expect(offsets).toContain(0.5);
    for (const [o, colour] of AURA_STOPS) {
      if (o <= 0.5) expect(colour, `stop ${o}`).toBe('rgba(255,255,255,0)');
    }
    expect(alphaAt(0)).toBe(0);
    expect(alphaAt(0.5)).toBe(0);
  });

  it('returns to zero at the rim — a mark with an edge is a mark that moirés', () => {
    // The load-bearing property. At REGION density the 5th-percentile
    // nearest-neighbour separation is 7.1 design px against a 4px orb floor, so
    // any hard curve outside the orb crosses its neighbours; a curve with no
    // edge has no locus to cross.
    expect(offsets[offsets.length - 1]).toBe(1);
    expect(alphaAt(1)).toBe(0);
  });

  it('peaks in the annulus between them, so the mark is actually visible', () => {
    const peak = AURA_STOPS.reduce((best, s) =>
      Number(/,([\d.]+)\)/.exec(s[1])![1]) > Number(/,([\d.]+)\)/.exec(best[1])![1]) ? s : best);
    expect(Number(/,([\d.]+)\)/.exec(peak[1])![1])).toBe(1);
    expect(peak[0]).toBeGreaterThan(0.5);
    expect(peak[0]).toBeLessThan(1);
  });

  it('lists its stops in ascending offset order, as createRadialGradient requires', () => {
    expect([...offsets].sort((a, b) => a - b)).toEqual(offsets);
  });
});

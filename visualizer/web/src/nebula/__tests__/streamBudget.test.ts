import { describe, it, expect } from 'vitest';
import { particleBudget, PARTICLE_CAP, lerpTint } from '../layers/streams';
import type { SceneLane } from '../sceneData';

const lane = (from: string, to: string, volume: number, profitPerHr = 100): SceneLane =>
  ({ from, to, profitPerHr, volume, realized: profitPerHr, projected: 0 });

describe('particleBudget', () => {
  it('pins the brief exactly: volumes 10/30 with cap 100 → 25/75', () => {
    const out = particleBudget([lane('A', 'B', 10), lane('B', 'C', 30)], 100);
    expect(out.get('A|B')).toBe(25);
    expect(out.get('B|C')).toBe(75);
    expect(out.size).toBe(2);
  });

  it('empty lanes → empty map', () => {
    expect(particleBudget([], 100).size).toBe(0);
  });

  it('exports the 5000 cap constant', () => {
    expect(PARTICLE_CAP).toBe(5000);
  });

  it('min-4 floor kicks in for tiny lanes, funded by the largest lane, total ≤ cap', () => {
    // Proportional floors: 100·1/1001 → 0, 100·1000/1001 → 99. The tiny lane is
    // raised to 4; the overshoot (3) is taken back from the largest lane.
    const out = particleBudget([lane('A', 'B', 1), lane('B', 'C', 1000)], 100);
    expect(out.get('A|B')).toBe(4);
    expect(out.get('B|C')).toBe(96);
    expect(total(out)).toBeLessThanOrEqual(100);
  });

  it('every active lane gets ≥ 4 whenever 4·laneCount fits the cap', () => {
    const lanes = [lane('A', 'B', 0), lane('B', 'C', 0), lane('C', 'D', 5000)];
    const out = particleBudget(lanes, 100);
    for (const [, n] of out) expect(n).toBeGreaterThanOrEqual(4);
    expect(total(out)).toBeLessThanOrEqual(100);
  });

  it('all-zero volume → every lane sits at the 4 floor', () => {
    const out = particleBudget([lane('A', 'B', 0), lane('B', 'C', 0)], 100);
    expect(out.get('A|B')).toBe(4);
    expect(out.get('B|C')).toBe(4);
  });

  it('cap too small for the floor (4·n > cap) → cap wins with an even split', () => {
    const out = particleBudget([lane('A', 'B', 9), lane('B', 'C', 1), lane('C', 'D', 90)], 10);
    expect(out.get('A|B')).toBe(3);
    expect(out.get('B|C')).toBe(3);
    expect(out.get('C|D')).toBe(3);
    expect(total(out)).toBeLessThanOrEqual(10);
  });

  it('duplicate directed lanes merge their volume under one key', () => {
    const out = particleBudget([lane('A', 'B', 10), lane('A', 'B', 10), lane('B', 'C', 20)], 100);
    expect(out.get('A|B')).toBe(50);
    expect(out.get('B|C')).toBe(50);
    expect(out.size).toBe(2);
  });

  it('stays ≤ cap under flooring remainders', () => {
    const out = particleBudget([lane('A', 'B', 1), lane('B', 'C', 1), lane('C', 'D', 1)], 100);
    // 33 each (floor of 33.33…) — the slack is not redistributed.
    expect(total(out)).toBeLessThanOrEqual(100);
    for (const [, n] of out) expect(n).toBe(33);
  });
});

describe('lerpTint', () => {
  const CYAN = 0x22d3ee;
  const VIOLET = 0xa78bfa;
  it('endpoints are exact', () => {
    expect(lerpTint(CYAN, VIOLET, 0)).toBe(CYAN);
    expect(lerpTint(CYAN, VIOLET, 1)).toBe(VIOLET);
  });
  it('midpoint is the per-channel rounded blend', () => {
    // r: (0x22+0xa7)/2=100.5→101(0x65), g: (0xd3+0x8b)/2=175(0xaf), b: (0xee+0xfa)/2=244(0xf4)
    expect(lerpTint(CYAN, VIOLET, 0.5)).toBe(0x65aff4);
  });
});

function total(m: Map<string, number>): number {
  let s = 0;
  for (const [, n] of m) s += n;
  return s;
}

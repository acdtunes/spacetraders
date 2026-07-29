import { describe, it, expect } from 'vitest';
import { aggregateCurrents } from '../aggregate';
import { formatPerHr } from '../layers/galaxyBand';
import type { Cluster } from '../clusters';
import type { SceneLane } from '../sceneData';

const lane = (from: string, to: string, profitPerHr: number, volume: number): SceneLane => ({
  from,
  to,
  profitPerHr,
  volume,
  realized: profitPerHr,
  projected: 0,
});

// Two clusters, id = first member (clustersFor convention).
const CA: Cluster = { id: 'X1-AA', members: ['X1-AA', 'X1-AB'], cx: 0, cy: 0, isHome: true };
const CB: Cluster = { id: 'X1-BA', members: ['X1-BA', 'X1-BB'], cx: 100, cy: 0, isHome: false };

describe('aggregateCurrents', () => {
  it('merges symmetric inter-cluster lanes into one keyed current, excluding intra-cluster lanes', () => {
    const lanes = [
      lane('X1-AA', 'X1-AB', 999_999, 999), // intra-cluster: excluded entirely
      lane('X1-AA', 'X1-BA', 5_000_000, 10), // A → B
      lane('X1-BB', 'X1-AB', -2_000_000, 4), // B → A (reversed duplicate; negative keeps sign)
    ];
    const out = aggregateCurrents([CA, CB], lanes);
    expect(out).toEqual([
      { fromCluster: 'X1-AA', toCluster: 'X1-BA', profitPerHr: 3_000_000, volume: 14 },
    ]);
  });

  it('keeps a negative net profit negative', () => {
    const out = aggregateCurrents([CA, CB], [lane('X1-BA', 'X1-AA', -750_000, 3)]);
    expect(out).toEqual([
      { fromCluster: 'X1-AA', toCluster: 'X1-BA', profitPerHr: -750_000, volume: 3 },
    ]);
  });

  it('is deterministic: output sorted by min|max key regardless of lane order', () => {
    const CC: Cluster = { id: 'X1-CA', members: ['X1-CA'], cx: 0, cy: 100, isHome: false };
    const lanes = [
      lane('X1-CA', 'X1-AA', 1_000, 1), // pair X1-AA|X1-CA
      lane('X1-BA', 'X1-CA', 2_000, 2), // pair X1-BA|X1-CA
      lane('X1-AB', 'X1-BB', 3_000, 3), // pair X1-AA|X1-BA
    ];
    const out = aggregateCurrents([CA, CB, CC], lanes);
    expect(out.map((c) => `${c.fromCluster}|${c.toCluster}`)).toEqual([
      'X1-AA|X1-BA',
      'X1-AA|X1-CA',
      'X1-BA|X1-CA',
    ]);
    // Shuffled lane input → identical output.
    expect(aggregateCurrents([CA, CB, CC], [...lanes].reverse())).toEqual(out);
  });

  it('ignores lanes with an endpoint outside every cluster, and empty inputs yield []', () => {
    expect(aggregateCurrents([CA, CB], [lane('X1-AA', 'X1-ZZ', 9_000, 9)])).toEqual([]);
    expect(aggregateCurrents([], [lane('X1-AA', 'X1-BA', 9_000, 9)])).toEqual([]);
    expect(aggregateCurrents([CA, CB], [])).toEqual([]);
  });
});

describe('formatPerHr (current label text)', () => {
  it('formats ±X.XM/hr at or above 1M, ±XXXk/hr below', () => {
    expect(formatPerHr(3_000_000)).toBe('+3.0M/hr');
    expect(formatPerHr(1_250_000)).toBe('+1.3M/hr');
    expect(formatPerHr(-2_400_000)).toBe('-2.4M/hr');
    expect(formatPerHr(750_000)).toBe('+750k/hr');
    expect(formatPerHr(-4_200)).toBe('-4k/hr');
    expect(formatPerHr(0)).toBe('+0k/hr');
  });
});

import { describe, it, expect } from 'vitest';
import { computeGalaxyLayout } from '../../utils/galaxyLayout.js';

describe('computeGalaxyLayout', () => {
  it('returns one node per distinct system', () => {
    const nodes = computeGalaxyLayout(
      ['X1-A', 'X1-B', 'X1-C', 'X1-A'],
      [{ from: 'X1-A', to: 'X1-B' }],
    );
    expect(nodes.map((n) => n.symbol).sort()).toEqual(['X1-A', 'X1-B', 'X1-C']);
  });

  it('is deterministic — identical input yields identical coordinates', () => {
    const args = [['X1-A', 'X1-B', 'X1-C'], [{ from: 'X1-A', to: 'X1-B' }]] as const;
    const a = computeGalaxyLayout([...args[0]], [...args[1]]);
    const b = computeGalaxyLayout([...args[0]], [...args[1]]);
    expect(a).toEqual(b);
  });

  it('produces finite, integer coordinates', () => {
    const nodes = computeGalaxyLayout(['X1-A', 'X1-B'], [{ from: 'X1-A', to: 'X1-B' }]);
    for (const n of nodes) {
      expect(Number.isFinite(n.x)).toBe(true);
      expect(Number.isInteger(n.x)).toBe(true);
      expect(Number.isInteger(n.y)).toBe(true);
    }
  });

  it('empty input yields empty layout', () => {
    expect(computeGalaxyLayout([], [])).toEqual([]);
  });
});

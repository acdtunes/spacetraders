import { describe, it, expect } from 'vitest';
import { computeGalaxyLayout } from '../../utils/galaxyLayout.js';
import { layoutWithAnchors } from '../../utils/galaxyLayout.js';

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

describe('layoutWithAnchors', () => {
  const edges = [
    { from: 'X1-AA', to: 'X1-BB' },
    { from: 'X1-BB', to: 'X1-CC' },
  ];

  it('passes real coordinates through verbatim with layout=real', () => {
    const real = new Map([
      ['X1-AA', { x: 100, y: 200 }],
      ['X1-BB', { x: -50, y: 80 }],
      ['X1-CC', { x: 0, y: -300 }],
    ]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-BB', 'X1-CC'], edges);
    const aa = nodes.find((n) => n.symbol === 'X1-AA')!;
    expect(aa).toMatchObject({ x: 100, y: 200, layout: 'real' });
  });

  it('anchors an unknown near its real neighbours, flagged force', () => {
    const real = new Map([
      ['X1-AA', { x: 0, y: 0 }],
      ['X1-CC', { x: 400, y: 0 }],
    ]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-BB', 'X1-CC'], edges);
    const bb = nodes.find((n) => n.symbol === 'X1-BB')!;
    expect(bb.layout).toBe('force');
    // Neighbour centroid is (200, 0); jitter is bounded by spread*0.06 + 100.
    expect(Math.hypot(bb.x - 200, bb.y - 0)).toBeLessThanOrEqual(400 * 0.06 + 100 + 1);
  });

  it('places a neighbourless unknown on a ring outside the real spread', () => {
    const real = new Map([['X1-AA', { x: 0, y: 0 }]]);
    const nodes = layoutWithAnchors(real, ['X1-AA', 'X1-ZZ'], []);
    const zz = nodes.find((n) => n.symbol === 'X1-ZZ')!;
    expect(zz.layout).toBe('force');
    expect(Math.hypot(zz.x, zz.y)).toBeGreaterThan(0);
  });

  it('degenerates to the classic force layout when nothing is real, deterministically', () => {
    const a = layoutWithAnchors(new Map(), ['X1-AA', 'X1-BB'], edges);
    const b = layoutWithAnchors(new Map(), ['X1-AA', 'X1-BB'], edges);
    expect(a).toEqual(b);
    expect(a.every((n) => n.layout === 'force')).toBe(true);
  });
});

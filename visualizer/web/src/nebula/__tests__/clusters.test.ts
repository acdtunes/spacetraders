import { describe, it, expect } from 'vitest';
import { clustersFor } from '../clusters';

const topo = {
  homeSystem: 'S3',
  systems: [
    { symbol: 'S1', x: 0, y: 0 }, { symbol: 'S2', x: 10, y: 0 }, { symbol: 'S3', x: 20, y: 0 },
    { symbol: 'S4', x: 30, y: 0 }, { symbol: 'S5', x: 400, y: 400 }, // isolated
  ],
  edges: [
    { from: 'S1', to: 'S2' }, { from: 'S2', to: 'S3' }, { from: 'S3', to: 'S4' },
  ],
};

describe('clustersFor', () => {
  it('groups connected systems and isolates the unconnected', () => {
    const cs = clustersFor(topo as any);
    const byId = new Map(cs.map(c => [c.id, c]));
    expect(byId.get('S1')!.members).toEqual(['S1', 'S2', 'S3', 'S4']);
    expect(byId.get('S5')!.members).toEqual(['S5']);
  });
  it('is deterministic (same input → identical output)', () => {
    expect(clustersFor(topo as any)).toEqual(clustersFor(topo as any));
  });
  it('caps clusters at 8 members', () => {
    const big = {
      homeSystem: null,
      systems: Array.from({ length: 12 }, (_, i) => ({ symbol: `T${String(i).padStart(2, '0')}`, x: i, y: 0 })),
      edges: Array.from({ length: 11 }, (_, i) => ({ from: `T${String(i).padStart(2, '0')}`, to: `T${String(i + 1).padStart(2, '0')}` })),
    };
    const cs = clustersFor(big as any);
    expect(Math.max(...cs.map(c => c.members.length))).toBeLessThanOrEqual(8);
    expect(cs.reduce((n, c) => n + c.members.length, 0)).toBe(12);
  });
  it('marks the home cluster and computes centroids', () => {
    const cs = clustersFor(topo as any);
    const home = cs.find(c => c.isHome)!;
    expect(home.members).toContain('S3');
    expect(home.cx).toBeCloseTo((0 + 10 + 20 + 30) / 4);
  });
});

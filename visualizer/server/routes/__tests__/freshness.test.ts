import { describe, it, expect } from 'vitest';
import { deriveScoutStatus, shapeFreshnessResponse } from '../../utils/freshness.js';

describe('deriveScoutStatus', () => {
  it('manned when assigned_hull is set', () => {
    expect(deriveScoutStatus({ assigned_hull: 'TORWIND-9', reposition_container_id: null })).toBe('manned');
  });
  it('relay when unmanned but a reposition is airborne', () => {
    expect(deriveScoutStatus({ assigned_hull: null, reposition_container_id: 'jump-1' })).toBe('relay');
    expect(deriveScoutStatus({ assigned_hull: '', reposition_container_id: 'jump-1' })).toBe('relay');
  });
  it('unmanned when both are empty', () => {
    expect(deriveScoutStatus({ assigned_hull: null, reposition_container_id: null })).toBe('unmanned');
    expect(deriveScoutStatus({ assigned_hull: '', reposition_container_id: '' })).toBe('unmanned');
  });
});

describe('shapeFreshnessResponse', () => {
  const marketRows = [
    { system: 'X1-AA', total: '60', fresh: '41', freshest_at: '2026-07-17T12:03:11Z' },
    { system: 'X1-BB', total: '10', fresh: '0', freshest_at: '2026-07-17T08:00:00Z' },
  ];
  const scoutRows = [
    { system_symbol: 'X1-AA', assigned_hull: 'TORWIND-9', reposition_container_id: null, kind: 'standing' },
    { system_symbol: 'X1-ZZ', assigned_hull: null, reposition_container_id: null, kind: 'standing' },
  ];

  it('merges market aggregates with scout posts, computing pct', () => {
    const systems = shapeFreshnessResponse(marketRows, scoutRows);
    const aa = systems.find((s) => s.system === 'X1-AA')!;
    expect(aa).toMatchObject({ totalListings: 60, freshListings: 41, freshnessPct: 68 });
    expect(aa.freshestAt).toBe(new Date('2026-07-17T12:03:11Z').toISOString());
    expect(aa.scoutPost).toEqual({ status: 'manned', hull: 'TORWIND-9', kind: 'standing' });
    const bb = systems.find((s) => s.system === 'X1-BB')!;
    expect(bb.freshnessPct).toBe(0);
    expect(bb.scoutPost).toBeNull();
  });

  it('emits a zero-listing record for a posted system with no market rows (post visible on unsensed system)', () => {
    const systems = shapeFreshnessResponse(marketRows, scoutRows);
    const zz = systems.find((s) => s.system === 'X1-ZZ')!;
    expect(zz).toMatchObject({ totalListings: 0, freshListings: 0, freshnessPct: 0, freshestAt: null });
    expect(zz.scoutPost).toEqual({ status: 'unmanned', hull: null, kind: 'standing' });
  });

  it('skips malformed market rows', () => {
    const systems = shapeFreshnessResponse([{ system: null, total: 'x', fresh: 'y', freshest_at: null } as any], []);
    expect(systems).toEqual([]);
  });
});

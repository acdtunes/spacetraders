import { describe, it, expect } from 'vitest';
import { deriveScoutStatus, shapeFreshnessResponse, deriveRotationBound, ROTATION_BOUND_FLOOR_MINUTES, ROTATION_BOUND_CEILING_MINUTES } from '../../utils/freshness.js';

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

// sp-3fcdx — the rotation bound. The number this replaces was a literal 75 that
// outlived the gobot constant it claimed to mirror; the property that matters is
// that NO constant can reappear here, and that "nothing to measure" never
// masquerades as a measurement.
describe('deriveRotationBound', () => {
  it('reports an observed p95 as observed, rounded', () => {
    expect(deriveRotationBound(402.4)).toEqual({ minutes: 402, basis: 'observed' });
    expect(deriveRotationBound('187.6')).toEqual({ minutes: 188, basis: 'observed' });
  });

  it('tracks the map instead of pinning a number — a bigger p95 gives a bigger scale', () => {
    // The whole point: the same function answers a 1,000-market map and an
    // 8,000-market one differently, because it is measuring rather than asserting.
    const small = deriveRotationBound(190);
    const large = deriveRotationBound(1524);
    expect(large.minutes).toBeGreaterThan(small.minutes);
    expect([small.basis, large.basis]).toEqual(['observed', 'observed']);
  });

  it('calls a missing measurement "unknown", never an age', () => {
    // An empty market table must not be reported as a 15-minute rotation.
    for (const empty of [null, undefined, NaN, 0, -3, 'not-a-number']) {
      expect(deriveRotationBound(empty).basis).toBe('unknown');
    }
  });

  it('clamps only at the rendering guardrails, and says which one bound', () => {
    expect(deriveRotationBound(2)).toEqual({ minutes: ROTATION_BOUND_FLOOR_MINUTES, basis: 'floor' });
    expect(deriveRotationBound(99_999)).toEqual({ minutes: ROTATION_BOUND_CEILING_MINUTES, basis: 'ceiling' });
    // A healthy live rotation (~6.7h observed 2026-07-30) sits far inside both,
    // so neither guardrail is doing the scaling in normal operation.
    expect(deriveRotationBound(402).basis).toBe('observed');
  });
});

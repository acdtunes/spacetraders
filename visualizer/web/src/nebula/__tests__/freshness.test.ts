import { describe, it, expect } from 'vitest';
import {
  DARK,
  FRESHNESS_RAMP,
  clusterFreshnessFor,
  formatAge,
  rampColor,
  systemFreshnessFor,
  type SystemFreshness,
} from '../freshness';
import type { SystemFreshnessRecord } from '../../types/flows';

const rec = (over: Partial<SystemFreshnessRecord> = {}): SystemFreshnessRecord => ({
  system: 'X1-AA',
  totalListings: 40,
  freshListings: 20,
  freshnessPct: 50,
  freshestAt: new Date('2026-07-30T12:00:00Z').toISOString(),
  scoutPost: null,
  ...over,
});

const NOW = Date.parse('2026-07-30T12:00:00Z');

describe('systemFreshnessFor', () => {
  it('scales from freshestAt, not freshnessPct', () => {
    // Same pct, ages an order of magnitude apart: a pct-driven ramp would place
    // these two identically, which is the defect this function exists to avoid.
    const young = systemFreshnessFor(rec({ freshnessPct: 50, freshestAt: new Date(NOW - 30 * 60_000).toISOString() }), NOW, 600);
    const old = systemFreshnessFor(rec({ freshnessPct: 50, freshestAt: new Date(NOW - 480 * 60_000).toISOString() }), NOW, 600);
    expect(young.t).toBeCloseTo(0.05, 5);
    expect(old.t).toBeCloseTo(0.8, 5);
  });

  it('renders a system with no freshestAt as DARK, never as maximally stale', () => {
    const f = systemFreshnessFor(rec({ freshestAt: null }), NOW, 600);
    expect(f.priced).toBe(false);
    expect(f.t).toBeNull();
    expect(f.ageMinutes).toBeNull();
  });

  it('treats a zero-listing record as dark even when it carries a timestamp', () => {
    const f = systemFreshnessFor(rec({ totalListings: 0, freshestAt: new Date(NOW).toISOString() }), NOW, 600);
    expect(f.priced).toBe(false);
  });

  it('is dark for a missing record — the endpoint omits unsensed systems', () => {
    expect(systemFreshnessFor(undefined, NOW, 600).priced).toBe(false);
    expect(systemFreshnessFor(null, NOW, 600).priced).toBe(false);
  });

  it('keeps a scout post on a dark system — the endpoint bends its rule for exactly this', () => {
    const f = systemFreshnessFor(
      rec({ totalListings: 0, freshestAt: null, scoutPost: { status: 'manned', hull: 'T-9', kind: 'standing' } }),
      NOW,
      600,
    );
    expect(f.priced).toBe(false);
    expect(f.scoutPost).toBe('manned');
  });

  it('clamps beyond the bound to 1 and never goes negative on clock skew', () => {
    expect(systemFreshnessFor(rec({ freshestAt: new Date(NOW - 5000 * 60_000).toISOString() }), NOW, 600).t).toBe(1);
    expect(systemFreshnessFor(rec({ freshestAt: new Date(NOW + 60_000).toISOString() }), NOW, 600).t).toBe(0);
  });

  it('tracks the live bound — the same age lands differently as the map grows', () => {
    const age = rec({ freshestAt: new Date(NOW - 120 * 60_000).toISOString() });
    // A 2h-old scan is nearly stale on a small map's fast rotation and barely
    // aged on a large map's slow one. A hardcoded scale could not express this.
    expect(systemFreshnessFor(age, NOW, 150).t).toBeCloseTo(0.8, 5);
    expect(systemFreshnessFor(age, NOW, 1200).t).toBeCloseTo(0.1, 5);
  });
});

describe('rampColor', () => {
  it('anchors both ends on the ramp and stays inside it', () => {
    expect(rampColor(0)).toBe(FRESHNESS_RAMP[0]);
    expect(rampColor(1)).toBe(FRESHNESS_RAMP[FRESHNESS_RAMP.length - 1]);
    expect(rampColor(-5)).toBe(FRESHNESS_RAMP[0]);
    expect(rampColor(9)).toBe(FRESHNESS_RAMP[FRESHNESS_RAMP.length - 1]);
    expect(rampColor(Number.NaN)).toBe(FRESHNESS_RAMP[0]);
  });

  it('is monotone in luminance so the scale reads as ordered', () => {
    const lum = (c: number) =>
      0.2126 * ((c >> 16) & 0xff) + 0.7152 * ((c >> 8) & 0xff) + 0.0722 * (c & 0xff);
    const steps = [0, 0.25, 0.5, 0.75, 1].map((t) => lum(rampColor(t)));
    for (let i = 1; i < steps.length; i++) expect(steps[i]).toBeLessThan(steps[i - 1]);
  });

  it('never reaches the backdrop — the stalest markets must stay visible', () => {
    // #070312 is the pixi clear colour. A ramp running to black would make the
    // worst state the least visible one.
    const stale = rampColor(1);
    expect((stale >> 16) & 0xff).toBeGreaterThan(0x30);
    expect(stale & 0xff).toBeGreaterThan(0x60);
  });
});

describe('clusterFreshnessFor', () => {
  const priced = (t: number): SystemFreshness => ({ priced: true, ageMinutes: t * 600, t, scoutPost: null });
  const map = (entries: [string, SystemFreshness][]) => new Map(entries);

  it('takes the WORST priced member, not the average', () => {
    // The point of the whole aggregation choice: a mean of these four is 0.2875,
    // which would render this cluster as comfortably fresh and hide the laggard.
    const cf = clusterFreshnessFor(['a', 'b', 'c', 'd'], map([
      ['a', priced(0.05)], ['b', priced(0.05)], ['c', priced(0.1)], ['d', priced(0.95)],
    ]));
    expect(cf.worstT).toBe(0.95);
    expect(cf.darkCount).toBe(0);
  });

  it('counts dark members separately rather than folding them into the age', () => {
    const cf = clusterFreshnessFor(['a', 'b', 'c', 'd'], map([['a', priced(0.2)], ['b', priced(0.3)]]));
    expect(cf.darkCount).toBe(2);
    expect(cf.darkRatio).toBe(0.5);
    // A dark member must NOT drag worstT toward 1 — that would assert an age.
    expect(cf.worstT).toBe(0.3);
  });

  it('reports worstT null when every member is dark', () => {
    const cf = clusterFreshnessFor(['a', 'b'], map([['a', DARK], ['b', DARK]]));
    expect(cf.worstT).toBeNull();
    expect(cf.darkRatio).toBe(1);
  });

  it('surfaces a scout post anywhere in the cluster', () => {
    const cf = clusterFreshnessFor(['a', 'b'], map([
      ['a', DARK], ['b', { ...DARK, scoutPost: 'relay' }],
    ]));
    expect(cf.hasScoutPost).toBe(true);
  });

  it('handles an empty cluster without dividing by zero', () => {
    const cf = clusterFreshnessFor([], new Map());
    expect(cf.darkRatio).toBe(0);
    expect(cf.worstT).toBeNull();
  });
});

describe('formatAge', () => {
  it('scales its unit and refuses to invent a number for null', () => {
    expect(formatAge(4)).toBe('4m');
    expect(formatAge(89)).toBe('89m');
    expect(formatAge(402)).toBe('6.7h');
    expect(formatAge(4320)).toBe('3.0d');
    expect(formatAge(null)).toBe('—');
    expect(formatAge(Number.NaN)).toBe('—');
  });
});

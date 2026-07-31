import { describe, it, expect } from 'vitest';
import {
  DARK,
  FRESHNESS_RAMP,
  clusterFreshnessFor,
  formatAge,
  rampColor,
  rampStep,
  systemFreshnessFor,
  type SystemFreshness,
} from '../freshness';
import type { SystemFreshnessRecord } from '../../types/flows';
import { AURA_ALPHA, CYAN as ORB_CYAN, ORB_HALO_ALPHA_ACTIVE } from '../layers/orbs';
import { BACKDROP_COLOR } from '../layers/backdrop';
import {
  contrastRatio,
  deltaE2000,
  normalOver,
  rgb,
  rgbToLab,
  screenOver,
  simulateCvd,
} from './helpers/colour';

const BACKDROP = rgb(BACKDROP_COLOR);
/** A ramp step exactly as orbs.ts composites it: the aura's flat alpha, normal
 * blend, over the scene's clear colour. Every claim about this scale is made
 * about THIS, never about the constant. */
const renderedRamp = (step: number) => normalOver(step, BACKDROP, AURA_ALPHA);
const contrastOnBackdrop = (step: number, alpha: number) =>
  contrastRatio(normalOver(step, BACKDROP, alpha), BACKDROP);

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
    // worst state the least visible one. Stated as CONTRAST, not as per-channel
    // byte floors: the old form of this test asserted blue > 0x60, which is a
    // fact about the cyan ramp that happened to be there rather than about
    // visibility, and it would have blocked any ramp that was not blue-heavy
    // while passing a blue one that had gone invisible.
    expect(contrastOnBackdrop(rampColor(1), AURA_ALPHA)).toBeGreaterThanOrEqual(2);
    expect(contrastOnBackdrop(rampColor(0), AURA_ALPHA)).toBeGreaterThanOrEqual(2);
  });
});

// sp-9m0bd — THE CHECK THAT WOULD HAVE CAUGHT THE UNREADABLE SCALE. sp-voyz7
// shipped a ramp whose five steps were, as drawn, one cyan; it passed its unit
// tests because they only ever compared hex literals, and a hex literal is not
// what anybody looks at. Everything below composites the ramp the way the
// renderer does — at the mark's real alpha, over the real backdrop — and then
// measures perceptual distance. CIEDE2000 ΔE ≈ 2.3 is the just-noticeable
// difference for adjacent LARGE fields; these marks are small, so the bar here
// is deliberately well above it.
describe('FRESHNESS_RAMP is legible as a scale, as rendered', () => {
  it('separates adjacent steps well past the JND once composited', () => {
    for (let i = 0; i < FRESHNESS_RAMP.length - 1; i++) {
      const d = deltaE2000(
        renderedRamp(FRESHNESS_RAMP[i]),
        renderedRamp(FRESHNESS_RAMP[i + 1]),
      );
      expect(d).toBeGreaterThan(6);
    }
  });

  it('is monotone in BOTH lightness and chroma — an ordinal scale, not a hue wheel', () => {
    const lab = FRESHNESS_RAMP.map((c) => rgbToLab(renderedRamp(c)));
    for (let i = 1; i < lab.length; i++) {
      expect(lab[i][0]).toBeLessThan(lab[i - 1][0]);                       // L*
      expect(Math.hypot(lab[i][1], lab[i][2]))
        .toBeLessThan(Math.hypot(lab[i - 1][1], lab[i - 1][2]));           // C*
    }
  });

  it('keeps the fresh end off the ACTIVE ORB HALO — the sp-voyz7 collision', () => {
    // The ramp's head used to BE orbs.ts CYAN (0x22d3ee), so a freshly-scanned
    // traded system drew its mark in the exact colour of the halo drawn on top
    // of it. Byte inequality is not enough to prove that is fixed: two different
    // hexes can composite to the same pixel. Compare the composites.
    expect(FRESHNESS_RAMP[0]).not.toBe(ORB_CYAN);
    const halo = screenOver(ORB_CYAN, BACKDROP, ORB_HALO_ALPHA_ACTIVE);
    for (const c of FRESHNESS_RAMP) {
      expect(deltaE2000(renderedRamp(c), halo)).toBeGreaterThan(20);
    }
  });

  it('survives colour-vision deficiency — adjacent steps stay apart for a dichromat', () => {
    // A single-hue ramp that relies on hue alone collapses here; this one is
    // carried by lightness and chroma, so it does not. (The cyan ramp scored
    // ~3 ΔE under deuteranopia — no usable scale at all.)
    for (const kind of ['deuteranopia', 'protanopia'] as const) {
      for (let i = 0; i < FRESHNESS_RAMP.length - 1; i++) {
        const d = deltaE2000(
          simulateCvd(renderedRamp(FRESHNESS_RAMP[i]), kind),
          simulateCvd(renderedRamp(FRESHNESS_RAMP[i + 1]), kind),
        );
        expect(d).toBeGreaterThan(5);
      }
    }
  });

  it('stays clear of every other mark the scene draws at the same place', () => {
    // A freshness mark that lands on the dark ring's colour, the dormant orb's
    // ember or the scout diamond's gold is a mark that says the wrong thing.
    // Each neighbour is composited the way IT renders, not taken as a literal.
    const neighbours = {
      dormantOrbHalo: screenOver(0x475569, BACKDROP, 0.45),
      latticeThread: normalOver(0x475569, BACKDROP, 0.3),
      darkRing: normalOver(0x8b95ab, BACKDROP, 0.5),
      scoutDiamond: normalOver(0xe8d9a0, BACKDROP, 0.95),
      label: rgb(0x8b9cc0),
    };
    for (const [who, colour] of Object.entries(neighbours)) {
      for (const c of FRESHNESS_RAMP) {
        const d = deltaE2000(renderedRamp(c), colour);
        expect(d, `ramp step vs ${who}`).toBeGreaterThan(12);
      }
    }
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

// sp-voyz7 — the priced CONTOUR quantises the ramp, so the step function is load
// bearing: an off-by-one at either edge would either drop the freshest systems
// into the second-brightest batch or index past the ramp and crash the build.
describe('rampStep', () => {
  it('puts each fifth of the scale in its own step, boundaries included', () => {
    expect(rampStep(0)).toBe(0);
    expect(rampStep(0.199)).toBe(0);
    expect(rampStep(0.2)).toBe(1);
    expect(rampStep(0.4)).toBe(2);
    expect(rampStep(0.6)).toBe(3);
    expect(rampStep(0.8)).toBe(4);
  });

  it('never indexes past the ramp — t = 1 is the last step, not a sixth one', () => {
    expect(rampStep(1)).toBe(FRESHNESS_RAMP.length - 1);
    expect(rampStep(1.5)).toBe(FRESHNESS_RAMP.length - 1);
    expect(FRESHNESS_RAMP[rampStep(1)]).toBeDefined();
  });

  it('clamps below and treats a non-finite t as freshest, matching rampColor', () => {
    expect(rampStep(-3)).toBe(0);
    expect(rampStep(Number.NaN)).toBe(0);
    expect(rampStep(Number.POSITIVE_INFINITY)).toBe(0);
  });

  it('agrees with rampColor at every step head — one scale, two readouts', () => {
    for (let i = 0; i < FRESHNESS_RAMP.length; i++) {
      const t = i / FRESHNESS_RAMP.length;
      expect(rampStep(t)).toBe(i);
    }
    // ...and the step's colour is the ramp entry the continuous read passes
    // through, so the contour never disagrees with the glow it edges.
    expect(FRESHNESS_RAMP[rampStep(0)]).toBe(rampColor(0));
  });
});

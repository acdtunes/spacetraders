// Pure market-freshness encoding for the nebula: how a /api/flows/freshness
// record becomes a colour, and how a cluster of systems aggregates without
// lying. No pixi, no fetching, no side effects.
//
// THE ENCODING, AND WHY IT IS SHAPED THIS WAY
//
// Two facts have to reach the eye at once — whether we have prices for a system
// at all, and how old they are — and the map already spends colour and size on
// economics (orb tint = active/dormant, orb radius = activity share; cluster
// aura tint = its backdrop fog's hue, aura alpha = profit share, aura radius =
// member count). Layering an age ramp onto any of those would put two meanings
// on one channel and produce exactly the unreadable map this view has been
// complained about for before. So freshness takes its OWN mark at every band —
// a soft band in the annulus just outside the orb at REGION, a ring outside the
// aura at GALAXY — and touches none of the existing channels.
//
// PRICED vs DARK IS A DIFFERENCE IN FORM, NOT A POINT ON THE RAMP. Both states
// draw in the SAME annulus outside the orb: a priced system fills it with a soft
// band whose colour runs the ramp, a dark system outlines it with a hollow
// dashed ring and fills nothing. That is deliberate and it is the whole
// point of the request: a system with no price data is UNSENSED, not "maximally
// stale", and rendering it at the far end of the ramp would assert an age nobody
// measured. Absence of data gets absence of glow.
//
// The ramp itself is a single-family ordinal scale (green, monotone lightness
// AND monotone chroma, validated: see FRESHNESS_RAMP). Bright and saturated =
// freshly priced, dim and dull = fading out of the rotation's reach, then dark =
// we cannot see this market at all. The progression reads as one continuous
// story about visibility.
import type { ScoutPostStatus, SystemFreshnessRecord } from '../types/flows';

/**
 * The freshness ramp: fresh → stale, five steps.
 *
 * GREEN, NOT CYAN, AND THAT IS THE LOAD-BEARING PART (sp-9m0bd). The ramp used
 * to open on 0x22d3ee — byte-identical to orbs.ts CYAN, the ACTIVE orb halo
 * tint — so a freshly-scanned traded system drew its freshness mark in the
 * exact colour of the thing on top of it. Composited as they actually render
 * (mark at its own alpha, halo at 0.85, both over the #070312 backdrop) the two
 * measured ΔE2000 = 2.9 apart: indistinguishable. They are now 26.8 apart.
 *
 * Chosen by search over CIE LCh, not by eye, against every constraint this
 * scene imposes — and every number below is the colour AS RENDERED at
 * AURA_ALPHA, normal blend, over the backdrop, never the hex literal, because
 * the hex is not what anybody looks at:
 *
 *   monotone L*        64.5 → 34.5   (the ordinal correctness condition)
 *   monotone C*        44.4 → 16.3   (bright+saturated = fresh, dull = stale)
 *   adjacent ΔE2000    8.22 / 8.17 / 8.92 / 8.44   (was 7.6 / 8.9 / 8.3 / 9.3)
 *   contrast, backdrop 7.46 → 2.57   (both ends clear the ≥ 2:1 bar)
 *   luminance          148.7 → 80.8  (EVERY step stays above the dark ring's
 *                                     measured 78.3 — see below)
 *   ΔE vs active halo  26.8          (was 2.9 — the collision above)
 *   ΔE vs the nearest OTHER mark in the scene: 26.1 (the home ring)
 *   ΔE adjacent under CVD: deuteranopia 6.71, protanopia 6.74, tritanopia 7.77
 *                          (was 3.05 / 3.37 / 6.89 — a colour-blind reader had
 *                           no ramp at all)
 *
 * THE LUMINANCE FLOOR IS WHAT PINS THIS RAMP, and it is sp-voyz7's invariant
 * doing its job: absence must not be drawn louder than presence. The dark ring
 * peaks at 78.3 in the same annulus, so no priced step may render below it —
 * which sets a floor on the ramp's stale end, and that floor is why AURA_ALPHA
 * is 0.7 and not lower. Every candidate at 0.60 and 0.65 puts the stalest priced
 * systems UNDER the dark ring and re-opens the defect sp-voyz7 was raised to fix.
 *
 * THE ADJACENT ΔE ONLY BOUGHT +8%, AND THAT IS THE HONEST STORY OF THIS RAMP.
 * A sweep of the whole LCh space under those constraints tops out near ΔE 9 per
 * step: five steps have to fit in the L* window between "not the orb halo" and
 * "not quieter than absence", which is about 30 L* points, and 30 over four gaps
 * is 7.5. Recolouring alone was never going to make this scale legible. What
 * made it illegible was carrying ΔE 8 on a 1.3px hairline that no reader ever
 * sees beside another one — measured on the live map, adjacent steps came off
 * the framebuffer 2.2 ΔE apart against the 9.3 the palette promised. The fix for
 * that is the MARK (orbs.ts AURA_STOPS: a filled band with real area, one colour
 * per system, quantised so a mark wears a legend colour exactly); this ramp is
 * the other half.
 *
 * The stale end deliberately stops well short of the backdrop rather than fading
 * to black: a run to near-black would make the STALEST systems the least visible
 * ones, inverting the salience the aura exists to provide.
 */
export const FRESHNESS_RAMP = [0x58f8ab, 0x6fd585, 0x75b467, 0x729450, 0x68753e] as const;

/** The dark (unsensed) mark's colour — a neutral slate deliberately OFF the
 * ramp, carried on a hollow dashed ring so the state is distinguishable by form
 * even where colour is not (CVD, dimmed layer, a screenshot in grayscale). */
export const DARK_COLOR = 0x8b95ab;

/** Scout-post actuator marker. A post on a dark system is the interesting case —
 * an actuator is placed but has not landed its first scan — so the marker is
 * drawn independently of priced/dark rather than as part of either. */
export const SCOUT_COLOR = 0xe8d9a0;

/**
 * Which of the five ramp steps a position falls in — 0 = just scanned, 4 = at or
 * past the bound. Clamped, and NaN reads as step 0 (the same convention
 * `rampColor` uses for a non-finite t).
 *
 * THE REGION AURA QUANTISES TO THESE STEPS RATHER THAN INTERPOLATING, and that
 * is a legibility decision, not a downgrade (sp-9m0bd). Adjacent steps are only
 * ~9 ΔE apart — that is the ceiling this scene's constraints allow, see
 * FRESHNESS_RAMP — so a continuously interpolated tint lands BETWEEN two legend
 * stops and is by construction less separable than either. Quantising makes a
 * mark wear a legend colour exactly, which is the only way "match the mark to
 * the scale" is a task a reader can actually perform. The continuous position
 * is still what `t` carries, and the GALAXY band's per-cluster gauge still
 * interpolates — one cluster ring is read against itself over time, not matched
 * against a five-stop legend at density.
 */
export function rampStep(t: number): number {
  const clamped = Number.isFinite(t) ? Math.min(1, Math.max(0, t)) : 0;
  return Math.min(FRESHNESS_RAMP.length - 1, Math.floor(clamped * FRESHNESS_RAMP.length));
}

/** Linear interpolation across FRESHNESS_RAMP for t ∈ [0,1] (clamped). */
export function rampColor(t: number): number {
  const clamped = Number.isFinite(t) ? Math.min(1, Math.max(0, t)) : 0;
  const last = FRESHNESS_RAMP.length - 1;
  const pos = clamped * last;
  const i = Math.min(last - 1, Math.floor(pos));
  const f = pos - i;
  const a = FRESHNESS_RAMP[i];
  const b = FRESHNESS_RAMP[i + 1];
  const mix = (shift: number) => {
    const av = (a >> shift) & 0xff;
    const bv = (b >> shift) & 0xff;
    return Math.round(av + (bv - av) * f) & 0xff;
  };
  return (mix(16) << 16) | (mix(8) << 8) | mix(0);
}

/**
 * A system's freshness state.
 *
 * `priced: false` is the dark state and carries no `t` — there is no age to
 * place on the ramp, and inventing one is the defect this type prevents.
 */
export interface SystemFreshness {
  priced: boolean;
  /** Age of the newest listing in minutes; null when dark. */
  ageMinutes: number | null;
  /** Position on the ramp, 0 = just scanned, 1 = at/past the bound; null when dark. */
  t: number | null;
  scoutPost: ScoutPostStatus | null;
}

export const DARK: SystemFreshness = { priced: false, ageMinutes: null, t: null, scoutPost: null };

/**
 * Fold one freshness record into a render state against the live rotation bound.
 *
 * Scaled from `freshestAt` and NOT from `freshnessPct`: the pct is a binary share
 * against a single cutoff, so it can only ever express "most of this system is
 * inside/outside the gate" — it cannot express a continuous ramp, and two systems
 * scanned 20 minutes and 8 hours ago can both report 0%.
 *
 * A record with no `freshestAt` (or none at all — the endpoint omits zero-listing
 * systems by design) is DARK, whatever else it carries. A scout post survives
 * that: the endpoint emits a record for a posted system before its first scan
 * lands precisely so the actuator can be drawn, and that record is dark-with-a-post.
 */
export function systemFreshnessFor(
  record: SystemFreshnessRecord | null | undefined,
  nowMs: number,
  boundMinutes: number,
): SystemFreshness {
  const scoutPost = record?.scoutPost?.status ?? null;
  const freshestMs = record?.freshestAt ? Date.parse(record.freshestAt) : NaN;
  if (record == null || record.totalListings <= 0 || Number.isNaN(freshestMs)) {
    return { priced: false, ageMinutes: null, t: null, scoutPost };
  }
  const ageMinutes = Math.max(0, (nowMs - freshestMs) / 60_000);
  const bound = boundMinutes > 0 ? boundMinutes : 1;
  return { priced: true, ageMinutes, t: Math.min(1, ageMinutes / bound), scoutPost };
}

/**
 * A cluster's freshness — the GALAXY band's aggregate.
 *
 * DELIBERATELY NOT AN AVERAGE. A mean age over a cluster hides a dark system
 * inside a fresh neighbourhood and dilutes one badly-lagging market into
 * invisibility, which is precisely the thing the map exists to surface. Two
 * independent facts are kept instead:
 *
 *   worstT   — the OLDEST priced member's ramp position, so one lagging system
 *              colours its cluster and cannot be averaged away. Clusters are
 *              capped at 8 members (clusters.ts MAX_CLUSTER), so a worst case is
 *              a claim about a small legible neighbourhood, not a whole region.
 *   darkRatio — the share of members with no price data at all, drawn as arc
 *              LENGTH on a separate dashed ring. Length, not opacity: an absent
 *              market is a countable fact and deserves a countable channel.
 *
 * worstT is null when every member is dark — there is no age to show, and the
 * dark ring alone (a full circle) says so.
 */
export interface ClusterFreshness {
  members: number;
  darkCount: number;
  darkRatio: number;
  worstT: number | null;
  /** Any member carrying a scout post — the cluster is being actively sensed. */
  hasScoutPost: boolean;
}

export function clusterFreshnessFor(
  members: readonly string[],
  bySystem: ReadonlyMap<string, SystemFreshness>,
): ClusterFreshness {
  let darkCount = 0;
  let worstT: number | null = null;
  let hasScoutPost = false;
  for (const m of members) {
    const f = bySystem.get(m) ?? DARK;
    if (f.scoutPost != null) hasScoutPost = true;
    if (!f.priced || f.t == null) {
      darkCount += 1;
      continue;
    }
    if (worstT == null || f.t > worstT) worstT = f.t;
  }
  const total = members.length;
  return {
    members: total,
    darkCount,
    darkRatio: total > 0 ? darkCount / total : 0,
    worstT,
    hasScoutPost,
  };
}

/** Compact age for legends and tooltips: `4m`, `52m`, `6.7h`, `2.1d`. */
export function formatAge(minutes: number | null | undefined): string {
  if (minutes == null || !Number.isFinite(minutes) || minutes < 0) return '—';
  if (minutes < 90) return `${Math.round(minutes)}m`;
  const hours = minutes / 60;
  if (hours < 48) return `${hours.toFixed(1)}h`;
  return `${(hours / 24).toFixed(1)}d`;
}

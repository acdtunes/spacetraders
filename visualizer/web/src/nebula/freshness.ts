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
// a halo behind the orb at REGION, a ring outside the aura at GALAXY — and
// touches none of the existing channels.
//
// PRICED vs DARK IS A DIFFERENCE IN FORM, NOT A POINT ON THE RAMP. A priced
// system draws a filled glow whose colour runs the ramp; a dark system draws a
// hollow dashed ring and no glow at all. That is deliberate and it is the whole
// point of the request: a system with no price data is UNSENSED, not "maximally
// stale", and rendering it at the far end of the ramp would assert an age nobody
// measured. Absence of data gets absence of glow.
//
// The ramp itself is a single-hue ordinal scale (cyan family, monotone
// lightness, validated: adjacent ΔL clear, both ends ≥ 2:1 against the #070312
// backdrop). Bright and saturated = freshly priced, dim and dull = fading out of
// the rotation's reach, then dark = we cannot see this market at all. The
// progression reads as one continuous story about visibility, and it keeps the
// SYSTEM-band drilldown's existing convention that cyan means fresh.
import type { ScoutPostStatus, SystemFreshnessRecord } from '../types/flows';

/**
 * The freshness ramp: fresh → stale, five steps.
 *
 * Single hue by construction (hue spread 32°, cyan family) with monotone
 * lightness — the correctness condition for a sequential/ordinal scale, and the
 * reason this is not a cyan→amber→red traffic light. The stale end deliberately
 * stops well short of the backdrop rather than fading to black: a run to
 * near-black would make the STALEST systems the least visible ones, inverting
 * the salience the aura exists to provide. Step 0 is the codebase's CYAN
 * (0x22d3ee) so the galaxy ramp and the SYSTEM-band rings start from one colour.
 */
export const FRESHNESS_RAMP = [0x22d3ee, 0x2eb6d6, 0x3799bd, 0x3d7ea3, 0x41648a] as const;

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
 * The glow interpolates continuously; the priced CONTOUR quantises to these
 * steps. That is not a downgrade of the encoding: the scale is ordinal by
 * construction (five steps, single hue, monotone lightness), a pixi Graphics
 * strokes its whole accumulated path with ONE style, and five legible batches
 * say more than 265 nearly-identical strokes would. The continuous read still
 * lives on the glow underneath.
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

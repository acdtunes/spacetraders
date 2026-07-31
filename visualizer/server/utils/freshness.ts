export type ScoutStatus = 'manned' | 'relay' | 'unmanned';

// ---- The rotation bound ------------------------------------------------------
//
// How old a market row may be before it is older than the scan rotation itself
// can explain. It is what the freshness aura's full scale MUST track, and it is
// emphatically not a constant: the marketscan budget makes each market's scan
// interval an OUTPUT of budget ÷ markets-known, so any minute count chosen at
// one map size is wrong at every later one. A fixed 75 minutes cost ~87% of
// trade throughput on 2026-07-30 (sp-k4z5b) and would paint essentially this
// whole map red — the observed median listing age is already ~4h.
//
// WHY OBSERVED RATHER THAN THE SOLVER'S OWN FORMULA. gobot computes the exact
// bound as marketscan.MaxStaleness = marketsKnown × ValueClampR ÷ RateReqPerSec.
// Only the first term is reachable from here: ValueClampR and RateReqPerSec live
// in the daemon's config.yaml (live: 0.70 req/s, clamp defaulted to 8) and are
// published through no table, no metric and no endpoint the visualizer can read —
// ScanBudget.Snapshot() has no caller outside its own tests. Importing those two
// numbers as visualizer constants would re-commit the exact sin this function
// exists to retire, and at 2× error today (the default is 0.35, the live value
// 0.70).
//
// So the bound is MEASURED instead of modelled: the p95 age of the era's own
// market rows. It carries the same dependence on map size that MaxStaleness does
// and in the same direction — more markets, longer rotation, wider scale — while
// naming no constant that can silently go stale. It is also the number that makes
// the aura legible: the theoretical bound is a worst case ~6× the age the
// rotation actually delivers, so a scale set to it would render the entire map in
// the bottom sixth of the ramp — uniformly "fresh", exactly as uninformative as
// the 75-minute version was uniformly "stale", only in the opposite direction.
//
// The cost of measuring is that a genuinely stalled rotation rescales the ramp
// instead of saturating it. That is why the scale is RETURNED and rendered in the
// legend rather than kept private: a stall shows up as the printed full scale
// jumping from hours to days, which is a louder signal than a red map — a red map
// is what the 75-minute bug produced for months and readers learned to ignore it.
export type RotationBoundBasis = 'observed' | 'floor' | 'ceiling' | 'unknown';

/** Rendering guardrails, NOT the scale. Below the floor every system is fresh and
 * the ramp is drawing noise; above the ceiling the rotation has failed so
 * comprehensively that saturating is the honest answer. Both sit far outside any
 * healthy rotation (observed p95 on 2026-07-30: ~6.7h), so neither normally binds. */
export const ROTATION_BOUND_FLOOR_MINUTES = 15;
export const ROTATION_BOUND_CEILING_MINUTES = 7 * 24 * 60;

export interface RotationBound {
  minutes: number;
  basis: RotationBoundBasis;
}

/**
 * Clamp an observed p95 listing age (minutes) into a renderable full scale.
 *
 * `basis` is reported rather than inferred so the legend can be honest about
 * which number the reader is looking at. An absent/degenerate p95 — no market
 * rows at all — is 'unknown': there is nothing to scale against, and the floor is
 * returned only so the ramp has a denominator, never as a claim about the map.
 */
export function deriveRotationBound(p95Minutes: unknown): RotationBound {
  const p95 = Number(p95Minutes);
  if (!Number.isFinite(p95) || p95 <= 0) {
    return { minutes: ROTATION_BOUND_FLOOR_MINUTES, basis: 'unknown' };
  }
  if (p95 < ROTATION_BOUND_FLOOR_MINUTES) {
    return { minutes: ROTATION_BOUND_FLOOR_MINUTES, basis: 'floor' };
  }
  if (p95 > ROTATION_BOUND_CEILING_MINUTES) {
    return { minutes: ROTATION_BOUND_CEILING_MINUTES, basis: 'ceiling' };
  }
  return { minutes: Math.round(p95), basis: 'observed' };
}

export interface SystemFreshnessRecord {
  system: string;
  totalListings: number;
  freshListings: number;
  freshnessPct: number; // round(100 * fresh / total); 0 when total is 0
  freshestAt: string | null;
  scoutPost: { status: ScoutStatus; hull: string | null; kind: string } | null;
}

// ScoutPostModel semantics: assigned_hull set => manned; else an airborne
// reposition relay => relay; else unmanned.
export function deriveScoutStatus(row: { assigned_hull?: string | null; reposition_container_id?: string | null }): ScoutStatus {
  if (row.assigned_hull) return 'manned';
  if (row.reposition_container_id) return 'relay';
  return 'unmanned';
}

// Merge the grouped market aggregation with scout_posts rows. Systems with
// zero listings are omitted (dark = unsensed) UNLESS they carry a scout post —
// the actuator marker must render even before its first scan lands.
// Contract: both inputs MUST already be era-scoped by the caller
// (`era_id = $current OR era_id IS NULL`, ScoutPostModel invariant sp-njpu);
// this shaper emits a record for every post row it receives, so an unscoped
// scout query would surface dead-era posts as phantom zero-listing systems.
export function shapeFreshnessResponse(
  marketRows: { system: unknown; total: unknown; fresh: unknown; freshest_at: unknown }[],
  scoutRows: { system_symbol: string; assigned_hull?: string | null; reposition_container_id?: string | null; kind?: string | null }[],
): SystemFreshnessRecord[] {
  const bySystem = new Map<string, SystemFreshnessRecord>();
  for (const r of marketRows) {
    const system = typeof r.system === 'string' ? r.system : '';
    const total = Number(r.total);
    const fresh = Number(r.fresh);
    if (!system || !Number.isFinite(total) || !Number.isFinite(fresh) || total <= 0) continue;
    const freshestMs = r.freshest_at ? Date.parse(String(r.freshest_at)) : NaN;
    bySystem.set(system, {
      system,
      totalListings: total,
      freshListings: fresh,
      freshnessPct: Math.round((100 * fresh) / total),
      freshestAt: Number.isNaN(freshestMs) ? null : new Date(freshestMs).toISOString(),
      scoutPost: null,
    });
  }
  for (const s of scoutRows) {
    if (!s.system_symbol) continue;
    const post = { status: deriveScoutStatus(s), hull: s.assigned_hull || null, kind: s.kind ?? '' };
    const rec = bySystem.get(s.system_symbol);
    if (rec) rec.scoutPost = post;
    else
      bySystem.set(s.system_symbol, {
        system: s.system_symbol,
        totalListings: 0,
        freshListings: 0,
        freshnessPct: 0,
        freshestAt: null,
        scoutPost: post,
      });
  }
  return [...bySystem.values()].sort((a, b) => a.system.localeCompare(b.system));
}

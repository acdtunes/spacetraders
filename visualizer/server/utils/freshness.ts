export type ScoutStatus = 'manned' | 'relay' | 'unmanned';

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

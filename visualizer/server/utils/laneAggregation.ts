export interface TelemetryRow {
  tourId: string;
  shipSymbol: string;
  legIndex: number;
  waypoint: string;
  isBuy: boolean;
  realizedUnits: number;
  realizedUnitPrice: number;
  realizedAt: string; // ISO
}

export interface ArbRow {
  buyMarket: string;
  sellMarket: string;
  unitsSold: number;
  actualNetProfit: number;
  executedAt: string; // ISO
}

export interface LaneRecord {
  from: string;
  to: string;
  realizedUnits: number;
  realizedProfit: number;
  legCount: number;
}

const key = (from: string, to: string) => `${from} ${to}`;

// Fold telemetry legs + arb executions into directed waypoint lanes within the
// window. Telemetry: per (tour, ship), rows collapse to one representative
// waypoint + signed value per leg_index (sell +units*price, buy -units*price);
// consecutive legs form a directed lane and the DESTINATION leg's realized value
// is what the lane carries. Arb: one lane per successful execution.
export function aggregateLanes(
  telemetry: TelemetryRow[],
  arb: ArbRow[],
  windowStartMs: number,
  windowEndMs: number,
): LaneRecord[] {
  const lanes = new Map<string, LaneRecord>();
  const bump = (from: string, to: string, units: number, profit: number) => {
    const k = key(from, to);
    const rec = lanes.get(k) ?? { from, to, realizedUnits: 0, realizedProfit: 0, legCount: 0 };
    rec.realizedUnits += units;
    rec.realizedProfit += profit;
    rec.legCount += 1;
    lanes.set(k, rec);
  };

  const groups = new Map<string, TelemetryRow[]>();
  for (const r of telemetry) {
    const at = Date.parse(r.realizedAt);
    if (Number.isNaN(at) || at < windowStartMs || at > windowEndMs) continue;
    const gk = key(r.tourId, r.shipSymbol);
    const arr = groups.get(gk);
    if (arr) arr.push(r);
    else groups.set(gk, [r]);
  }

  for (const rows of groups.values()) {
    const byLeg = new Map<number, { waypoint: string; value: number; units: number; firstAt: number }>();
    for (const r of rows) {
      const signed = (r.isBuy ? -1 : 1) * r.realizedUnits * r.realizedUnitPrice;
      const at = Date.parse(r.realizedAt);
      const cur = byLeg.get(r.legIndex);
      if (!cur) {
        byLeg.set(r.legIndex, { waypoint: r.waypoint, value: signed, units: r.realizedUnits, firstAt: at });
      } else {
        cur.value += signed;
        cur.units += r.realizedUnits;
        if (at < cur.firstAt) {
          cur.firstAt = at;
          cur.waypoint = r.waypoint;
        }
      }
    }
    const legs = [...byLeg.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v);
    for (let i = 1; i < legs.length; i++) {
      const from = legs[i - 1].waypoint;
      const to = legs[i].waypoint;
      if (from === to) continue;
      bump(from, to, legs[i].units, legs[i].value);
    }
  }

  for (const a of arb) {
    const at = Date.parse(a.executedAt);
    if (Number.isNaN(at) || at < windowStartMs || at > windowEndMs) continue;
    if (!a.buyMarket || !a.sellMarket || a.buyMarket === a.sellMarket) continue;
    bump(a.buyMarket, a.sellMarket, a.unitsSold, a.actualNetProfit);
  }

  return [...lanes.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
}

export interface SystemActivityRecord {
  system: string;
  realizedProfit: number;
  legCount: number;
}

// "X1-AA-P1" -> "X1-AA" (SECTOR-SYSTEM-WAYPOINT); non-conforming pass through.
export function systemOfWaypoint(wp: string): string {
  const parts = wp.split('-');
  return parts.length >= 2 ? `${parts[0]}-${parts[1]}` : wp;
}

// Galaxy-level rollup: directed system→system lanes. Intra-system realizations
// are excluded — they light the node (see rollupSystemActivity), not an edge.
export function rollupSystemLanes(lanes: LaneRecord[]): LaneRecord[] {
  const out = new Map<string, LaneRecord>();
  for (const l of lanes) {
    const from = systemOfWaypoint(l.from);
    const to = systemOfWaypoint(l.to);
    if (from === to) continue;
    const k = key(from, to);
    const rec = out.get(k) ?? { from, to, realizedUnits: 0, realizedProfit: 0, legCount: 0 };
    rec.realizedUnits += l.realizedUnits;
    rec.realizedProfit += l.realizedProfit;
    rec.legCount += l.legCount;
    out.set(k, rec);
  }
  return [...out.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
}

// Per-system realized activity in the window, credited to the system where the
// value realized (the lane destination; intra-system lanes credit their own).
export function rollupSystemActivity(lanes: LaneRecord[]): SystemActivityRecord[] {
  const out = new Map<string, SystemActivityRecord>();
  for (const l of lanes) {
    const system = systemOfWaypoint(l.to);
    const rec = out.get(system) ?? { system, realizedProfit: 0, legCount: 0 };
    rec.realizedProfit += l.realizedProfit;
    rec.legCount += l.legCount;
    out.set(system, rec);
  }
  return [...out.values()].sort((a, b) => b.realizedProfit - a.realizedProfit);
}

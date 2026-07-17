export interface FillRecord {
  id: string;
  at: string; // ISO
  ship: string;
  good: string;
  isBuy: boolean;
  units: number;
  credits: number; // signed: sells +, buys −; arb rows carry net profit
  waypoint: string;
}

// Merge realized tour-leg trades with arb executions into one desc-by-time
// fills stream. Malformed rows are skipped; ids are stable per source row so
// the client can dedupe across polls.
export function mergeFills(
  telemetryRows: any[],
  arbRows: any[],
  limit: number,
): FillRecord[] {
  const out: FillRecord[] = [];
  for (const r of telemetryRows) {
    const at = r?.realized_at ? Date.parse(String(r.realized_at)) : NaN;
    const units = Number(r?.realized_units);
    const price = Number(r?.realized_unit_price);
    if (!r?.ship_symbol || !r?.good || Number.isNaN(at) || !Number.isFinite(units) || !Number.isFinite(price)) continue;
    const isBuy = Boolean(r.is_buy);
    out.push({
      id: `t-${r.id}`,
      at: new Date(at).toISOString(),
      ship: r.ship_symbol,
      good: r.good,
      isBuy,
      units,
      credits: (isBuy ? -1 : 1) * units * price,
      waypoint: r.waypoint ?? '',
    });
  }
  for (const r of arbRows) {
    const at = r?.executed_at ? Date.parse(String(r.executed_at)) : NaN;
    if (!r?.ship_symbol || !r?.good_symbol || Number.isNaN(at)) continue;
    out.push({
      id: `a-${r.id}`,
      at: new Date(at).toISOString(),
      ship: r.ship_symbol,
      good: r.good_symbol,
      isBuy: false,
      units: Number(r.units_sold) || 0,
      credits: Number(r.actual_net_profit) || 0,
      waypoint: r.sell_market ?? '',
    });
  }
  out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
  return out.slice(0, limit);
}

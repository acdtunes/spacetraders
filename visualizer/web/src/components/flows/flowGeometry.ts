import { NOIR } from '../../theme/noir';
import type { LiveFlow, LaneRecord, TopologyResponse } from '../../types/flows';

export interface Point {
  x: number;
  y: number;
}

// "X1-NK36-FE8A" -> "X1-NK36"
export function systemOf(waypoint: string): string {
  const parts = waypoint.split('-');
  return parts.length >= 2 ? `${parts[0]}-${parts[1]}` : waypoint;
}

export function buildSystemIndex(topology: TopologyResponse): Map<string, Point> {
  return new Map(topology.systems.map((s) => [s.symbol, { x: s.x, y: s.y }]));
}

// Galaxy-space position of a flow's hull. Interpolates the current leg between
// its endpoint SYSTEMS using departedAt/arrivesAt with the same clamp math as
// domain/ship.ts (before departure -> origin, after arrival -> destination).
// Intra-system legs collapse to the origin; with no leg, fall back to last-known
// PG nav system; otherwise null (position-only glyphs are dropped upstream).
export function projectFlowShip(
  flow: LiveFlow,
  systemPos: Map<string, Point>,
  nowMs: number,
): Point | null {
  const leg = flow.currentLeg;
  if (leg) {
    const fromSys = systemOf(leg.from);
    const toSys = systemOf(leg.to);
    const from = systemPos.get(fromSys);
    const to = systemPos.get(toSys);
    const dep = Date.parse(leg.departedAt);
    const arr = Date.parse(leg.arrivesAt);
    if (from && to && !Number.isNaN(dep) && !Number.isNaN(arr)) {
      if (fromSys === toSys) return { x: from.x, y: from.y };
      const progress = (nowMs - dep) / Math.max(arr - dep, 1);
      const tClamped = Math.max(0, Math.min(1, progress));
      return { x: from.x + (to.x - from.x) * tClamped, y: from.y + (to.y - from.y) * tClamped };
    }
  }
  const nav = flow.shipNav;
  if (nav) {
    const p = systemPos.get(nav.systemSymbol);
    if (p) return { x: p.x, y: p.y };
  }
  return null;
}

export function laneEndpoints(
  lane: LaneRecord,
  systemPos: Map<string, Point>,
): { from: Point; to: Point } | null {
  const from = systemPos.get(systemOf(lane.from));
  const to = systemPos.get(systemOf(lane.to));
  if (!from || !to) return null;
  return { from, to };
}

// Profit -> Noir color ramp: loss dim, then good-green, accent-blue, star-gold.
export function laneProfitColor(profit: number): string {
  if (profit <= 0) return NOIR.dim;        // #5A6478
  if (profit < 50_000) return NOIR.good;   // #3DD68C
  if (profit < 250_000) return NOIR.accent; // #7DB1FF
  return NOIR.star;                          // #F5E9C8
}

// Log-scaled stroke width, divided by the current stage scale so lanes hold a
// roughly constant on-screen weight while zooming (the TradeRouteLayer idiom).
export function laneWidth(profit: number, scale: number): number {
  const mag = Math.min(6, Math.max(0.5, Math.log10(Math.abs(profit) + 10) - 1));
  return Math.max(0.5, mag / scale);
}

// Shift a segment sideways by `offsetPx` along its right-hand unit normal
// (dy,-dx)/|d|. The reversed direction yields the opposite normal, so a lane pair
// realized in BOTH directions (A->B and B->A are distinct aggregation keys) lands
// on opposite sides of the true line — parallel, never overlapping, never a
// directionless smear — with no cross-lane lookup. Degenerate (zero-length)
// segments pass through untouched so we never divide by zero.
export function offsetSegmentRight(from: Point, to: Point, offsetPx: number): { from: Point; to: Point } {
  const dx = to.x - from.x;
  const dy = to.y - from.y;
  const len = Math.hypot(dx, dy);
  if (len === 0) return { from, to };
  const nx = dy / len;
  const ny = -dx / len;
  return {
    from: { x: from.x + nx * offsetPx, y: from.y + ny * offsetPx },
    to: { x: to.x + nx * offsetPx, y: to.y + ny * offsetPx },
  };
}

// A point at fraction t (0..1) along from->to. Used to anchor a mid-lane
// arrowhead a little short of the destination node (the TradeRouteLayer idiom).
export function pointAlong(from: Point, to: Point, t: number): Point {
  return { x: from.x + (to.x - from.x) * t, y: from.y + (to.y - from.y) * t };
}

// Marching-dash phase for a lane. Canvas/Konva shift the dash pattern BACKWARD as
// the offset grows, so we negate: the phase decreases over time and the dashes
// crawl TOWARD the destination. Scale-normalized (÷ scale) so the on-screen crawl
// speed holds steady while zooming, matching the lane stroke-width convention.
const LANE_DASH_SPEED_PX_PER_SEC = 14;
export function laneDashPhase(nowMs: number, scale: number): number {
  return -((nowMs / 1000) * LANE_DASH_SPEED_PX_PER_SEC) / Math.max(scale, 1e-6);
}

// Remaining planned hops as polyline segments in system space. Consecutive hop
// waypoints (and the current leg's destination as the first anchor) are paired.
export function planPathPoints(flow: LiveFlow, systemPos: Map<string, Point>): number[][] {
  const anchors: string[] = [];
  if (flow.currentLeg) anchors.push(flow.currentLeg.to);
  for (const hop of flow.remainingHops) anchors.push(hop.waypoint);

  const segments: number[][] = [];
  for (let i = 1; i < anchors.length; i++) {
    const a = systemPos.get(systemOf(anchors[i - 1]));
    const b = systemPos.get(systemOf(anchors[i]));
    if (!a || !b) continue;
    segments.push([a.x, a.y, b.x, b.y]);
  }
  // When every hop resolves, the segment count equals remainingHops.length.
  return segments;
}

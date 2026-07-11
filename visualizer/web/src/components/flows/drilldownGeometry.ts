import type { FlowTranche, LaneRecord, LiveFlow } from '../../types/flows';
import { systemOf, type Point } from './flowGeometry';

// Geometry for the system drilldown: waypoints rendered TO SCALE from their real
// intra-system x/y, with realized lanes drawn between true positions. All pure so
// the fit transform + lane classification are unit-tested (the Konva scene stays
// thin, verified by screenshot).

// Minimal waypoint shape (the real Waypoint type is assignable to this).
export interface WaypointPoint {
  symbol: string;
  type?: string;
  x: number;
  y: number;
}

// A world->screen transform: screen = world * scale + offset.
export interface FitTransform {
  scale: number;
  offsetX: number;
  offsetY: number;
}

// Fit a cloud of world points into width x height, PRESERVING ASPECT RATIO
// (single scale on both axes) and centering the content, inset by `padding`.
// Degenerate inputs are handled: empty -> centered identity-ish; a single point
// or a colinear set (zero span on an axis) collapses to the other axis' scale,
// or scale 1 when all points coincide.
export function fitToViewport(
  points: WaypointPoint[] | Point[],
  width: number,
  height: number,
  padding = 40,
): FitTransform {
  if (points.length === 0) {
    return { scale: 1, offsetX: width / 2, offsetY: height / 2 };
  }
  let minX = Infinity;
  let maxX = -Infinity;
  let minY = Infinity;
  let maxY = -Infinity;
  for (const p of points) {
    if (p.x < minX) minX = p.x;
    if (p.x > maxX) maxX = p.x;
    if (p.y < minY) minY = p.y;
    if (p.y > maxY) maxY = p.y;
  }
  const spanX = maxX - minX;
  const spanY = maxY - minY;
  const availW = Math.max(1, width - padding * 2);
  const availH = Math.max(1, height - padding * 2);
  const scaleX = spanX > 0 ? availW / spanX : Infinity;
  const scaleY = spanY > 0 ? availH / spanY : Infinity;
  let scale = Math.min(scaleX, scaleY);
  if (!Number.isFinite(scale)) scale = 1; // all points coincident
  const contentW = spanX * scale;
  const contentH = spanY * scale;
  const offsetX = (width - contentW) / 2 - minX * scale;
  const offsetY = (height - contentH) / 2 - minY * scale;
  return { scale, offsetX, offsetY };
}

export function applyFit(p: Point, t: FitTransform): Point {
  return { x: p.x * t.scale + t.offsetX, y: p.y * t.scale + t.offsetY };
}

export function buildWaypointIndex(waypoints: WaypointPoint[]): Map<string, Point> {
  return new Map(waypoints.map((w) => [w.symbol, { x: w.x, y: w.y }]));
}

// The system's own jump gate — the anchor a cross-system leg exits toward. Falls
// back to the centroid of all waypoints when the gate is uncharted/missing, so an
// exit vector still points somewhere sane rather than vanishing.
export function gateAnchor(waypoints: WaypointPoint[]): Point | null {
  if (waypoints.length === 0) return null;
  const gate = waypoints.find((w) => w.type === 'JUMP_GATE');
  if (gate) return { x: gate.x, y: gate.y };
  const sum = waypoints.reduce((acc, w) => ({ x: acc.x + w.x, y: acc.y + w.y }), { x: 0, y: 0 });
  return { x: sum.x / waypoints.length, y: sum.y / waypoints.length };
}

export type LaneKind = 'intra' | 'exit' | 'entry' | 'external';

// Where a realized lane sits relative to the drilled-in system:
//  intra  — both endpoints are waypoints of this system (draw waypoint->waypoint)
//  exit   — leaves this system (from in-system endpoint toward the gate)
//  entry  — arrives into this system (from the gate toward the in-system endpoint)
//  external — neither endpoint here (not drawn in this view)
export function classifyLaneForSystem(lane: LaneRecord, systemSymbol: string): LaneKind {
  const fromIn = systemOf(lane.from) === systemSymbol;
  const toIn = systemOf(lane.to) === systemSymbol;
  if (fromIn && toIn) return 'intra';
  if (fromIn) return 'exit';
  if (toIn) return 'entry';
  return 'external';
}

export interface LaneSegment {
  from: Point;
  to: Point;
  kind: LaneKind;
  profit: number;
}

// Resolve a lane to a drawable world-space segment for this system, or null when
// it is external or a needed waypoint/gate is unavailable. Intra lanes connect the
// two true waypoint positions; exit/entry lanes connect the in-system endpoint and
// the gate anchor (direction encodes departure vs arrival).
export function resolveLaneSegment(
  lane: LaneRecord,
  systemSymbol: string,
  wpIndex: Map<string, Point>,
  gate: Point | null,
): LaneSegment | null {
  const kind = classifyLaneForSystem(lane, systemSymbol);
  if (kind === 'external') return null;
  if (kind === 'intra') {
    const from = wpIndex.get(lane.from);
    const to = wpIndex.get(lane.to);
    if (!from || !to) return null;
    return { from, to, kind, profit: lane.realizedProfit };
  }
  if (!gate) return null;
  if (kind === 'exit') {
    const from = wpIndex.get(lane.from);
    if (!from) return null;
    return { from, to: gate, kind, profit: lane.realizedProfit };
  }
  // entry
  const to = wpIndex.get(lane.to);
  if (!to) return null;
  return { from: gate, to, kind, profit: lane.realizedProfit };
}

// Flows resident in (or transiting) this system — same predicate as the galaxy
// detail grammar: last-known nav here, or a current leg with either endpoint here.
export function residentFlows(flows: LiveFlow[], systemSymbol: string): LiveFlow[] {
  return flows.filter(
    (f) =>
      f.shipNav?.systemSymbol === systemSymbol ||
      (f.currentLeg !== null &&
        (systemOf(f.currentLeg.from) === systemSymbol || systemOf(f.currentLeg.to) === systemSymbol)),
  );
}

// The actual waypoint a resident hull sits at (position truth from the PG nav
// join), when that waypoint is in this system; null otherwise.
export function hullWaypointInSystem(flow: LiveFlow, systemSymbol: string): string | null {
  const nav = flow.shipNav;
  if (nav && nav.waypointSymbol && systemOf(nav.waypointSymbol) === systemSymbol) {
    return nav.waypointSymbol;
  }
  return null;
}

// Ordered in-system waypoints of a flow's forward intent (waypoint granularity):
// the hull's current waypoint (if here) followed by the remaining-hop waypoints
// that fall in this system. The dashed overlay connects these — and, because it
// reads only from published remainingHops, it is naturally empty when the feed is
// lost (no flows) rather than fabricated.
export function intentWaypointsInSystem(flow: LiveFlow, systemSymbol: string): string[] {
  const anchors: string[] = [];
  const start = hullWaypointInSystem(flow, systemSymbol);
  if (start) anchors.push(start);
  for (const hop of flow.remainingHops) {
    if (systemOf(hop.waypoint) === systemSymbol) anchors.push(hop.waypoint);
  }
  // Drop a leading duplicate if the first hop equals the hull's waypoint.
  return anchors.filter((s, i) => i === 0 || s !== anchors[i - 1]);
}

// ---- The selected flow's ACTUAL ordered tour route ---------------------------
// Distinct from the aggregate realized lanes: this is the connected path a ship
// is actually flying — currentLeg + remainingHops at waypoint granularity — with
// its stops numbered in tour order and each stop's buy/sell intent distinguishable.

export type StopKind = 'buy' | 'sell' | 'mixed' | 'none';

// Classify a stop from its tranches: all buys, all sells, both (mixed), or none.
export function hopKind(tranches: FlowTranche[]): StopKind {
  if (tranches.length === 0) return 'none';
  let anyBuy = false;
  let anySell = false;
  for (const tr of tranches) {
    if (tr.isBuy) anyBuy = true;
    else anySell = true;
  }
  if (anyBuy && anySell) return 'mixed';
  return anyBuy ? 'buy' : 'sell';
}

export interface RouteStop {
  index: number;     // 1-based position in the full remaining tour
  waypoint: string;
  kind: StopKind;
}

// The full remaining tour as globally-ordered, buy/sell-classified stops. Reads
// only published remainingHops, so it is empty (never fabricated) on feed loss.
export function tourRouteStops(flow: LiveFlow): RouteStop[] {
  return flow.remainingHops.map((hop, i) => ({
    index: i + 1,
    waypoint: hop.waypoint,
    kind: hopKind(hop.tranches),
  }));
}

export type AnchorKind = StopKind | 'depart' | 'entry' | 'exit';

export interface RouteAnchor {
  index: number;            // stop number (0 for the leading depart/entry anchor)
  point: Point;             // world-space position
  kind: AnchorKind;
  waypoint: string | null;  // null for gate transit anchors (entry/exit)
}

// The selected flow's route as a drawable, ordered anchor list for THIS system.
// The path starts at the current leg's tail (the in-system departure waypoint, or
// the gate when the ship is arriving from elsewhere), threads each in-system stop
// at its true position, and terminates at the gate the first time the route leaves
// the system — so the connected path reads as "flew in from here, hits stops
// 1..n, then exits toward the gate". Cross-system continuations collapse to that
// single gate exit (their positions are off this view). Empty when nothing is
// drawable (no leg and no hops, or the needed waypoints/gate are unavailable).
export function tourRoutePathInSystem(
  flow: LiveFlow,
  systemSymbol: string,
  wpIndex: Map<string, Point>,
  gate: Point | null,
): RouteAnchor[] {
  const anchors: RouteAnchor[] = [];

  // Leading anchor: where the current leg is coming FROM.
  const leg = flow.currentLeg;
  if (leg) {
    const fromInSystem = systemOf(leg.from) === systemSymbol;
    const toInSystem = systemOf(leg.to) === systemSymbol;
    if (fromInSystem) {
      const p = wpIndex.get(leg.from);
      if (p) anchors.push({ index: 0, point: p, kind: 'depart', waypoint: leg.from });
    } else if (toInSystem && gate) {
      anchors.push({ index: 0, point: gate, kind: 'entry', waypoint: null });
    }
  }

  for (const stop of tourRouteStops(flow)) {
    if (systemOf(stop.waypoint) === systemSymbol) {
      const p = wpIndex.get(stop.waypoint);
      if (p) anchors.push({ index: stop.index, point: p, kind: stop.kind, waypoint: stop.waypoint });
    } else {
      // First stop that leaves the system: draw the exit toward the gate, then
      // stop — the remainder of the tour lives outside this view.
      if (gate) anchors.push({ index: stop.index, point: gate, kind: 'exit', waypoint: null });
      break;
    }
  }

  // A lone leading anchor with no stops is not a route — drop it.
  return anchors.some((a) => a.index > 0) ? anchors : [];
}

// The hull's real-time position WITHIN this system, in world (waypoint) space.
// An in-flight hull is interpolated along its current leg using departedAt/
// arrivesAt with the same clamp as domain/ship.ts (before departure -> origin,
// after arrival -> destination): between the two waypoints for an intra-system
// leg, or between the in-system waypoint and the gate when the leg enters/leaves
// the system. A docked/orbiting hull (no leg, or unusable timestamps) sits at its
// last-known waypoint. null when the hull has no resolvable presence here — so
// the caller renders nothing rather than guessing.
export function interpolateHullInSystem(
  flow: LiveFlow,
  systemSymbol: string,
  wpIndex: Map<string, Point>,
  gate: Point | null,
  nowMs: number,
): Point | null {
  const leg = flow.currentLeg;
  if (leg) {
    const fromInSystem = systemOf(leg.from) === systemSymbol;
    const toInSystem = systemOf(leg.to) === systemSymbol;
    let a: Point | null = null;
    let b: Point | null = null;
    if (fromInSystem && toInSystem) {
      a = wpIndex.get(leg.from) ?? null;
      b = wpIndex.get(leg.to) ?? null;
    } else if (fromInSystem) {
      a = wpIndex.get(leg.from) ?? null;
      b = gate;
    } else if (toInSystem) {
      a = gate;
      b = wpIndex.get(leg.to) ?? null;
    }
    const dep = Date.parse(leg.departedAt);
    const arr = Date.parse(leg.arrivesAt);
    if (a && b && !Number.isNaN(dep) && !Number.isNaN(arr)) {
      const progress = (nowMs - dep) / Math.max(arr - dep, 1);
      const t = Math.max(0, Math.min(1, progress));
      return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
    }
  }

  // Not interpolating (docked/orbiting, or unusable leg) — sit at the waypoint.
  const wpSym = hullWaypointInSystem(flow, systemSymbol);
  if (wpSym) {
    const p = wpIndex.get(wpSym);
    if (p) return p;
  }
  return null;
}

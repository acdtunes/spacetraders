import type { LiveFlow, TopologyResponse } from '../../types/flows';
import { systemOf, type Point } from './flowGeometry';

export interface MotionState {
  x: number;
  y: number;
  bearingRad: number;          // travel bearing (orbit tangent when dwelling)
  mode: 'dwell' | 'glide';
  fromSystem: string;          // glide: rendered edge endpoints (= fromSystem when dwelling)
  toSystem: string;
  phase: number;               // 0..1 along the rendered edge; 0 when dwelling
}

export type Adjacency = Map<string, string[]>;

// Edge X→Y is a progress bar for the whole crossing: [0,0.5] inside X to its
// gate, 0.5 the instant jump, [0.5,1] inside Y from its gate to the stop.
const PRE_JUMP_HOLD = 0.47;    // parked at own gate, jump pending
const POST_JUMP_HOLD = 0.53;   // parked at own gate, just arrived (cooldown)
const PRE_DEPARTURE = 0.04;    // parked away from the gate, departure pending
const ORBIT_RADIUS_PX = 9;     // dwell orbit, screen-stable (÷ scale)
const ORBIT_RAD_PER_SEC = 0.35;

export function buildAdjacency(topology: TopologyResponse): Adjacency {
  const adj: Adjacency = new Map();
  const push = (a: string, b: string) => {
    const arr = adj.get(a);
    if (arr) { if (!arr.includes(b)) arr.push(b); } else adj.set(a, [b]);
  };
  for (const e of topology.edges) {
    if (e.from === e.to) continue;
    push(e.from, e.to);
    push(e.to, e.from);
  }
  return adj;
}

// Each system's own jump-gate waypoint. gate_edges.gate_waypoint carries the
// CONNECTED (to-side) system's gate, and every system has one gate, so any
// edge INTO a system names that system's gate.
export function buildSystemGates(topology: TopologyResponse): Map<string, string> {
  const gates = new Map<string, string>();
  for (const e of topology.edges) {
    if (e.gateWaypoint && !gates.has(e.to)) gates.set(e.to, e.gateWaypoint);
  }
  return gates;
}

// BFS shortest path over the gate graph (systems inclusive of both endpoints).
export function gatePath(adj: Adjacency, from: string, to: string): string[] | null {
  if (from === to) return [from];
  const prev = new Map<string, string>([[from, from]]);
  const queue = [from];
  while (queue.length > 0) {
    const cur = queue.shift()!;
    for (const nxt of adj.get(cur) ?? []) {
      if (prev.has(nxt)) continue;
      prev.set(nxt, cur);
      if (nxt === to) {
        const path = [to];
        let p = to;
        while (p !== from) { p = prev.get(p)!; path.unshift(p); }
        return path;
      }
      queue.push(nxt);
    }
  }
  return null;
}

export interface Stop {
  waypoint: string;
  system: string;
  travelSeconds: number;
  deadhead: boolean; // no tranches at this stop (pure repositioning / return leg)
}

// The flow's remaining stop sequence: the current leg's destination first
// (unknown tranches → not deadhead), then every remaining hop.
export function buildStops(flow: LiveFlow): Stop[] {
  const stops: Stop[] = [];
  if (flow.currentLeg) {
    stops.push({ waypoint: flow.currentLeg.to, system: systemOf(flow.currentLeg.to), travelSeconds: 0, deadhead: false });
  }
  for (const h of flow.remainingHops) {
    stops.push({
      waypoint: h.waypoint,
      system: h.system || systemOf(h.waypoint),
      travelSeconds: h.travelSeconds || 0,
      deadhead: h.tranches.length === 0,
    });
  }
  return stops;
}

// A flow that only repositions: it has hops and none of them trade.
export function flowIsRelocation(flow: LiveFlow): boolean {
  return flow.remainingHops.length > 0 && flow.remainingHops.every((h) => h.tranches.length === 0);
}

const clamp01 = (v: number) => Math.max(0, Math.min(1, v));

function navProgress(departureIso: string, arrivalIso: string, nowMs: number): number {
  const dep = Date.parse(departureIso);
  const arr = Date.parse(arrivalIso);
  if (Number.isNaN(dep) || Number.isNaN(arr)) return 0;
  return clamp01((nowMs - dep) / Math.max(arr - dep, 1));
}

function hashShip(sym: string): number {
  let h = 2166136261;
  for (let i = 0; i < sym.length; i++) {
    h ^= sym.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

function glide(a: Point, b: Point, s: number, fromSystem: string, toSystem: string): MotionState {
  return {
    x: a.x + (b.x - a.x) * s,
    y: a.y + (b.y - a.y) * s,
    bearingRad: Math.atan2(b.y - a.y, b.x - a.x),
    mode: 'glide',
    fromSystem,
    toSystem,
    phase: s,
  };
}

function dwell(system: string, p: Point, ship: string, nowMs: number, scale: number): MotionState {
  const ang = (nowMs / 1000) * ORBIT_RAD_PER_SEC + (hashShip(ship) % 628) / 100;
  const r = ORBIT_RADIUS_PX / Math.max(scale, 1e-6);
  return {
    x: p.x + Math.cos(ang) * r,
    y: p.y + Math.sin(ang) * r,
    bearingRad: ang + Math.PI / 2, // orbit tangent
    mode: 'dwell',
    fromSystem: system,
    toSystem: system,
    phase: 0,
  };
}

// Galaxy-space kinematics for one flow. Nav truth (PG ships) grounds every
// branch; planned data only shapes the route. Null = nothing renderable.
export function projectFlowMotion(
  flow: LiveFlow,
  adj: Adjacency,
  systemGates: Map<string, string>,
  systemPos: Map<string, Point>,
  nowMs: number,
  scale: number,
): MotionState | null {
  const nav = flow.shipNav;
  const stops = buildStops(flow);

  // Legacy fallback: no nav row — lerp the current leg between its endpoint
  // systems on the daemon's best-effort timestamps (old projectFlowShip).
  if (!nav) {
    const leg = flow.currentLeg;
    if (!leg) return null;
    const fromSys = systemOf(leg.from);
    const toSys = systemOf(leg.to);
    const a = systemPos.get(fromSys);
    const b = systemPos.get(toSys);
    if (!a || !b) return null;
    if (fromSys === toSys) return dwell(fromSys, a, flow.ship, nowMs, scale);
    return glide(a, b, navProgress(leg.departedAt, leg.arrivesAt, nowMs), fromSys, toSys);
  }

  const hullSystem = nav.systemSymbol || (flow.currentLeg ? systemOf(flow.currentLeg.from) : '');
  const hullPos = hullSystem ? systemPos.get(hullSystem) : undefined;
  if (!hullSystem || !hullPos) return null;

  const inTransit = nav.status === 'IN_TRANSIT' && Boolean(nav.departureTime) && Boolean(nav.arrivalTime);

  // True warp: a single nav leg that itself crosses systems.
  if (inTransit && nav.originSymbol) {
    const originSys = systemOf(nav.originSymbol);
    const destSys = nav.waypointSymbol ? systemOf(nav.waypointSymbol) : hullSystem;
    if (originSys !== destSys) {
      const wa = systemPos.get(originSys);
      const wb = systemPos.get(destSys);
      if (wa && wb) return glide(wa, wb, navProgress(nav.departureTime!, nav.arrivalTime!, nowMs), originSys, destSys);
    }
  }

  const ownGate = systemGates.get(hullSystem);

  const target = stops.length > 0 ? stops[0].system : hullSystem;
  // The crossing runs from the previous stop's system to the target.
  const crossingStart = flow.currentLeg ? systemOf(flow.currentLeg.from) : hullSystem;
  // Arrival half of a completed crossing: still gliding from our own gate to
  // the stop — finish the incoming edge before dwelling.
  const arrivingThroughGate = inTransit && nav.originSymbol === ownGate;
  // Post-jump cooldown at the crossing's FINAL system: parked at our own gate
  // with the leg's origin in another system — the incoming edge isn't done
  // yet, so hold at POST_JUMP_HOLD instead of teleporting to the node.
  const coolingAtOwnGate = !inTransit && nav.waypointSymbol === ownGate && crossingStart !== hullSystem;
  if (target === hullSystem && !arrivingThroughGate && !coolingAtOwnGate) {
    return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);
  }

  let path = gatePath(adj, crossingStart, target);
  let i = path ? path.indexOf(hullSystem) : -1;
  if (!path || i === -1) {
    path = gatePath(adj, hullSystem, target);
    i = 0;
  }
  if (!path || path.length < 2) return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);

  const edgeOf = (fromIdx: number): { a: Point; b: Point; from: string; to: string } | null => {
    const from = path![fromIdx];
    const to = path![fromIdx + 1];
    const a = systemPos.get(from);
    const b = systemPos.get(to);
    return a && b ? { a, b, from, to } : null;
  };

  if (inTransit) {
    const t = navProgress(nav.departureTime!, nav.arrivalTime!, nowMs);
    if (nav.originSymbol === ownGate && i > 0) {
      const e = edgeOf(i - 1); // arrival half: completing the incoming edge
      if (e) return glide(e.a, e.b, 0.5 + 0.5 * t, e.from, e.to);
    }
    if (i < path.length - 1) {
      const e = edgeOf(i); // outbound half (gate-bound, or any detour leg)
      if (e) return glide(e.a, e.b, 0.5 * t, e.from, e.to);
    }
    return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);
  }

  // Parked while a crossing is pending or just completed. The cooldown check
  // runs BEFORE the end-of-path dwell so it also covers the final system.
  if (nav.waypointSymbol === ownGate && i > 0) {
    const e = edgeOf(i - 1); // cooldown: just arrived through our gate
    if (e) return glide(e.a, e.b, POST_JUMP_HOLD, e.from, e.to);
  }
  if (i >= path.length - 1) return dwell(hullSystem, hullPos, flow.ship, nowMs, scale);
  if (nav.waypointSymbol === ownGate) {
    const e = edgeOf(i); // first hop of the journey: jump pending
    return e ? glide(e.a, e.b, PRE_JUMP_HOLD, e.from, e.to) : null;
  }
  const e = edgeOf(i);
  return e ? glide(e.a, e.b, PRE_DEPARTURE, e.from, e.to) : null;
}

// Planned route as gate-graph polylines, one entry per stop transition that
// changes system (consecutive same-system stops collapse). deadhead mirrors
// the DESTINATION stop's flag.
export function planRoutePolylines(
  flow: LiveFlow,
  adj: Adjacency,
  systemPos: Map<string, Point>,
): { points: number[]; deadhead: boolean }[] {
  const stops = buildStops(flow);
  const startSystem = flow.shipNav?.systemSymbol || (flow.currentLeg ? systemOf(flow.currentLeg.from) : stops[0]?.system);
  if (!startSystem) return [];

  const out: { points: number[]; deadhead: boolean }[] = [];
  let prev = startSystem;
  for (const stop of stops) {
    if (stop.system === prev) continue;
    const path = gatePath(adj, prev, stop.system);
    prev = stop.system;
    if (!path) continue;
    const points: number[] = [];
    for (const sys of path) {
      const p = systemPos.get(sys);
      if (!p) { points.length = 0; break; }
      points.push(p.x, p.y);
    }
    if (points.length >= 4) out.push({ points, deadhead: stop.deadhead });
  }
  return out;
}

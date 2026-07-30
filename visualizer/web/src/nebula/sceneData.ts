// Pure snapshot adapter: the /trade-flows poll results (topology + lanes + live
// flows) folded into one render-ready SceneData frame for the nebula renderer.
// No pixi, no fetching, no side effects — and never throws: any missing input
// degrades to the empty scene (or the static subset the inputs can support).
import type { FlowWindow, LanesResponse, LiveFlowsResponse, TopologyResponse } from '../types/flows';
import type { Point } from '../components/flows/flowGeometry';
import { buildAdjacency, buildSystemGates, projectFlowMotion } from '../components/flows/flowMotion';
import { clustersFor, type Cluster } from './clusters';

export interface SceneSystem { symbol: string; x: number; y: number; activity: number; isHome: boolean; underConstruction: boolean }
export interface SceneLane { from: string; to: string; profitPerHr: number; volume: number; realized: number; projected: number }
export interface SceneShip { id: string; flowId: string; x: number; y: number; headingRad: number; system: string | null }
/** Raw directed topology (gate) edge — the L1 dormant-thread lattice. Distinct
 * from SceneLane: an edge exists whether or not anything traded on it.
 * `relevant` tags the edge as touching the neighbourhood we actually trade in —
 * the density cull orbs.ts tiers on (see the seed comment in buildSceneData). */
export interface SceneEdge { from: string; to: string; underConstruction: boolean; relevant: boolean }
export interface SceneData {
  systems: SceneSystem[]; lanes: SceneLane[]; ships: SceneShip[]; edges: SceneEdge[];
  clusters: Cluster[]; homeSystem: string | null; fitPoints: { x: number; y: number }[];
}

const WINDOW_HOURS: Record<FlowWindow, number> = { '1h': 1, '6h': 6, '24h': 24 };

const emptyScene = (): SceneData =>
  ({ systems: [], lanes: [], ships: [], edges: [], clusters: [], homeSystem: null, fitPoints: [] });

export function buildSceneData(
  topology: TopologyResponse | null | undefined,
  lanes: LanesResponse | null | undefined,
  live: LiveFlowsResponse | null | undefined,
  nowMs: number,
): SceneData {
  if (!topology || !Array.isArray(topology.systems) || topology.systems.length === 0) return emptyScene();
  const edges = Array.isArray(topology.edges) ? topology.edges : [];
  const homeSystem = topology.homeSystem ?? null;

  // Lanes 1:1 from the directed system→system rollup (the galaxy layer — its
  // endpoints are SceneSystem symbols). profitPerHr normalizes the window's
  // realized profit so lane weight is comparable across 1h/6h/24h.
  const hours = WINDOW_HOURS[lanes?.window as FlowWindow] ?? 1;
  const sceneLanes: SceneLane[] = (lanes?.systemLanes ?? []).map((l) => ({
    from: l.from,
    to: l.to,
    profitPerHr: l.realizedProfit / hours,
    volume: l.realizedUnits,
    realized: l.realizedProfit,
    projected: 0,
  }));
  const laneByEdge = new Map(sceneLanes.map((l) => [`${l.from}→${l.to}`, l]));

  // Ships via the existing motion model (position truth — reused verbatim, not
  // reimplemented). A gliding flow also attributes its projected profit onto
  // the directed lane it is rendered on: `projected` = in-flight expectation
  // currently traversing the lane (no route fan-out, so no double counting).
  const ships: SceneShip[] = [];
  const flows = live?.flows ?? [];
  if (flows.length > 0) {
    const adj = buildAdjacency(topology);
    const systemGates = buildSystemGates(topology);
    const systemPos = new Map<string, Point>(topology.systems.map((s) => [s.symbol, { x: s.x, y: s.y }]));
    for (const flow of flows) {
      const m = projectFlowMotion(flow, adj, systemGates, systemPos, nowMs, 1);
      if (!m || !Number.isFinite(m.x) || !Number.isFinite(m.y)) continue;
      ships.push({
        id: flow.ship,
        flowId: flow.containerId,
        x: m.x,
        y: m.y,
        headingRad: m.bearingRad,
        system: flow.shipNav?.systemSymbol ?? null,
      });
      if (m.mode === 'glide' && flow.projected) {
        const lane = laneByEdge.get(`${m.fromSystem}→${m.toSystem}`);
        if (lane) lane.projected += flow.projected.profit;
      }
    }
  }

  // Activity: realized lane profit touching the system (either endpoint;
  // signed, so loss lanes dim their neighbourhoods).
  const activity = new Map<string, number>();
  for (const l of sceneLanes) {
    activity.set(l.from, (activity.get(l.from) ?? 0) + l.realized);
    if (l.to !== l.from) activity.set(l.to, (activity.get(l.to) ?? 0) + l.realized);
  }

  // Lattice relevance — the density cull. The raw gate graph outgrew the view
  // (1.2k edges → 5.2k in one day of parked sensing): drawn undifferentiated it
  // is an opaque mat that buries the ~240 trade lanes it exists to frame. Tag
  // each edge with whether it touches the neighbourhood we ACTUALLY trade in;
  // orbs.ts draws that tier at REGION and holds the rest back until SYSTEM.
  // Seed = the systems the live data names:
  //   - both endpoints of every realized system lane,
  //   - every system that booked realized profit (systemActivity),
  //   - every live ship's current system — a hull must never be left gliding
  //     over a thread that was culled out from under it (measured: 4 of 17 ship
  //     systems are in neither lane list).
  // ANY endpoint in the seed keeps the edge: a thread reaching from a traded
  // system out to a quiet neighbour is the context that makes the lane legible.
  const traded = new Set<string>();
  for (const l of lanes?.systemLanes ?? []) {
    if (l?.from) traded.add(l.from);
    if (l?.to) traded.add(l.to);
  }
  for (const a of lanes?.systemActivity ?? []) if (a?.system) traded.add(a.system);
  for (const s of ships) if (s.system != null) traded.add(s.system);

  // An edge INTO a system names that system's own gate (gate_edges semantics —
  // see buildSystemGates), so an under-construction edge flags its `to` system.
  const underConstruction = new Set<string>();
  for (const e of edges) if (e.underConstruction) underConstruction.add(e.to);

  const systems: SceneSystem[] = topology.systems.map((s) => ({
    symbol: s.symbol,
    x: s.x,
    y: s.y,
    activity: activity.get(s.symbol) ?? 0,
    isHome: s.symbol === homeSystem,
    underConstruction: underConstruction.has(s.symbol),
  }));

  return {
    systems,
    lanes: sceneLanes,
    ships,
    // Raw directed gate edges 1:1 (gateWaypoint dropped — the render needs only
    // endpoints, the per-edge construction flag, and the relevance tier). The
    // REGION band's dormant threads draw from these, so untraded topology still
    // shows its lattice — tiered, not culled outright.
    edges: edges.map((e) => ({
      from: e.from,
      to: e.to,
      underConstruction: e.underConstruction,
      relevant: traded.has(e.from) || traded.has(e.to),
    })),
    // clustersFor takes the TopoLike shape: TopologyResponse.homeSystem is
    // optional (string | undefined) → map to the null it expects here, at the
    // call site — clusters.ts stays generic.
    clusters: clustersFor({ systems: topology.systems, edges, homeSystem }),
    homeSystem,
    fitPoints: topology.systems.map((s) => ({ x: s.x, y: s.y })),
  };
}

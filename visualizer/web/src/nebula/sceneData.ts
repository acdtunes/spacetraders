// Pure snapshot adapter: the /trade-flows poll results (topology + lanes + live
// flows) folded into one render-ready SceneData frame for the nebula renderer.
// No pixi, no fetching, no side effects — and never throws: any missing input
// degrades to the empty scene (or the static subset the inputs can support).
import type { FlowWindow, FreshnessResponse, LanesResponse, LiveFlowsResponse, TopologyResponse } from '../types/flows';
import type { Point } from '../components/flows/flowGeometry';
import { buildAdjacency, buildSystemGates, projectFlowMotion } from '../components/flows/flowMotion';
import { clustersFor, type Cluster } from './clusters';
import {
  DARK,
  clusterFreshnessFor,
  systemFreshnessFor,
  type ClusterFreshness,
  type SystemFreshness,
} from './freshness';

export interface SceneSystem {
  symbol: string; x: number; y: number; activity: number; isHome: boolean; underConstruction: boolean;
  /** Market-freshness render state. Every drawn system carries one: the
   * priced/dark distinction is a property of the ORB SET, so it is resolved here
   * against topology (the only source of placeable systems) rather than left to
   * each band to re-derive. See `freshness` below for why the join runs this way. */
  freshness: SystemFreshness;
}
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
  /** Per-cluster freshness aggregate, keyed by Cluster.id — the GALAXY band's
   * input. Computed here so both bands read one snapshot and can never disagree. */
  clusterFreshness: Map<string, ClusterFreshness>;
  /** The aura's full scale in minutes and how the server got it, carried through
   * for the legend. Zero/`'unknown'` means the server had nothing to measure. */
  rotationBoundMinutes: number;
  rotationBoundBasis: FreshnessResponse['rotationBoundBasis'];
  marketsKnown: number;
}

const WINDOW_HOURS: Record<FlowWindow, number> = { '1h': 1, '6h': 6, '24h': 24 };

const emptyScene = (): SceneData => ({
  systems: [], lanes: [], ships: [], edges: [], clusters: [], homeSystem: null, fitPoints: [],
  clusterFreshness: new Map(), rotationBoundMinutes: 0, rotationBoundBasis: 'unknown', marketsKnown: 0,
});

export function buildSceneData(
  topology: TopologyResponse | null | undefined,
  lanes: LanesResponse | null | undefined,
  live: LiveFlowsResponse | null | undefined,
  nowMs: number,
  freshness?: FreshnessResponse | null,
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

  // ---- Market freshness: the priced/dark join ------------------------------
  //
  // DARK SYSTEMS COME FROM THE DIFFERENCE AGAINST TOPOLOGY, CLIENT-SIDE, and
  // that is a deliberate choice over extending /api/flows/freshness to emit them.
  // The endpoint omits zero-listing systems by design (except those carrying a
  // scout post, so an actuator marker renders before its first scan) — so dark
  // has to be inferred somewhere. Doing it here, against topology:
  //
  //   - joins against the RIGHT set. /topology serves exactly the systems that
  //     can be placed truthfully, which is exactly the orb set being drawn. A
  //     server-side dark list would be built from charted markets — a strictly
  //     larger set (1,490 charted market systems vs ~495 placeable) — so most of
  //     it would name systems the renderer cannot draw, and the two endpoints
  //     would need topology's placement rules duplicated to agree.
  //   - costs nothing on the wire. Absence is already an unambiguous signal;
  //     emitting ~1,100 all-zero records to say the same thing is pure payload.
  //   - keeps the scout-post exception working for free: those records still
  //     arrive, still have zero listings, and still resolve to dark-with-a-post.
  //
  // A system in the freshness response but NOT in topology simply isn't drawn —
  // the map has never claimed to show unplaceable systems (see coverage notice).
  const bound = freshness?.rotationBoundMinutes ?? freshness?.staleAfterMinutes ?? 0;
  const freshBySystem = new Map<string, SystemFreshness>();
  for (const rec of freshness?.systems ?? []) {
    if (rec?.system) freshBySystem.set(rec.system, systemFreshnessFor(rec, nowMs, bound));
  }

  const systems: SceneSystem[] = topology.systems.map((s) => ({
    symbol: s.symbol,
    x: s.x,
    y: s.y,
    activity: activity.get(s.symbol) ?? 0,
    isHome: s.symbol === homeSystem,
    underConstruction: underConstruction.has(s.symbol),
    // No record ⇒ dark. Never a synthesised age: `DARK` carries t: null, so no
    // downstream ramp can accidentally render "we have never seen this market"
    // as "we saw it exactly one full rotation ago".
    freshness: freshBySystem.get(s.symbol) ?? DARK,
  }));

  const clusters = clustersFor({ systems: topology.systems, edges, homeSystem });
  const bySymbol = new Map(systems.map((s) => [s.symbol, s.freshness]));
  const clusterFreshness = new Map(
    clusters.map((c) => [c.id, clusterFreshnessFor(c.members, bySymbol)]),
  );

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
    clusters,
    homeSystem,
    fitPoints: topology.systems.map((s) => ({ x: s.x, y: s.y })),
    clusterFreshness,
    rotationBoundMinutes: bound,
    rotationBoundBasis: freshness?.rotationBoundBasis ?? (freshness ? 'observed' : 'unknown'),
    marketsKnown: freshness?.marketsKnown ?? 0,
  };
}

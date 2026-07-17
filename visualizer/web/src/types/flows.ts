// Wire contract for the Trade Flows tab. The daemon (Part 2) serializes
// DaemonFlow / LiveFlowsResponse verbatim; the visualizer server adds the
// shipNav enrichment + feedLost/lastPlanAt envelope fields.

export type FlowProgram = 'tour' | 'trade-route' | 'arb';

export interface FlowTranche {
  good: string;
  isBuy: boolean;
  units: number;
  expectedUnitPrice: number;
}

export interface FlowHop {
  waypoint: string;
  system: string;          // hop's system (daemon-tagged; galaxy glide chaining)
  travelSeconds: number;   // planned travel from previous stop; 0 = no estimate
  tranches: FlowTranche[];
}

export interface FlowCargoItem {
  good: string;
  units: number;
}

export interface FlowLeg {
  from: string;         // waypoint symbol
  to: string;           // waypoint symbol
  departedAt: string;   // ISO; TRUE leg-start (not publish time) — schedule-drift anchor
  arrivesAt: string;    // ISO
  travelSeconds: number; // planner's projected duration for THIS leg; 0 = no estimate (drift = arrivesAt − (departedAt + travelSeconds))
}

export interface FlowProjection {
  profit: number;
  ratePerHour: number;
}

// One active flow as the daemon publishes it.
export interface DaemonFlow {
  containerId: string;
  program: FlowProgram;
  ship: string;
  tourId: string | null;
  closed: boolean;
  currentLeg: FlowLeg | null;
  cargo: FlowCargoItem[];
  remainingHops: FlowHop[];
  projected: FlowProjection | null;
  plannedAt: string;    // ISO
}

// Server enrichment: last-known PG nav for the flow's ship (position truth).
export interface FlowShipNav {
  status: string;          // ships.nav_status
  systemSymbol: string;    // ships.system_symbol
  waypointSymbol: string;  // ships.location_symbol
  x: number;               // ships.location_x
  y: number;               // ships.location_y
  arrivalTime: string | null; // ships.arrival_time
  originSymbol: string | null;   // ships.origin_symbol (migration 040)
  originX: number | null;
  originY: number | null;
  departureTime: string | null;  // ships.departure_time
  cargoCapacity: number | null;  // ships.cargo_capacity; drives hull silhouette split (heavy vs light). null = unknown
}

// Signed realized-so-far from the container-attributed transaction ledger
// (purchases negative, sells positive, refuels negative).
export interface FlowRealized {
  net: number;
  lastEventAt: string | null;
}

export interface LiveFlow extends DaemonFlow {
  shipNav: FlowShipNav | null;
  realized: FlowRealized;
}

export interface LiveFlowsResponse {
  flows: LiveFlow[];
  generatedAt: string;   // ISO
  feedLost: boolean;
  lastPlanAt: string | null; // max plannedAt from the daemon; null when feedLost
}

export type FlowWindow = '1h' | '6h' | '24h';

export interface LaneRecord {
  from: string;          // waypoint symbol
  to: string;            // waypoint symbol
  realizedUnits: number;
  realizedProfit: number;
  legCount: number;
  goods?: Record<string, number>; // per-good signed credits (additive; system rollup folds these into topGoods)
}

// A directed system→system lane carrying its dominant goods (top 3 by |credits|),
// surfaced in the galaxy lane-hover tooltip.
export type SystemLaneRecord = LaneRecord & { topGoods: { good: string; credits: number }[] };

export interface SystemActivityRecord {
  system: string;
  realizedProfit: number;
  legCount: number;
}

export interface LanesResponse {
  lanes: LaneRecord[];
  systemLanes: SystemLaneRecord[];        // directed system→system (galaxy layer)
  systemActivity: SystemActivityRecord[]; // node sizing/brightness
  window: FlowWindow;
  generatedAt: string;
}

export interface TopologySystem {
  symbol: string;
  x: number;             // galaxy-scale layout coordinate (server-computed)
  y: number;
  layout: 'real' | 'force';
}

export interface TopologyEdge {
  from: string;          // gate_edges.system_symbol
  to: string;            // gate_edges.connected_system
  gateWaypoint: string;  // gate_edges.gate_waypoint
  underConstruction: boolean;
}

export interface TopologyResponse {
  systems: TopologySystem[];
  edges: TopologyEdge[];
  homeSystem?: string;   // server-derived from /my/agent; absent when unknown (never guessed)
  generatedAt: string;
}

// Per-system market freshness = solver visibility. `/api/flows/freshness` shapes
// these from the era-scoped market aggregation merged with scout_posts.
export type ScoutPostStatus = 'manned' | 'relay' | 'unmanned';

export interface SystemFreshnessRecord {
  system: string;
  totalListings: number;
  freshListings: number;
  freshnessPct: number;          // 0..100 solver visibility
  freshestAt: string | null;     // ISO of the newest listing scan
  scoutPost: { status: ScoutPostStatus; hull: string | null; kind: string } | null;
}

export interface FreshnessResponse {
  systems: SystemFreshnessRecord[];
  staleAfterMinutes: number;     // mirrors gobot maxListingAge; never hardcode
  generatedAt: string;
}

// Recent realized trade — tour-leg fill or arb execution — mirroring the server's
// utils/fills.ts FillRecord. The galaxy view's ambient ticker feed, newest-first.
export interface FillRecord {
  id: string;            // stable per source row: `t-<id>` tour leg, `a-<id>` arb
  at: string;            // ISO
  ship: string;
  good: string;
  isBuy: boolean;
  units: number;
  credits: number;       // signed: sells +, buys −; arb rows carry net profit
  waypoint: string;
}

export interface FillsResponse {
  fills: FillRecord[];
  generatedAt: string;   // ISO
}

// Shared galaxy hover target: a system node or a directed system→system lane.
// `key` is the system symbol ('X1-NK36') or `"from→to"`; x/y are client coords
// (stage-container rect + pointer) for the floating tooltip card.
export type TooltipState = { kind: 'system' | 'lane'; key: string; x: number; y: number } | null;

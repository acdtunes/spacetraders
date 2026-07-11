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
  tranches: FlowTranche[];
}

export interface FlowCargoItem {
  good: string;
  units: number;
}

export interface FlowLeg {
  from: string;         // waypoint symbol
  to: string;           // waypoint symbol
  departedAt: string;   // ISO
  arrivesAt: string;    // ISO
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
}

export interface LiveFlow extends DaemonFlow {
  shipNav: FlowShipNav | null;
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
}

export interface LanesResponse {
  lanes: LaneRecord[];
  window: FlowWindow;
  generatedAt: string;
}

export interface TopologySystem {
  symbol: string;
  x: number;             // galaxy-scale layout coordinate (server-computed)
  y: number;
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
  generatedAt: string;
}

import type {
  TopologyResponse,
  LanesResponse,
  LiveFlowsResponse,
  LiveFlow,
  FlowWindow,
} from '../types/flows';
import type { Waypoint, WaypointType } from '../types/spacetraders';

// A compact synthetic gate network (two systems + a couple of neighbours) with
// hand-placed galaxy coordinates so the demo tab is self-contained (the live
// server computes real coordinates from the gate graph — see galaxyLayout.ts).
// Every node is flagged layout:'real' (verbatim coordinates, no force fallback).
// Each edge's gateWaypoint is the CONNECTED (to-side) system's gate — matching
// real gate_edges semantics that buildSystemGates depends on for glide chaining.
// X1-NK36 is the demo HOME system (server derives the real one from /my/agent).
export const mockTopology: TopologyResponse = {
  systems: [
    { symbol: 'X1-NK36', x: -400, y: -120, layout: 'real' },
    { symbol: 'X1-KA42', x: 260, y: 40, layout: 'real' },
    { symbol: 'X1-ZC66', x: 120, y: 380, layout: 'real' },
    { symbol: 'X1-UU57', x: -260, y: 280, layout: 'real' },
  ],
  edges: [
    { from: 'X1-NK36', to: 'X1-KA42', gateWaypoint: 'X1-KA42-I52', underConstruction: false },
    { from: 'X1-KA42', to: 'X1-ZC66', gateWaypoint: 'X1-ZC66-I52', underConstruction: false },
    { from: 'X1-ZC66', to: 'X1-UU57', gateWaypoint: 'X1-UU57-I52', underConstruction: false },
    { from: 'X1-UU57', to: 'X1-NK36', gateWaypoint: 'X1-NK36-I52', underConstruction: true },
  ],
  homeSystem: 'X1-NK36',
  generatedAt: new Date(0).toISOString(),
};

// Realized lanes, pre-sorted by profit desc (as the live endpoint returns them).
// Includes intra-system pairs in X1-NK36 (the home system) so the drilldown shows
// real waypoint->waypoint routes, plus cross-system lanes that render as exit
// vectors toward the gate.
export function mockLanes(window: FlowWindow): LanesResponse {
  const base = [
    { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', realizedUnits: 480, realizedProfit: 312000, legCount: 6 },  // cross-system
    { from: 'X1-NK36-FE8A', to: 'X1-NK36-A1', realizedUnits: 360, realizedProfit: 205000, legCount: 5 },   // intra X1-NK36
    { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', realizedUnits: 300, realizedProfit: 141000, legCount: 4 },  // cross-system
    { from: 'X1-NK36-A1', to: 'X1-NK36-B2', realizedUnits: 220, realizedProfit: 96000, legCount: 3 },      // intra X1-NK36
    { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', realizedUnits: 120, realizedProfit: -8000, legCount: 2 },  // cross-system (loss)
  ];
  // Galaxy-layer rollups of the base lanes (matching the Task 6 server semantics),
  // pre-computed by hand. systemLanes are directed SYSTEM->SYSTEM with intra-system
  // pairs excluded; systemActivity credits each base lane's realized profit + legs
  // to its DESTINATION system (intra pairs credit their own), sorted profit desc —
  // so X1-NK36 folds in both intra-home lanes (205000 + 96000 = 301000).
  const systemLanesBase = [
    { from: 'X1-NK36', to: 'X1-KA42', realizedUnits: 480, realizedProfit: 312000, legCount: 6 },
    { from: 'X1-KA42', to: 'X1-ZC66', realizedUnits: 300, realizedProfit: 141000, legCount: 4 },
    { from: 'X1-ZC66', to: 'X1-UU57', realizedUnits: 120, realizedProfit: -8000, legCount: 2 },
  ];
  const systemActivityBase = [
    { system: 'X1-KA42', realizedProfit: 312000, legCount: 6 },
    { system: 'X1-NK36', realizedProfit: 301000, legCount: 8 },
    { system: 'X1-ZC66', realizedProfit: 141000, legCount: 4 },
    { system: 'X1-UU57', realizedProfit: -8000, legCount: 2 },
  ];
  // The window only scales the volume in the fixture — enough to see the switch work.
  const scale = window === '1h' ? 0.25 : window === '6h' ? 1 : 3.5;
  return {
    lanes: base.map((l) => ({
      ...l,
      realizedUnits: Math.round(l.realizedUnits * scale),
      realizedProfit: Math.round(l.realizedProfit * scale),
    })),
    systemLanes: systemLanesBase.map((l) => ({
      ...l,
      realizedUnits: Math.round(l.realizedUnits * scale),
      realizedProfit: Math.round(l.realizedProfit * scale),
    })),
    systemActivity: systemActivityBase.map((a) => ({
      ...a,
      realizedProfit: Math.round(a.realizedProfit * scale),
    })),
    window,
    generatedAt: new Date(0).toISOString(),
  };
}

// Four live flows for the galaxy demo, anchored to `nowMs` so a fleet-stopped demo
// still interpolates stable positions and exercises every headline visual:
//   A  cross-system glide toward a jump gate, partial profit ring (~0.38);
//   B  closed-tour loop (returns to its anchor on a no-trade leg), overshoot ring;
//   C  early arb, capital committed => negative (red under-glow) ring;
//   D  pure deadhead relocation (all hops trade-empty, no projection, zero ring).
// A is first so the roster/detail specs (find program==='tour') read the glide.
export function mockLiveFlows(nowMs: number): LiveFlowsResponse {
  const iso = (ms: number) => new Date(ms).toISOString();
  const flows: LiveFlow[] = [
    // A — cross-system glide, partial ring. Mid-leg inside X1-NK36 heading for its
    // gate, then a two-stop plan in X1-KA42 (travelSeconds gates the glide chain).
    {
      containerId: 'tour-run-TORWIND-3-galaxyA',
      program: 'tour',
      ship: 'TORWIND-3',
      tourId: 'tour-run-TORWIND-3-galaxyA',
      closed: false,
      currentLeg: { from: 'X1-NK36-FE8A', to: 'X1-NK36-I52', departedAt: iso(nowMs - 90_000), arrivesAt: iso(nowMs + 90_000) },
      cargo: [{ good: 'FABRICS', units: 120 }],
      remainingHops: [
        { waypoint: 'X1-KA42-D39', system: 'X1-KA42', travelSeconds: 420, tranches: [{ good: 'ELECTRONICS', isBuy: false, units: 60, expectedUnitPrice: 2200 }] },
        { waypoint: 'X1-KA42-A1', system: 'X1-KA42', travelSeconds: 0, tranches: [{ good: 'ADVANCED_CIRCUITRY', isBuy: true, units: 100, expectedUnitPrice: 4100 }] },
      ],
      projected: { profit: 250000, ratePerHour: 445000 },
      plannedAt: iso(nowMs - 120_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-NK36', waypointSymbol: 'X1-NK36-I52', x: 390, y: 165, arrivalTime: iso(nowMs + 90_000), originSymbol: 'X1-NK36-FE8A', originX: 280, originY: -70, departureTime: iso(nowMs - 90_000) },
      realized: { net: 96000, lastEventAt: iso(nowMs - 30_000) },
    },
    // B — closed-tour loop, overshoot ring. Dwelling (IN_ORBIT) in X1-KA42; the plan
    // circles KA42 -> ZC66 -> UU57 and returns to the X1-NK36 anchor on a trade-empty
    // leg (the honest no-trade return that closed-tour mode appends).
    {
      containerId: 'tour-run-TORWIND-7-loopB',
      program: 'tour',
      ship: 'TORWIND-7',
      tourId: 'tour-run-TORWIND-7-loopB',
      closed: true,
      currentLeg: null,
      cargo: [{ good: 'ELECTRONICS', units: 80 }],
      remainingHops: [
        { waypoint: 'X1-KA42-D39', system: 'X1-KA42', travelSeconds: 0, tranches: [{ good: 'ELECTRONICS', isBuy: true, units: 80, expectedUnitPrice: 2100 }] },
        { waypoint: 'X1-ZC66-C39A', system: 'X1-ZC66', travelSeconds: 500, tranches: [{ good: 'ELECTRONICS', isBuy: false, units: 80, expectedUnitPrice: 3200 }] },
        { waypoint: 'X1-UU57-E21Z', system: 'X1-UU57', travelSeconds: 600, tranches: [{ good: 'MACHINERY', isBuy: true, units: 40, expectedUnitPrice: 1800 }] },
        { waypoint: 'X1-NK36-FE8A', system: 'X1-NK36', travelSeconds: 700, tranches: [] },
      ],
      projected: { profit: 180000, ratePerHour: 220000 },
      plannedAt: iso(nowMs - 300_000),
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-KA42', waypointSymbol: 'X1-KA42-D39', x: -260, y: 80, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null },
      realized: { net: 205000, lastEventAt: iso(nowMs - 20_000) },
    },
    // C — early arb, capital committed => negative ring. In transit toward the sell.
    {
      containerId: 'arb-run-TORWIND-54-beba64e7',
      program: 'arb',
      ship: 'TORWIND-54',
      tourId: null,
      closed: false,
      currentLeg: { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', departedAt: iso(nowMs - 150_000), arrivesAt: iso(nowMs + 30_000) },
      cargo: [{ good: 'EQUIPMENT', units: 200 }],
      remainingHops: [
        { waypoint: 'X1-UU57-E21Z', system: 'X1-UU57', travelSeconds: 0, tranches: [{ good: 'EQUIPMENT', isBuy: false, units: 200, expectedUnitPrice: 1500 }] },
      ],
      projected: { profit: 60000, ratePerHour: 96000 },
      plannedAt: iso(nowMs - 160_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-C39A', x: 4, y: 22, arrivalTime: iso(nowMs + 30_000), originSymbol: 'X1-ZC66-C39A', originX: 240, originY: 120, departureTime: iso(nowMs - 150_000) },
      realized: { net: -42000, lastEventAt: iso(nowMs - 120_000) },
    },
    // D — pure deadhead relocation. Dwelling in X1-ZC66; a single trade-empty hop to
    // X1-UU57, no projection (flowIsRelocation styling, zero ring).
    {
      containerId: 'tour-run-TORWIND-11-relocD',
      program: 'tour',
      ship: 'TORWIND-11',
      tourId: 'tour-run-TORWIND-11-relocD',
      closed: false,
      currentLeg: null,
      cargo: [],
      remainingHops: [
        { waypoint: 'X1-UU57-A1', system: 'X1-UU57', travelSeconds: 600, tranches: [] },
      ],
      projected: null,
      plannedAt: iso(nowMs - 60_000),
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-A1', x: -80, y: -60, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null },
      realized: { net: 0, lastEventAt: null },
    },
  ];
  const lastPlanAt = flows.reduce<string | null>((max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max), null);
  return { flows, generatedAt: iso(nowMs), feedLost: false, lastPlanAt };
}

// The feed-loss scenario: no flows, no intent, flagged.
export function mockFeedLostResponse(nowMs: number): LiveFlowsResponse {
  return { flows: [], generatedAt: new Date(nowMs).toISOString(), feedLost: true, lastPlanAt: null };
}

// Per-system waypoints with real intra-system x/y (±~500), so the demo drilldown
// renders each flows-topology system TO SCALE — including the JUMP_GATE (exit
// anchor) and every waypoint referenced by the demo lanes/flows. Returns null for
// any non-demo system so the mock client falls back to its scenario waypoints.
const DEMO_WAYPOINTS: Record<string, Array<{ symbol: string; type: WaypointType; x: number; y: number }>> = {
  'X1-NK36': [
    { symbol: 'X1-NK36-A1', type: 'PLANET', x: -140, y: 60 },
    { symbol: 'X1-NK36-B2', type: 'MOON', x: 30, y: 210 },
    { symbol: 'X1-NK36-FE8A', type: 'ORBITAL_STATION', x: 280, y: -70 },
    { symbol: 'X1-NK36-C3', type: 'ASTEROID_FIELD', x: -340, y: -220 },
    { symbol: 'X1-NK36-I52', type: 'JUMP_GATE', x: 500, y: 400 },
  ],
  'X1-KA42': [
    { symbol: 'X1-KA42-A1', type: 'PLANET', x: 100, y: -120 },
    { symbol: 'X1-KA42-D39', type: 'ORBITAL_STATION', x: -260, y: 80 },
    { symbol: 'X1-KA42-F5', type: 'MOON', x: 60, y: 300 },
    { symbol: 'X1-KA42-I52', type: 'JUMP_GATE', x: 420, y: -380 },
  ],
  'X1-ZC66': [
    { symbol: 'X1-ZC66-A1', type: 'PLANET', x: -80, y: -60 },
    { symbol: 'X1-ZC66-C39A', type: 'ORBITAL_STATION', x: 240, y: 120 },
    { symbol: 'X1-ZC66-B2', type: 'ASTEROID_FIELD', x: -300, y: 260 },
    { symbol: 'X1-ZC66-I52', type: 'JUMP_GATE', x: -460, y: -400 },
  ],
  'X1-UU57': [
    { symbol: 'X1-UU57-A1', type: 'PLANET', x: 120, y: 80 },
    { symbol: 'X1-UU57-E21Z', type: 'ORBITAL_STATION', x: -220, y: -140 },
    { symbol: 'X1-UU57-G7', type: 'MOON', x: 300, y: -260 },
    { symbol: 'X1-UU57-I52', type: 'JUMP_GATE', x: 440, y: 420 },
  ],
};

export function mockSystemWaypoints(systemSymbol: string): Waypoint[] | null {
  const rows = DEMO_WAYPOINTS[systemSymbol];
  if (!rows) return null;
  return rows.map((r) => ({
    symbol: r.symbol,
    type: r.type,
    systemSymbol,
    x: r.x,
    y: r.y,
    orbitals: [],
    traits: r.type === 'ORBITAL_STATION'
      ? [{ symbol: 'MARKETPLACE', name: 'Marketplace', description: 'Trade hub' }]
      : [],
    isUnderConstruction: false,
    hasMarketplace: r.type === 'ORBITAL_STATION',
  }));
}

import type {
  TopologyResponse,
  LanesResponse,
  LiveFlowsResponse,
  LiveFlow,
  FlowWindow,
} from '../types/flows';

// A compact synthetic gate network (two systems + a couple of neighbours) with
// hand-placed galaxy coordinates so the demo tab is self-contained (the live
// server computes real coordinates from the gate graph — see galaxyLayout.ts).
export const mockTopology: TopologyResponse = {
  systems: [
    { symbol: 'X1-NK36', x: -400, y: -120 },
    { symbol: 'X1-KA42', x: 260, y: 40 },
    { symbol: 'X1-ZC66', x: 120, y: 380 },
    { symbol: 'X1-UU57', x: -260, y: 280 },
  ],
  edges: [
    { from: 'X1-NK36', to: 'X1-KA42', gateWaypoint: 'X1-NK36-I52', underConstruction: false },
    { from: 'X1-KA42', to: 'X1-ZC66', gateWaypoint: 'X1-KA42-I52', underConstruction: false },
    { from: 'X1-ZC66', to: 'X1-UU57', gateWaypoint: 'X1-ZC66-I52', underConstruction: false },
    { from: 'X1-UU57', to: 'X1-NK36', gateWaypoint: 'X1-UU57-I52', underConstruction: true },
  ],
  generatedAt: new Date(0).toISOString(),
};

// Realized lanes, pre-sorted by profit desc (as the live endpoint returns them).
export function mockLanes(window: FlowWindow): LanesResponse {
  const base = [
    { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', realizedUnits: 480, realizedProfit: 312000, legCount: 6 },
    { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', realizedUnits: 300, realizedProfit: 141000, legCount: 4 },
    { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', realizedUnits: 120, realizedProfit: -8000, legCount: 2 },
  ];
  // The window only scales the volume in the fixture — enough to see the switch work.
  const scale = window === '1h' ? 0.25 : window === '6h' ? 1 : 3.5;
  return {
    lanes: base.map((l) => ({
      ...l,
      realizedUnits: Math.round(l.realizedUnits * scale),
      realizedProfit: Math.round(l.realizedProfit * scale),
    })),
    window,
    generatedAt: new Date(0).toISOString(),
  };
}

// Three live flows — one per program — with current legs anchored to `nowMs`
// so a fleet-stopped demo still interpolates a stable mid-leg position.
export function mockLiveFlows(nowMs: number): LiveFlowsResponse {
  const iso = (ms: number) => new Date(ms).toISOString();
  const flows: LiveFlow[] = [
    {
      containerId: 'tour-run-TORWIND-19-086680f9',
      program: 'tour',
      ship: 'TORWIND-19',
      tourId: 'tour-run-TORWIND-19-086680f9',
      currentLeg: { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', departedAt: iso(nowMs - 90_000), arrivesAt: iso(nowMs + 90_000) },
      cargo: [{ good: 'FABRICS', units: 120 }, { good: 'SHIP_PARTS', units: 12 }],
      remainingHops: [
        { waypoint: 'X1-KA42-D39', tranches: [{ good: 'SHIP_PARTS', isBuy: false, units: 12, expectedUnitPrice: 3959 }] },
        { waypoint: 'X1-ZC66-C39A', tranches: [{ good: 'ADVANCED_CIRCUITRY', isBuy: true, units: 100, expectedUnitPrice: 4100 }] },
      ],
      projected: { profit: 312000, ratePerHour: 445000 },
      plannedAt: iso(nowMs - 120_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-NK36', waypointSymbol: 'X1-NK36-FE8A', x: 12, y: -8, arrivalTime: iso(nowMs + 90_000) },
    },
    {
      containerId: 'trade-route-TORWIND-2B-28a64d19',
      program: 'trade-route',
      ship: 'TORWIND-2B',
      tourId: null,
      currentLeg: { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', departedAt: iso(nowMs - 30_000), arrivesAt: iso(nowMs + 150_000) },
      cargo: [{ good: 'ELECTRONICS', units: 60 }],
      remainingHops: [
        { waypoint: 'X1-ZC66-C39A', tranches: [{ good: 'ELECTRONICS', isBuy: false, units: 60, expectedUnitPrice: 2200 }] },
      ],
      projected: { profit: 88000, ratePerHour: 190000 },
      plannedAt: iso(nowMs - 45_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-KA42', waypointSymbol: 'X1-KA42-D39', x: -20, y: 15, arrivalTime: iso(nowMs + 150_000) },
    },
    {
      containerId: 'arb-run-TORWIND-54-beba64e7',
      program: 'arb',
      ship: 'TORWIND-54',
      tourId: null,
      currentLeg: { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', departedAt: iso(nowMs - 150_000), arrivesAt: iso(nowMs + 30_000) },
      cargo: [{ good: 'EQUIPMENT', units: 200 }],
      remainingHops: [
        { waypoint: 'X1-UU57-E21Z', tranches: [{ good: 'EQUIPMENT', isBuy: false, units: 200, expectedUnitPrice: 1500 }] },
      ],
      projected: { profit: 41000, ratePerHour: 96000 },
      plannedAt: iso(nowMs - 160_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-C39A', x: 4, y: 22, arrivalTime: iso(nowMs + 30_000) },
    },
  ];
  const lastPlanAt = flows.reduce<string | null>((max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max), null);
  return { flows, generatedAt: iso(nowMs), feedLost: false, lastPlanAt };
}

// The feed-loss scenario: no flows, no intent, flagged.
export function mockFeedLostResponse(nowMs: number): LiveFlowsResponse {
  return { flows: [], generatedAt: new Date(nowMs).toISOString(), feedLost: true, lastPlanAt: null };
}

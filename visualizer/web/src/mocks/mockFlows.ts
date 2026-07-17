import type {
  TopologyResponse,
  TopologySystem,
  TopologyEdge,
  LanesResponse,
  LaneRecord,
  SystemLaneRecord,
  SystemActivityRecord,
  LiveFlowsResponse,
  LiveFlow,
  FlowWindow,
  FreshnessResponse,
  FillsResponse,
  FillRecord,
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
  // Each lane carries a small per-good signed-credits map (sums to realizedProfit) so the
  // system rollup below can surface a topGoods breakdown in the lane-hover tooltip. The
  // explicit element type keeps each `goods` literal widened to Record<string, number>
  // (otherwise TS unions the distinct key sets into optional-undefined and rejects them).
  type MockLane = { from: string; to: string; realizedUnits: number; realizedProfit: number; legCount: number; goods: Record<string, number> };
  const base: MockLane[] = [
    { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', realizedUnits: 480, realizedProfit: 312000, legCount: 6, goods: { ELECTRONICS: 210000, FABRICS: 102000 } },  // cross-system
    { from: 'X1-NK36-FE8A', to: 'X1-NK36-A1', realizedUnits: 360, realizedProfit: 205000, legCount: 5, goods: { ADVANCED_CIRCUITRY: 130000, FABRICS: 75000 } },   // intra X1-NK36
    { from: 'X1-KA42-D39', to: 'X1-ZC66-C39A', realizedUnits: 300, realizedProfit: 141000, legCount: 4, goods: { ELECTRONICS: 141000 } },  // cross-system
    { from: 'X1-NK36-A1', to: 'X1-NK36-B2', realizedUnits: 220, realizedProfit: 96000, legCount: 3, goods: { MACHINERY: 60000, FABRICS: 36000 } },      // intra X1-NK36
    { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', realizedUnits: 120, realizedProfit: -8000, legCount: 2, goods: { EQUIPMENT: -8000 } },  // cross-system (loss)
  ];
  // Galaxy-layer rollups of the base lanes (matching the Task 6 server semantics),
  // pre-computed by hand. systemLanes are directed SYSTEM->SYSTEM with intra-system
  // pairs excluded; systemActivity credits each base lane's realized profit + legs
  // to its DESTINATION system (intra pairs credit their own), sorted profit desc —
  // so X1-NK36 folds in both intra-home lanes (205000 + 96000 = 301000).
  // Directed system lanes fold their base lanes' goods; topGoods = top 3 by |credits|
  // (matching the Task 2 server rollup), consumed by the galaxy lane-hover tooltip.
  const systemLanesBase: Array<MockLane & { topGoods: { good: string; credits: number }[] }> = [
    { from: 'X1-NK36', to: 'X1-KA42', realizedUnits: 480, realizedProfit: 312000, legCount: 6, goods: { ELECTRONICS: 210000, FABRICS: 102000 }, topGoods: [{ good: 'ELECTRONICS', credits: 210000 }, { good: 'FABRICS', credits: 102000 }] },
    { from: 'X1-KA42', to: 'X1-ZC66', realizedUnits: 300, realizedProfit: 141000, legCount: 4, goods: { ELECTRONICS: 141000 }, topGoods: [{ good: 'ELECTRONICS', credits: 141000 }] },
    { from: 'X1-ZC66', to: 'X1-UU57', realizedUnits: 120, realizedProfit: -8000, legCount: 2, goods: { EQUIPMENT: -8000 }, topGoods: [{ good: 'EQUIPMENT', credits: -8000 }] },
  ];
  const systemActivityBase = [
    { system: 'X1-KA42', realizedProfit: 312000, legCount: 6 },
    { system: 'X1-NK36', realizedProfit: 301000, legCount: 8 },
    { system: 'X1-ZC66', realizedProfit: 141000, legCount: 4 },
    { system: 'X1-UU57', realizedProfit: -8000, legCount: 2 },
  ];
  // The window only scales the volume in the fixture — enough to see the switch work.
  const scale = window === '1h' ? 0.25 : window === '6h' ? 1 : 3.5;
  const scaleGoods = (g: Record<string, number>): Record<string, number> =>
    Object.fromEntries(Object.entries(g).map(([k, v]) => [k, Math.round(v * scale)]));
  const scaleTop = (t: { good: string; credits: number }[]) =>
    t.map((e) => ({ good: e.good, credits: Math.round(e.credits * scale) }));
  return {
    lanes: base.map((l) => ({
      ...l,
      realizedUnits: Math.round(l.realizedUnits * scale),
      realizedProfit: Math.round(l.realizedProfit * scale),
      goods: scaleGoods(l.goods),
    })),
    systemLanes: systemLanesBase.map((l) => ({
      ...l,
      realizedUnits: Math.round(l.realizedUnits * scale),
      realizedProfit: Math.round(l.realizedProfit * scale),
      goods: scaleGoods(l.goods),
      topGoods: scaleTop(l.topGoods),
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
  // In-transit legs are pinned to the current 10-minute wall-clock window, NOT
  // to the poll instant: re-anchoring "now - 90s" on every poll freezes every
  // glide at a constant phase. With a window anchor the hulls advance in real
  // time (and across page reloads), and the demo resets each window boundary.
  const epoch = Math.floor(nowMs / 600_000) * 600_000;
  const flows: LiveFlow[] = [
    // A — cross-system glide, partial ring. Mid-leg inside X1-NK36 heading for its
    // gate, then a two-stop plan in X1-KA42 (travelSeconds gates the glide chain).
    {
      containerId: 'tour-run-TORWIND-3-galaxyA',
      program: 'tour',
      ship: 'TORWIND-3',
      tourId: 'tour-run-TORWIND-3-galaxyA',
      closed: false,
      // Logical leg is cross-system (next stop in X1-KA42); the PHYSICAL nav leg
      // below flies toward the home gate. That pairing is the wire contract the
      // motion model expects (see flowMotion outbound-half tests) and maps the
      // hull onto the NK36→KA42 galaxy edge at phase 0.5*t.
      // Drift anchor: currentLeg times are nowMs-relative (independent of the epoch-anchored
      // shipNav glide) so schedule drift is phase-stable. arrivesAt=(nowMs+90s), departedAt
      // =(nowMs−500s), travelSeconds=180 ⇒ drift ≈ 410s → amber tick / +7m roster suffix.
      currentLeg: { from: 'X1-NK36-FE8A', to: 'X1-KA42-D39', departedAt: iso(nowMs - 500_000), arrivesAt: iso(nowMs + 90_000), travelSeconds: 180 },
      cargo: [{ good: 'FABRICS', units: 120 }],
      remainingHops: [
        { waypoint: 'X1-KA42-D39', system: 'X1-KA42', travelSeconds: 420, tranches: [{ good: 'ELECTRONICS', isBuy: false, units: 60, expectedUnitPrice: 2200 }] },
        { waypoint: 'X1-KA42-A1', system: 'X1-KA42', travelSeconds: 0, tranches: [{ good: 'ADVANCED_CIRCUITRY', isBuy: true, units: 100, expectedUnitPrice: 4100 }] },
      ],
      projected: { profit: 250000, ratePerHour: 445000 },
      plannedAt: iso(nowMs - 500_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-NK36', waypointSymbol: 'X1-NK36-I52', x: 390, y: 165, arrivalTime: iso(epoch + 510_000), originSymbol: 'X1-NK36-FE8A', originX: 280, originY: -70, departureTime: iso(epoch - 90_000), cargoCapacity: 120 },
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
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-KA42', waypointSymbol: 'X1-KA42-D39', x: -260, y: 80, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: 40 },
      realized: { net: 205000, lastEventAt: iso(nowMs - 20_000) },
    },
    // C — early arb, capital committed => negative ring. In transit toward the sell.
    {
      containerId: 'arb-run-TORWIND-54-beba64e7',
      program: 'arb',
      ship: 'TORWIND-54',
      tourId: null,
      closed: false,
      // Drift anchor (red): arrivesAt=(nowMs+100s), departedAt=(nowMs−1_300s),
      // travelSeconds=200 ⇒ drift ≈ 1200s → red tick / +20m roster suffix.
      currentLeg: { from: 'X1-ZC66-C39A', to: 'X1-UU57-E21Z', departedAt: iso(nowMs - 1_300_000), arrivesAt: iso(nowMs + 100_000), travelSeconds: 200 },
      cargo: [{ good: 'EQUIPMENT', units: 200 }],
      remainingHops: [
        { waypoint: 'X1-UU57-E21Z', system: 'X1-UU57', travelSeconds: 0, tranches: [{ good: 'EQUIPMENT', isBuy: false, units: 200, expectedUnitPrice: 1500 }] },
      ],
      projected: { profit: 60000, ratePerHour: 96000 },
      plannedAt: iso(nowMs - 1_300_000),
      shipNav: { status: 'IN_TRANSIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-UU57-E21Z', x: 4, y: 22, arrivalTime: iso(epoch + 450_000), originSymbol: 'X1-ZC66-C39A', originX: 240, originY: 120, departureTime: iso(epoch - 150_000), cargoCapacity: 40 },
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
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-A1', x: -80, y: -60, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: 80 },
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

// Ambient ticker feed: 8 recent fills strictly alternating sell/buy across the 4 demo
// ships, timestamps descending from (nowMs − 30s) in 45s steps. Deterministic in nowMs
// (no Date.now here) so a fleet-stopped demo still shows a stable stream. Arb-ship rows
// (TORWIND-54) are sells with `a-` ids; the rest carry `t-` ids. credits are signed
// (sells +, buys −) exactly like the server's mergeFills.
export function mockFills(nowMs: number): FillsResponse {
  const iso = (ms: number) => new Date(ms).toISOString();
  const rows: Array<{ ship: string; good: string; isBuy: boolean; units: number; unit: number; wp: string; arb?: boolean }> = [
    { ship: 'TORWIND-3', good: 'ELECTRONICS', isBuy: false, units: 60, unit: 2200, wp: 'X1-KA42-D39' },
    { ship: 'TORWIND-7', good: 'MACHINERY', isBuy: true, units: 40, unit: 1800, wp: 'X1-UU57-E21Z' },
    { ship: 'TORWIND-54', good: 'EQUIPMENT', isBuy: false, units: 200, unit: 300, wp: 'X1-UU57-E21Z', arb: true },
    { ship: 'TORWIND-11', good: 'FABRICS', isBuy: true, units: 120, unit: 900, wp: 'X1-ZC66-C39A' },
    { ship: 'TORWIND-7', good: 'ELECTRONICS', isBuy: false, units: 80, unit: 3200, wp: 'X1-ZC66-C39A' },
    { ship: 'TORWIND-11', good: 'IRON', isBuy: true, units: 50, unit: 260, wp: 'X1-NK36-A1' },
    { ship: 'TORWIND-54', good: 'FUEL', isBuy: false, units: 80, unit: 120, wp: 'X1-ZC66-C39A', arb: true },
    { ship: 'TORWIND-3', good: 'ADVANCED_CIRCUITRY', isBuy: true, units: 100, unit: 4100, wp: 'X1-KA42-A1' },
  ];
  const fills: FillRecord[] = rows.map((r, i) => ({
    id: `${r.arb ? 'a' : 't'}-${i + 1}`,
    at: iso(nowMs - 30_000 - i * 45_000),
    ship: r.ship,
    good: r.good,
    isBuy: r.isBuy,
    units: r.units,
    credits: (r.isBuy ? -1 : 1) * r.units * r.unit,
    waypoint: r.wp,
  }));
  return { fills, generatedAt: iso(nowMs) };
}

// Small deterministic PRNG (mulberry32) — seeds the dense fixture's spatial jitter
// with zero reliance on Math.random, so every render is byte-identical.
function mulberry32(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const DENSE_GOODS = ['ELECTRONICS', 'FABRICS', 'MACHINERY', 'FUEL', 'IRON', 'ADVANCED_CIRCUITRY', 'EQUIPMENT', 'FOOD', 'PLATINUM', 'MEDICINE'];

// A deterministic "hairball" galaxy for the declutter demo (VITE_MOCK_DENSE=1): ~40
// systems on a jittered ring plus two inner clusters, chain + chord gate edges, and 30
// directed system lanes whose profit magnitudes are log-spread 5,000,000 → 20,000 so
// EXACTLY 12 clear the top-N artery cut (LANE_EMPHASIS_N) while several fall under the
// 2% floor (100k) and are dropped by T4's partitionLanes. Pure in nowMs (mulberry32,
// no Math.random / Date.now): topology + lanes are stable across polls while the ~10
// live flows still interpolate. Export the three /flows/* payloads at once.
export function mockDenseGalaxy(nowMs: number): { topology: TopologyResponse; lanes: LanesResponse; live: LiveFlowsResponse } {
  const rand = mulberry32(0x5eed);
  const RING = 28;
  const CLUSTERS = 2;
  const CLUSTER_SIZE = 6;
  const total = RING + CLUSTERS * CLUSTER_SIZE; // 40
  const sym = (i: number) => `X1-D${String(i).padStart(2, '0')}`;

  // --- systems: jittered ring + two inner clusters ---
  const systems: TopologySystem[] = [];
  for (let i = 0; i < RING; i++) {
    const ang = (i / RING) * Math.PI * 2;
    const r = 1000 + (rand() - 0.5) * 260;
    systems.push({ symbol: sym(i), x: Math.round(Math.cos(ang) * r), y: Math.round(Math.sin(ang) * r), layout: 'real' });
  }
  const clusterCenters = [{ x: -320, y: 160 }, { x: 300, y: -220 }];
  for (let c = 0; c < CLUSTERS; c++) {
    for (let k = 0; k < CLUSTER_SIZE; k++) {
      const idx = RING + c * CLUSTER_SIZE + k;
      systems.push({
        symbol: sym(idx),
        x: Math.round(clusterCenters[c].x + (rand() - 0.5) * 300),
        y: Math.round(clusterCenters[c].y + (rand() - 0.5) * 300),
        layout: 'real',
      });
    }
  }

  // --- edges: ring cycle + a few chords + cluster tie-ins (both directions each) ---
  const edges: TopologyEdge[] = [];
  const gateOf = (to: string) => `${to}-I52`;
  const addEdge = (from: string, to: string, uc = false) => {
    edges.push({ from, to, gateWaypoint: gateOf(to), underConstruction: uc });
    edges.push({ from: to, to: from, gateWaypoint: gateOf(from), underConstruction: uc });
  };
  for (let i = 0; i < RING; i++) addEdge(sym(i), sym((i + 1) % RING));            // ring cycle
  addEdge(sym(0), sym(9)); addEdge(sym(4), sym(18)); addEdge(sym(13), sym(24));  // chords
  addEdge(sym(6), sym(RING));                                                    // cluster 0 tie-in
  addEdge(sym(20), sym(RING + CLUSTER_SIZE));                                    // cluster 1 tie-in
  for (let c = 0; c < CLUSTERS; c++) {
    for (let k = 0; k < CLUSTER_SIZE - 1; k++) addEdge(sym(RING + c * CLUSTER_SIZE + k), sym(RING + c * CLUSTER_SIZE + k + 1));
  }
  addEdge(sym(2), sym(15), true);                                               // one under-construction gate

  // --- 30 directed system lanes, |profit| log-spread 5,000,000 → 20,000 ---
  const N_LANES = 30;
  const PMAX = 5_000_000;
  const ratio = 20_000 / PMAX; // 0.004
  const systemLanes: SystemLaneRecord[] = [];
  for (let i = 0; i < N_LANES; i++) {
    const mag = Math.round(PMAX * Math.pow(ratio, i / (N_LANES - 1)));
    const sign = i === 5 || i === 11 || i === 19 ? -1 : 1; // a few losses; |profit| still ranks them
    const profit = mag * sign;
    const a = i % total;
    let b = (a + 1 + (i % 7)) % total;
    if (b === a) b = (b + 1) % total;
    const g0 = DENSE_GOODS[i % DENSE_GOODS.length];
    const g1 = DENSE_GOODS[(i + 3) % DENSE_GOODS.length];
    const c0 = Math.round(profit * 0.62);
    const c1 = profit - c0;
    systemLanes.push({
      from: sym(a), to: sym(b), realizedUnits: 40 + (i % 9) * 20, realizedProfit: profit, legCount: 1 + (i % 5),
      goods: { [g0]: c0, [g1]: c1 },
      topGoods: [{ good: g0, credits: c0 }, { good: g1, credits: c1 }],
    });
  }

  // waypoint-level lanes (drilldown) + destination-credited system activity
  const lanes: LaneRecord[] = systemLanes
    .map((l) => ({ from: `${l.from}-A1`, to: `${l.to}-B2`, realizedUnits: l.realizedUnits, realizedProfit: l.realizedProfit, legCount: l.legCount, goods: l.goods }))
    .sort((x, y) => y.realizedProfit - x.realizedProfit);
  const activity = new Map<string, { realizedProfit: number; legCount: number }>();
  for (const l of systemLanes) {
    const cur = activity.get(l.to) ?? { realizedProfit: 0, legCount: 0 };
    cur.realizedProfit += l.realizedProfit;
    cur.legCount += l.legCount;
    activity.set(l.to, cur);
  }
  const systemActivity: SystemActivityRecord[] = [...activity.entries()]
    .map(([system, v]) => ({ system, realizedProfit: v.realizedProfit, legCount: v.legCount }))
    .sort((x, y) => y.realizedProfit - x.realizedProfit);

  const lanesResponse: LanesResponse = { lanes, systemLanes, systemActivity, window: '6h', generatedAt: new Date(0).toISOString() };

  // --- ~10 live flows: gliding true-warp freighters + dwelling haulers, capacity mix ---
  const iso = (ms: number) => new Date(ms).toISOString();
  const epoch = Math.floor(nowMs / 600_000) * 600_000;
  const capacities = [120, 40, 80, 40, 100, 60, 80, 40, 120, 40];
  const flows: LiveFlow[] = [];
  for (let i = 0; i < 10; i++) {
    const ship = `DENSE-${i}`;
    const homeIdx = (i * 3) % RING;
    const home = sym(homeIdx);
    const cap = capacities[i];
    const good = DENSE_GOODS[i % DENSE_GOODS.length];
    if (i % 3 === 0) {
      // Gliding freighter across two adjacent ring systems (true warp). i===0 carries a
      // red drift (departedAt far in the past + travelSeconds); the rest have no estimate.
      const dest = sym((homeIdx + 1) % RING);
      flows.push({
        containerId: `tour-run-${ship}-dense`,
        program: 'tour', ship, tourId: `tour-run-${ship}-dense`, closed: false,
        // i===0 drift = (nowMs+120s) − (nowMs−1_100s + 120s) ≈ 1100s → red.
        currentLeg: { from: `${home}-A1`, to: `${dest}-B2`, departedAt: iso(nowMs - (i === 0 ? 1_100_000 : 400_000)), arrivesAt: iso(nowMs + 120_000), travelSeconds: i === 0 ? 120 : 0 },
        cargo: [{ good, units: Math.min(cap, 100) }],
        remainingHops: [{ waypoint: `${dest}-B2`, system: dest, travelSeconds: 300, tranches: [{ good, isBuy: false, units: 40, expectedUnitPrice: 2000 }] }],
        projected: { profit: 120_000, ratePerHour: 200_000 },
        plannedAt: iso(nowMs - (i === 0 ? 1_100_000 : 300_000)),
        shipNav: { status: 'IN_TRANSIT', systemSymbol: home, waypointSymbol: `${dest}-B2`, x: 0, y: 0, arrivalTime: iso(epoch + 480_000), originSymbol: `${home}-A1`, originX: 0, originY: 0, departureTime: iso(epoch - 120_000), cargoCapacity: cap },
        realized: { net: 40_000, lastEventAt: iso(nowMs - 30_000) },
      });
    } else {
      // Dwelling hauler: orbits its home node with one same-system remaining hop.
      const isTour = i % 3 === 1;
      const empty = i % 2 === 0;
      flows.push({
        containerId: `${isTour ? 'tour' : 'arb'}-run-${ship}-dense`,
        program: isTour ? 'tour' : 'arb', ship, tourId: isTour ? `tour-run-${ship}-dense` : null, closed: false,
        currentLeg: null,
        cargo: [],
        remainingHops: [{ waypoint: `${home}-C3`, system: home, travelSeconds: 0, tranches: empty ? [] : [{ good, isBuy: true, units: 30, expectedUnitPrice: 1500 }] }],
        projected: empty ? null : { profit: 60_000, ratePerHour: 90_000 },
        plannedAt: iso(nowMs - 120_000 - i * 5_000),
        shipNav: { status: 'IN_ORBIT', systemSymbol: home, waypointSymbol: `${home}-A1`, x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: cap },
        realized: { net: i * 1_000, lastEventAt: iso(nowMs - 60_000) },
      });
    }
  }
  const lastPlanAt = flows.reduce<string | null>((max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max), null);
  const live: LiveFlowsResponse = { flows, generatedAt: iso(nowMs), feedLost: false, lastPlanAt };

  return {
    topology: { systems, edges, homeSystem: sym(0), generatedAt: new Date(0).toISOString() },
    lanes: lanesResponse,
    live,
  };
}

// Ramp-spanning freshness demo (spec §6): three sensed systems staggered along
// the 0-100% solver-visibility ramp with distinct scout-post states, plus one
// demo system (X1-UU57) deliberately omitted so it renders dark/unsensed —
// no halo, no marker. freshestAt is anchored to the caller clock so the "Nm ago"
// drilldown line reads plausibly.
export function mockFreshness(): FreshnessResponse {
  return {
    systems: [
      { system: 'X1-NK36', totalListings: 42, freshListings: 40, freshnessPct: 95, freshestAt: new Date(Date.now() - 4 * 60_000).toISOString(), scoutPost: { status: 'manned', hull: 'TORWIND-9', kind: 'standing' } },
      { system: 'X1-KA42', totalListings: 60, freshListings: 30, freshnessPct: 50, freshestAt: new Date(Date.now() - 38 * 60_000).toISOString(), scoutPost: { status: 'relay', hull: null, kind: 'standing' } },
      { system: 'X1-ZC66', totalListings: 31, freshListings: 3, freshnessPct: 10, freshestAt: new Date(Date.now() - 71 * 60_000).toISOString(), scoutPost: { status: 'unmanned', hull: null, kind: 'standing' } },
      // X1-UU57 deliberately absent: unsensed — no halo, no marker.
    ],
    staleAfterMinutes: 75,
    generatedAt: new Date().toISOString(),
  };
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

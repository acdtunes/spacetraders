import { describe, it, expect } from 'vitest';
import {
  buildAdjacency, buildSystemGates, gatePath, buildStops, flowIsRelocation, projectFlowMotion,
  planRoutePolylines,
} from '../flowMotion';
import type { LiveFlow, TopologyResponse } from '../../../types/flows';

const topology: TopologyResponse = {
  systems: [
    { symbol: 'X1-AA', x: 0, y: 0, layout: 'real' },
    { symbol: 'X1-BB', x: 1000, y: 0, layout: 'real' },
    { symbol: 'X1-CC', x: 2000, y: 0, layout: 'real' },
  ],
  edges: [
    // gate_waypoint carries the CONNECTED (to-side) system's gate.
    { from: 'X1-AA', to: 'X1-BB', gateWaypoint: 'X1-BB-G1', underConstruction: false },
    { from: 'X1-BB', to: 'X1-AA', gateWaypoint: 'X1-AA-G1', underConstruction: false },
    { from: 'X1-BB', to: 'X1-CC', gateWaypoint: 'X1-CC-G1', underConstruction: false },
    { from: 'X1-CC', to: 'X1-BB', gateWaypoint: 'X1-BB-G1', underConstruction: false },
  ],
  generatedAt: 'x',
};
const adj = buildAdjacency(topology);
const gates = buildSystemGates(topology);
const pos = new Map(topology.systems.map((s) => [s.symbol, { x: s.x, y: s.y }]));
const NOW = Date.parse('2026-07-17T12:00:00Z');
const iso = (deltaSec: number) => new Date(NOW + deltaSec * 1000).toISOString();

const nav = (over: Partial<NonNullable<LiveFlow['shipNav']>>): NonNullable<LiveFlow['shipNav']> => ({
  status: 'IN_ORBIT', systemSymbol: 'X1-AA', waypointSymbol: 'X1-AA-M1', x: 0, y: 0,
  arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null, ...over,
});
const flow = (over: Partial<LiveFlow>): LiveFlow => ({
  containerId: 'tour-1', program: 'tour', ship: 'SHIP-1', tourId: 'tour-1', closed: false,
  currentLeg: null, cargo: [], remainingHops: [], projected: null,
  plannedAt: iso(-600), shipNav: nav({}), realized: { net: 0, lastEventAt: null }, ...over,
});
const hop = (waypoint: string, system: string, travelSeconds = 0, tranches: any[] = [{ good: 'IRON', isBuy: false, units: 1, expectedUnitPrice: 1 }]) =>
  ({ waypoint, system, travelSeconds, tranches });

describe('graph helpers', () => {
  it('BFS gate path, both trivial and multi-hop', () => {
    expect(gatePath(adj, 'X1-AA', 'X1-AA')).toEqual(['X1-AA']);
    expect(gatePath(adj, 'X1-AA', 'X1-CC')).toEqual(['X1-AA', 'X1-BB', 'X1-CC']);
    expect(gatePath(adj, 'X1-AA', 'X1-ZZ')).toBeNull();
  });
  it('systemGates maps each system to its own gate waypoint', () => {
    expect(gates.get('X1-AA')).toBe('X1-AA-G1');
    expect(gates.get('X1-BB')).toBe('X1-BB-G1');
  });
  it('buildStops puts the current leg destination first, then remaining hops', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-60), arrivesAt: iso(60), travelSeconds: 0 },
      remainingHops: [hop('X1-CC-M1', 'X1-CC', 900, [])],
    });
    expect(buildStops(f)).toEqual([
      { waypoint: 'X1-BB-M1', system: 'X1-BB', travelSeconds: 0, deadhead: false },
      { waypoint: 'X1-CC-M1', system: 'X1-CC', travelSeconds: 900, deadhead: true },
    ]);
  });
  it('flowIsRelocation is true only when every hop is trade-less', () => {
    expect(flowIsRelocation(flow({ remainingHops: [hop('X1-BB-M1', 'X1-BB', 0, [])] }))).toBe(true);
    expect(flowIsRelocation(flow({ remainingHops: [hop('X1-BB-M1', 'X1-BB')] }))).toBe(false);
    expect(flowIsRelocation(flow({ remainingHops: [] }))).toBe(false);
  });
});

describe('projectFlowMotion', () => {
  it('dwells in orbit when the objective is intra-system', () => {
    const f = flow({ remainingHops: [hop('X1-AA-M2', 'X1-AA')] });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('dwell');
    const r = Math.hypot(m.x - 0, m.y - 0);
    expect(r).toBeGreaterThan(1);
    expect(r).toBeLessThan(20);
  });

  it('outbound half: in-transit toward own gate maps to [0, 0.5]', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-60), arrivesAt: iso(60), travelSeconds: 0 },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({ status: 'IN_TRANSIT', waypointSymbol: 'X1-AA-G1', departureTime: iso(-60), arrivalTime: iso(60) }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('glide');
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.25, 5); // t=0.5 → s=0.25
    expect(m.x).toBeCloseTo(250, 0);
    expect(m.bearingRad).toBeCloseTo(0, 5); // due +x
  });

  it('holds at 0.47 parked at own gate pre-jump', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-120), arrivesAt: iso(-10), travelSeconds: 0 },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({ status: 'IN_ORBIT', waypointSymbol: 'X1-AA-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.phase).toBeCloseTo(0.47, 5);
  });

  it('arrival half: in-transit FROM own gate completes the incoming edge [0.5, 1]', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-300), arrivesAt: iso(100), travelSeconds: 0 },
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 420)],
      shipNav: nav({
        status: 'IN_TRANSIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-M1',
        originSymbol: 'X1-BB-G1', departureTime: iso(-60), arrivalTime: iso(40),
      }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.5 + 0.5 * 0.6, 5);
    expect(m.x).toBeCloseTo(800, 0);
  });

  it('holds at 0.53 on the incoming edge during post-jump cooldown', () => {
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-CC-M1', departedAt: iso(-300), arrivesAt: iso(600), travelSeconds: 0 },
      remainingHops: [hop('X1-CC-M1', 'X1-CC', 900)],
      shipNav: nav({ status: 'IN_ORBIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    // Pass-through at X1-BB (came from AA, heading CC): held just past the AA→BB midpoint.
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.53, 5);
  });

  it('holds at 0.53 when parked at the DESTINATION system\'s own gate (single-hop cooldown)', () => {
    // Regression: A→B jump just completed, ship IN_ORBIT at B's own gate,
    // currentLeg still points at a market in B. This must render the
    // POST_JUMP_HOLD on the A→B edge — not dwell at B's node (which caused a
    // forward teleport to the node then a backward teleport to the midpoint
    // when the gate→market flight started).
    const f = flow({
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-300), arrivesAt: iso(120), travelSeconds: 0 },
      shipNav: nav({ status: 'IN_ORBIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('glide');
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB' });
    expect(m.phase).toBeCloseTo(0.53, 5);
  });

  it('still dwells when parked at own gate with no crossing behind it', () => {
    // Parked at our own gate but the current leg is intra-system: no jump
    // occurred, so a dwell at the node is correct (no phantom cooldown).
    const f = flow({
      currentLeg: { from: 'X1-BB-M2', to: 'X1-BB-M1', departedAt: iso(-300), arrivesAt: iso(-10), travelSeconds: 0 },
      shipNav: nav({ status: 'IN_ORBIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-G1' }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('dwell');
    expect(m.fromSystem).toBe('X1-BB');
  });

  it('true warp renders directly on the origin→destination edge', () => {
    const f = flow({
      remainingHops: [hop('X1-BB-M1', 'X1-BB', 0)],
      shipNav: nav({
        status: 'IN_TRANSIT', systemSymbol: 'X1-BB', waypointSymbol: 'X1-BB-M1',
        originSymbol: 'X1-AA-M1', departureTime: iso(-50), arrivalTime: iso(50),
      }),
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m).toMatchObject({ fromSystem: 'X1-AA', toSystem: 'X1-BB', mode: 'glide' });
    expect(m.phase).toBeCloseTo(0.5, 5);
    expect(m.x).toBeCloseTo(500, 0);
  });

  it('falls back to currentLeg timestamp lerp when shipNav is missing', () => {
    const f = flow({
      shipNav: null,
      currentLeg: { from: 'X1-AA-M1', to: 'X1-BB-M1', departedAt: iso(-50), arrivesAt: iso(50), travelSeconds: 0 },
    });
    const m = projectFlowMotion(f, adj, gates, pos, NOW, 1)!;
    expect(m.mode).toBe('glide');
    expect(m.x).toBeCloseTo(500, 0);
  });

  it('returns null when the hull system has no known position', () => {
    const f = flow({ shipNav: nav({ systemSymbol: 'X1-ZZ' }) });
    expect(projectFlowMotion(f, adj, gates, pos, NOW, 1)).toBeNull();
  });
});

describe('planRoutePolylines', () => {
  it('expands stop pairs through the gate graph with deadhead flags', () => {
    const f = flow({
      shipNav: nav({ systemSymbol: 'X1-AA' }),
      remainingHops: [hop('X1-CC-M1', 'X1-CC', 900), hop('X1-AA-M9', 'X1-AA', 900, [])],
    });
    const segs = planRoutePolylines(f, adj, pos);
    expect(segs).toHaveLength(2);
    // AA→CC runs through BB: 3 points = 6 numbers.
    expect(segs[0].points).toEqual([0, 0, 1000, 0, 2000, 0]);
    expect(segs[0].deadhead).toBe(false);
    // CC→AA return leg is trade-less → deadhead.
    expect(segs[1].deadhead).toBe(true);
  });
});

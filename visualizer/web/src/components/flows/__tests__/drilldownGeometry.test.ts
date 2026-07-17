import { describe, it, expect } from 'vitest';
import {
  fitToViewport,
  applyFit,
  buildWaypointIndex,
  gateAnchor,
  classifyLaneForSystem,
  resolveLaneSegment,
  residentFlows,
  hullWaypointInSystem,
  intentWaypointsInSystem,
  hopKind,
  tourRouteStops,
  tourRoutePathInSystem,
  interpolateHullInSystem,
  type WaypointPoint,
} from '../drilldownGeometry';
import type { FlowTranche, LaneRecord, LiveFlow } from '../../../types/flows';

const wps: WaypointPoint[] = [
  { symbol: 'X1-UQ16-FF5F', type: 'PLANET', x: -100, y: -100 },
  { symbol: 'X1-UQ16-ZZ3F', type: 'MOON', x: 100, y: 100 },
  { symbol: 'X1-UQ16-XB8B', type: 'ASTEROID', x: 0, y: 50 },
  { symbol: 'X1-UQ16-GATE', type: 'JUMP_GATE', x: 200, y: -200 },
];

describe('fitToViewport', () => {
  it('preserves aspect ratio and centers the content in the viewport', () => {
    const t = fitToViewport([{ x: -100, y: -100 }, { x: 100, y: 100 }], 800, 600, 40);
    // Single scale on both axes (square stays square).
    const a = applyFit({ x: -100, y: -100 }, t);
    const b = applyFit({ x: 100, y: 100 }, t);
    expect(b.x - a.x).toBeCloseTo(b.y - a.y, 6); // equal on-screen span => aspect preserved
    // The world centroid maps to the viewport center.
    const c = applyFit({ x: 0, y: 0 }, t);
    expect(c.x).toBeCloseTo(400, 6);
    expect(c.y).toBeCloseTo(300, 6);
  });

  it('keeps every point inside the padded viewport', () => {
    const t = fitToViewport(wps, 800, 600, 40);
    for (const w of wps) {
      const p = applyFit(w, t);
      expect(p.x).toBeGreaterThanOrEqual(40 - 1e-6);
      expect(p.x).toBeLessThanOrEqual(800 - 40 + 1e-6);
      expect(p.y).toBeGreaterThanOrEqual(40 - 1e-6);
      expect(p.y).toBeLessThanOrEqual(600 - 40 + 1e-6);
    }
  });

  it('is limited by the tighter axis (tall content is height-bound)', () => {
    // Wide-and-short box: x span 1000, y span 100 in a square viewport.
    const t = fitToViewport([{ x: 0, y: 0 }, { x: 1000, y: 100 }], 500, 500, 50);
    // Width-bound: availW / spanX = 400 / 1000 = 0.4.
    expect(t.scale).toBeCloseTo(0.4, 6);
  });

  it('centers a single point at the viewport center', () => {
    const t = fitToViewport([{ x: 42, y: -7 }], 800, 600, 40);
    const p = applyFit({ x: 42, y: -7 }, t);
    expect(p.x).toBeCloseTo(400, 6);
    expect(p.y).toBeCloseTo(300, 6);
  });

  it('returns a centered transform for empty input', () => {
    const t = fitToViewport([], 800, 600);
    expect(t).toEqual({ scale: 1, offsetX: 400, offsetY: 300 });
  });
});

describe('gateAnchor', () => {
  it('returns the JUMP_GATE waypoint position when present', () => {
    expect(gateAnchor(wps)).toEqual({ x: 200, y: -200 });
  });
  it('falls back to the centroid when there is no gate', () => {
    const noGate = wps.filter((w) => w.type !== 'JUMP_GATE');
    const anchor = gateAnchor(noGate)!;
    expect(anchor.x).toBeCloseTo((-100 + 100 + 0) / 3, 6);
    expect(anchor.y).toBeCloseTo((-100 + 100 + 50) / 3, 6);
  });
  it('returns null for no waypoints', () => {
    expect(gateAnchor([])).toBeNull();
  });
});

describe('classifyLaneForSystem', () => {
  const lane = (from: string, to: string): LaneRecord => ({ from, to, realizedUnits: 1, realizedProfit: 1, legCount: 1 });
  it('labels both-endpoints-in-system as intra', () => {
    expect(classifyLaneForSystem(lane('X1-UQ16-FF5F', 'X1-UQ16-ZZ3F'), 'X1-UQ16')).toBe('intra');
  });
  it('labels a departing lane as exit and an arriving lane as entry', () => {
    expect(classifyLaneForSystem(lane('X1-UQ16-FF5F', 'X1-JP61-A1'), 'X1-UQ16')).toBe('exit');
    expect(classifyLaneForSystem(lane('X1-JP61-A1', 'X1-UQ16-ZZ3F'), 'X1-UQ16')).toBe('entry');
  });
  it('labels a lane with neither endpoint here as external', () => {
    expect(classifyLaneForSystem(lane('X1-JP61-A1', 'X1-HU21-B2'), 'X1-UQ16')).toBe('external');
  });
});

describe('resolveLaneSegment', () => {
  const idx = buildWaypointIndex(wps);
  const gate = gateAnchor(wps);
  const lane = (from: string, to: string, profit: number): LaneRecord => ({ from, to, realizedUnits: 10, realizedProfit: profit, legCount: 2 });

  it('draws an intra lane between the two true waypoint positions', () => {
    const seg = resolveLaneSegment(lane('X1-UQ16-FF5F', 'X1-UQ16-ZZ3F', 5000), 'X1-UQ16', idx, gate)!;
    expect(seg.kind).toBe('intra');
    expect(seg.from).toEqual({ x: -100, y: -100 });
    expect(seg.to).toEqual({ x: 100, y: 100 });
    expect(seg.profit).toBe(5000);
  });

  it('draws an exit lane from the in-system endpoint toward the gate', () => {
    const seg = resolveLaneSegment(lane('X1-UQ16-FF5F', 'X1-JP61-A1', 900), 'X1-UQ16', idx, gate)!;
    expect(seg.kind).toBe('exit');
    expect(seg.from).toEqual({ x: -100, y: -100 });
    expect(seg.to).toEqual(gate);
  });

  it('draws an entry lane from the gate toward the in-system endpoint', () => {
    const seg = resolveLaneSegment(lane('X1-JP61-A1', 'X1-UQ16-ZZ3F', 300), 'X1-UQ16', idx, gate)!;
    expect(seg.kind).toBe('entry');
    expect(seg.from).toEqual(gate);
    expect(seg.to).toEqual({ x: 100, y: 100 });
  });

  it('returns null for an external lane', () => {
    expect(resolveLaneSegment(lane('X1-JP61-A1', 'X1-HU21-B2', 1), 'X1-UQ16', idx, gate)).toBeNull();
  });

  it('returns null when the in-system endpoint is not among fetched waypoints', () => {
    expect(resolveLaneSegment(lane('X1-UQ16-MISSING', 'X1-UQ16-ZZ3F', 1), 'X1-UQ16', idx, gate)).toBeNull();
  });
});

const baseFlow = (overrides: Partial<LiveFlow>): LiveFlow => ({
  containerId: 'c', program: 'tour', ship: 'S', tourId: null, closed: false,
  currentLeg: null, cargo: [], remainingHops: [], projected: null,
  plannedAt: '2026-07-11T00:00:00Z', shipNav: null,
  realized: { net: 0, lastEventAt: null }, ...overrides,
});

describe('residentFlows / hullWaypointInSystem', () => {
  it('keeps flows whose nav is here or whose current leg touches the system', () => {
    const navHere = baseFlow({ containerId: 'a', shipNav: { status: 'DOCKED', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-FF5F', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null } });
    const legHere = baseFlow({ containerId: 'b', currentLeg: { from: 'X1-JP61-A1', to: 'X1-UQ16-ZZ3F', departedAt: '', arrivesAt: '', travelSeconds: 0 } });
    const elsewhere = baseFlow({ containerId: 'c', shipNav: { status: 'DOCKED', systemSymbol: 'X1-JP61', waypointSymbol: 'X1-JP61-A1', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null } });
    const kept = residentFlows([navHere, legHere, elsewhere], 'X1-UQ16').map((f) => f.containerId);
    expect(kept).toEqual(['a', 'b']);
  });

  it('reports the actual in-system waypoint for a resident hull, null when elsewhere', () => {
    const here = baseFlow({ shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-XB8B', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null } });
    const away = baseFlow({ shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-JP61', waypointSymbol: 'X1-JP61-A1', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null } });
    expect(hullWaypointInSystem(here, 'X1-UQ16')).toBe('X1-UQ16-XB8B');
    expect(hullWaypointInSystem(away, 'X1-UQ16')).toBeNull();
    expect(hullWaypointInSystem(baseFlow({}), 'X1-UQ16')).toBeNull();
  });
});

describe('intentWaypointsInSystem', () => {
  it('chains the hull waypoint and in-system remaining hops, dropping cross-system hops', () => {
    const flow = baseFlow({
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-FF5F', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null },
      remainingHops: [
        { waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [] },
        { waypoint: 'X1-JP61-A1', system: 'X1-JP61', travelSeconds: 0, tranches: [] }, // cross-system: excluded
        { waypoint: 'X1-UQ16-XB8B', system: 'X1-UQ16', travelSeconds: 0, tranches: [] },
      ],
    });
    expect(intentWaypointsInSystem(flow, 'X1-UQ16')).toEqual(['X1-UQ16-FF5F', 'X1-UQ16-ZZ3F', 'X1-UQ16-XB8B']);
  });

  it('does not duplicate the hull waypoint when it equals the first hop', () => {
    const flow = baseFlow({
      shipNav: { status: 'DOCKED', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-ZZ3F', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null },
      remainingHops: [{ waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [] }, { waypoint: 'X1-UQ16-FF5F', system: 'X1-UQ16', travelSeconds: 0, tranches: [] }],
    });
    expect(intentWaypointsInSystem(flow, 'X1-UQ16')).toEqual(['X1-UQ16-ZZ3F', 'X1-UQ16-FF5F']);
  });

  it('is empty when the flow has no in-system presence or intent', () => {
    expect(intentWaypointsInSystem(baseFlow({}), 'X1-UQ16')).toEqual([]);
  });
});

const tranche = (isBuy: boolean): FlowTranche => ({ good: 'G', isBuy, units: 1, expectedUnitPrice: 1 });

describe('hopKind', () => {
  it('classifies a stop by its tranches: all-buy, all-sell, mixed, or none', () => {
    expect(hopKind([tranche(true), tranche(true)])).toBe('buy');
    expect(hopKind([tranche(false)])).toBe('sell');
    expect(hopKind([tranche(true), tranche(false)])).toBe('mixed');
    expect(hopKind([])).toBe('none');
  });
});

describe('tourRouteStops', () => {
  it('emits the remaining hops as globally-ordered 1-based stops with buy/sell kind', () => {
    const flow = baseFlow({
      remainingHops: [
        { waypoint: 'X1-UQ16-FF5F', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(false)] },       // sell
        { waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(true)] },        // buy
        { waypoint: 'X1-JP61-A1', system: 'X1-JP61', travelSeconds: 0, tranches: [tranche(true), tranche(false)] }, // mixed, cross-system
      ],
    });
    expect(tourRouteStops(flow)).toEqual([
      { index: 1, waypoint: 'X1-UQ16-FF5F', kind: 'sell' },
      { index: 2, waypoint: 'X1-UQ16-ZZ3F', kind: 'buy' },
      { index: 3, waypoint: 'X1-JP61-A1', kind: 'mixed' },
    ]);
  });

  it('is empty for a flow with no remaining hops', () => {
    expect(tourRouteStops(baseFlow({}))).toEqual([]);
  });
});

describe('tourRoutePathInSystem', () => {
  const idx = buildWaypointIndex(wps);
  const gate = gateAnchor(wps)!;

  it('walks depart -> in-system stops -> gate exit, numbering stops by global order', () => {
    // Intra-system current leg (FF5F -> XB8B, both here) then a cross-system stop.
    const flow = baseFlow({
      currentLeg: { from: 'X1-UQ16-FF5F', to: 'X1-UQ16-XB8B', departedAt: '', arrivesAt: '', travelSeconds: 0 },
      remainingHops: [
        { waypoint: 'X1-UQ16-XB8B', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(false)] }, // stop 1, sell, in-system
        { waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(true)] },  // stop 2, buy, in-system
        { waypoint: 'X1-JP61-A1', system: 'X1-JP61', travelSeconds: 0, tranches: [tranche(false)] },   // stop 3, cross-system -> gate exit
      ],
    });
    const anchors = tourRoutePathInSystem(flow, 'X1-UQ16', idx, gate);
    expect(anchors.map((a) => ({ index: a.index, kind: a.kind, waypoint: a.waypoint }))).toEqual([
      { index: 0, kind: 'depart', waypoint: 'X1-UQ16-FF5F' },
      { index: 1, kind: 'sell', waypoint: 'X1-UQ16-XB8B' },
      { index: 2, kind: 'buy', waypoint: 'X1-UQ16-ZZ3F' },
      { index: 3, kind: 'exit', waypoint: null },
    ]);
    // The exit anchor sits at the gate; in-system anchors at their true positions.
    expect(anchors[3].point).toEqual(gate);
    expect(anchors[1].point).toEqual({ x: 0, y: 50 }); // XB8B
  });

  it('leads with a gate ENTRY anchor when the ship is arriving from another system', () => {
    const flow = baseFlow({
      currentLeg: { from: 'X1-JP61-A1', to: 'X1-UQ16-ZZ3F', departedAt: '', arrivesAt: '', travelSeconds: 0 },
      remainingHops: [{ waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(true)] }],
    });
    const anchors = tourRoutePathInSystem(flow, 'X1-UQ16', idx, gate);
    expect(anchors[0]).toEqual({ index: 0, kind: 'entry', waypoint: null, point: gate });
    expect(anchors[1]).toMatchObject({ index: 1, kind: 'buy', waypoint: 'X1-UQ16-ZZ3F' });
  });

  it('stays entirely in-system when the route never leaves (no gate anchors)', () => {
    const flow = baseFlow({
      currentLeg: { from: 'X1-UQ16-FF5F', to: 'X1-UQ16-ZZ3F', departedAt: '', arrivesAt: '', travelSeconds: 0 },
      remainingHops: [
        { waypoint: 'X1-UQ16-ZZ3F', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(false)] },
        { waypoint: 'X1-UQ16-XB8B', system: 'X1-UQ16', travelSeconds: 0, tranches: [tranche(true)] },
      ],
    });
    const anchors = tourRoutePathInSystem(flow, 'X1-UQ16', idx, gate);
    expect(anchors.every((a) => a.kind !== 'exit' && a.kind !== 'entry')).toBe(true);
    expect(anchors.map((a) => a.kind)).toEqual(['depart', 'sell', 'buy']);
  });

  it('is empty when the flow has no route to draw', () => {
    expect(tourRoutePathInSystem(baseFlow({}), 'X1-UQ16', idx, gate)).toEqual([]);
  });
});

describe('interpolateHullInSystem', () => {
  const idx = buildWaypointIndex(wps);
  const gate = gateAnchor(wps)!;
  const leg = (from: string, to: string, dep: number, arr: number) => ({
    from, to, departedAt: new Date(dep).toISOString(), arrivesAt: new Date(arr).toISOString(), travelSeconds: 0,
  });

  it('interpolates halfway between two in-system waypoints on an intra-system leg', () => {
    const flow = baseFlow({ currentLeg: leg('X1-UQ16-FF5F', 'X1-UQ16-ZZ3F', 0, 1000) });
    const p = interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 500)!;
    expect(p.x).toBeCloseTo(0, 6);  // midpoint of (-100,-100) and (100,100)
    expect(p.y).toBeCloseTo(0, 6);
  });

  it('clamps to the origin before departure and the destination after arrival', () => {
    const flow = baseFlow({ currentLeg: leg('X1-UQ16-FF5F', 'X1-UQ16-ZZ3F', 1000, 2000) });
    expect(interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 0)).toEqual({ x: -100, y: -100 });
    expect(interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 5000)).toEqual({ x: 100, y: 100 });
  });

  it('interpolates from the in-system waypoint toward the gate when the ship is exiting', () => {
    const flow = baseFlow({ currentLeg: leg('X1-UQ16-FF5F', 'X1-JP61-A1', 0, 1000) });
    const p = interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 500)!;
    expect(p.x).toBeCloseTo((-100 + gate.x) / 2, 6); // midpoint FF5F -> gate
    expect(p.y).toBeCloseTo((-100 + gate.y) / 2, 6);
  });

  it('interpolates from the gate toward the in-system waypoint when the ship is entering', () => {
    const flow = baseFlow({ currentLeg: leg('X1-JP61-A1', 'X1-UQ16-ZZ3F', 0, 1000) });
    const p = interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 500)!;
    expect(p.x).toBeCloseTo((gate.x + 100) / 2, 6); // midpoint gate -> ZZ3F
    expect(p.y).toBeCloseTo((gate.y + 100) / 2, 6);
  });

  it('sits a docked/orbiting hull at its actual waypoint (no current leg)', () => {
    const flow = baseFlow({
      shipNav: { status: 'DOCKED', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-XB8B', x: 0, y: 0, arrivalTime: null, originSymbol: null, originX: null, originY: null, departureTime: null, cargoCapacity: null },
    });
    expect(interpolateHullInSystem(flow, 'X1-UQ16', idx, gate, 500)).toEqual({ x: 0, y: 50 });
  });

  it('returns null when the hull is neither in transit here nor docked here', () => {
    expect(interpolateHullInSystem(baseFlow({}), 'X1-UQ16', idx, gate, 500)).toBeNull();
  });
});

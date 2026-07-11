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
  type WaypointPoint,
} from '../drilldownGeometry';
import type { LaneRecord, LiveFlow } from '../../../types/flows';

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
  containerId: 'c', program: 'tour', ship: 'S', tourId: null,
  currentLeg: null, cargo: [], remainingHops: [], projected: null,
  plannedAt: '2026-07-11T00:00:00Z', shipNav: null, ...overrides,
});

describe('residentFlows / hullWaypointInSystem', () => {
  it('keeps flows whose nav is here or whose current leg touches the system', () => {
    const navHere = baseFlow({ containerId: 'a', shipNav: { status: 'DOCKED', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-FF5F', x: 0, y: 0, arrivalTime: null } });
    const legHere = baseFlow({ containerId: 'b', currentLeg: { from: 'X1-JP61-A1', to: 'X1-UQ16-ZZ3F', departedAt: '', arrivesAt: '' } });
    const elsewhere = baseFlow({ containerId: 'c', shipNav: { status: 'DOCKED', systemSymbol: 'X1-JP61', waypointSymbol: 'X1-JP61-A1', x: 0, y: 0, arrivalTime: null } });
    const kept = residentFlows([navHere, legHere, elsewhere], 'X1-UQ16').map((f) => f.containerId);
    expect(kept).toEqual(['a', 'b']);
  });

  it('reports the actual in-system waypoint for a resident hull, null when elsewhere', () => {
    const here = baseFlow({ shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-XB8B', x: 0, y: 0, arrivalTime: null } });
    const away = baseFlow({ shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-JP61', waypointSymbol: 'X1-JP61-A1', x: 0, y: 0, arrivalTime: null } });
    expect(hullWaypointInSystem(here, 'X1-UQ16')).toBe('X1-UQ16-XB8B');
    expect(hullWaypointInSystem(away, 'X1-UQ16')).toBeNull();
    expect(hullWaypointInSystem(baseFlow({}), 'X1-UQ16')).toBeNull();
  });
});

describe('intentWaypointsInSystem', () => {
  it('chains the hull waypoint and in-system remaining hops, dropping cross-system hops', () => {
    const flow = baseFlow({
      shipNav: { status: 'IN_ORBIT', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-FF5F', x: 0, y: 0, arrivalTime: null },
      remainingHops: [
        { waypoint: 'X1-UQ16-ZZ3F', tranches: [] },
        { waypoint: 'X1-JP61-A1', tranches: [] }, // cross-system: excluded
        { waypoint: 'X1-UQ16-XB8B', tranches: [] },
      ],
    });
    expect(intentWaypointsInSystem(flow, 'X1-UQ16')).toEqual(['X1-UQ16-FF5F', 'X1-UQ16-ZZ3F', 'X1-UQ16-XB8B']);
  });

  it('does not duplicate the hull waypoint when it equals the first hop', () => {
    const flow = baseFlow({
      shipNav: { status: 'DOCKED', systemSymbol: 'X1-UQ16', waypointSymbol: 'X1-UQ16-ZZ3F', x: 0, y: 0, arrivalTime: null },
      remainingHops: [{ waypoint: 'X1-UQ16-ZZ3F', tranches: [] }, { waypoint: 'X1-UQ16-FF5F', tranches: [] }],
    });
    expect(intentWaypointsInSystem(flow, 'X1-UQ16')).toEqual(['X1-UQ16-ZZ3F', 'X1-UQ16-FF5F']);
  });

  it('is empty when the flow has no in-system presence or intent', () => {
    expect(intentWaypointsInSystem(baseFlow({}), 'X1-UQ16')).toEqual([]);
  });
});

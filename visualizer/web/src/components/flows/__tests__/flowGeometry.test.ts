import { describe, it, expect } from 'vitest';
import {
  systemOf,
  buildSystemIndex,
  projectFlowShip,
  laneProfitColor,
  laneWidth,
  planPathPoints,
  offsetSegmentRight,
  pointAlong,
  laneDashPhase,
} from '../flowGeometry';
import { mockTopology, mockLiveFlows } from '../../../mocks/mockFlows';
import type { LiveFlow } from '../../../types/flows';

const idx = buildSystemIndex(mockTopology);

describe('systemOf', () => {
  it('extracts the system prefix from a waypoint symbol', () => {
    expect(systemOf('X1-NK36-FE8A')).toBe('X1-NK36');
    expect(systemOf('X1-KA42-D39')).toBe('X1-KA42');
  });
});

describe('projectFlowShip', () => {
  const baseFlow = (overrides: Partial<LiveFlow>): LiveFlow => ({
    containerId: 'c', program: 'tour', ship: 'S', tourId: null,
    currentLeg: { from: 'X1-NK36-A', to: 'X1-KA42-B', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:10:00Z' },
    cargo: [], remainingHops: [], projected: null, plannedAt: '2026-07-11T00:00:00Z', shipNav: null,
    ...overrides,
  });

  it('clamps to the origin system before departure', () => {
    const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-10T23:00:00Z'));
    expect(p).toEqual(idx.get('X1-NK36'));
  });

  it('clamps to the destination system after arrival', () => {
    const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-11T01:00:00Z'));
    expect(p).toEqual(idx.get('X1-KA42'));
  });

  it('interpolates halfway at the leg midpoint', () => {
    const from = idx.get('X1-NK36')!;
    const to = idx.get('X1-KA42')!;
    const p = projectFlowShip(baseFlow({}), idx, Date.parse('2026-07-11T00:05:00Z'))!;
    expect(p.x).toBeCloseTo((from.x + to.x) / 2, 3);
    expect(p.y).toBeCloseTo((from.y + to.y) / 2, 3);
  });

  it('returns the origin position for an intra-system leg', () => {
    const p = projectFlowShip(
      baseFlow({ currentLeg: { from: 'X1-NK36-A', to: 'X1-NK36-B', departedAt: '2026-07-11T00:00:00Z', arrivesAt: '2026-07-11T00:10:00Z' } }),
      idx, Date.parse('2026-07-11T00:05:00Z'),
    );
    expect(p).toEqual(idx.get('X1-NK36'));
  });

  it('falls back to last-known shipNav system when no current leg', () => {
    const p = projectFlowShip(
      baseFlow({ currentLeg: null, shipNav: { status: 'DOCKED', systemSymbol: 'X1-ZC66', waypointSymbol: 'X1-ZC66-C', x: 0, y: 0, arrivalTime: null } }),
      idx, Date.now(),
    );
    expect(p).toEqual(idx.get('X1-ZC66'));
  });

  it('returns null when neither leg nor known nav resolves', () => {
    const p = projectFlowShip(baseFlow({ currentLeg: null, shipNav: null }), idx, Date.now());
    expect(p).toBeNull();
  });
});

describe('laneProfitColor / laneWidth', () => {
  it('maps loss to the dim token and large profit to the star token', () => {
    expect(laneProfitColor(-5000)).toBe('#5A6478');
    expect(laneProfitColor(500000)).toBe('#F5E9C8');
  });
  it('width grows with magnitude and shrinks with scale', () => {
    expect(laneWidth(500000, 1)).toBeGreaterThan(laneWidth(100, 1));
    expect(laneWidth(500000, 4)).toBeLessThan(laneWidth(500000, 1));
  });
});

describe('planPathPoints', () => {
  it('emits one polyline segment per remaining hop, in system space', () => {
    const tour = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')).flows.find((f) => f.program === 'tour')!;
    const segments = planPathPoints(tour, idx);
    expect(segments.length).toBe(tour.remainingHops.length);
    for (const seg of segments) expect(seg).toHaveLength(4); // [x1,y1,x2,y2]
  });
});

describe('offsetSegmentRight', () => {
  it('shifts a horizontal segment sideways by exactly offsetPx, perpendicular to travel', () => {
    const { from, to } = offsetSegmentRight({ x: 0, y: 0 }, { x: 10, y: 0 }, 2);
    // Normal (dy,-dx)/|d| of (10,0) is (0,-1): both endpoints move by (0,-2).
    expect(from).toEqual({ x: 0, y: -2 });
    expect(to).toEqual({ x: 10, y: -2 });
  });

  it('offsets the reverse direction to the OPPOSITE side (bidirectional lanes never overlap)', () => {
    const fwd = offsetSegmentRight({ x: 0, y: 0 }, { x: 10, y: 0 }, 2);
    const rev = offsetSegmentRight({ x: 10, y: 0 }, { x: 0, y: 0 }, 2);
    // Forward sits at y=-2, reverse at y=+2 — mirror images across the true line.
    expect(fwd.from.y).toBeCloseTo(-2, 6);
    expect(rev.from.y).toBeCloseTo(2, 6);
  });

  it('keeps the offset vector perpendicular to the segment with magnitude offsetPx', () => {
    const a = { x: -30, y: 40 };
    const b = { x: 90, y: -10 };
    const off = offsetSegmentRight(a, b, 5);
    const dx = b.x - a.x;
    const dy = b.y - a.y;
    const ox = off.from.x - a.x;
    const oy = off.from.y - a.y;
    expect(dx * ox + dy * oy).toBeCloseTo(0, 6);          // perpendicular
    expect(Math.hypot(ox, oy)).toBeCloseTo(5, 6);         // magnitude preserved
  });

  it('returns the endpoints unchanged for a degenerate zero-length segment', () => {
    const p = { x: 7, y: 7 };
    expect(offsetSegmentRight(p, { ...p }, 3)).toEqual({ from: p, to: { ...p } });
  });
});

describe('pointAlong', () => {
  it('returns the endpoints at t=0 and t=1 and the midpoint at t=0.5', () => {
    const a = { x: 0, y: 0 };
    const b = { x: 8, y: 4 };
    expect(pointAlong(a, b, 0)).toEqual(a);
    expect(pointAlong(a, b, 1)).toEqual(b);
    expect(pointAlong(a, b, 0.5)).toEqual({ x: 4, y: 2 });
  });
});

describe('laneDashPhase', () => {
  it('decreases over time so dashes crawl toward the destination', () => {
    expect(laneDashPhase(2000, 1)).toBeLessThan(laneDashPhase(1000, 1));
  });
  it('scale-normalizes so on-screen crawl speed holds while zooming', () => {
    expect(Math.abs(laneDashPhase(1000, 2))).toBeLessThan(Math.abs(laneDashPhase(1000, 1)));
  });
});

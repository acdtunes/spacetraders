import { describe, it, expect } from 'vitest';
import {
  systemOf,
  buildSystemIndex,
  projectFlowShip,
  laneProfitColor,
  laneWidth,
  planPathPoints,
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

import { describe, it, expect } from 'vitest';
import { getRouteEndpoint } from '../routeVectorsUtils';
import type { TaggedShip, Waypoint as WaypointType } from '../../types/spacetraders';

const createWaypoint = (overrides: Partial<WaypointType>): WaypointType => ({
  symbol: 'DEST',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 100,
  y: 0,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
  ...overrides,
});

describe('RouteVectors#getRouteEndpoint', () => {
  it('returns null when route destination is missing', () => {
    const ship = {
      symbol: 'TEST',
      nav: {
        status: 'IN_TRANSIT',
        waypointSymbol: 'ORIGIN',
        systemSymbol: 'X1-TEST',
        flightMode: 'CRUISE',
        route: {
          origin: { symbol: 'ORIGIN', x: 0, y: 0 },
          destination: undefined,
          departureTime: new Date().toISOString(),
          arrival: new Date(Date.now() + 1000).toISOString(),
        },
      },
    } as unknown as TaggedShip;

    const result = getRouteEndpoint(ship, { x: 0, y: 0 }, new Map());
    expect(result).toBeNull();
  });

  it('stops endpoint before orbit radius when approaching waypoint', () => {
    const now = Date.now();
    const ship = {
      symbol: 'TEST-2',
      nav: {
        status: 'IN_TRANSIT',
        waypointSymbol: 'ORIGIN',
        systemSymbol: 'X1-TEST',
        flightMode: 'CRUISE',
        route: {
          origin: { symbol: 'ORIGIN', x: 0, y: 0 },
          destination: { symbol: 'DEST', x: 100, y: 0 },
          departureTime: new Date(now - 1000).toISOString(),
          arrival: new Date(now + 60000).toISOString(),
        },
      },
    } as unknown as TaggedShip;

    const waypoints = new Map<string, WaypointType>([
      ['DEST', createWaypoint({ symbol: 'DEST', x: 100, y: 0 })],
    ]);

    const endpoint = getRouteEndpoint(ship, { x: 0, y: 0 }, waypoints);
    expect(endpoint).not.toBeNull();
    const totalDistance = Math.hypot((endpoint!.x - 0), (endpoint!.y - 0));
    expect(totalDistance).toBeLessThan(100);
    expect(endpoint?.x).toBeGreaterThan(0);
  });
});

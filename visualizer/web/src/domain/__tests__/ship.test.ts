import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { Ship } from '../ship';
import { Waypoint } from '../waypoint';
import type { Ship as ShipType, Waypoint as WaypointType } from '../../types/spacetraders';
import { CANVAS_CONSTANTS } from '../../constants/canvas';

describe('Ship domain', () => {
  const baseWaypoint: WaypointType = {
    symbol: 'X1-TEST-A1',
    type: 'PLANET',
    systemSymbol: 'X1-TEST',
    x: 50,
    y: 0,
    orbitals: [],
    traits: [],
    chart: null,
    isUnderConstruction: false,
  };

  const waypoints = new Map([[baseWaypoint.symbol, baseWaypoint]]);

  const baseShip: ShipType = {
    symbol: 'TEST-1',
    registration: {
      name: 'Test Ship',
      factionSymbol: 'COSMIC',
      role: 'COMMAND',
    },
    nav: {
      systemSymbol: 'X1-TEST',
      waypointSymbol: baseWaypoint.symbol,
      status: 'DOCKED',
      flightMode: 'CRUISE',
      route: {
        origin: { symbol: baseWaypoint.symbol, x: 0, y: 0 },
        destination: { symbol: baseWaypoint.symbol, x: baseWaypoint.x, y: baseWaypoint.y },
        departureTime: new Date().toISOString(),
        arrival: new Date(Date.now() + 60000).toISOString(),
      },
    },
    crew: { current: 1, required: 1, capacity: 5, morale: 100, wages: 0 },
    fuel: { current: 50, capacity: 100, consumed: { amount: 0, timestamp: new Date().toISOString() } },
    frame: {} as any,
    reactor: {} as any,
    engine: {} as any,
    modules: [],
    mounts: [],
    cargo: { capacity: 100, units: 0, inventory: [] },
    cooldown: { remainingSeconds: 0, totalSeconds: 0 },
    state: 'OPERATIONAL',
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('offsets docked ships around the waypoint to avoid overlap', () => {
    const position = Ship.getDockedPosition(baseShip, waypoints);
    expect(position.x).not.toBe(baseWaypoint.x);
    expect(position.y).not.toBe(baseWaypoint.y);

    const radius = Math.hypot(position.x - baseWaypoint.x, position.y - baseWaypoint.y);
    expect(radius).toBeGreaterThan(Waypoint.getRadius(baseWaypoint));
  });

  it('aligns orbit entry based on destination orbit radius when transit completes', () => {
    const arrival = new Date(Date.now() - 1000).toISOString();
    const ship: ShipType = {
      ...baseShip,
      nav: {
        ...baseShip.nav,
        status: 'IN_TRANSIT',
        route: {
          origin: { symbol: baseWaypoint.symbol, x: 0, y: 0 },
          destination: { symbol: baseWaypoint.symbol, x: baseWaypoint.x, y: baseWaypoint.y },
          departureTime: new Date(Date.now() - 600000).toISOString(),
          arrival,
        },
      },
    };

    const position = Ship.interpolateTransitPosition(ship, waypoints);
    const distanceToWaypoint = Math.hypot(position.x - baseWaypoint.x, position.y - baseWaypoint.y);
    const expectedRadius = Waypoint.getRadius(baseWaypoint) + CANVAS_CONSTANTS.ORBIT_DISTANCE_DEFAULT;
    expect(Math.abs(distanceToWaypoint - expectedRadius)).toBeLessThan(0.01);
  });

  it('clamps interpolation when progress exceeds destination orbit radius', () => {
    const ship: ShipType = {
      ...baseShip,
      nav: {
        ...baseShip.nav,
        status: 'IN_TRANSIT',
        route: {
          origin: { symbol: baseWaypoint.symbol, x: 0, y: 0 },
          destination: { symbol: baseWaypoint.symbol, x: baseWaypoint.x, y: baseWaypoint.y },
          departureTime: new Date(Date.now() - 1000).toISOString(),
          arrival: new Date(Date.now() + 1000).toISOString(),
        },
      },
    };

    const position = Ship.interpolateTransitPosition(ship, waypoints);
    const distance = Math.hypot(position.x - 0, position.y - 0);
    const maxDistance = Math.hypot(baseWaypoint.x, baseWaypoint.y);
    expect(distance).toBeLessThanOrEqual(maxDistance);
  });
});

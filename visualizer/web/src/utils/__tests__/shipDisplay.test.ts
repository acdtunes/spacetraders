import { describe, it, expect } from 'vitest';
import { calculateShipRotation, getShipLabelInfo } from '../shipDisplay';
import type { TaggedShip, Waypoint as WaypointType } from '../../types/spacetraders';

const baseWaypoint: WaypointType = {
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 0,
  y: 0,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
};

const baseShip: TaggedShip = {
  symbol: 'SHIP-1',
  agentId: 'AGENT',
  agentColor: '#fff',
  registration: {
    name: 'Test Ship',
    factionSymbol: 'COSMIC',
    role: 'COMMAND_FRIGATE',
  },
  nav: {
    systemSymbol: 'X1-TEST',
    waypointSymbol: baseWaypoint.symbol,
    status: 'IN_TRANSIT',
    flightMode: 'CRUISE',
    route: {
      origin: {
        symbol: 'ORIGIN',
        type: 'PLANET',
        systemSymbol: 'X1-TEST',
        x: 0,
        y: 0,
      },
      destination: {
        symbol: 'DEST',
        type: 'PLANET',
        systemSymbol: 'X1-TEST',
        x: 10,
        y: 0,
      },
      departureTime: new Date().toISOString(),
      arrival: new Date(Date.now() + 60000).toISOString(),
    },
  },
  cargo: { capacity: 10, units: 0, inventory: [] },
  fuel: { capacity: 100, current: 50, consumed: { amount: 0, timestamp: new Date().toISOString() } },
  frame: {} as any,
  engine: {} as any,
  reactor: {} as any,
  modules: [],
  mounts: [],
  crew: { capacity: 1, current: 1, required: 1, morale: 100, wages: 0 },
  cooldown: { shipSymbol: 'SHIP-1', remainingSeconds: 0, totalSeconds: 0 },
};

describe('shipDisplay utilities', () => {
  it('rotates ships toward destination while in transit', () => {
    const rotation = calculateShipRotation(baseShip, { x: 0, y: 0 }, new Map());
    expect(rotation).toBeCloseTo(90, 1);
  });

  it('uses route vector for rotation even when position deviates from path', () => {
    const rotation = calculateShipRotation(baseShip, { x: -5, y: 5 }, new Map());
    expect(rotation).toBeCloseTo(90, 1);
  });

  it('aligns rotation with resolved waypoint offsets when waypoints overlap', () => {
    const originWaypoint: WaypointType = {
      ...baseWaypoint,
      symbol: 'ORIGIN',
      x: 0,
      y: 0,
    };

    const destinationWaypoint: WaypointType = {
      ...baseWaypoint,
      symbol: 'DEST',
      x: 0,
      y: 0,
    };

    const waypointMap = new Map<string, WaypointType>([
      [originWaypoint.symbol, originWaypoint],
      [destinationWaypoint.symbol, destinationWaypoint],
    ]);

    const ship: TaggedShip = {
      ...baseShip,
      nav: {
        ...baseShip.nav,
        status: 'IN_TRANSIT',
        route: {
          ...baseShip.nav.route,
          origin: {
            ...baseShip.nav.route.origin,
            x: 0,
            y: 0,
          },
          destination: {
            ...baseShip.nav.route.destination,
            x: 0,
            y: 0,
          },
        },
      },
    };

    const resolveWaypointPosition = (waypoint: WaypointType) => {
      if (waypoint.symbol === 'ORIGIN') {
        return { x: -10, y: -5 };
      }
      if (waypoint.symbol === 'DEST') {
        return { x: 25, y: 45 };
      }
      return { x: waypoint.x, y: waypoint.y };
    };

    const rotation = calculateShipRotation(
      ship,
      { x: -10, y: -5 },
      waypointMap,
      undefined,
      resolveWaypointPosition
    );

    // Vector from (-10,-5) to (25,45) -> angle ~ 55°, plus 90° offset -> ~145°
    expect(rotation).toBeCloseTo(145, 1);
  });

  it('uses orbital vector when in orbit', () => {
    const ship: TaggedShip = {
      ...baseShip,
      nav: {
        ...baseShip.nav,
        status: 'IN_ORBIT',
        waypointSymbol: baseWaypoint.symbol,
      },
    };
    const rotation = calculateShipRotation(ship, { x: 0, y: 5 }, new Map([[baseWaypoint.symbol, baseWaypoint]]));
    expect(rotation).not.toBe(0);
  });

  it('formats ship label with role and symbol suffix', () => {
    const context = {
      currentScale: 1,
      projectToScreen: () => ({ x: 100, y: 200 }),
      projectToWorld: () => ({ x: 110, y: 190 }),
    };
    const labelInfo = getShipLabelInfo(baseShip, { x: 0, y: 0 }, context);
    expect(labelInfo?.labelText).toBe('Command Frigate 1');
    expect(labelInfo?.offsetX).toBeCloseTo(110);
  });

  it('returns null when projections fail', () => {
    const context = {
      currentScale: 1,
      projectToScreen: () => null,
      projectToWorld: () => null,
    };
    expect(getShipLabelInfo(baseShip, { x: 0, y: 0 }, context)).toBeNull();
  });
});

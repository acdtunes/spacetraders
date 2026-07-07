import { describe, it, expect } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useSelectionOverlay } from '../useSelectionOverlay';
import type { TaggedShip, Waypoint as WaypointType } from '../../types/spacetraders';

const waypoint: WaypointType = {
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 25,
  y: -10,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
};

const ship: TaggedShip = {
  symbol: 'SHIP-99',
  agentId: 'AGENT',
  agentColor: '#fff',
  registration: {
    name: 'Ship',
    factionSymbol: 'COSMIC',
    role: 'COMMAND',
  },
  nav: {
    systemSymbol: 'X1-TEST',
    waypointSymbol: waypoint.symbol,
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
        symbol: waypoint.symbol,
        type: waypoint.type,
        systemSymbol: waypoint.systemSymbol,
        x: waypoint.x,
        y: waypoint.y,
      },
      departureTime: new Date().toISOString(),
      arrival: new Date(Date.now() + 60000).toISOString(),
    },
  },
  cargo: { capacity: 0, units: 0, inventory: [] },
  fuel: { capacity: 0, current: 0, consumed: { amount: 0, timestamp: new Date().toISOString() } },
  frame: {} as any,
  engine: {} as any,
  reactor: {} as any,
  modules: [],
  mounts: [],
  crew: { capacity: 1, current: 1, required: 1, morale: 100, wages: 0 },
  cooldown: { shipSymbol: 'SHIP-99', remainingSeconds: 0, totalSeconds: 0 },
};

const projectToScreen = (point: { x: number; y: number }) => ({ x: point.x + 100, y: point.y + 200 });
const getWaypointPosition = () => ({ x: waypoint.x, y: waypoint.y });
const getShipPosition = () => ({ x: waypoint.x, y: waypoint.y });

const ships = [ship];
const waypoints = new Map([[waypoint.symbol, waypoint]]);

const render = (selection: {
  selectedShip?: TaggedShip | null;
  selectedWaypoint?: WaypointType | null;
  getShipPosition?: () => { x: number; y: number } | null;
}) =>
  renderHook(() =>
    useSelectionOverlay({
      selectedShip: selection.selectedShip ?? null,
      selectedWaypoint: selection.selectedWaypoint ?? null,
      ships,
      waypoints,
      projectToScreen,
      getWaypointPosition,
      getShipPosition: selection.getShipPosition ?? getShipPosition,
      frameTimestamp: 0,
    })
  );

describe('useSelectionOverlay', () => {
  it('returns an empty overlay list when nothing is selected', () => {
    const { result } = render({});
    expect(result.current).toEqual([]);
  });

  it('projects waypoint selection into screen coordinates', () => {
    const { result } = render({ selectedWaypoint: waypoint });
    expect(result.current).toEqual([{ left: 125, top: 190, size: 18, type: 'waypoint' }]);
  });

  it('projects ship selection using supplied ship position', () => {
    const { result } = render({ selectedShip: ship });
    expect(result.current).toEqual([{ left: 125, top: 190, size: 14, type: 'ship' }]);
  });

  it('omits the ship overlay when ship position cannot be resolved', () => {
    const { result } = render({ selectedShip: ship, getShipPosition: () => null });
    expect(result.current).toEqual([]);
  });
});

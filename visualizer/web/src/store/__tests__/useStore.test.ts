import { describe, it, expect, beforeEach } from 'vitest';
import { act } from '@testing-library/react';
import { createAppStore } from '../useStore';
import { TRAIL_MAX_POINTS as TRAIL_BUFFER_CAP, TRAIL_FADE_MS } from '../trails';
import type { Agent, TaggedShip, Waypoint, WaypointRef, ShipTrailPoint, FlightMode } from '../../types/spacetraders';

type AppStore = ReturnType<typeof createAppStore>;

const buildAgent = (overrides: Partial<Agent> = {}): Agent => ({
  id: 'AGENT-1',
  symbol: 'AGENT-1',
  color: '#60a5fa',
  visible: true,
  createdAt: new Date(0).toISOString(),
  credits: 0,
  ...overrides,
});

const buildWaypointRef = (symbol: string, overrides: Partial<WaypointRef> = {}): WaypointRef => ({
  symbol,
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 0,
  y: 0,
  ...overrides,
});

const buildShip = (overrides: Partial<TaggedShip> = {}): TaggedShip => ({
  symbol: 'SHIP-1',
  agentId: 'AGENT-1',
  agentColor: '#abcdef',
  registration: {
    name: 'Ship-1',
    factionSymbol: 'COSMIC',
    role: 'COMMAND',
  },
  nav: {
    systemSymbol: 'X1-TEST',
    waypointSymbol: 'X1-TEST-A1',
    status: 'IN_ORBIT',
    flightMode: 'CRUISE',
    route: {
      origin: buildWaypointRef('ORIGIN'),
      destination: buildWaypointRef('DEST', { x: 10, y: 5 }),
      departureTime: new Date().toISOString(),
      arrival: new Date(Date.now() + 60_000).toISOString(),
    },
  },
  cargo: { capacity: 10, units: 0, inventory: [] },
  fuel: { capacity: 100, current: 100, consumed: { amount: 0, timestamp: new Date().toISOString() } },
  crew: { capacity: 1, current: 1, required: 1, morale: 100, wages: 0 },
  frame: {},
  reactor: {},
  engine: {},
  modules: [],
  mounts: [],
  cooldown: { shipSymbol: 'SHIP-1', remainingSeconds: 0, totalSeconds: 0 },
  ...overrides,
});

const buildWaypoint = (overrides: Partial<Waypoint> = {}): Waypoint => ({
  symbol: 'X1-TEST-A1',
  type: 'PLANET',
  systemSymbol: 'X1-TEST',
  x: 12,
  y: 8,
  orbitals: [],
  traits: [],
  chart: null,
  isUnderConstruction: false,
  ...overrides,
});

const buildTrailPoint = (
  flightMode: FlightMode,
  overrides: Partial<ShipTrailPoint> = {}
): ShipTrailPoint => ({
  shipSymbol: 'SHIP-1',
  x: 0,
  y: 0,
  timestamp: Date.now(),
  flightMode,
  ...overrides,
});

describe('useStore', () => {
  let store: AppStore;

  beforeEach(() => {
    store = createAppStore();
  });

  it('manages agent collection', () => {
    const { setAgents, addAgent, updateAgent, removeAgent } = store.getState();
    const baseAgent = buildAgent();

    act(() => setAgents([baseAgent]));
    expect(store.getState().agents).toHaveLength(1);

    act(() => addAgent(buildAgent({ id: 'AGENT-2', symbol: 'AGENT-2' })));
    expect(store.getState().agents.map((agent) => agent.id)).toEqual(['AGENT-1', 'AGENT-2']);

    act(() => updateAgent('AGENT-2', { credits: 50 }));
    expect(store.getState().agents.find((agent) => agent.id === 'AGENT-2')?.credits).toBe(50);

    act(() => removeAgent('AGENT-1'));
    expect(store.getState().agents.map((agent) => agent.id)).toEqual(['AGENT-2']);
  });

  it('sets ships and waypoints maps', () => {
    const { setShips, setWaypoints } = store.getState();
    const ship = buildShip();
    const waypoint = buildWaypoint();

    act(() => setShips([ship]));
    expect(store.getState().ships).toEqual([ship]);

    act(() => setWaypoints([waypoint]));
    // setWaypoints enriches each waypoint with a derived hasMarketplace flag.
    expect(store.getState().waypoints.get(waypoint.symbol)).toEqual({ ...waypoint, hasMarketplace: false });
  });

  it('toggles visualization flags and filters', () => {
    const {
      showDestinationRoutes,
      toggleDestinationRoutes,
      showMapOverlays,
      toggleMapOverlays,
      showShipNames,
      toggleShipNames,
      showWaypointNames,
      toggleWaypointNames,
      filterStatus,
      toggleStatusFilter,
      filterWaypointTypes,
      toggleWaypointTypeFilter,
    } = store.getState();

    expect(showDestinationRoutes).toBe(true);
    act(() => toggleDestinationRoutes());
    expect(store.getState().showDestinationRoutes).toBe(false);

    expect(showMapOverlays).toBe(false);
    act(() => toggleMapOverlays());
    expect(store.getState().showMapOverlays).toBe(true);

    expect(showShipNames).toBe(true);
    act(() => toggleShipNames());
    expect(store.getState().showShipNames).toBe(false);

    expect(showWaypointNames).toBe(true);
    act(() => toggleWaypointNames());
    expect(store.getState().showWaypointNames).toBe(false);

    expect(filterStatus.has('IN_TRANSIT')).toBe(true);
    act(() => toggleStatusFilter('IN_TRANSIT'));
    expect(store.getState().filterStatus.has('IN_TRANSIT')).toBe(false);

    expect(filterWaypointTypes.has('PLANET')).toBe(true);
    act(() => toggleWaypointTypeFilter('PLANET'));
    expect(store.getState().filterWaypointTypes.has('PLANET')).toBe(false);
  });

  it('handles selection state for ships and waypoints', () => {
    const { setSelectedShip, setSelectedWaypoint } = store.getState();
    const ship = buildShip();
    const waypoint = buildWaypoint();

    act(() => setSelectedShip(ship));
    expect(store.getState().selectedShip?.symbol).toBe(ship.symbol);

    act(() => setSelectedWaypoint(waypoint));
    expect(store.getState().selectedWaypoint?.symbol).toBe(waypoint.symbol);

    act(() => setSelectedShip(null));
    expect(store.getState().selectedShip).toBeNull();
  });

  it('maintains ship trail history respecting flight mode constraints', () => {
    const { addTrailPosition, clearTrail } = store.getState();

    const cruisePoint = buildTrailPoint('CRUISE', { x: 10, y: 10 });
    act(() => addTrailPosition('SHIP-1', cruisePoint));
    expect(store.getState().trails.get('SHIP-1')).toEqual([cruisePoint]);

    const nearPoint = buildTrailPoint('CRUISE', { x: 11, y: 10.5 });
    act(() => addTrailPosition('SHIP-1', nearPoint));
    expect(store.getState().trails.get('SHIP-1')).toEqual([cruisePoint]);

    const farPoint = buildTrailPoint('CRUISE', { x: 40, y: 40 });
    act(() => addTrailPosition('SHIP-1', farPoint));
    expect(store.getState().trails.get('SHIP-1')).toEqual([cruisePoint, farPoint]);

    const driftPoint = buildTrailPoint('DRIFT', { x: 50, y: 50 });
    act(() => addTrailPosition('SHIP-1', driftPoint));
    expect(store.getState().trails.get('SHIP-1')).toBeUndefined();

    act(() => clearTrail('SHIP-1'));
    expect(store.getState().trails.get('SHIP-1')).toBeUndefined();
  });

  it('caps the shipped trail buffer and drops points beyond the fade window', () => {
    const { addTrailPosition } = store.getState();
    const t0 = Date.now();

    // Push well past the cap; each step clears the min-distance gate (5 world units).
    act(() => {
      for (let i = 0; i < TRAIL_BUFFER_CAP + 40; i++) {
        addTrailPosition('SHIP-1', buildTrailPoint('CRUISE', { x: i * 5, y: 0, timestamp: t0 + i }));
      }
    });
    const capped = store.getState().trails.get('SHIP-1');
    expect(capped).toHaveLength(TRAIL_BUFFER_CAP);
    expect(capped?.[0].x).toBe(40 * 5); // oldest 40 points fell off the cap

    // A point far beyond the fade window drops every stale point before it.
    act(() =>
      addTrailPosition(
        'SHIP-1',
        buildTrailPoint('CRUISE', { x: 99_999, y: 0, timestamp: t0 + TRAIL_FADE_MS + 10_000 })
      )
    );
    const faded = store.getState().trails.get('SHIP-1');
    expect(faded).toHaveLength(1);
    expect(faded?.[0].x).toBe(99_999);
  });

  it('queues ship focus requests with timestamp metadata', () => {
    const { requestShipFocus, clearShipFocusRequest } = store.getState();

    act(() => requestShipFocus('SHIP-1', 4));
    const request = store.getState().shipFocusRequest;

    expect(request?.symbol).toBe('SHIP-1');
    expect(request?.zoom).toBe(4);
    expect(typeof request?.timestamp).toBe('number');

    act(() => clearShipFocusRequest());
    expect(store.getState().shipFocusRequest).toBeNull();
  });
});

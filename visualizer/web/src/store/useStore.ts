import { create } from 'zustand';
import { createStore } from 'zustand/vanilla';
import type { StateCreator } from 'zustand';
import type {
  Agent,
  TaggedShip,
  Waypoint,
  ShipTrailPoint,
  Market,
  System,
  FlightMode,
  ShipAssignment,
  MarketFreshness,
  ScoutTour,
  TradeOpportunityData,
} from '../types/spacetraders';

const TRAIL_MAX_POINTS: Record<FlightMode, number> = {
  DRIFT: 0,
  CRUISE: 18,
  BURN: 32,
  STEALTH: 0,
};

const TRAIL_MIN_DISTANCE = 2;

const initializeWaypointMap = (waypoints: Waypoint[]) => new Map(waypoints.map((waypoint) => [waypoint.symbol, waypoint]));

const addTrailPoint = (existing: ShipTrailPoint[] | undefined, point: ShipTrailPoint, maxPoints: number) => {
  if (!existing) {
    return [point];
  }

  const lastPoint = existing[existing.length - 1];
  if (
    lastPoint &&
    Math.hypot(point.x - lastPoint.x, point.y - lastPoint.y) < TRAIL_MIN_DISTANCE
  ) {
    return existing;
  }

  const sliceCount = Math.max(0, maxPoints - 1);
  const pointsToKeep = existing.length ? existing.slice(-sliceCount) : [];
  return [...pointsToKeep, point].slice(-maxPoints);
};

export interface AppState {
  // Agents
  agents: Agent[];
  setAgents: (agents: Agent[]) => void;
  addAgent: (agent: Agent) => void;
  updateAgent: (id: string, updates: Partial<Agent>) => void;
  removeAgent: (id: string) => void;

  // Ships
  ships: TaggedShip[];
  setShips: (ships: TaggedShip[]) => void;

  // Waypoints
  waypoints: Map<string, Waypoint>;
  setWaypoints: (waypoints: Waypoint[]) => void;

  // Current system
  currentSystem: string | null;
  setCurrentSystem: (systemSymbol: string | null) => void;

  // Ship trails
  trails: Map<string, ShipTrailPoint[]>;
  addTrailPosition: (shipSymbol: string, point: ShipTrailPoint) => void;
  clearTrail: (shipSymbol: string) => void;

  // Visualization toggles
  showDestinationRoutes: boolean;
  toggleDestinationRoutes: () => void;
  showShipNames: boolean;
  toggleShipNames: () => void;
  showWaypointNames: boolean;
  toggleWaypointNames: () => void;

  // Markets / overlays
  markets: Map<string, Market>;
  setMarkets: (markets: Map<string, Market>) => void;
  updateMarket: (waypointSymbol: string, market: Market) => void;
  showMapOverlays: boolean;
  toggleMapOverlays: () => void;

  // Galaxy
  systems: System[];
  setSystems: (systems: System[]) => void;
  viewMode: 'system' | 'galaxy';
  setViewMode: (mode: 'system' | 'galaxy') => void;

  // UI state
  filterStatus: Set<string>;
  toggleStatusFilter: (status: string) => void;

  filterAgents: Set<string>;
  toggleAgentFilter: (agentId: string) => void;

  filterWaypointTypes: Set<string>;
  toggleWaypointTypeFilter: (type: string) => void;
  selectAllWaypointTypes: (types: string[]) => void;
  clearAllWaypointTypes: () => void;

  shipNameFilter: string;
  setShipNameFilter: (value: string) => void;

  // Connection status
  isPolling: boolean;
  setPolling: (polling: boolean) => void;
  lastUpdate: number | null;
  setLastUpdate: (timestamp: number) => void;

  // Selection
  selectedShip: TaggedShip | null;
  setSelectedShip: (ship: TaggedShip | null) => void;
  selectedWaypoint: Waypoint | null;
  setSelectedWaypoint: (waypoint: Waypoint | null) => void;

  // Bot operations
  assignments: Map<string, ShipAssignment>;
  setAssignments: (assignments: ShipAssignment[]) => void;
  marketFreshness: Map<string, MarketFreshness>;
  setMarketFreshness: (freshness: MarketFreshness[]) => void;
  scoutTours: ScoutTour[];
  setScoutTours: (tours: ScoutTour[]) => void;
  tradeOpportunities: TradeOpportunityData[];
  setTradeOpportunities: (opportunities: TradeOpportunityData[]) => void;

  // Bot visualization toggles
  showScoutTours: boolean;
  toggleScoutTours: () => void;
  showTradeRoutes: boolean;
  toggleTradeRoutes: () => void;
  showMiningRoutes: boolean;
  toggleMiningRoutes: () => void;
  showOperationBadges: boolean;
  toggleOperationBadges: () => void;
  showMarketFreshness: boolean;
  toggleMarketFreshness: () => void;

  // Tour filtering
  visibleTours: Set<string>;
  toggleTourVisibility: (tourSystem: string) => void;
  showAllTours: () => void;
  hideAllTours: () => void;
}

const storeInitializer: StateCreator<AppState, [], []> = (set) => ({
  // Agents
  agents: [],
  setAgents: (agents) => set({ agents }),
  addAgent: (agent) => set((state) => ({ agents: [...state.agents, agent] })),
  updateAgent: (id, updates) =>
    set((state) => ({
      agents: state.agents.map((a) => (a.id === id ? { ...a, ...updates } : a)),
    })),
  removeAgent: (id) => set((state) => ({ agents: state.agents.filter((a) => a.id !== id) })),

  // Ships
  ships: [],
  setShips: (ships) => set({ ships }),

  // Waypoints
  waypoints: new Map(),
  setWaypoints: (waypoints) =>
    set({
      waypoints: initializeWaypointMap(waypoints),
    }),

  // Current system
  currentSystem: null,
  setCurrentSystem: (systemSymbol) => set({ currentSystem: systemSymbol }),

  // Ship trails
  trails: new Map(),
  addTrailPosition: (shipSymbol, point) =>
    set((state) => {
      const maxPoints = TRAIL_MAX_POINTS[point.flightMode] ?? 0;
      const existing = state.trails.get(shipSymbol);

      if (maxPoints <= 0) {
        if (!existing || existing.length === 0) {
          return state;
        }
        const cleared = new Map(state.trails);
        cleared.delete(shipSymbol);
        return { trails: cleared };
      }

      const newTrails = new Map(state.trails);
      const updatedTrail = addTrailPoint(existing, point, maxPoints);
      newTrails.set(shipSymbol, updatedTrail);
      return { trails: newTrails };
    }),
  clearTrail: (shipSymbol) =>
    set((state) => {
      if (!state.trails.has(shipSymbol)) {
        return state;
      }
      const newTrails = new Map(state.trails);
      newTrails.delete(shipSymbol);
      return { trails: newTrails };
    }),

  // Visualization toggles
  showDestinationRoutes: true,
  toggleDestinationRoutes: () =>
    set((state) => ({ showDestinationRoutes: !state.showDestinationRoutes })),
  showShipNames: true,
  toggleShipNames: () => set((state) => ({ showShipNames: !state.showShipNames })),
  showWaypointNames: true,
  toggleWaypointNames: () => set((state) => ({ showWaypointNames: !state.showWaypointNames })),

  // Markets
  markets: new Map(),
  setMarkets: (markets) => set({ markets }),
  updateMarket: (waypointSymbol, market) =>
    set((state) => {
      const newMarkets = new Map(state.markets);
      newMarkets.set(waypointSymbol, market);
      return { markets: newMarkets };
    }),
  showMapOverlays: false,
  toggleMapOverlays: () => set((state) => ({ showMapOverlays: !state.showMapOverlays })),

  // Galaxy
  systems: [],
  setSystems: (systems) => set({ systems }),
  viewMode: 'system',
  setViewMode: (mode) => set({ viewMode: mode }),

  filterStatus: new Set(['IN_TRANSIT', 'DOCKED', 'IN_ORBIT']),
  toggleStatusFilter: (status) =>
    set((state) => {
      const newFilter = new Set(state.filterStatus);
      if (newFilter.has(status)) {
        newFilter.delete(status);
      } else {
        newFilter.add(status);
      }
      return { filterStatus: newFilter };
    }),

  filterAgents: new Set(),
  toggleAgentFilter: (agentId) =>
    set((state) => {
      const newFilter = new Set(state.filterAgents);
      if (newFilter.has(agentId)) {
        newFilter.delete(agentId);
      } else {
        newFilter.add(agentId);
      }
      return { filterAgents: newFilter };
    }),

  filterWaypointTypes: new Set(['PLANET', 'GAS_GIANT', 'MOON', 'ORBITAL_STATION', 'JUMP_GATE', 'ASTEROID_FIELD', 'ASTEROID', 'ENGINEERED_ASTEROID', 'ASTEROID_BASE', 'NEBULA', 'DEBRIS_FIELD', 'GRAVITY_WELL', 'ARTIFICIAL_GRAVITY_WELL', 'FUEL_STATION']),
  toggleWaypointTypeFilter: (type) =>
    set((state) => {
      const newFilter = new Set(state.filterWaypointTypes);
      if (newFilter.has(type)) {
        newFilter.delete(type);
      } else {
        newFilter.add(type);
      }
      return { filterWaypointTypes: newFilter };
    }),
  selectAllWaypointTypes: (types) => set({ filterWaypointTypes: new Set(types) }),
  clearAllWaypointTypes: () => set({ filterWaypointTypes: new Set() }),

  shipNameFilter: '',
  setShipNameFilter: (value) => set({ shipNameFilter: value }),

  // Connection status
  isPolling: false,
  setPolling: (polling) => set({ isPolling: polling }),
  lastUpdate: null,
  setLastUpdate: (timestamp) => set({ lastUpdate: timestamp }),

  // Selection
  selectedShip: null,
  setSelectedShip: (ship) => set({ selectedShip: ship }),
  selectedWaypoint: null,
  setSelectedWaypoint: (waypoint) => set({ selectedWaypoint: waypoint }),

  // Bot operations
  assignments: new Map(),
  setAssignments: (assignments) =>
    set({
      assignments: new Map(assignments.map((a) => [a.ship_symbol, a])),
    }),
  marketFreshness: new Map(),
  setMarketFreshness: (freshness) =>
    set({
      marketFreshness: new Map(freshness.map((f) => [f.waypoint_symbol, f])),
    }),
  scoutTours: [],
  setScoutTours: (tours) =>
    set((state) => {
      // Auto-add new tours to visible set
      const newVisible = new Set(state.visibleTours);
      tours.forEach((tour) => {
        if (!newVisible.has(tour.system)) {
          newVisible.add(tour.system);
        }
      });
      return { scoutTours: tours, visibleTours: newVisible };
    }),
  tradeOpportunities: [],
  setTradeOpportunities: (opportunities) => set({ tradeOpportunities: opportunities }),

  // Bot visualization toggles
  showScoutTours: true,
  toggleScoutTours: () => set((state) => ({ showScoutTours: !state.showScoutTours })),
  showTradeRoutes: false,
  toggleTradeRoutes: () => set((state) => ({ showTradeRoutes: !state.showTradeRoutes })),
  showMiningRoutes: false,
  toggleMiningRoutes: () => set((state) => ({ showMiningRoutes: !state.showMiningRoutes })),
  showOperationBadges: true,
  toggleOperationBadges: () => set((state) => ({ showOperationBadges: !state.showOperationBadges })),
  showMarketFreshness: true,
  toggleMarketFreshness: () => set((state) => ({ showMarketFreshness: !state.showMarketFreshness })),

  // Tour filtering
  visibleTours: new Set(),
  toggleTourVisibility: (tourSystem) =>
    set((state) => {
      const newVisible = new Set(state.visibleTours);
      if (newVisible.has(tourSystem)) {
        newVisible.delete(tourSystem);
      } else {
        newVisible.add(tourSystem);
      }
      return { visibleTours: newVisible };
    }),
  showAllTours: () =>
    set((state) => {
      const allSystems = new Set(state.scoutTours.map((t) => t.system));
      return { visibleTours: allSystems };
    }),
  hideAllTours: () => set({ visibleTours: new Set() }),
});

export const createAppStore = () => createStore<AppState>(storeInitializer);

export const useStore = create<AppState>()(storeInitializer);

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
  MarketData,
  ScoutTour,
  TradeOpportunityData,
} from '../types/spacetraders';
import { getTourId } from '../utils/tourHelpers';

const TRAIL_MAX_POINTS: Record<FlightMode, number> = {
  DRIFT: 0,
  CRUISE: 9,
  BURN: 16,
  STEALTH: 0,
};

const TRAIL_MIN_DISTANCE = 2;

const initializeWaypointMap = (waypoints: Waypoint[]) => new Map(waypoints.map((waypoint) => [
  waypoint.symbol,
  {
    ...waypoint,
    hasMarketplace: waypoint.traits.some((trait) => trait.symbol === 'MARKETPLACE'),
  }
]));

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
  marketIntel: Map<string, MarketData>;
  setMarketIntel: (marketData: MarketData[]) => void;
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

  filterShipRoles: Set<string>;
  toggleShipRoleFilter: (role: string) => void;
  clearShipRoleFilters: () => void;

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
  toggleTourVisibility: (tourId: string) => void;
  showAllTours: () => void;
  hideAllTours: () => void;

  // Camera focus
  shipFocusRequest: {
    symbol: string;
    zoom?: number;
    timestamp: number;
  } | null;
  requestShipFocus: (symbol: string, zoom?: number) => void;
  clearShipFocusRequest: () => void;

  // Player filtering
  selectedPlayerId: number | null;
  setSelectedPlayerId: (playerId: number | null) => void;
  availablePlayers: number[];
  setAvailablePlayers: (playerIds: number[]) => void;

  // Agent to player_id mapping
  playerMappings: Map<string, number>;
  setPlayerMappings: (mappings: Map<string, number>) => void;
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
  marketIntel: new Map(),
  setMarketIntel: (marketData) =>
    set(() => ({
      marketIntel: new Map(marketData.map((entry) => [entry.waypointSymbol, entry])),
    })),
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

  filterShipRoles: new Set(),
  toggleShipRoleFilter: (role) =>
    set((state) => {
      const normalized = role.toUpperCase();
      const newFilter = new Set(state.filterShipRoles);
      if (newFilter.has(normalized)) {
        if (newFilter.size === 1) {
          return { filterShipRoles: new Set() };
        }
        newFilter.delete(normalized);
      } else {
        newFilter.add(normalized);
      }
      return { filterShipRoles: newFilter };
    }),
  clearShipRoleFilters: () => set({ filterShipRoles: new Set() }),

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
      // Check if this is the initial load (no existing tours)
      if (state.scoutTours.length === 0 && tours.length > 0) {
        // Initial load: auto-select all tours
        const allTourIds = new Set(tours.map((tour) => getTourId(tour)));
        return { scoutTours: tours, visibleTours: allTourIds };
      }

      // For subsequent updates: add new tours to visible set while preserving user selections
      const currentTourIds = new Set(state.scoutTours.map((t) => getTourId(t)));
      const newTourIds = tours
        .map((t) => getTourId(t))
        .filter((id) => !currentTourIds.has(id));

      if (newTourIds.length > 0) {
        // New tours detected - add them to visible set
        const updatedVisibleTours = new Set(state.visibleTours);
        newTourIds.forEach((id) => updatedVisibleTours.add(id));
        return { scoutTours: tours, visibleTours: updatedVisibleTours };
      }

      // No new tours - just update the tour list
      return { scoutTours: tours };
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
  toggleTourVisibility: (tourId) =>
    set((state) => {
      const newVisible = new Set(state.visibleTours);
      if (newVisible.has(tourId)) {
        newVisible.delete(tourId);
      } else {
        newVisible.add(tourId);
      }
      return { visibleTours: newVisible };
    }),
  showAllTours: () =>
    set((state) => {
      const allTourIds = new Set(state.scoutTours.map((t) => getTourId(t)));
      return { visibleTours: allTourIds };
    }),
  hideAllTours: () => set({ visibleTours: new Set() }),

  // Camera focus
  shipFocusRequest: null,
  requestShipFocus: (symbol, zoom) =>
    set({
      shipFocusRequest: {
        symbol,
        zoom,
        timestamp: Date.now(),
      },
    }),
  clearShipFocusRequest: () => set({ shipFocusRequest: null }),

  // Player filtering
  selectedPlayerId: null,
  setSelectedPlayerId: (playerId) => set({ selectedPlayerId: playerId }),
  availablePlayers: [],
  setAvailablePlayers: (playerIds) => set({ availablePlayers: playerIds }),

  // Agent to player_id mapping
  playerMappings: new Map(),
  setPlayerMappings: (mappings) => set({ playerMappings: mappings }),
});

export const createAppStore = () => createStore<AppState>(storeInitializer);

export const useStore = create<AppState>()(storeInitializer);

// Agent types
export interface Agent {
  id: string;
  symbol: string;
  color: string;
  visible: boolean;
  createdAt: string;
  credits?: number;
}

// Ship types
export type ShipNavStatus = 'IN_TRANSIT' | 'IN_ORBIT' | 'DOCKED';
export type FlightMode = 'DRIFT' | 'CRUISE' | 'BURN' | 'STEALTH';

export interface WaypointRef {
  symbol: string;
  type: string;
  systemSymbol: string;
  x: number;
  y: number;
}

export interface ShipNavRoute {
  origin: WaypointRef;  // API uses "origin", not "departure"
  destination: WaypointRef;
  departureTime: string;
  arrival: string;
}

export interface ShipNav {
  systemSymbol: string;
  waypointSymbol: string;
  route: ShipNavRoute;
  status: ShipNavStatus;
  flightMode: FlightMode;
}

export interface CargoItem {
  symbol: string;
  name: string;
  description: string;
  units: number;
}

export interface Cargo {
  capacity: number;
  units: number;
  inventory: CargoItem[];
}

export interface Fuel {
  current: number;
  capacity: number;
  consumed?: {
    amount: number;
    timestamp: string;
  };
}

export interface ShipRegistration {
  name: string;
  factionSymbol: string;
  role: string;
}

export interface Cooldown {
  shipSymbol: string;
  totalSeconds: number;
  remainingSeconds: number;
  expiration?: string;
}

export interface Ship {
  symbol: string;
  registration: ShipRegistration;
  nav: ShipNav;
  crew: any; // Not needed for visualization
  frame: any; // Not needed for visualization
  reactor: any; // Not needed for visualization
  engine: any; // Not needed for visualization
  cooldown?: Cooldown;
  modules: any[]; // Not needed for visualization
  mounts: any[]; // Not needed for visualization
  cargo: Cargo;
  fuel: Fuel;
}

// Tagged ship with agent information
export interface TaggedShip extends Ship {
  agentId: string;
  agentColor: string;
}

// Waypoint types
export type WaypointType =
  | 'PLANET'
  | 'GAS_GIANT'
  | 'MOON'
  | 'ORBITAL_STATION'
  | 'JUMP_GATE'
  | 'ASTEROID_FIELD'
  | 'ASTEROID'
  | 'ENGINEERED_ASTEROID'
  | 'ASTEROID_BASE'
  | 'NEBULA'
  | 'DEBRIS_FIELD'
  | 'GRAVITY_WELL'
  | 'ARTIFICIAL_GRAVITY_WELL'
  | 'FUEL_STATION';

export interface WaypointTrait {
  symbol: string;
  name: string;
  description: string;
}

export interface WaypointOrbital {
  symbol: string;
}

export interface WaypointFaction {
  symbol: string;
}

export interface Waypoint {
  symbol: string;
  type: WaypointType;
  systemSymbol: string;
  x: number;
  y: number;
  orbitals: WaypointOrbital[];
  orbits?: string;
  faction?: WaypointFaction;
  traits: WaypointTrait[];
  modifiers?: any[];
  chart?: any;
  isUnderConstruction: boolean;
  hasMarketplace?: boolean; // Derived from traits
}

// System types
export interface System {
  symbol: string;
  sectorSymbol: string;
  type: string;
  x: number;
  y: number;
  waypoints: WaypointRef[];
  factions: any[];
}

// Market types
export type MarketSupply = 'SCARCE' | 'LIMITED' | 'MODERATE' | 'HIGH' | 'ABUNDANT';

export interface MarketTradeGood {
  symbol: string;
  name?: string;
  tradeVolume: number;
  supply: MarketSupply;
  purchasePrice: number;
  sellPrice: number;
}

export interface Market {
  symbol: string; // waypoint symbol
  exports: MarketTradeGood[];
  imports: MarketTradeGood[];
  exchange: MarketTradeGood[];
  transactions?: any[]; // optional
  tradeGoods?: MarketTradeGood[]; // optional full list
}

export interface TradeOpportunity {
  good: string;
  profitPerUnit: number;
  buyLocation: string;
  sellLocation: string;
}

// Position tracking
export interface ShipTrailPoint {
  shipSymbol: string;
  x: number;
  y: number;
  timestamp: number;
  flightMode: FlightMode;
}

// API response types
export interface ApiResponse<T> {
  data: T;
  meta?: {
    total?: number;
    page?: number;
    limit?: number;
  };
}

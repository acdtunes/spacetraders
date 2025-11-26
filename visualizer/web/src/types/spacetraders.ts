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

// Bot operation types
export type OperationType = 'scout-markets' | 'trade' | 'mine' | 'transport' | 'contract' | 'factory' | 'shipyard' | 'arbitrage' | 'manufacturing' | 'manual' | 'idle';
export type AssignmentStatus = 'active' | 'idle' | 'stale';

export interface ShipAssignment {
  ship_symbol: string;
  player_id: number;
  assigned_to: string | null;
  daemon_id: string | null;
  operation: OperationType | null;
  status: AssignmentStatus;
  assigned_at: string | null;
  released_at: string | null;
  metadata: {
    system?: string;
    markets?: string[];
    asteroid?: string;
    market?: string;
    cycles?: number;
    [key: string]: any;
  } | null;
}

export interface Daemon {
  daemon_id: string;
  player_id: number;
  pid: number | null;
  command: string[];
  started_at: string;
  stopped_at: string | null;
  status: 'running' | 'stopping' | 'stopped' | 'crashed';
  log_file: string | null;
  err_file: string | null;
}

export interface MarketGood {
  symbol: string;
  supply: MarketSupply;
  activity: string | null;
  purchasePrice: number;
  sellPrice: number;
  tradeVolume: number;
}

export interface MarketData {
  waypointSymbol: string;
  lastUpdated: string;
  goods: MarketGood[];
}

export interface MarketFreshness {
  waypoint_symbol: string;
  last_updated: string;
}

export interface ScoutTour {
  system: string;
  markets: string[];
  algorithm: string;
  start_waypoint: string | null;
  tour_order: string[];
  total_distance: number;
  calculated_at: string;
  ship_symbol: string;
  daemon_id: string;
  player_id: number;
}

export interface TradeOpportunityData {
  buy_waypoint: string;
  sell_waypoint: string;
  good_symbol: string;
  buy_price: number;
  sell_price: number;
  profit_per_unit: number;
  supply: MarketSupply;
  activity: string;
  buy_updated: string;
  sell_updated: string;
}

export interface MarketTransaction {
  ship_symbol: string;
  waypoint_symbol: string;
  good_symbol: string;
  transaction_type: 'BUY' | 'SELL';
  units: number;
  price_per_unit: number;
  total_cost: number;
  timestamp: string;
}

export interface SystemGraph {
  system_symbol: string;
  graph_data: {
    nodes: Record<string, { x: number; y: number }>;
    edges: Record<string, Record<string, number>>;
  };
  created_at: string;
  updated_at: string;
}

export interface OperationSummary {
  operation: OperationType | null;
  count: number;
  status: AssignmentStatus;
}

export interface PlayerMapping {
  player_id: number;
  agent_symbol: string;
}

// Financial Ledger types
export type TransactionType =
  | 'REFUEL'
  | 'PURCHASE_CARGO'
  | 'SELL_CARGO'
  | 'PURCHASE_SHIP'
  | 'CONTRACT_ACCEPTED'
  | 'CONTRACT_FULFILLED';

export type TransactionCategory =
  | 'FUEL_COSTS'
  | 'TRADING_REVENUE'
  | 'TRADING_COSTS'
  | 'SHIP_INVESTMENTS'
  | 'CONTRACT_REVENUE';

export interface FinancialTransaction {
  id: string;
  player_id: number;
  timestamp: string;
  transaction_type: TransactionType;
  category: TransactionCategory;
  operation_type?: string | null;
  amount: number;
  balance_before: number;
  balance_after: number;
  description: string;
  metadata: {
    ship_symbol?: string;
    good_symbol?: string;
    waypoint?: string;
    units?: number;
    price_per_unit?: number;
    [key: string]: any;
  } | null;
  related_entity_type: string | null;
  related_entity_id: string | null;
}

export interface CategoryCashFlow {
  category: TransactionCategory;
  total_inflow: number;
  total_outflow: number;
  net_flow: number;
  transaction_count: number;
}

export interface CashFlowData {
  period: {
    start: string;
    end: string;
  };
  summary: {
    total_inflow: number;
    total_outflow: number;
    net_cash_flow: number;
  };
  categories: CategoryCashFlow[];
}

export interface ProfitLossData {
  period: {
    start: string;
    end: string;
  };
  revenue: {
    total: number;
    breakdown: Record<string, number>;
  };
  expenses: {
    total: number;
    breakdown: Record<string, number>;
  };
  net_profit: number;
  profit_margin: number;
}

export interface BalanceDataPoint {
  timestamp: string;
  balance: number;
  transaction_id: string;
  transaction_type: TransactionType;
  amount: number;
}

export interface BalanceHistoryData {
  dataPoints: BalanceDataPoint[];
  current_balance: number;
  starting_balance: number;
  net_change: number;
}

export interface OperationPLBreakdown {
  operation: string;
  revenue: number;
  expenses: number;
  net_profit: number;
  transaction_count: number;
  breakdown: Record<string, number>;
}

export interface OperationPLData {
  period: {
    start: string;
    end: string;
  };
  summary: {
    total_revenue: number;
    total_expenses: number;
    net_profit: number;
  };
  operations: OperationPLBreakdown[];
}

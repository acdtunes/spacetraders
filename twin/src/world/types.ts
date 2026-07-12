/** RFC3339 timestamp string, e.g. "2026-07-11T18:04:05.123Z" (Date#toISOString). */
export type Rfc3339 = string;

export interface Meta { total: number; page: number; limit: number }
export interface Envelope<T> { data: T }
export interface PagedEnvelope<T> { data: T[]; meta: Meta }

export interface Agent {
  accountId: string;
  symbol: string;
  headquarters: string;      // waypoint symbol, e.g. "X1-PZ28-A1"
  credits: number;
  startingFaction: string;   // e.g. "COSMIC"
}

export interface ShipRequirements { power: number; crew: number; slots: number }

export interface ShipNavRoute {
  departureTime: Rfc3339;
  arrival: Rfc3339;
}

export type NavStatus = 'DOCKED' | 'IN_ORBIT' | 'IN_TRANSIT';
export type FlightMode = 'CRUISE' | 'DRIFT' | 'BURN' | 'STEALTH';

export interface ShipNav {
  systemSymbol: string;
  waypointSymbol: string;    // current location; flips to destination AT arrival
  status: NavStatus;         // COMPUTED ON READ while a transit is stored
  flightMode: FlightMode;
  route: ShipNavRoute | null;
}

export interface CargoItem { symbol: string; name: string; description: string; units: number }

export interface Ship {
  symbol: string;
  registration: { role: string };          // "COMMAND" | "SATELLITE" | ...
  nav: ShipNav;
  fuel: { current: number; capacity: number };
  cargo: { capacity: number; units: number; inventory: CargoItem[] };
  cooldown: { expiration: Rfc3339 } | null;
  engine: { speed: number };
  frame: { symbol: string; moduleSlots: number; mountingPoints: number };
  reactor: { symbol: string; name: string; powerOutput: number; requirements: ShipRequirements };
  crew: { current: number; required: number; capacity: number };
  modules: Array<{ symbol: string; capacity: number; range: number; requirements: ShipRequirements }>;
  mounts:  Array<{ symbol: string; name: string; strength: number; deposits: string[]; requirements: ShipRequirements }>;
}

export interface WaypointTrait { symbol: string; name: string; description: string }

export interface Waypoint {
  symbol: string;
  type: string;
  systemSymbol: string;
  x: number;
  y: number;
  traits: WaypointTrait[];
  orbitals: Array<{ symbol: string }>;
  isUnderConstruction: boolean;
}

export interface System {
  symbol: string;
  waypoints: Map<string, Waypoint>;
}

export type SupplyLevel = 'SCARCE' | 'LIMITED' | 'MODERATE' | 'HIGH' | 'ABUNDANT';
export type ActivityLevel = 'WEAK' | 'GROWING' | 'STRONG' | 'RESTRICTED';

export interface TradeGood {
  symbol: string;
  supply: SupplyLevel | string;
  activity: ActivityLevel | string;
  sellPrice: number;
  purchasePrice: number;
  tradeVolume: number;
}

export interface Market {
  symbol: string;
  // CRITICAL: the client derives EXPORT/IMPORT/EXCHANGE by WHICH ARRAY a good's symbol
  // appears in (client.go:1090). Each tradeGoods entry appears in exactly one array.
  exports:  Array<{ symbol: string }>;
  imports:  Array<{ symbol: string }>;
  exchange: Array<{ symbol: string }>;
  tradeGoods: TradeGood[];
}

export interface ShipyardListing {
  type: string;
  name: string;
  description: string;
  purchasePrice: number;
  frame: Record<string, unknown>;
  reactor: Record<string, unknown>;
  engine: Record<string, unknown>;   // MUST contain numeric "speed"
  modules: Array<Record<string, unknown>>;
  mounts: Array<Record<string, unknown>>;
}

export interface Shipyard {
  symbol: string;
  shipTypes: Array<{ type: string }>;
  ships: ShipyardListing[];
  transactions: Array<Record<string, unknown>>;
  modificationsFee: number;
}

export interface TransitState {
  shipSymbol: string;
  originWaypoint: string;
  destinationWaypoint: string;
  departureTime: Rfc3339;
  arrival: Rfc3339;
}

export interface World {
  serverStatus: { resetDate: string; serverResets: { next: Rfc3339; frequency: string } };
  agent: Agent | null;
  agentToken: string | null;
  ships: Map<string, Ship>;
  systems: Map<string, System>;
  markets: Map<string, Market>;
  shipyards: Map<string, Shipyard>;
  transits: Map<string, TransitState>;
  shipCounter: number;
}

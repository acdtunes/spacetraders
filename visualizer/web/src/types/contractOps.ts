// Shapes served by server/routes/contract-ops.ts (sp-c6pm).

export interface DepotElement {
  waypoint: string;
  shipSymbol: string; // '' = declared-but-uncrewed slot
}

export interface Depot {
  id: string;
  warehouses: DepotElement[];
  stockers: DepotElement[];
  deliveryHulls: DepotElement[];
  sourceHubs: DepotElement[];
}

export interface OpsWaypoint {
  symbol: string;
  system: string;
  type: string;
  x: number;
  y: number;
}

export interface ContractOpsTopology {
  playerId: number | null;
  depots: Depot[];
  systems: string[];
  waypoints: OpsWaypoint[];
}

export type ContractPhase = 'IDLE' | 'NEGOTIATE' | 'ACCEPT' | 'SOURCE' | 'DELIVER' | 'FULFILL';

export interface DeliveryProgress {
  tradeSymbol: string;
  destinationSymbol: string;
  unitsRequired: number;
  unitsFulfilled: number;
}

export interface OpsContract {
  id: string;
  accepted: boolean;
  fulfilled: boolean;
  deadline: string;
  paymentOnAccepted: number;
  paymentOnFulfilled: number;
  deliveries: DeliveryProgress[];
  lastUpdated: string;
}

export type OpsShipRole = 'worker' | 'stocker' | 'warehouse' | 'delivery' | 'contract';

export interface OpsShip {
  symbol: string;
  role: OpsShipRole;
  navStatus: 'DOCKED' | 'IN_ORBIT' | 'IN_TRANSIT';
  waypoint: string;
  system: string;
  x: number;
  y: number;
  arrivalTime: string | null;
  cargoUnits: number;
  cargoCapacity: number;
  cargo: Array<{ symbol: string; units: number }>;
  containerId: string | null;
}

export interface WarehouseLevel {
  waypoint: string;
  good: string;
  units: number;
}

export interface OpsEvent {
  at: string;
  kind: 'stocking' | 'withdrawal' | 'transaction';
  good?: string;
  units?: number;
  waypoint?: string;
  shipSymbol?: string;
  contractId?: string;
  amount?: number;
  description?: string;
}

export interface ContractOpsLive {
  playerId: number | null;
  generatedAt: string;
  phase: ContractPhase;
  contract: OpsContract | null;
  lastFulfilled: { id: string; at: string; payment: number } | null;
  cycle: { fulfilledLastHour: number; avgCycleMinutes: number | null };
  coordinator: { id: string; heartbeatAt: string } | null;
  worker: { id: string; shipSymbol: string | null; heartbeatAt: string } | null;
  ships: OpsShip[];
  warehouses: WarehouseLevel[];
  destinations: Array<{ symbol: string; system: string; x: number; y: number }>;
  pl: { revenue: number; cost: number } | null;
  events: OpsEvent[];
}

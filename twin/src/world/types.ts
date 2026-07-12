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

// ─── Contracts ───────────────────────────────────────────────────────────────────
// The CANONICAL SpaceTraders contract object — field-for-field the shape the Go client
// decodes (client.go parseContractData:1295). The client reads terms.deadline ONLY, and the
// deliverables key is "deliver" (NOT "deliveries"). serializeContract (world/contracts.ts)
// emits exactly this shape everywhere a contract appears on the /v2 surface.

/** Contract payment split: credits paid at acceptance vs. at fulfillment. */
export interface Payment { onAccepted: number; onFulfilled: number }

/** One required delivery line. `unitsFulfilled` climbs (capped at `unitsRequired`) as cargo is
 *  delivered; the contract is fulfillable once every line has unitsFulfilled >= unitsRequired. */
export interface ContractDeliverable {
  tradeSymbol: string;
  destinationSymbol: string;
  unitsRequired: number;
  unitsFulfilled: number;
}

export interface ContractTerms {
  deadline: Rfc3339;               // the delivery deadline (terms.deadline — the only one the client reads)
  payment: Payment;
  deliver: ContractDeliverable[];  // KEY is "deliver" — the client rejects "deliveries"
}

export interface Contract {
  id: string;
  factionSymbol: string;
  type: string;        // PROCUREMENT | TRANSPORT | SHUTTLE
  accepted: boolean;
  fulfilled: boolean;
  terms: ContractTerms;
}

// ─── TWIN-OWNED control-plane state (backs GET /_twin/state + POST /_twin/report) ───
// Reshaped /_twin/state is a BASE object; the INCOME and GATE phase views read supersets
// of it. Cold defaults are produced by newControlPlaneState() (loader.ts); mode-specific
// seeding (income-entry / gate-entry) is layered on top by the reset endpoint.

/** One append-only mutation-log record. `seq` is monotonic (1-indexed, +1 over the prior
 *  entry); `at` is the WORLD-clock now (getNow) at append time — NOT wall time. `detail`
 *  is omitted from the entry entirely when the mutation carries none. */
export interface MutationLogEntry {
  seq: number;
  call: string;
  detail?: Record<string, unknown>;
  at: Rfc3339;
}

/** Per-waypoint market scouting flags. Held OFF the /v2 Market DTO (which the game API
 *  serializes verbatim) so fidelity stays clean; only the /_twin/state markets[] view reads
 *  these. Keyed by waypoint symbol in ControlPlaneState.marketScouting. */
export interface MarketScouting {
  scouted: boolean;
  fresh: boolean;
}

/** INCOME-view hauler projection. `parkedHub` is the hub waypoint a hauler has been parked
 *  at (null until a navigate parks it there). */
export interface HaulerState {
  symbol: string;
  role: string;
  parkedHub: string | null;
}

/** GATE-view worker projection. `source` distinguishes a repurposed existing hull (logs
 *  nothing, no PurchaseShip) from a freshly bought one (bought == PurchaseShip count). */
export interface GateWorkerState {
  symbol: string;
  source: 'repurposed' | 'bought';
}

/** GATE-view construction progress. `percent` NEVER auto-advances (only POST /_twin/construction
 *  moves it, incl. ->100); `started`/`adopted` are exactly-once report flips. */
export interface ConstructionState {
  site: string;
  percent: number;
  started: boolean;
  adopted: boolean;
}

/** GATE-view standing coordinators, each launched exactly-once via the /_twin/report seam. */
export interface StandingCoordinators {
  siting: boolean;
  workerRebalancer: boolean;
}

/** The TWIN-OWNED control-plane state mixed into World. Backs the GET /_twin/state superset
 *  (BASE + INCOME + GATE) and the POST /_twin/report ingest (applyReport flips the paired
 *  flags exactly-once). Cold defaults via newControlPlaneState(). */
export interface ControlPlaneState {
  // BASE
  mutationLog: MutationLogEntry[];
  coverage: number;
  marketScouting: Map<string, MarketScouting>;   // keyed by waypoint symbol
  scoutAssigned: boolean;                         // scout-all-markets assigned (report seam) -> probes' scoutAssignment
  contracts: Map<string, Contract>;               // keyed by contract id — the negotiate/accept/deliver/fulfill state machine
  activeContractId: string | null;                // the ONE active contract (null until negotiate; cleared on fulfill)
  // INCOME view (+)
  haulers: HaulerState[];
  frigateContractTagged: boolean;
  batchContractRunning: boolean;
  creditsPerHour: number;                         // == gate incomePerHour (the SAME $/hr var)
  hubs: string[];
  // GATE view (+)
  construction: ConstructionState;
  gateWorkers: GateWorkerState[];
  executorRunning: boolean;
  autosizerRunning: boolean;
  standingCoordinators: StandingCoordinators;
  done: boolean;
}

export interface World extends ControlPlaneState {
  serverStatus: { resetDate: string; serverResets: { next: Rfc3339; frequency: string } };
  agent: Agent | null;
  agentToken: string | null;
  ships: Map<string, Ship>;
  systems: Map<string, System>;
  markets: Map<string, Market>;
  shipyards: Map<string, Shipyard>;
  transits: Map<string, TransitState>;
  shipCounter: number;
  // ─── reset price/sizing levers (NOT part of the /_twin/state superset) ───────────
  // Seeded by POST /_twin/reset so later pieces (shipyard listing / PurchaseShip route /
  // gate autosizer) can read them; optional so existing World literals need no change.
  shipPrices?: Record<string, number>;   // e.g. { SHIP_PROBE: probePrice, LIGHT_HAULER: haulerPrice|workerPrice }
  gateMaterialChains?: number;           // gate-entry worker-sizing input (producing chains)
  // POST /_twin/construction derives this from percent>=100 (the api-fidelity-spec's /v2
  // Construction.isComplete) for a future /v2 construction endpoint to read directly off
  // World. Deliberately NOT read by GET /_twin/state — the frozen contract's construction
  // view is exactly {site, percent, started, adopted} (3 existing tests assert it via toEqual).
  constructionIsComplete?: boolean;
}

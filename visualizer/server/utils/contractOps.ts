// Pure logic for the Contract Ops endpoints (sp-c6pm). Everything here is
// deterministic row-in/shape-out so it can be unit-tested without a database;
// routes/contract-ops.ts owns the SQL and wires these together.

export interface DepotElement {
  waypoint: string;
  shipSymbol: string; // '' = declared-but-uncrewed slot
}

export interface DeliveryProgress {
  tradeSymbol: string;
  destinationSymbol: string;
  unitsRequired: number;
  unitsFulfilled: number;
}

export type ContractPhase = 'IDLE' | 'NEGOTIATE' | 'ACCEPT' | 'SOURCE' | 'DELIVER' | 'FULFILL';

export interface WarehouseLevel {
  waypoint: string;
  good: string;
  units: number;
}

export type ShipRole = 'worker' | 'stocker' | 'warehouse' | 'delivery' | 'contract';

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

// contract_depots columns and contracts.deliveries_json are Go structs
// marshaled WITHOUT json tags, so keys arrive PascalCase.
export function parseElements(json: string | null): DepotElement[] {
  if (!json) return [];
  try {
    const parsed = JSON.parse(json);
    if (!Array.isArray(parsed)) return [];
    return parsed.map((e) => ({
      waypoint: String(e?.Waypoint ?? ''),
      shipSymbol: String(e?.ShipSymbol ?? ''),
    }));
  } catch {
    return [];
  }
}

export function parseDeliveries(json: string | null): DeliveryProgress[] {
  if (!json) return [];
  try {
    const parsed = JSON.parse(json);
    if (!Array.isArray(parsed)) return [];
    return parsed.map((d) => ({
      tradeSymbol: String(d?.TradeSymbol ?? ''),
      destinationSymbol: String(d?.DestinationSymbol ?? ''),
      unitsRequired: Number(d?.UnitsRequired ?? 0),
      unitsFulfilled: Number(d?.UnitsFulfilled ?? 0),
    }));
  } catch {
    return [];
  }
}

// Warehouse fill is event-sourced: current level = Σ stockings − Σ withdrawals
// per (waypoint, good). Negative nets (events pruned before their stockings)
// are clamped to zero and dropped so fill rings never render nonsense.
export function reduceWarehouseLevels(
  stockings: Array<{ waypoint: string; good: string; units: number }>,
  withdrawals: Array<{ waypoint: string; good: string; units: number }>,
): WarehouseLevel[] {
  const net = new Map<string, number>();
  const key = (w: string, g: string) => `${w}|${g}`;
  for (const s of stockings) net.set(key(s.waypoint, s.good), (net.get(key(s.waypoint, s.good)) ?? 0) + s.units);
  for (const w of withdrawals) net.set(key(w.waypoint, w.good), (net.get(key(w.waypoint, w.good)) ?? 0) - w.units);
  const levels: WarehouseLevel[] = [];
  for (const [k, units] of net) {
    if (units <= 0) continue;
    const [waypoint, good] = k.split('|');
    levels.push({ waypoint, good, units });
  }
  return levels.sort((a, b) => b.units - a.units);
}

// The live phase of the serialized contract loop, derived from what the DB can
// see. Deliberately coarse — it drives a five-lamp phase strip, not logic.
export function derivePhase(input: {
  contract: { accepted: boolean; deliveries: DeliveryProgress[] } | null;
  workerRunning: boolean;
  workerCargo: Array<{ symbol: string; units: number }>;
}): ContractPhase {
  const { contract, workerRunning, workerCargo } = input;
  if (!contract) return workerRunning ? 'NEGOTIATE' : 'IDLE';
  if (!contract.accepted) return 'ACCEPT';
  const remaining = contract.deliveries.some((d) => d.unitsFulfilled < d.unitsRequired);
  if (!remaining) return 'FULFILL';
  const goods = new Set(contract.deliveries.map((d) => d.tradeSymbol));
  const holdingGood = workerCargo.some((c) => goods.has(c.symbol) && c.units > 0);
  return holdingGood ? 'DELIVER' : 'SOURCE';
}

// Cycle health of the one-contract-at-a-time loop: how many were fulfilled in
// the trailing hour, and the average gap between consecutive fulfillments
// (last 20 rows). contracts.last_updated is the fulfillment timestamp for
// fulfilled rows — the row stops changing once fulfilled=true.
export function computeCycleStats(
  rows: Array<{ fulfilled: boolean; lastUpdated: string }>,
  nowMs: number,
): { fulfilledLastHour: number; avgCycleMinutes: number | null } {
  const fulfilledTimes = rows
    .filter((r) => r.fulfilled)
    .map((r) => Date.parse(r.lastUpdated))
    .filter((t) => Number.isFinite(t))
    .sort((a, b) => b - a)
    .slice(0, 20);
  const fulfilledLastHour = fulfilledTimes.filter((t) => nowMs - t <= 3_600_000).length;
  if (fulfilledTimes.length < 2) return { fulfilledLastHour, avgCycleMinutes: null };
  let gapSum = 0;
  for (let i = 0; i < fulfilledTimes.length - 1; i++) gapSum += fulfilledTimes[i] - fulfilledTimes[i + 1];
  const avgCycleMinutes = gapSum / (fulfilledTimes.length - 1) / 60_000;
  return { fulfilledLastHour, avgCycleMinutes };
}

export interface DepotShipSets {
  delivery: Set<string>;
  warehouse: Set<string>;
  stocker: Set<string>;
}

// A ship's live function. Precedence: the container it is currently claimed by
// (live truth) > its standing dedicated_fleet tag > its depot pin (this era's
// depot hulls carry EMPTY dedicated_fleet, so the pin is load-bearing) >
// generic 'contract' fleet.
export function classifyShip(
  ship: { shipSymbol: string; dedicatedFleet: string; containerId: string | null },
  containersById: Map<string, { containerType: string; commandType: string }>,
  depotSets: DepotShipSets,
): ShipRole {
  const container = ship.containerId ? containersById.get(ship.containerId) : undefined;
  if (container) {
    if (container.commandType === 'stocker') return 'stocker';
    if (container.containerType === 'WAREHOUSE') return 'warehouse';
    if (container.containerType === 'CONTRACT_WORKFLOW') return 'worker';
  }
  if (ship.dedicatedFleet === 'stocker') return 'stocker';
  if (ship.dedicatedFleet === 'warehouse') return 'warehouse';
  if (depotSets.warehouse.has(ship.shipSymbol)) return 'warehouse';
  if (depotSets.stocker.has(ship.shipSymbol)) return 'stocker';
  if (depotSets.delivery.has(ship.shipSymbol)) return 'delivery';
  return 'contract';
}

// 'X1-VB74-J58' → 'X1-VB74'. SpaceTraders waypoint symbols are
// SECTOR-SYSTEM-WAYPOINT; anything without three segments is not a waypoint.
export function systemOf(waypointSymbol: string): string | null {
  const parts = waypointSymbol.split('-');
  if (parts.length < 3) return null;
  return `${parts[0]}-${parts[1]}`;
}

export function involvedSystems(elements: DepotElement[], extraWaypoints: string[]): string[] {
  const systems = new Set<string>();
  for (const e of elements) {
    const s = systemOf(e.waypoint);
    if (s) systems.add(s);
  }
  for (const w of extraWaypoints) {
    const s = systemOf(w);
    if (s) systems.add(s);
  }
  return [...systems];
}

export function mergeEvents(
  stockings: Array<{ at: string; good: string; units: number; waypoint: string; shipSymbol: string }>,
  withdrawals: Array<{ at: string; good: string; units: number; waypoint: string; shipSymbol: string; contractId: string }>,
  transactions: Array<{ at: string; amount: number; description: string }>,
  cap: number,
): OpsEvent[] {
  const events: OpsEvent[] = [
    ...stockings.map((s): OpsEvent => ({ kind: 'stocking', at: s.at, good: s.good, units: s.units, waypoint: s.waypoint, shipSymbol: s.shipSymbol })),
    ...withdrawals.map((w): OpsEvent => ({ kind: 'withdrawal', at: w.at, good: w.good, units: w.units, waypoint: w.waypoint, shipSymbol: w.shipSymbol, contractId: w.contractId || undefined })),
    ...transactions.map((t): OpsEvent => ({ kind: 'transaction', at: t.at, amount: t.amount, description: t.description })),
  ];
  return events
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
    .slice(0, cap);
}

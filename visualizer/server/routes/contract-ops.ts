import { Router } from 'express';
import pkg from 'pg';
const { Pool } = pkg;
import {
  parseElements,
  parseDeliveries,
  reduceWarehouseLevels,
  derivePhase,
  computeCycleStats,
  classifyShip,
  involvedSystems,
  mergeEvents,
  type DepotElement,
  type OpsEvent,
} from '../utils/contractOps.js';

const router = Router();

// Lazy pg pool — construction does NOT connect (mirrors routes/flows.ts). The
// idle-client 'error' listener prevents a DB restart from crashing the process.
const pool = new Pool({
  connectionString:
    process.env.DATABASE_URL || 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders',
});
pool.on('error', (err) => {
  console.error('pg pool idle-client error (DB likely restarting):', err.message);
});

type PgClient = { query: (sql: string, params?: unknown[]) => Promise<{ rows: any[] }> };

// Era re-registration leaves several players with the same agent symbol, and
// players.last_active alone can point at a dead era. The live player is the one
// whose containers are actually running (freshest heartbeat); only if nothing
// runs do we fall back to last_active.
async function resolveLivePlayer(client: PgClient): Promise<number | null> {
  const running = await client.query(
    `SELECT player_id FROM containers WHERE status = 'RUNNING'
     GROUP BY player_id ORDER BY MAX(heartbeat_at) DESC NULLS LAST LIMIT 1`,
  );
  if (running.rows[0]?.player_id != null) return Number(running.rows[0].player_id);
  const fallback = await client.query(
    `SELECT id FROM players ORDER BY last_active DESC NULLS LAST, id LIMIT 1`,
  );
  return fallback.rows[0]?.id != null ? Number(fallback.rows[0].id) : null;
}

interface DepotRow {
  id: string;
  warehouses: DepotElement[];
  stockers: DepotElement[];
  deliveryHulls: DepotElement[];
  sourceHubs: DepotElement[];
}

async function loadDepots(client: PgClient, playerId: number): Promise<DepotRow[]> {
  const result = await client.query(
    `SELECT id, warehouses, stockers, delivery_hulls, source_hubs
     FROM contract_depots WHERE player_id = $1 ORDER BY id`,
    [playerId],
  );
  return result.rows.map((r) => ({
    id: r.id,
    warehouses: parseElements(r.warehouses),
    stockers: parseElements(r.stockers),
    deliveryHulls: parseElements(r.delivery_hulls),
    sourceHubs: parseElements(r.source_hubs),
  }));
}

// ---- GET /api/contract-ops/topology -----------------------------------------
// Depot layout + waypoint backdrop for every involved system. Depot topology
// changes on the order of CLI edits, so a short in-memory cache absorbs the
// browser's poll without hammering PG.
let topologyCache: { payload: unknown; builtAtMs: number } | null = null;
const TOPOLOGY_TTL_MS = 60 * 1000;

router.get('/topology', async (_req, res) => {
  if (topologyCache && Date.now() - topologyCache.builtAtMs < TOPOLOGY_TTL_MS) {
    return res.json(topologyCache.payload);
  }
  let client;
  try {
    client = await pool.connect();
    const playerId = await resolveLivePlayer(client);
    if (playerId == null) return res.json({ playerId: null, depots: [], systems: [], waypoints: [] });

    const depots = await loadDepots(client, playerId);
    const allElements = depots.flatMap((d) => [...d.warehouses, ...d.stockers, ...d.deliveryHulls, ...d.sourceHubs]);

    // The active contract's destination system joins the backdrop so a contract
    // outside the depot systems still renders on real coordinates.
    const activeContract = await client.query(
      `SELECT deliveries_json FROM contracts
       WHERE player_id = $1 AND accepted = true AND fulfilled = false
       ORDER BY last_updated DESC LIMIT 1`,
      [playerId],
    );
    const destinations = parseDeliveries(activeContract.rows[0]?.deliveries_json ?? null).map(
      (d) => d.destinationSymbol,
    );

    const systems = involvedSystems(allElements, destinations);
    const waypoints = systems.length
      ? await client.query(
          `SELECT waypoint_symbol, system_symbol, type, x, y
           FROM waypoints WHERE system_symbol = ANY($1) ORDER BY waypoint_symbol`,
          [systems],
        )
      : { rows: [] };

    const payload = {
      playerId,
      depots,
      systems,
      waypoints: waypoints.rows.map((w) => ({
        symbol: w.waypoint_symbol,
        system: w.system_symbol,
        type: w.type,
        x: Number(w.x),
        y: Number(w.y),
      })),
    };
    topologyCache = { payload, builtAtMs: Date.now() };
    res.json(payload);
  } catch (error: any) {
    console.error('contract-ops topology failed:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    client?.release();
  }
});

// ---- GET /api/contract-ops/live ----------------------------------------------
// One aggregate the browser polls every ~5s: active contract + phase, cycle
// stats, the contract containers, every role ship with nav+cargo, event-sourced
// warehouse levels, the recent event stream, and the active contract's P/L.
router.get('/live', async (_req, res) => {
  let client;
  try {
    client = await pool.connect();
    const playerId = await resolveLivePlayer(client);
    if (playerId == null) return res.json(emptyLivePayload());

    const [contractRows, containerRows, depots] = await Promise.all([
      client.query(
        `SELECT id, accepted, fulfilled, deadline, payment_on_accepted, payment_on_fulfilled,
                deliveries_json, last_updated
         FROM contracts WHERE player_id = $1 ORDER BY last_updated DESC LIMIT 60`,
        [playerId],
      ),
      client.query(
        `SELECT id, container_type, command_type, status, parent_container_id, heartbeat_at, config
         FROM containers
         WHERE player_id = $1 AND status = 'RUNNING'
           AND (container_type IN ('CONTRACT_FLEET_COORDINATOR', 'CONTRACT_WORKFLOW', 'WAREHOUSE')
                OR command_type = 'stocker')`,
        [playerId],
      ),
      loadDepots(client, playerId),
    ]);

    const containersById = new Map<string, { containerType: string; commandType: string }>();
    for (const c of containerRows.rows) {
      containersById.set(c.id, { containerType: c.container_type, commandType: c.command_type });
    }
    const containerIds = [...containersById.keys()];
    const nonEmpty = (elements: DepotElement[]) => elements.map((e) => e.shipSymbol).filter(Boolean);
    const depotSets = {
      delivery: new Set(depots.flatMap((d) => nonEmpty(d.deliveryHulls))),
      warehouse: new Set(depots.flatMap((d) => nonEmpty(d.warehouses))),
      stocker: new Set(depots.flatMap((d) => nonEmpty(d.stockers))),
    };
    const depotShipSymbols = [...new Set([...depotSets.delivery, ...depotSets.warehouse, ...depotSets.stocker])];

    // This era's depot hulls carry an empty dedicated_fleet tag, so the depot
    // element lists are a required inclusion path, not an optimization.
    const shipRows = await client.query(
      `SELECT ship_symbol, nav_status, location_symbol, location_x, location_y, system_symbol,
              arrival_time, cargo_units, cargo_capacity, cargo_inventory, dedicated_fleet,
              container_id, engine_speed
       FROM ships
       WHERE player_id = $1
         AND (dedicated_fleet IN ('contract', 'stocker', 'warehouse')
              OR container_id = ANY($2)
              OR ship_symbol = ANY($3))
       ORDER BY ship_symbol`,
      [playerId, containerIds, depotShipSymbols],
    );

    const [stockRows, withdrawRows, stockEventRows, withdrawEventRows, txnEventRows] = await Promise.all([
      client.query(
        `SELECT warehouse_waypoint AS waypoint, good, COALESCE(SUM(units), 0)::bigint AS units
         FROM warehouse_stockings WHERE player_id = $1 GROUP BY 1, 2`,
        [playerId],
      ),
      client.query(
        `SELECT waypoint, good, COALESCE(SUM(units), 0)::bigint AS units
         FROM warehouse_withdrawals WHERE player_id = $1 GROUP BY 1, 2`,
        [playerId],
      ),
      client.query(
        `SELECT deposited_at, good, units, warehouse_waypoint, ship_symbol
         FROM warehouse_stockings WHERE player_id = $1 ORDER BY deposited_at DESC LIMIT 12`,
        [playerId],
      ),
      client.query(
        `SELECT withdrawn_at, good, units, waypoint, ship_symbol, contract_id
         FROM warehouse_withdrawals WHERE player_id = $1 ORDER BY withdrawn_at DESC LIMIT 12`,
        [playerId],
      ),
      client.query(
        `SELECT timestamp, amount, description
         FROM transactions
         WHERE player_id = $1 AND (operation_type = 'contract' OR related_entity_type = 'contract')
         ORDER BY timestamp DESC LIMIT 12`,
        [playerId],
      ),
    ]);

    const contracts = contractRows.rows.map((r) => ({
      id: r.id as string,
      accepted: Boolean(r.accepted),
      fulfilled: Boolean(r.fulfilled),
      deadline: r.deadline as string,
      paymentOnAccepted: Number(r.payment_on_accepted),
      paymentOnFulfilled: Number(r.payment_on_fulfilled),
      deliveries: parseDeliveries(r.deliveries_json),
      lastUpdated: r.last_updated as string,
    }));
    const active = contracts.find((c) => c.accepted && !c.fulfilled) ?? null;
    const lastFulfilled = contracts.find((c) => c.fulfilled) ?? null;

    // Destination coordinates for the beacon (denormalized ship x/y cover the
    // fleet; only the contract destination needs a waypoint join).
    const destinationSymbols = active ? active.deliveries.map((d) => d.destinationSymbol) : [];
    const destRows = destinationSymbols.length
      ? await client.query(
          `SELECT waypoint_symbol, system_symbol, x, y FROM waypoints WHERE waypoint_symbol = ANY($1)`,
          [destinationSymbols],
        )
      : { rows: [] };

    const worker = containerRows.rows.find((c) => c.container_type === 'CONTRACT_WORKFLOW') ?? null;
    const coordinator = containerRows.rows.find((c) => c.container_type === 'CONTRACT_FLEET_COORDINATOR') ?? null;
    const workerShipSymbol = worker ? safeConfigShipSymbol(worker.config) : null;
    const workerShipRow = workerShipSymbol
      ? shipRows.rows.find((s) => s.ship_symbol === workerShipSymbol) ?? null
      : null;

    const phase = derivePhase({
      contract: active ? { accepted: active.accepted, deliveries: active.deliveries } : null,
      workerRunning: worker != null,
      workerCargo: parseCargo(workerShipRow?.cargo_inventory),
    });

    const plRows = active
      ? await client.query(
          `SELECT COALESCE(SUM(CASE WHEN amount > 0 THEN amount ELSE 0 END), 0)::bigint AS revenue,
                  COALESCE(SUM(CASE WHEN amount < 0 THEN -amount ELSE 0 END), 0)::bigint AS cost
           FROM transactions
           WHERE player_id = $1 AND related_entity_type = 'contract' AND related_entity_id = $2`,
          [playerId, active.id],
        )
      : null;

    const events: OpsEvent[] = mergeEvents(
      stockEventRows.rows.map((r) => ({
        at: toIso(r.deposited_at), good: r.good, units: Number(r.units),
        waypoint: r.warehouse_waypoint, shipSymbol: r.ship_symbol,
      })),
      withdrawEventRows.rows.map((r) => ({
        at: toIso(r.withdrawn_at), good: r.good, units: Number(r.units),
        waypoint: r.waypoint, shipSymbol: r.ship_symbol, contractId: r.contract_id ?? '',
      })),
      txnEventRows.rows.map((r) => ({
        at: toIso(r.timestamp), amount: Number(r.amount), description: r.description ?? '',
      })),
      20,
    );

    res.json({
      playerId,
      generatedAt: new Date().toISOString(),
      phase,
      contract: active,
      lastFulfilled: lastFulfilled
        ? { id: lastFulfilled.id, at: lastFulfilled.lastUpdated, payment: lastFulfilled.paymentOnFulfilled }
        : null,
      cycle: computeCycleStats(
        contracts.map((c) => ({ fulfilled: c.fulfilled, lastUpdated: c.lastUpdated })),
        Date.now(),
      ),
      coordinator: coordinator ? { id: coordinator.id, heartbeatAt: toIso(coordinator.heartbeat_at) } : null,
      worker: worker
        ? { id: worker.id, shipSymbol: workerShipSymbol, heartbeatAt: toIso(worker.heartbeat_at) }
        : null,
      ships: shipRows.rows.map((s) => ({
        symbol: s.ship_symbol,
        role: classifyShip(
          { shipSymbol: s.ship_symbol, dedicatedFleet: s.dedicated_fleet ?? '', containerId: s.container_id },
          containersById,
          depotSets,
        ),
        navStatus: s.nav_status,
        waypoint: s.location_symbol,
        system: s.system_symbol,
        x: Number(s.location_x),
        y: Number(s.location_y),
        arrivalTime: s.arrival_time ? toIso(s.arrival_time) : null,
        cargoUnits: Number(s.cargo_units),
        cargoCapacity: Number(s.cargo_capacity),
        cargo: parseCargo(s.cargo_inventory),
        containerId: s.container_id,
      })),
      warehouses: reduceWarehouseLevels(
        stockRows.rows.map((r) => ({ waypoint: r.waypoint, good: r.good, units: Number(r.units) })),
        withdrawRows.rows.map((r) => ({ waypoint: r.waypoint, good: r.good, units: Number(r.units) })),
      ),
      destinations: destRows.rows.map((d) => ({
        symbol: d.waypoint_symbol, system: d.system_symbol, x: Number(d.x), y: Number(d.y),
      })),
      pl: plRows ? { revenue: Number(plRows.rows[0].revenue), cost: Number(plRows.rows[0].cost) } : null,
      events,
    });
  } catch (error: any) {
    console.error('contract-ops live failed:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    client?.release();
  }
});

function emptyLivePayload() {
  return {
    playerId: null,
    generatedAt: new Date().toISOString(),
    phase: 'IDLE',
    contract: null,
    lastFulfilled: null,
    cycle: { fulfilledLastHour: 0, avgCycleMinutes: null },
    coordinator: null,
    worker: null,
    ships: [],
    warehouses: [],
    destinations: [],
    pl: null,
    events: [],
  };
}

function safeConfigShipSymbol(config: string | null): string | null {
  if (!config) return null;
  try {
    const parsed = JSON.parse(config);
    return typeof parsed?.ship_symbol === 'string' ? parsed.ship_symbol : null;
  } catch {
    return null;
  }
}

// ships.cargo_inventory is JSONB — pg may hand it back already parsed.
function parseCargo(value: unknown): Array<{ symbol: string; units: number }> {
  let items: unknown = value;
  if (typeof value === 'string') {
    try {
      items = JSON.parse(value);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(items)) return [];
  return items.map((i: any) => ({ symbol: String(i?.symbol ?? ''), units: Number(i?.units ?? 0) }));
}

function toIso(value: unknown): string {
  if (value instanceof Date) return value.toISOString();
  return String(value ?? '');
}

export default router;

import { Router } from 'express';
import pkg from 'pg';
const { Pool } = pkg;
import { computeGalaxyLayout } from '../utils/galaxyLayout.js';
import { aggregateLanes } from '../utils/laneAggregation.js';
import { homeSystemFromHeadquarters } from '../utils/homeSystem.js';
import { SpaceTradersClient } from '../src/client.js';

const router = Router();

const API_BASE_URL = 'https://api.spacetraders.io/v2';

// Best-effort home-system derivation for the galaxy/drilldown marker. The token
// lives in PG players.token (the server already owns it); one GET /my/agent gives
// the headquarters waypoint, whose first two segments are the home system. This
// runs only on a topology cache MISS (~once/5min), so it adds zero polling. ANY
// failure (no token, API down, malformed response) returns null and the field is
// omitted — the Admiral directive is to render no marker rather than guess.
async function deriveHomeSystem(client: {
  query: (sql: string) => Promise<{ rows: any[] }>;
}): Promise<string | null> {
  try {
    const tokenResult = await client.query(
      `SELECT token FROM players WHERE token <> '' ORDER BY last_active DESC NULLS LAST, id LIMIT 1`,
    );
    const token = tokenResult.rows[0]?.token as string | undefined;
    if (!token) return null;
    const stClient = new SpaceTradersClient(API_BASE_URL, token);
    const agent = await stClient.get('/my/agent');
    return homeSystemFromHeadquarters(agent?.data?.headquarters);
  } catch (error: any) {
    console.error('Home-system derivation failed (omitting marker):', error?.message ?? error);
    return null;
  }
}

// Lazy pg pool — construction does NOT connect (mirrors routes/bot.ts). The
// idle-client 'error' listener prevents a DB restart from crashing the process.
const pool = new Pool({
  connectionString:
    process.env.DATABASE_URL || 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders',
});
pool.on('error', (err) => {
  console.error('pg pool idle-client error (DB likely restarting):', err.message);
});

// ---- GET /api/flows/topology -------------------------------------------------
// PG gate_edges (real edges only) + a deterministic server-computed galaxy
// layout. Cached in-memory: the gate graph changes on the order of eras, and
// the browser polls this once per mount. Any DB failure degrades to 503.
let topologyCache: { payload: unknown; builtAtMs: number } | null = null;
const TOPOLOGY_TTL_MS = 5 * 60 * 1000;

router.get('/topology', async (_req, res) => {
  if (topologyCache && Date.now() - topologyCache.builtAtMs < TOPOLOGY_TTL_MS) {
    return res.json(topologyCache.payload);
  }
  let client;
  try {
    client = await pool.connect();
    const result = await client.query(`
      SELECT system_symbol, connected_system, gate_waypoint, under_construction
      FROM gate_edges
      WHERE connected_system <> ''
    `);

    const edges = result.rows.map((r: any) => ({
      from: r.system_symbol as string,
      to: r.connected_system as string,
      gateWaypoint: r.gate_waypoint as string,
      underConstruction: Boolean(r.under_construction),
    }));

    const systemSet = new Set<string>();
    for (const e of edges) {
      systemSet.add(e.from);
      systemSet.add(e.to);
    }
    const layout = computeGalaxyLayout([...systemSet], edges.map((e) => ({ from: e.from, to: e.to })));

    const homeSystem = await deriveHomeSystem(client);

    const payload = {
      systems: layout,
      edges,
      ...(homeSystem ? { homeSystem } : {}),
      generatedAt: new Date().toISOString(),
    };
    topologyCache = { payload, builtAtMs: Date.now() };
    res.json(payload);
  } catch (error: any) {
    console.error('Failed to build flows topology:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    if (client) client.release();
  }
});

// ---- GET /api/flows/lanes?window=1h|6h|24h -----------------------------------
// Realized directed-lane volume/profit over the window, from tour_leg_telemetry
// (multi-hop tours + trade-route circuits) + arbitrage_execution_logs (arb).
const WINDOW_MS: Record<string, number> = {
  '1h': 60 * 60 * 1000,
  '6h': 6 * 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000,
};

router.get('/lanes', async (req, res) => {
  const window = (req.query.window as string) || '6h';
  const span = WINDOW_MS[window];
  if (!span) {
    return res.status(400).json({ error: 'invalid_window' });
  }
  const windowEndMs = Date.now();
  const windowStartMs = windowEndMs - span;
  const sinceIso = new Date(windowStartMs).toISOString();

  let client;
  try {
    client = await pool.connect();

    const telemetryResult = await client.query(`
      SELECT tour_id, ship_symbol, leg_index, waypoint, is_buy,
             realized_units, realized_unit_price, realized_at
      FROM tour_leg_telemetry
      WHERE realized_at IS NOT NULL AND realized_at >= $1
      ORDER BY tour_id, ship_symbol, leg_index, realized_at
    `, [sinceIso]);

    const arbResult = await client.query(`
      SELECT buy_market, sell_market, units_sold, actual_net_profit, executed_at
      FROM arbitrage_execution_logs
      WHERE success = true AND executed_at >= $1
    `, [sinceIso]);

    const telemetry = telemetryResult.rows.map((r: any) => ({
      tourId: r.tour_id,
      shipSymbol: r.ship_symbol,
      legIndex: Number(r.leg_index),
      waypoint: r.waypoint,
      isBuy: Boolean(r.is_buy),
      realizedUnits: Number(r.realized_units) || 0,
      realizedUnitPrice: Number(r.realized_unit_price) || 0,
      realizedAt: new Date(r.realized_at).toISOString(),
    }));
    const arb = arbResult.rows.map((r: any) => ({
      buyMarket: r.buy_market,
      sellMarket: r.sell_market,
      unitsSold: Number(r.units_sold) || 0,
      actualNetProfit: Number(r.actual_net_profit) || 0,
      executedAt: new Date(r.executed_at).toISOString(),
    }));

    const lanes = aggregateLanes(telemetry, arb, windowStartMs, windowEndMs);
    res.json({ lanes, window, generatedAt: new Date().toISOString() });
  } catch (error: any) {
    console.error('Failed to build flows lanes:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    if (client) client.release();
  }
});

// ---- GET /api/flows/live -----------------------------------------------------
// Proxy the daemon in-memory plan feed, join PG ships for position truth. The
// browser never talks to the daemon directly — this is the only hop. Daemon
// unreachable/slow/non-200 => feedLost (tab keeps working on PG); PG down during
// the nav join => 503.
const DAEMON_FLOWS_URL = process.env.DAEMON_FLOWS_URL || 'http://localhost:9090/api/flows';
const DAEMON_TIMEOUT_MS = 2000;

interface RawDaemonFlow {
  containerId: string;
  program: 'tour' | 'trade-route' | 'arb';
  ship: string;
  tourId: string | null;
  currentLeg: { from: string; to: string; departedAt: string; arrivesAt: string } | null;
  cargo: { good: string; units: number }[];
  remainingHops: { waypoint: string; tranches: { good: string; isBuy: boolean; units: number; expectedUnitPrice: number }[] }[];
  projected: { profit: number; ratePerHour: number } | null;
  plannedAt: string;
}

async function fetchDaemonFlows(): Promise<{ flows: RawDaemonFlow[] } | null> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), DAEMON_TIMEOUT_MS);
  try {
    const resp = await fetch(DAEMON_FLOWS_URL, { signal: controller.signal });
    if (!resp.ok) return null;
    const body: any = await resp.json();
    return { flows: Array.isArray(body?.flows) ? body.flows : [] };
  } catch {
    return null; // unreachable / timeout / bad JSON => feed lost
  } finally {
    clearTimeout(timer);
  }
}

router.get('/live', async (_req, res) => {
  const feed = await fetchDaemonFlows();

  if (feed === null) {
    // Feed lost: the tab still works on PG residue; never fabricate intent.
    return res.json({ flows: [], generatedAt: new Date().toISOString(), feedLost: true, lastPlanAt: null });
  }

  // Feed up: join PG ships for last-known position truth. PG failure here is a
  // real error state (503), distinct from feed loss.
  let client;
  try {
    client = await pool.connect();
    const shipSymbols = [...new Set(feed.flows.map((f) => f.ship))];
    const navByShip = new Map<string, any>();
    if (shipSymbols.length > 0) {
      const result = await client.query(`
        SELECT ship_symbol, nav_status, system_symbol, location_symbol,
               location_x, location_y, arrival_time
        FROM ships
        WHERE ship_symbol = ANY($1)
      `, [shipSymbols]);
      for (const r of result.rows) navByShip.set(r.ship_symbol, r);
    }

    const flows = feed.flows.map((f) => {
      const nav = navByShip.get(f.ship);
      return {
        ...f,
        shipNav: nav
          ? {
              status: nav.nav_status,
              systemSymbol: nav.system_symbol,
              waypointSymbol: nav.location_symbol,
              x: Number(nav.location_x) || 0,
              y: Number(nav.location_y) || 0,
              arrivalTime: nav.arrival_time ? new Date(nav.arrival_time).toISOString() : null,
            }
          : null,
      };
    });

    const lastPlanAt = flows.reduce<string | null>(
      (max, f) => (max === null || f.plannedAt > max ? f.plannedAt : max),
      null,
    );

    res.json({ flows, generatedAt: new Date().toISOString(), feedLost: false, lastPlanAt });
  } catch (error: any) {
    console.error('Failed to join ship nav for flows/live:', error?.message ?? error);
    res.status(503).json({ error: 'db_unavailable' });
  } finally {
    if (client) client.release();
  }
});

export default router;

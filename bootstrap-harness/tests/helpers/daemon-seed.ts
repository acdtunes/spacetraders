import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { TEST_DATABASE_URL, API_BASE_URL } from './config';
import type { GateFixture } from './fixtures-gate';

// Seed the DAEMON's LOCAL Postgres (not the twin world) so `derivePhase` observes DATA-complete
// market coverage and crosses DATA→INCOME immediately, without waiting on real scout tours that
// never complete a scan inside the harness's compressed fast-tick window.
//
// WHY THIS EXISTS: the income scenario admin-seeds the TWIN into the post-DATA / INCOME-entry world,
// but `resetDaemonDb()` truncates the daemon's `waypoints` + `market_data` mirror, and the daemon's
// startup sync only re-syncs SHIPS — never markets. So the daemon boots with coverage ~1/30 (3%) and
// `derivePhase` (bootstrap_ports.go / run_bootstrap_reconcile.go) correctly holds at DATA forever,
// so INCOME behaviour never fires. This helper writes the market coverage the twin would have if the
// scouts had finished, using the twin's REAL prices (all 30 marketplaces are served → no fabrication).
//
// CALL SITE: between `resetDaemonDb()` and `startTestDaemon()` in scenario-income.ts. The rows are
// read by the daemon's first bootstrap reconcile; the ship pre-tag survives the startup ship sync
// (SyncAllFromAPI preserves an existing row's `dedicated_fleet`, sp-bi75).

const SYSTEM_SYMBOL = 'X1-PZ28';
const PLAYER_ID = 1;
const COMMAND_ROLE = 'COMMAND';
const CONTRACT_FLEET_TAG = 'contract'; // gobot bootstrap_ports.go: contractFleetTag

interface TwinWaypoint {
  symbol: string;
  type: string;
  x: number;
  y: number;
  traits: string[]; // trait symbols only
}

interface TwinTradeGood {
  symbol: string;
  type: string; // EXPORT | IMPORT | EXCHANGE
  tradeVolume: number;
  supply: string;
  activity: string;
  purchasePrice: number;
  sellPrice: number;
}

export interface SeedCoverageResult {
  system: string;
  total: number; // marketplace waypoints seeded (= MarketsTotal the daemon will observe)
  covered: number; // marketplaces given a fresh market_data row (= MarketsCovered)
  goods: number; // total market_data rows written
  commandShip: string | null; // the frigate tagged dedicated_fleet=contract, or null if not found
  eraId: number | null; // era stamped on seeded waypoints (open era's id, or null)
}

async function getJson(url: string, headers?: Record<string, string>): Promise<any> {
  const res = await fetch(url, headers ? { headers } : undefined);
  if (!res.ok) throw new Error(`GET ${url} → ${res.status} ${await res.text()}`);
  return res.json();
}

// Replicate the daemon's graph-build marketplace set: list ALL waypoints in the system and filter
// CLIENT-SIDE to those whose own traits array contains MARKETPLACE. (The twin's `?traits=` query
// filter is broken — it returns every waypoint — so the daemon filters client-side, and so must we,
// or MarketsTotal inflates to the full system and the 90% bar becomes unreachable.)
async function listMarketplaceWaypoints(): Promise<TwinWaypoint[]> {
  const out: TwinWaypoint[] = [];
  for (let page = 1; ; page++) {
    const body = await getJson(`${API_BASE_URL}/systems/${SYSTEM_SYMBOL}/waypoints?limit=20&page=${page}`);
    const data: any[] = body.data ?? [];
    for (const w of data) {
      const traits: string[] = (w.traits ?? []).map((t: any) => t?.symbol).filter(Boolean);
      if (traits.includes('MARKETPLACE')) {
        out.push({ symbol: w.symbol, type: w.type, x: w.x, y: w.y, traits });
      }
    }
    const total = body.meta?.total ?? 0;
    if (data.length === 0 || page * 20 >= total) break;
  }
  return out;
}

async function fetchTradeGoods(waypointSymbol: string): Promise<TwinTradeGood[]> {
  const body = await getJson(`${API_BASE_URL}/systems/${SYSTEM_SYMBOL}/waypoints/${waypointSymbol}/market`);
  return (body.data?.tradeGoods ?? []) as TwinTradeGood[];
}

// The COMMAND frigate is the INCOME step-1 retire target; the reconcile reads its daemon-local
// `dedicated_fleet == "contract"` tag. Discover the symbol from the API (role=COMMAND), falling
// back to the <agent>-1 convention so the seed still tags it if /my/ships is unavailable.
async function commandShipSymbol(token: string, agentSymbol: string): Promise<string | null> {
  try {
    const body = await getJson(`${API_BASE_URL}/my/ships`, { authorization: `Bearer ${token}` });
    for (const s of body.data ?? []) {
      if (s?.registration?.role === COMMAND_ROLE) return s.symbol as string;
    }
  } catch {
    // fall through to the convention
  }
  return agentSymbol ? `${agentSymbol}-1` : null;
}

function q(v: string): string {
  return `'${v.replace(/'/g, "''")}'`;
}

function psql(args: string[], input?: string): { stdout: string; stderr: string; status: number } {
  const res = spawnSync('psql', [TEST_DATABASE_URL, ...args], { encoding: 'utf8', input });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', status: res.status ?? -1 };
}

function scalar(sql: string): string {
  const res = psql(['-tAc', sql]);
  if (res.status !== 0) throw new Error(`seedDaemonMarketCoverage query failed: ${res.stderr}`);
  return res.stdout.trim();
}

export async function seedDaemonMarketCoverage(): Promise<SeedCoverageResult> {
  const marketplaces = await listMarketplaceWaypoints();
  if (marketplaces.length === 0) {
    throw new Error('seedDaemonMarketCoverage: twin served no MARKETPLACE waypoints in ' + SYSTEM_SYMBOL);
  }

  // Real twin prices for every marketplace (all served → 100% real coverage, no fabrication).
  const goodsByWp = new Map<string, TwinTradeGood[]>();
  for (const wp of marketplaces) goodsByWp.set(wp.symbol, await fetchTradeGoods(wp.symbol));

  // Era to stamp on seeded waypoints — resolved exactly as the daemon's openEraID() does (the open
  // era's id, else NULL). eraScopePredicate always admits era_id IS NULL, so a NULL fallback still
  // counts toward coverage, and a matching era_id stays idempotent with the daemon's later upsert.
  const eraRaw = scalar(`SELECT era_id FROM eras WHERE closed_at IS NULL ORDER BY era_id DESC LIMIT 1`);
  const eraId: number | null = eraRaw === '' ? null : Number(eraRaw);
  const eraLit = eraId === null ? 'NULL' : String(eraId);

  const agentSymbol = scalar(`SELECT agent_symbol FROM players WHERE id=${PLAYER_ID}`);
  const token = scalar(`SELECT token FROM players WHERE id=${PLAYER_ID}`);
  const commandShip = await commandShipSymbol(token, agentSymbol);

  // (a) waypoints — the 30 marketplace rows (real type/x/y/traits), era-scoped as above.
  const wpValues = marketplaces
    .map(
      (w) =>
        `(${q(w.symbol)}, ${q(SYSTEM_SYMBOL)}, ${q(w.type)}, ${w.x}, ${w.y}, ` +
        `${q(JSON.stringify(w.traits))}, 1, ${eraLit})`,
    )
    .join(',\n');
  const wpInsert =
    `INSERT INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, era_id)\nVALUES\n` +
    wpValues +
    `\nON CONFLICT (waypoint_symbol) DO UPDATE SET ` +
    `system_symbol=EXCLUDED.system_symbol, type=EXCLUDED.type, x=EXCLUDED.x, y=EXCLUDED.y, ` +
    `traits=EXCLUDED.traits, has_fuel=EXCLUDED.has_fuel, era_id=EXCLUDED.era_id;`;

  // (b) market_data — a fresh (now()) row per good per marketplace, verbatim twin values. Coverage
  // counts a marketplace as covered when it has ANY fresh row; seeding every good on all 30 → 100%.
  const mdRows: string[] = [];
  let covered = 0;
  for (const wp of marketplaces) {
    const goods = goodsByWp.get(wp.symbol) ?? [];
    if (goods.length > 0) covered++;
    for (const g of goods) {
      mdRows.push(
        `(${q(wp.symbol)}, ${q(g.symbol)}, ${q(g.supply)}, ${q(g.activity)}, ` +
          `${g.purchasePrice}, ${g.sellPrice}, ${g.tradeVolume}, ${q(g.type)}, now(), ${PLAYER_ID})`,
      );
    }
  }
  if (mdRows.length === 0) {
    throw new Error('seedDaemonMarketCoverage: twin served no tradeGoods for any marketplace');
  }
  const mdInsert =
    `INSERT INTO market_data (waypoint_symbol, good_symbol, supply, activity, purchase_price, ` +
    `sell_price, trade_volume, trade_type, last_updated, player_id)\nVALUES\n` +
    mdRows.join(',\n') +
    `\nON CONFLICT (waypoint_symbol, good_symbol) DO UPDATE SET ` +
    `supply=EXCLUDED.supply, activity=EXCLUDED.activity, purchase_price=EXCLUDED.purchase_price, ` +
    `sell_price=EXCLUDED.sell_price, trade_volume=EXCLUDED.trade_volume, ` +
    `trade_type=EXCLUDED.trade_type, last_updated=EXCLUDED.last_updated, player_id=EXCLUDED.player_id;`;

  // (c) command frigate — pre-tag its daemon-local dedicated_fleet=contract (INCOME retire target).
  // The row is otherwise minimal; the startup ship sync fills every other column from the API and
  // preserves this tag. Skipped only if we could not resolve the command ship symbol.
  const shipInsert = commandShip
    ? `INSERT INTO ships (ship_symbol, player_id, dedicated_fleet) ` +
      `VALUES (${q(commandShip)}, ${PLAYER_ID}, ${q(CONTRACT_FLEET_TAG)}) ` +
      `ON CONFLICT (ship_symbol, player_id) DO UPDATE SET dedicated_fleet=EXCLUDED.dedicated_fleet;`
    : '';

  const sql = ['BEGIN;', wpInsert, mdInsert, shipInsert, 'COMMIT;'].filter(Boolean).join('\n');
  const res = psql(['-v', 'ON_ERROR_STOP=1', '-f', '-'], sql);
  if (res.status !== 0) throw new Error(`seedDaemonMarketCoverage failed: ${res.stderr}`);

  return {
    system: SYSTEM_SYMBOL,
    total: marketplaces.length,
    covered,
    goods: mdRows.length,
    commandShip,
    eraId,
  };
}

// ─── GATE daemon-local seed (Slice 3) ────────────────────────────────────────────────────────────
//
// WHY THIS EXISTS: `withGateScenario` admin-seeds the TWIN into the GATE-entry world, then
// `resetDaemonDb()` wipes the daemon's LOCAL mirror. That leaves THREE GATE observables unmet in the
// daemon's own Postgres — none of which the twin fixture can supply, because the daemon derives GATE
// from what IT observes locally, not from the twin's frozen fixture levers:
//
//   1. INCOME→GATE crossing. `derivePhase` exits INCOME when `obs.IncomePerHour >= income_bar` (default
//      10000). The daemon reads that from its OWN ledger (readIncomePerHour → GetProfitLoss NetProfit
//      over the trailing 1h). The twin's `incomePerHour`/`creditsPerHour` fixture lever is NOT
//      daemon-observable, so without seeded ledger rows the daemon reads 0 $/hr and holds at INCOME.
//      NetProfit == Σ(amount) over the window and IsIncome()==(amount>0) (ledger/transaction.go), so a
//      positive realized row IS income regardless of transaction_type.
//   2. Gate-site discovery. `readBootstrapGateSnapshot` finds the site by scanning LOCAL waypoints for a
//      row whose `type == 'JUMP_GATE'` (bootstrap_ports_gate.go), then reads the twin's construction
//      site for it. `seedDaemonMarketCoverage` already inserts the I67 row with type=JUMP_GATE when the
//      site is a marketplace (it is), but we re-assert it so discovery is robust regardless.
//   3. D1 hauler dedication. The twin seeds the income haulers into `world.haulers` only — never
//      `world.ships` — so `GET /my/ships` never returns them, the daemon never syncs them, and
//      `obs.Haulers` (:5434 ships tagged dedicated_fleet='contract') is EMPTY → GATE repurposes 0. We
//      seed N contract-tagged SHIP_LIGHT_HAULER rows directly. SyncAllFromAPI upserts only API ships and
//      never deletes missing rows (and preserves dedicated_fleet, sp-bi75), so these survive daemon boot.
//
// CALL SITE: between `resetDaemonDb()` and `startTestDaemon()` in scenario-gate.ts (mirrors the INCOME
// wiring). Verified daemon-local only: repurpose is an AssignFleet re-tag in :5434, so the haulers are
// NOT needed in the twin's world.ships for the GATE observables to fire.

const HAULER_SHIP_FRAME = 'FRAME_LIGHT_FREIGHTER';
const HAULER_ROLE = 'HAULER'; // NOT SATELLITE → IsScoutType()==false → counted as a hauler, not a probe
const INCOME_TX_TYPE = 'SELL_CARGO'; // ledger.TransactionTypeSellCargo — a realized (amount>0) income row
const INCOME_TX_CATEGORY = 'TRADING_REVENUE'; // ledger.CategoryTradingRevenue (the SELL_CARGO category)
const DEFAULT_GATE_SITE = 'X1-PZ28-I67';
const DEFAULT_INCOME_PER_HOUR = 50_000; // fixture default; >> income_bar (10000) so INCOME→GATE crosses
const DEFAULT_HAULERS = 4;

export interface SeedGateEntryResult {
  coverage: SeedCoverageResult; // the reused INCOME coverage seed (DATA-complete + command-frigate tag)
  gateSite: string; // the JUMP_GATE waypoint re-asserted in local `waypoints` for gate-site discovery
  gateSiteType: string; // the site's waypoint type as served by the twin (must be JUMP_GATE)
  incomePerHour: number; // realized net $/hr seeded into the local ledger (>= income_bar)
  incomeRows: number; // transactions rows written
  haulers: string[]; // contract-dedicated hauler ship symbols seeded into :5434
}

// seedDaemonGateEntry establishes the daemon-local world the GATE derivation needs: DATA-complete
// coverage (reused), realized income over the bar, the JUMP_GATE site row, and the contract-hauler pool.
export async function seedDaemonGateEntry(fixture: GateFixture = {}): Promise<SeedGateEntryResult> {
  // (0) Reuse the INCOME coverage seed verbatim — the SAME class of daemon-local seeding, giving
  // DATA-complete market coverage (real twin prices) + the command-frigate contract tag.
  const coverage = await seedDaemonMarketCoverage();

  // Era + agent, resolved exactly as the coverage seed does (open era's id, else NULL).
  const eraRaw = scalar(`SELECT era_id FROM eras WHERE closed_at IS NULL ORDER BY era_id DESC LIMIT 1`);
  const eraId: number | null = eraRaw === '' ? null : Number(eraRaw);
  const eraLit = eraId === null ? 'NULL' : String(eraId);
  const agentSymbol = scalar(`SELECT agent_symbol FROM players WHERE id=${PLAYER_ID}`);

  // (1) JUMP_GATE waypoint — fetch the real site waypoint from the twin (faithful type/x/y/traits) and
  // UPSERT it, so `readBootstrapGateSnapshot`'s ListBySystem discovers it by type=='JUMP_GATE'. Idempotent
  // with (0)'s insert when the site is a marketplace; ensures the row exists even when it is not.
  const gateSite = fixture.gateSite ?? DEFAULT_GATE_SITE;
  const gateWpBody = await getJson(`${API_BASE_URL}/systems/${SYSTEM_SYMBOL}/waypoints/${gateSite}`);
  const gwp = gateWpBody.data ?? {};
  const gwpType: string = gwp.type ?? 'JUMP_GATE';
  const gwpTraits: string[] = (gwp.traits ?? []).map((t: any) => t?.symbol).filter(Boolean);
  const gateWpInsert =
    `INSERT INTO waypoints (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, era_id) VALUES ` +
    `(${q(gateSite)}, ${q(SYSTEM_SYMBOL)}, ${q(gwpType)}, ${gwp.x ?? 0}, ${gwp.y ?? 0}, ` +
    `${q(JSON.stringify(gwpTraits))}, 1, ${eraLit}) ` +
    `ON CONFLICT (waypoint_symbol) DO UPDATE SET type=EXCLUDED.type, x=EXCLUDED.x, y=EXCLUDED.y, ` +
    `traits=EXCLUDED.traits, era_id=EXCLUDED.era_id;`;

  // (2) Income ledger — realized CREDIT so readIncomePerHour clears income_bar. Split into one row per
  // hauler-earning, stamped 5 min ago so each sits firmly inside the trailing 1h window and clearly in the
  // past. balance_before/after are kept consistent though the read path (ReconstructTransaction) skips the
  // balance invariant.
  const incomePerHour = fixture.incomePerHour ?? DEFAULT_INCOME_PER_HOUR;
  const nHaulers = fixture.haulers ?? DEFAULT_HAULERS;
  const earners = Math.max(nHaulers, 1);
  const perRow = Math.ceil(incomePerHour / earners);
  const txRows: string[] = [];
  let bal = 0;
  for (let i = 0; i < earners; i++) {
    const before = bal;
    const after = bal + perRow;
    bal = after;
    txRows.push(
      `(${q(randomUUID())}, ${PLAYER_ID}, now() - interval '5 minutes', ${q(INCOME_TX_TYPE)}, ` +
        `${q(INCOME_TX_CATEGORY)}, ${perRow}, ${before}, ${after}, ` +
        `'gate-entry seed: realized contract-fleet income', now() - interval '5 minutes')`,
    );
  }
  const txInsert =
    `INSERT INTO transactions (id, player_id, timestamp, transaction_type, category, amount, ` +
    `balance_before, balance_after, description, created_at) VALUES\n` +
    txRows.join(',\n') +
    `;`;

  // (3) D1 hauler dedication — N contract-tagged SHIP_LIGHT_HAULER rows so obs.Haulers is non-empty.
  // Under Option B these STAY contract earners the whole GATE run: the coordinator no longer repurposes
  // them — it BUYS the gate-delivery fleet from their income — so the contract fleet is left intact and
  // earning. They carry NO container assignment, so each is IsIdle()==true and therefore DOUBLES as an
  // idle purchaser (obs.HasIdlePurchaser, bootstrap_ports.go:144-145): the coordinator flies one to the
  // yard to execute each all-bought buy and it returns still contract-tagged (only the newly bought hulls
  // are manufacturing-tagged gate workers). N>=1 thus guarantees a free hull for the larger all-bought
  // count. Realistic fuel/cargo/frame/role so modelToDomain reconstructs each cleanly and classifies it as
  // a non-scout hauler. Parked at a real home-system marketplace (fallback-safe if absent).
  const homeMarket =
    scalar(
      `SELECT waypoint_symbol FROM waypoints WHERE system_symbol=${q(SYSTEM_SYMBOL)} ` +
        `AND traits::text LIKE '%MARKETPLACE%' ORDER BY waypoint_symbol LIMIT 1`,
    ) || gateSite;
  const haulers: string[] = [];
  const shipRows: string[] = [];
  for (let i = 1; i <= nHaulers; i++) {
    const sym = `${agentSymbol}-H${i}`;
    haulers.push(sym);
    shipRows.push(
      `(${q(sym)}, ${PLAYER_ID}, 'IN_ORBIT', 'CRUISE', ${q(homeMarket)}, ${q(SYSTEM_SYMBOL)}, ` +
        `400, 400, 40, 0, 30, ${q(HAULER_SHIP_FRAME)}, ${q(HAULER_ROLE)}, ${q(CONTRACT_FLEET_TAG)})`,
    );
  }
  const shipInsert =
    nHaulers > 0
      ? `INSERT INTO ships (ship_symbol, player_id, nav_status, flight_mode, location_symbol, ` +
        `system_symbol, fuel_current, fuel_capacity, cargo_capacity, cargo_units, engine_speed, ` +
        `frame_symbol, role, dedicated_fleet) VALUES\n` +
        shipRows.join(',\n') +
        `\nON CONFLICT (ship_symbol, player_id) DO UPDATE SET dedicated_fleet=EXCLUDED.dedicated_fleet, ` +
        `role=EXCLUDED.role, frame_symbol=EXCLUDED.frame_symbol, nav_status=EXCLUDED.nav_status, ` +
        `system_symbol=EXCLUDED.system_symbol, location_symbol=EXCLUDED.location_symbol;`
      : '';

  const sql = ['BEGIN;', gateWpInsert, txInsert, shipInsert, 'COMMIT;'].filter(Boolean).join('\n');
  const res = psql(['-v', 'ON_ERROR_STOP=1', '-f', '-'], sql);
  if (res.status !== 0) throw new Error(`seedDaemonGateEntry failed: ${res.stderr}`);

  return {
    coverage,
    gateSite,
    gateSiteType: gwpType,
    incomePerHour,
    incomeRows: txRows.length,
    haulers,
  };
}

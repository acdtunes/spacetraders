import { spawnSync } from 'node:child_process';
import { TEST_DATABASE_URL, API_BASE_URL } from './config';

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

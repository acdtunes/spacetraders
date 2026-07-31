#!/usr/bin/env bash
#
# capture-x1pz28.sh — READ-ONLY capture of the X1-PZ28 home-system fixture into
# twin/fixtures/era2-X1-PZ28/. Refuses any DSN that is not the local read-only prod
# capture source (host localhost/127.0.0.1, port 5432, db exactly 'spacetraders'), and
# forces default_transaction_read_only=on on every psql. Topology era is resolved
# dynamically; markets/shipyards are synthesized from a real reference catalog.
#
# Usage: twin/scripts/capture-x1pz28.sh [--dry-run]
# Env (empty ⇒ default): CAPTURE_DSN, FIXTURE_DIR, CAPTURE_SYSTEM (X1-PZ28), EXPECTED_WAYPOINTS (90)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TWIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$TWIN_DIR/.." && pwd)"

CAPTURE_DSN="${CAPTURE_DSN:-postgresql://spacetraders:dev_password@localhost:5432/spacetraders}"
FIXTURE_DIR="${FIXTURE_DIR:-$TWIN_DIR/fixtures/era2-X1-PZ28}"
CAPTURE_SYSTEM="${CAPTURE_SYSTEM:-X1-PZ28}"
EXPECTED_WAYPOINTS="${EXPECTED_WAYPOINTS:-90}"

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then DRY_RUN=1; fi
fail() { echo "REFUSING TO CAPTURE: $*" >&2; exit 1; }

rest="${CAPTURE_DSN#*://}"; rest="${rest#*@}"
hostport="${rest%%/*}"; after="${rest#*/}"; db="${after%%\?*}"
host="${hostport%%:*}"; port="${hostport##*:}"
[ "$host" = "$port" ] && port=5432

HINT="Expected the READ-ONLY prod capture source: localhost:5432/spacetraders (prod data is never written; the test DB is 5433/spacetraders_test)."
case "$host" in localhost|127.0.0.1) : ;; *) fail "DSN host '$host' is not local. $HINT" ;; esac
[ "$port" = "5432" ] || fail "DSN port '$port' is not 5432. $HINT"
[ "$db" = "spacetraders" ] || fail "DSN database '$db' is not 'spacetraders' (refusing '$db'). $HINT"

echo "capture system: $CAPTURE_SYSTEM"
echo "capture dsn:    postgresql://…@$host:$port/$db (READ-ONLY)"
echo "fixture dir:    $FIXTURE_DIR"
if [ "$DRY_RUN" = "1" ]; then echo "dry-run: guards passed; nothing captured."; exit 0; fi

command -v psql >/dev/null 2>&1 || fail "psql not found on PATH."
command -v jq   >/dev/null 2>&1 || fail "jq not found on PATH."
command -v node >/dev/null 2>&1 || fail "node not found on PATH."
export PGOPTIONS='-c default_transaction_read_only=on'
mkdir -p "$FIXTURE_DIR"

COUNT="$(psql "$CAPTURE_DSN" -tA -v ON_ERROR_STOP=1 -v sys="$CAPTURE_SYSTEM" <<'SQL'
WITH era AS (SELECT era_id FROM waypoints WHERE system_symbol = :'sys' GROUP BY era_id ORDER BY count(*) DESC LIMIT 1)
SELECT count(*) FROM waypoints w CROSS JOIN era WHERE w.system_symbol = :'sys' AND w.era_id = era.era_id;
SQL
)"
[ "$COUNT" = "$EXPECTED_WAYPOINTS" ] || fail "expected $EXPECTED_WAYPOINTS $CAPTURE_SYSTEM waypoints, got '$COUNT'."

TOPO_ERA="$(psql "$CAPTURE_DSN" -tA -v ON_ERROR_STOP=1 -v sys="$CAPTURE_SYSTEM" <<'SQL'
SELECT era_id FROM waypoints WHERE system_symbol = :'sys' GROUP BY era_id ORDER BY count(*) DESC LIMIT 1;
SQL
)"

psql "$CAPTURE_DSN" -tA -v ON_ERROR_STOP=1 -v sys="$CAPTURE_SYSTEM" <<'SQL' | jq '.' > "$FIXTURE_DIR/waypoints.json"
WITH era AS (SELECT era_id FROM waypoints WHERE system_symbol = :'sys' GROUP BY era_id ORDER BY count(*) DESC LIMIT 1)
SELECT json_agg(o ORDER BY sym) FROM (
  SELECT w.waypoint_symbol AS sym, json_build_object(
    'symbol', w.waypoint_symbol, 'type', w.type, 'systemSymbol', w.system_symbol,
    'x', w.x::float8, 'y', w.y::float8,
    'traits', (SELECT COALESCE(json_agg(json_build_object('symbol', t, 'name', initcap(replace(lower(t), '_', ' ')), 'description', '') ORDER BY ord), '[]'::json)
               FROM json_array_elements_text(w.traits::json) WITH ORDINALITY AS tt(t, ord)),
    'orbitals', (SELECT COALESCE(json_agg(json_build_object('symbol', ob) ORDER BY ord), '[]'::json)
                 FROM json_array_elements_text(NULLIF(w.orbitals, '')::json) WITH ORDINALITY AS oo(ob, ord)),
    'isUnderConstruction', false
  ) AS o
  FROM waypoints w CROSS JOIN era WHERE w.system_symbol = :'sys' AND w.era_id = era.era_id
) t;
SQL

RESET_DATE="$(psql "$CAPTURE_DSN" -tA -v ON_ERROR_STOP=1 -c "SELECT to_char(universe_reset_date, 'YYYY-MM-DD') FROM eras WHERE closed_at IS NULL ORDER BY era_id DESC LIMIT 1;")"
[ -n "$RESET_DATE" ] || RESET_DATE="$(date -u +%Y-%m-%d)"
CAPTURED_AT="$(date -u +%Y-%m-%dT%H:%M:%S.000Z)"

FIXTURE_DIR="$FIXTURE_DIR" CAPTURE_SYSTEM="$CAPTURE_SYSTEM" RESET_DATE="$RESET_DATE" \
CAPTURED_AT="$CAPTURED_AT" TOPO_ERA="$TOPO_ERA" node - <<'NODE'
const fs = require('fs'); const path = require('path');
const FIXTURE_DIR = process.env.FIXTURE_DIR, SYSTEM = process.env.CAPTURE_SYSTEM;
const RESET_DATE = process.env.RESET_DATE, CAPTURED_AT = process.env.CAPTURED_AT, TOPO_ERA = process.env.TOPO_ERA;
if (!FIXTURE_DIR || !SYSTEM || !RESET_DATE) throw new Error('capture: FIXTURE_DIR/CAPTURE_SYSTEM/RESET_DATE required');
const waypoints = JSON.parse(fs.readFileSync(path.join(FIXTURE_DIR, 'waypoints.json'), 'utf8'));
const bySymbol = new Map(waypoints.map((w) => [w.symbol, w]));
const hasTrait = (w, s) => (w.traits || []).some((t) => t.symbol === s);
const GOODS = {
  FUEL: { sellPrice: 66, purchasePrice: 72, supply: 'MODERATE', activity: 'WEAK', tradeVolume: 100 },
  IRON_ORE: { sellPrice: 40, purchasePrice: 46, supply: 'MODERATE', activity: 'WEAK', tradeVolume: 60 },
  IRON: { sellPrice: 120, purchasePrice: 130, supply: 'MODERATE', activity: 'WEAK', tradeVolume: 60 },
  MACHINERY: { sellPrice: 240, purchasePrice: 260, supply: 'MODERATE', activity: 'WEAK', tradeVolume: 40 },
  SILICON_CRYSTALS: { sellPrice: 90, purchasePrice: 100, supply: 'MODERATE', activity: 'WEAK', tradeVolume: 50 },
  MICROPROCESSORS: { sellPrice: 800, purchasePrice: 860, supply: 'LIMITED', activity: 'GROWING', tradeVolume: 20 },
};
function marketFor(w) {
  const exchange = ['FUEL']; const exports = []; const imports = [];
  if (hasTrait(w, 'INDUSTRIAL')) { exports.push('IRON', 'MACHINERY'); imports.push('IRON_ORE'); }
  if (hasTrait(w, 'HIGH_TECH')) { exports.push('MICROPROCESSORS'); imports.push('SILICON_CRYSTALS'); }
  const all = [...exchange, ...exports, ...imports];
  if (new Set(all).size !== all.length) throw new Error('market good in >1 array at ' + w.symbol);
  const tradeGoods = all.map((sym) => { const g = GOODS[sym]; if (!g) throw new Error('no price for ' + sym);
    return { symbol: sym, supply: g.supply, activity: g.activity, sellPrice: g.sellPrice, purchasePrice: g.purchasePrice, tradeVolume: g.tradeVolume }; });
  return { symbol: w.symbol, exports: exports.map((s) => ({ symbol: s })), imports: imports.map((s) => ({ symbol: s })), exchange: exchange.map((s) => ({ symbol: s })), tradeGoods };
}
const REQ0 = { power: 0, crew: 0, slots: 0 };
const PROBE_LISTING = { type: 'SHIP_PROBE', name: 'Probe', description: 'A small, unmanned exploration craft.', purchasePrice: 24680,
  frame: { symbol: 'FRAME_PROBE', name: 'Frame Probe', moduleSlots: 0, mountingPoints: 0, fuelCapacity: 0, requirements: REQ0 },
  reactor: { symbol: 'REACTOR_SOLAR_I', name: 'Solar Reactor I', powerOutput: 3, requirements: REQ0 },
  engine: { symbol: 'ENGINE_IMPULSE_DRIVE', name: 'Impulse Drive', speed: 3, requirements: { power: 1, crew: 0, slots: 0 } }, modules: [], mounts: [] };
const FRIGATE_LISTING = { type: 'SHIP_COMMAND_FRIGATE', name: 'Command Frigate', description: 'A versatile starting frigate.', purchasePrice: 150000,
  frame: { symbol: 'FRAME_FRIGATE', name: 'Frigate', moduleSlots: 8, mountingPoints: 5, fuelCapacity: 400, requirements: { power: 8, crew: 25, slots: 0 } },
  reactor: { symbol: 'REACTOR_FISSION_I', name: 'Fission Reactor I', powerOutput: 31, requirements: { power: 0, crew: 8, slots: 1 } },
  engine: { symbol: 'ENGINE_ION_DRIVE_I', name: 'Ion Drive I', speed: 30, requirements: { power: 6, crew: 0, slots: 0 } }, modules: [], mounts: [] };
function shipyardFor(w) { return { symbol: w.symbol, shipTypes: [{ type: 'SHIP_PROBE' }, { type: 'SHIP_COMMAND_FRIGATE' }], ships: [PROBE_LISTING, FRIGATE_LISTING], transactions: [], modificationsFee: 5000 }; }
const HQ = SYSTEM + '-A1';
if (!bySymbol.has(HQ)) throw new Error('HQ waypoint ' + HQ + ' not present in captured topology');
const dockedNav = () => ({ systemSymbol: SYSTEM, waypointSymbol: HQ, status: 'DOCKED', flightMode: 'CRUISE', route: null });
const frigate = { symbol: '{AGENT}-1', registration: { role: 'COMMAND' }, nav: dockedNav(), fuel: { current: 400, capacity: 400 },
  cargo: { capacity: 40, units: 0, inventory: [] }, cooldown: null, engine: { speed: 30 },
  frame: { symbol: 'FRAME_FRIGATE', moduleSlots: 8, mountingPoints: 5 },
  reactor: { symbol: 'REACTOR_FISSION_I', name: 'Fission Reactor I', powerOutput: 31, requirements: { power: 0, crew: 8, slots: 1 } },
  crew: { current: 57, required: 57, capacity: 80 },
  modules: [ { symbol: 'MODULE_CARGO_HOLD_II', capacity: 40, range: 0, requirements: { power: 2, crew: 2, slots: 2 } },
             { symbol: 'MODULE_CREW_QUARTERS_I', capacity: 40, range: 0, requirements: { power: 1, crew: 2, slots: 1 } } ],
  mounts: [ { symbol: 'MOUNT_SENSOR_ARRAY_II', name: 'Sensor Array II', strength: 4, deposits: [], requirements: { power: 2, crew: 2, slots: 1 } },
            { symbol: 'MOUNT_MINING_LASER_I', name: 'Mining Laser I', strength: 10, deposits: [], requirements: { power: 1, crew: 1, slots: 1 } } ] };
const probe = { symbol: '{AGENT}-2', registration: { role: 'SATELLITE' }, nav: dockedNav(), fuel: { current: 0, capacity: 0 },
  cargo: { capacity: 0, units: 0, inventory: [] }, cooldown: null, engine: { speed: 3 },
  frame: { symbol: 'FRAME_PROBE', moduleSlots: 0, mountingPoints: 0 },
  reactor: { symbol: 'REACTOR_SOLAR_I', name: 'Solar Reactor I', powerOutput: 3, requirements: REQ0 },
  crew: { current: 0, required: 0, capacity: 0 }, modules: [], mounts: [] };
const register = { startingCredits: 175000, headquarters: HQ, startingFaction: 'COSMIC', ships: [frigate, probe] };
const [ry, rm, rd] = RESET_DATE.split('-').map(Number);
const nextReset = new Date(Date.UTC(ry, rm, rd, 0, 0, 0));
const serverStatus = { resetDate: RESET_DATE, serverResets: { next: nextReset.toISOString(), frequency: 'monthly' } };
const markets = waypoints.filter((w) => hasTrait(w, 'MARKETPLACE')).map(marketFor);
const shipyards = waypoints.filter((w) => hasTrait(w, 'SHIPYARD')).map(shipyardFor);
for (const s of shipyards) { const p = s.ships.find((x) => x.type === 'SHIP_PROBE');
  if (!p || typeof p.engine.speed !== 'number') throw new Error('shipyard ' + s.symbol + ' missing SHIP_PROBE engine.speed'); }
const meta = { system: SYSTEM, eraId: TOPO_ERA ? Number(TOPO_ERA) : null, capturedAt: CAPTURED_AT,
  sources: { topology: 'prod waypoints table (read-only, localhost:5432/spacetraders); era_id holding the full ' + SYSTEM + ' topology',
    markets: 'synthesized from real SpaceTraders reference catalog keyed to captured MARKETPLACE topology (prod market_data empty for ' + SYSTEM + ')',
    shipyards: 'synthesized from real SHIP_PROBE + SHIP_COMMAND_FRIGATE reference listings keyed to captured SHIPYARD topology',
    register: 'documented /register cold-start defaults (175000 cr, COSMIC, frigate + probe docked at HQ)',
    serverStatus: 'OPEN era universe_reset_date from prod eras table' },
  notes: 'Fixture dir named era2-* (OPEN era-2 home system); topology rows scanned under an earlier era in prod (the only ' + SYSTEM + ' topology present). Markets/shipyards synthesized, shape-faithful to the Go decode targets.',
  counts: { waypoints: waypoints.length, markets: markets.length, shipyards: shipyards.length } };
const write = (name, obj) => fs.writeFileSync(path.join(FIXTURE_DIR, name), JSON.stringify(obj, null, 2) + '\n');
write('markets.json', markets); write('shipyards.json', shipyards); write('register.json', register);
write('server-status.json', serverStatus); write('meta.json', meta);
console.log('synthesized: markets=' + markets.length + ' shipyards=' + shipyards.length);
NODE

echo ""; echo "captured $CAPTURE_SYSTEM → $FIXTURE_DIR"
for f in waypoints markets shipyards register server-status meta; do
  printf '  %-18s %s bytes\n' "$f.json" "$(wc -c < "$FIXTURE_DIR/$f.json" | tr -d ' ')"
done
echo "waypoints: $COUNT (topology era $TOPO_ERA)   resetDate: $RESET_DATE"

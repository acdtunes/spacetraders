# SpaceTraders Digital Twin — Implementation Plan

**For agentic workers:** Execute tasks in the given order 1 → N. Each task is red → green → commit (TDD). Prefix every shell command with `rtk` (repo convention). Absolute repo root: `/Users/andres.dandrea/IdeaProjects/cities/spacetraders` (referred to as `<repo>`). **REQUIRED SUB-SKILL:** `superpowers:subagent-driven-development` (or `superpowers:executing-plans` if running solo) — read it before starting, and run every task through its red→green→review→commit loop.

**Goal:** Build a fast, disposable, deterministic Node + TypeScript + Fastify reimplementation of the ~14 SpaceTraders v2 API endpoints the captain-bootstrap DATA phase exercises, plus a one-line Go seam so the hardened `spacetraders` CLI/daemon can be pointed at it and driven end-to-end with travel compressed 100×.

**Architecture:** A single Fastify server (`twin/`) holds the X1-PZ28 home system in memory (captured verbatim from the prod `waypoints` table + a synthesized market/shipyard catalog), serves the `/v2` API surface plus a `/_twin` admin namespace, and computes ship arrival lazily on read from compressed timestamps. A ~4-line `os.Getenv("ST_API_BASE_URL")` fallback in the Go client's zero-arg constructor redirects the whole bot; a checked-in `twin/test-config.yaml` moves the test daemon's pidfile/socket/gRPC/metrics/DB onto isolated slots so a test run can never touch production. Every endpoint is proven test-first through the real CLI (contract) plus `GET /_twin/state` (behavior).

**Tech Stack:** Node ≥ 22, TypeScript ^5.7.2 (ESM, `"type": "module"`), Fastify ^5.2.0, Vitest ^3.2.4 (`rtk vitest run`); Go 1.24 for the bot-side seam + config guards; bash dev scripts; Docker Compose for the parallel observability stack.

---

## Global Constraints

Copied verbatim from the foundation (`00-foundation.md` §6). Every task holds these.

| Constant | Value |
|---|---|
| Twin listen address | `127.0.0.1:8080` |
| Twin API base URL (bot seam value) | `http://127.0.0.1:8080/v2` |
| Twin admin namespace | `/_twin` (`POST /_twin/reset`, `GET /_twin/state`, `POST /_twin/time-compression`) |
| Compression default | `100` (env `TWIN_TIME_COMPRESSION`, runtime `POST /_twin/time-compression`) |
| CLI binary | `gobot/bin/spacetraders` (`make -C gobot build-cli`) |
| Daemon binary | `gobot/bin/spacetraders-daemon` (`make -C gobot build-daemon`) |
| Test daemon pid_file (**--force trap**) | `/tmp/spacetraders-daemon-test.pid` |
| Test daemon socket_path | `/tmp/spacetraders-daemon-test.sock` |
| Test daemon gRPC address | `localhost:50062` (prod: 50052) |
| Test daemon metrics.port | `9092` (prod scrape: 9090) |
| Test Prometheus host port | `9093` |
| Test Grafana host port | `3001` |
| Test Postgres host port / DB | `5433` / `spacetraders_test` (user `spacetraders`, password `dev_password`) |
| Test DATABASE_URL | `postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable` |
| Prod DB (READ-ONLY, capture only) | `postgresql://spacetraders:dev_password@localhost:5432/spacetraders` |
| Home system fixture | `X1-PZ28` (90 waypoints, era 2) in `twin/fixtures/era2-X1-PZ28/` |
| Test agent | symbol `TWINAGENT`, faction `COSMIC`, `ST_ACCOUNT_TOKEN=twin-test-account-token` |
| Error codes | 4214 must-be-docked · 4244 not-docked · 4511 agent-has-contract · generic 400/404/401 |
| `resetDate` format | `YYYY-MM-DD` (Go layout `2006-01-02`) |
| Timestamps | strict RFC3339 (`Date#toISOString()`); Go parses with `time.RFC3339` |
| Node | `>=22` (`"engines": { "node": ">=22" }`; dev machine has v24.12.0) |
| TypeScript | `^5.7.2` (matches visualizer) |
| Fastify | `^5.2.0` |
| Vitest | `^3.2.4` (matches visualizer); run via `rtk vitest run` |
| Ship pagination | `GET /my/ships` limit 20/page; terminate on empty `data` page (HTTP 200) |
| Travel formula | `max(1, floor(dist × M / max(1, speed)))` s; M: CRUISE 31 · DRIFT 26 · BURN 15 · STEALTH 50 (routing_engine.py:24-44) |
| Fuel formula | `dist === 0 ? 0 : max(1, ceil(dist × rate))`; rate: CRUISE 1.0 · DRIFT 0.003 · BURN 2.0 · STEALTH 1.0 |
| Refuel price | captured FUEL `purchasePrice` at that waypoint's market ÷ per-unit (fallback 72 cr/unit if market lacks FUEL) |

**Invariants every task must hold:**
1. All `/v2` payloads wrapped `{ data: ... }` except `GET /v2/` (unwrapped).
2. Market good classification is by array placement (exports/imports/exchange) — never a `type` field.
3. `nav` in ANY response passes through `resolveNav` (single flip point at stored arrival).
4. `departureTime <= arrival`, both RFC3339.
5. Errors always use `{ error: { message, code, data? } }`.
6. Never touch prod: pid/socket/metrics/gRPC/DB values above are mandatory in `test-config.yaml`; prod DB is read-only capture input.

---

## Canonical reconciliation decisions

The 14 source sections diverged on names, file paths, and server wiring. These are the single authoritative choices this plan enforces; where a source section differed, its code below is already adjusted to match.

1. **World module = `twin/src/world/loader.ts`** (foundation §4 + skeleton naming). It exports EVERYTHING that builds/rebuilds the world: `FIXTURES_DIR`, `interface RegisterTemplate`, `loadRegisterTemplate()`, `loadColdStartWorld()`, `mintToken()`, `registerAgent()`. The `20b` name `load.ts`/`loadWorld()` and the `30b` split into `cold-start.ts`/`register-template.ts` are DROPPED — all folded into `loader.ts`.
2. **`loadColdStartWorld(): World`** returns the PRE-register world: `agent=null`, `agentToken=null`, `ships=∅`, `transits=∅`, `systems`/`markets`/`shipyards`/`serverStatus` from the capture, **`shipCounter=0`**.
3. **Register/reset mechanism:** `mintToken(symbol): string` (deterministic JWT-shaped) and `registerAgent(world, { symbol, faction, token }): { agent, ships }` (materializes agent + starting ships from `register.json` into `world`, sets `shipCounter = ships.length + 1`). The `POST /v2/register` route mints the token then calls `registerAgent`; `resetWorld()` re-calls `registerAgent` with the PRESERVED token. `30b`'s `applyRegister` (which minted internally and returned `{token,…}`) is replaced by this `mintToken` + `registerAgent` pair.
4. **Server = store-singleton.** `twin/src/world/store.ts` owns `getWorld()`, `setWorld(world)`, `resetWorld()`. **Every route handler calls `getWorld()` at handler entry** (never closes over a `world` argument). `resetWorld()` replaces the singleton — safe because no route captures a reference. The `30b`/`40b`/`50a`/`50b` "pass `world` to the registrar" pattern is DROPPED.
5. **Route modules are Fastify plugins** `export async function xxxRoutes(app: FastifyInstance): Promise<void>` (or `const xxxRoutes: FastifyPluginAsync`) declaring paths **relative to `/v2`**. `buildServer()` registers them all inside one `app.register(async (v2) => { await …Routes(v2) }, { prefix: '/v2' })` scope; `/_twin` plugins are siblings. Full `/v2/...`/`/_twin/...` path strings in source sections are rewritten to relative. Canonical export names: `serverStatusRoutes`, `registerRoutes`, `agentRoutes`, `shipRoutes`, `waypointRoutes`, `marketRoutes`, `shipyardRoutes`, `shipNavigateRoutes`, `shipActionRoutes`, `myShipsPurchaseRoutes`, `adminRoutes`, `testAdminRoutes`.
6. **`GET /v2/` is implemented once**, by `serverStatusRoutes` in the skeleton (Task 15). `30c`'s duplicate `status.ts`/`statusRoute` is DROPPED; its `universe status` CLI acceptance survives as a test-only task (Task 19).
7. **Capture script = `twin/scripts/capture-x1pz28.sh`** (the one real implementation, Task 5). References to `capture-topology.ts`/`capture-markets.sh` in the README section are rewritten to it.
8. **Fixture golden values are read from the committed fixture at test time, never hardcoded.** The captured `resetDate` is `2026-07-05` (from prod), NOT the foundation's illustrative `2026-06-29`. Hermetic unit tests that build their OWN synthetic worlds may use any literal (they touch no fixture); every fixture-reading acceptance test reads `server-status.json`/`register.json`/etc. for its golden.
9. **`twin/vitest.config.ts`** owns the live-stack `globalSetup`; **`twin/vitest.unit.config.ts`** (no globalSetup) runs pure/in-process tests under `tests/unit/**`, `tests/world/**`, `tests/skeleton/**`. Skeleton/loader/clock/purchase unit tests run under the unit config; CLI-driven acceptance tests run under the default config.

---

## Execution waves (parallelization)

Ultracode waves — each wave's tasks may run in parallel; a wave starts only when its stated dependencies are green.

- **Wave 0 — independent foundations (no dependency on twin server code).** Tasks 1–8: the Go seam + `test-config.yaml` guard (Go-only), the twin package scaffold + `types.ts` + `run-cli.ts` helper, the X1-PZ28 capture + fixtures, and the launch/seed scripts. The two script tasks (7, 8) and the capture (6) need only the scaffold (3) for their vitest guard tests; the Go tasks (1, 2) need nothing.
- **Wave 1 — world-loader + twin skeleton.** Tasks 9–16. Depends on Wave 0 (types.ts, errors need Fastify from the scaffold; loader needs the captured fixtures). Internal order: loader (9) → mintToken/registerAgent (10, 11) → clock (12) + errors (13) → store (14, needs loader) → `buildServer()` skeleton + `GET /v2/` (15) → `/_twin` admin (16, needs clock+store+errors) → globalSetup/daemon helpers (17, needs buildServer). **Clock lands in Wave 1** (not Wave 2) because the skeleton's `GET /_twin/state` consumes `resolveNav`/`getCompression`.
- **Wave 2 — identity + read endpoints (parallel once the skeleton exists).** Tasks 18–26: `POST /register`, `GET /my/agent` (+auth), the `universe status` acceptance, `GET /my/ships[/{s}]`, `GET …/waypoints[/{w}]`, `…/market`, `…/shipyard`. Each is an independent route module that only calls `getWorld()` + adds one line to `buildServer()`; all depend on the Wave-1 skeleton + loader + clock, nothing on each other.
- **Wave 3 — mutations (need clock + skeleton + the read endpoints).** Tasks 27–32: `POST …/navigate`, `POST …/orbit|dock|refuel` (+ test-admin seams), the pure `applyPurchaseShip`, and `POST /my/ships` (buy). These mint transits / mutate the world and their CLI acceptances drive the daemon through sibling read/move endpoints, so they gate after Wave 2.
- **Wave 4 — end-to-end + observability (need all endpoints).** Tasks 33–38: the bootstrap-tuned daemon config + Go guard, the Slice-1 DATA `workflow bootstrap` acceptance, and the parallel Prometheus/Grafana/Postgres compose stack + configs + README. The E2E drives every endpoint; observability is documentation/infra sequenced last.

---

## Task 1 — Go seam: `ST_API_BASE_URL` in `NewSpaceTradersClient()`

**Files:** Modify `gobot/internal/adapters/api/client.go` (import block + zero-arg constructor). Test `gobot/internal/adapters/api/client_baseurl_seam_test.go` (create).

**Interfaces:** Produces the env contract `ST_API_BASE_URL`: when set+non-empty, `NewSpaceTradersClient()` uses it as the base URL; unset ⇒ byte-identical to today (the `baseURL` const). `NewSpaceTradersClientWithConfig` is unaffected (explicit args always win). Consumed by the `runCli` helper, globalSetup, and Tasks 7/8.

- [ ] **Step 1 — failing test.** Create `gobot/internal/adapters/api/client_baseurl_seam_test.go`:

```go
package api

import "testing"

func TestNewSpaceTradersClientBaseURLSeam(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"env unset falls back to production const", "", "https://api.spacetraders.io/v2"},
		{"env set redirects the client", "http://127.0.0.1:8080/v2", "http://127.0.0.1:8080/v2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ST_API_BASE_URL", tc.env)
			c := NewSpaceTradersClient()
			if c.baseURL != tc.want {
				t.Fatalf("baseURL = %q, want %q", c.baseURL, tc.want)
			}
		})
	}
}

func TestNewSpaceTradersClientWithConfigIgnoresEnvSeam(t *testing.T) {
	t.Setenv("ST_API_BASE_URL", "http://127.0.0.1:9999/v2")
	c := NewSpaceTradersClientWithConfig("http://explicit.test/v2", defaultMaxRetries, defaultBackoffBase, nil)
	if c.baseURL != "http://explicit.test/v2" {
		t.Fatalf("baseURL = %q, want the explicit argument to win over ST_API_BASE_URL", c.baseURL)
	}
}
```

`t.Setenv` forbids `t.Parallel()` — do not add it.

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd gobot && rtk go test ./internal/adapters/api/ -run 'BaseURLSeam|IgnoresEnvSeam' -v
```
Expected: the `env set redirects the client` subtest fails `baseURL = "https://api.spacetraders.io/v2", want "http://127.0.0.1:8080/v2"`.

- [ ] **Step 3 — implement.** In `gobot/internal/adapters/api/client.go`, add `"os"` to the stdlib import group, then replace the zero-arg constructor with:

```go
// NewSpaceTradersClient creates a new SpaceTraders API client with default settings.
// ST_API_BASE_URL, when set and non-empty, overrides the production base URL — the
// digital-twin seam. Production never sets it and keeps hitting the real API.
func NewSpaceTradersClient() *SpaceTradersClient {
	bu := baseURL
	if v := os.Getenv("ST_API_BASE_URL"); v != "" {
		bu = v
	}
	return NewSpaceTradersClientWithConfig(
		bu,
		defaultMaxRetries,
		defaultBackoffBase,
		nil, // Use RealClock by default
	)
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd gobot && rtk go test ./internal/adapters/api/ -run 'BaseURLSeam|IgnoresEnvSeam' -v
cd gobot && rtk go test ./internal/adapters/api/
```
Both PASS (whole-package run proves the httptest-based tests are untouched).

- [ ] **Step 5 — commit.**
```bash
rtk git add gobot/internal/adapters/api/client.go gobot/internal/adapters/api/client_baseurl_seam_test.go
rtk git commit -m "feat(api): ST_API_BASE_URL env seam in NewSpaceTradersClient (digital-twin redirect)"
```

---

## Task 2 — `twin/test-config.yaml` + Go loader guard

**Files:** Create `twin/test-config.yaml`. Test `gobot/internal/infrastructure/config/twin_test_config_test.go` (create).

**Interfaces:** Produces the canonical harness config path pinning every production-isolation value: `daemon.pid_file=/tmp/spacetraders-daemon-test.pid`, `daemon.socket_path=/tmp/spacetraders-daemon-test.sock`, `daemon.address=localhost:50062`, `metrics.port=9092`, `database.url=<test DSN>`, `captain.player_id=1`, `api.rate_limit={requests:10,burst:30}`. Consumed by `run-cli.ts` (`TEST_CONFIG`), globalSetup, and Tasks 7/8.

- [ ] **Step 1 — failing test.** Create `gobot/internal/infrastructure/config/twin_test_config_test.go`:

```go
package config

import (
	"path/filepath"
	"testing"
)

// TestTwinTestConfigIsolatesFromProduction loads the checked-in digital-twin harness
// config through the REAL loader and pins every value that keeps a test daemon out of
// production's blast radius (the --force PID trap: --force SIGTERM-kills whatever PID is
// in cfg.Daemon.PIDFile; the compiled-in default is production's pidfile).
func TestTwinTestConfigIsolatesFromProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ST_METRICS_PORT", "")

	path := filepath.Join("..", "..", "..", "..", "twin", "test-config.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) failed: %v", path, err)
	}

	checks := []struct{ name, got, want string }{
		{"daemon.pid_file", cfg.Daemon.PIDFile, "/tmp/spacetraders-daemon-test.pid"},
		{"daemon.socket_path", cfg.Daemon.SocketPath, "/tmp/spacetraders-daemon-test.sock"},
		{"daemon.address", cfg.Daemon.Address, "localhost:50062"},
		{"database.url", cfg.Database.URL, "postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q — a test daemon booted with this config collides with production", c.name, c.got, c.want)
		}
	}
	if cfg.Metrics.Port != 9092 {
		t.Errorf("metrics.port = %d, want 9092 — production's daemon serves 9090", cfg.Metrics.Port)
	}
	if cfg.Captain.PlayerID != 1 {
		t.Errorf("captain.player_id = %d, want 1 — the seeded TWINAGENT row in a fresh spacetraders_test DB", cfg.Captain.PlayerID)
	}
}
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd gobot && rtk go test ./internal/infrastructure/config/ -run TestTwinTestConfigIsolatesFromProduction -v
```
Expected: `LoadConfig(../../../../twin/test-config.yaml) failed: … no such file or directory`.

- [ ] **Step 3 — implement.** Create `twin/test-config.yaml`:

```yaml
# twin/test-config.yaml — config for the ISOLATED digital-twin test daemon.
#
# ⚠ THE --force PID TRAP — READ BEFORE EDITING ⚠
# The production daemon runs `spacetraders-daemon --force`; --force SIGTERM-kills whatever
# PID is recorded in the loaded config's daemon.pid_file. The compiled-in default is
# /tmp/spacetraders-daemon.pid — PRODUCTION's pidfile. Every override below is a
# production-isolation boundary, guarded by twin_test_config_test.go and launch-test-stack.sh.

database:
  type: postgres
  # Test Postgres on 5433 — NEVER 5432 (prod, READ-ONLY for the twin project).
  url: postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable

api:
  # Display/default only — the real redirect is the ST_API_BASE_URL env seam.
  base_url: http://127.0.0.1:8080/v2
  rate_limit:
    requests: 10 # twin is local; loosen the 2/s live-API ceiling for fast tests
    burst: 30

daemon:
  pid_file: /tmp/spacetraders-daemon-test.pid # NOT /tmp/spacetraders-daemon.pid (prod)
  socket_path: /tmp/spacetraders-daemon-test.sock # NOT /tmp/spacetraders-daemon.sock (prod)
  address: localhost:50062 # prod gRPC is localhost:50052

metrics:
  enabled: true
  port: 9092 # prod daemon serves 9090 (Prometheus scrapes it)

captain:
  player_id: 1 # the seeded TWINAGENT row — first players row in a fresh spacetraders_test DB

logging:
  level: info
  format: text
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd gobot && rtk go test ./internal/infrastructure/config/ -run TestTwinTestConfigIsolatesFromProduction -v
cd gobot && rtk go test ./internal/infrastructure/config/
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/test-config.yaml gobot/internal/infrastructure/config/twin_test_config_test.go
rtk git commit -m "feat(twin): test-config.yaml isolation overrides + Go loader guard (--force PID trap)"
```

---

## Task 3 — Twin package scaffold + world-model types

**Files:** Create `twin/package.json`, `twin/tsconfig.json`, `twin/vitest.config.ts`, `twin/vitest.unit.config.ts`, `twin/src/world/types.ts`. Test `twin/tests/skeleton/types.test.ts` (create).

**Interfaces:** Produces the TS project (ESM, Node ≥ 22, Fastify ^5.2.0, Vitest ^3.2.4) and `twin/src/world/types.ts` — the foundation §1 world-model types every module imports VERBATIM. `package.json` exposes `"start": "tsx src/main.ts"` (consumed by `launch-test-stack.sh` + globalSetup). `vitest.config.ts` carries the live-stack `globalSetup`; `vitest.unit.config.ts` (no globalSetup) scopes `tests/unit|world|skeleton/**`.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/types.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import type { Agent, Ship, Market, Shipyard, World } from '../../src/world/types';

// Compile-only guard: constructs each shape so a field rename in types.ts is a red compile.
describe('world types compile to the Go decode-target shapes', () => {
  it('constructs an Agent / Ship / Market / Shipyard / World', () => {
    const agent: Agent = { accountId: 'a', symbol: 'S', headquarters: 'X1-PZ28-A1', credits: 1, startingFaction: 'COSMIC' };
    const market: Market = { symbol: 'X1-PZ28-A1', exports: [], imports: [], exchange: [], tradeGoods: [] };
    const yard: Shipyard = { symbol: 'X1-PZ28-A2', shipTypes: [], ships: [], transactions: [], modificationsFee: 0 };
    const world: World = {
      serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
      agent, agentToken: null, ships: new Map(), systems: new Map(),
      markets: new Map([[market.symbol, market]]), shipyards: new Map([[yard.symbol, yard]]),
      transits: new Map(), shipCounter: 0,
    };
    const ship: Ship | undefined = world.ships.get('X');
    expect(agent.credits).toBe(1);
    expect(world.markets.size).toBe(1);
    expect(ship).toBeUndefined();
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npm install && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/types.test.ts
```
Expected: `Failed to load url ../../src/world/types` (module missing). If `npm install`/config files are missing the run cannot even start — create them in Step 3 first, then re-run.

- [ ] **Step 3 — implement.** Create `twin/package.json`:

```json
{
  "name": "spacetraders-twin",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "engines": { "node": ">=22" },
  "scripts": {
    "start": "tsx src/main.ts",
    "test": "vitest run"
  },
  "dependencies": {
    "fastify": "^5.2.0"
  },
  "devDependencies": {
    "@types/node": "^22.10.0",
    "tsx": "^4.19.0",
    "typescript": "^5.7.2",
    "vitest": "^3.2.4"
  }
}
```

Create `twin/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ES2022",
    "moduleResolution": "node",
    "esModuleInterop": true,
    "strict": true,
    "skipLibCheck": true,
    "resolveJsonModule": true,
    "types": ["node"],
    "outDir": "dist"
  },
  "include": ["src/**/*.ts", "tests/**/*.ts", "scripts/**/*.ts"]
}
```

Create `twin/vitest.config.ts`:

```ts
import { defineConfig } from 'vitest/config';

// Default config for the twin: CLI-driven acceptance tests run under the live-stack
// globalSetup (boots twin + isolated test daemon + seeds TWINAGENT). Serialized so the
// single shared daemon + shared reset are safe.
export default defineConfig({
  test: {
    include: ['tests/**/*.test.ts'],
    exclude: ['tests/unit/**', 'tests/world/**', 'tests/skeleton/**'],
    environment: 'node',
    globals: false,
    globalSetup: ['tests/global-setup.ts'],
    fileParallelism: false,
    testTimeout: 30_000,
    hookTimeout: 120_000,
  },
});
```

Create `twin/vitest.unit.config.ts`:

```ts
import { defineConfig } from 'vitest/config';

// Live-stack-free config for PURE / in-process (Fastify inject) tests: no globalSetup,
// so they run even before the register route / daemon exist.
export default defineConfig({
  test: {
    include: ['tests/unit/**/*.test.ts', 'tests/world/**/*.test.ts', 'tests/skeleton/**/*.test.ts'],
    environment: 'node',
    globals: false,
    testTimeout: 15_000,
  },
});
```

Create `twin/src/world/types.ts` (foundation §1, verbatim — field names/casing are the Go decode-target contract; do not "improve" them):

```ts
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
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/types.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/package.json twin/package-lock.json twin/tsconfig.json twin/vitest.config.ts twin/vitest.unit.config.ts twin/src/world/types.ts twin/tests/skeleton/types.test.ts
rtk git commit -m "feat(twin): package scaffold (ESM/Fastify/Vitest) + world-model types (Go decode-target shapes)"
```

---

## Task 4 — Harness helper `twin/tests/helpers/run-cli.ts`

**Files:** Create `twin/tests/helpers/run-cli.ts`. Test `twin/tests/skeleton/run-cli.test.ts` (create).

**Interfaces:** Produces `runCli(args, opts)`, `RunCliResult`, and constants `REPO_ROOT`, `GOBOT_DIR`, `CLI_BIN`, `DAEMON_BIN`, `TWIN_BASE_URL`, `TWIN_ADMIN`, `TEST_CONFIG`, `TEST_DATABASE_URL` (foundation §5.1). Consumed by every CLI-driven acceptance test, globalSetup, and `daemon.ts`.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/run-cli.test.ts`:

```ts
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { CLI_BIN, GOBOT_DIR, REPO_ROOT, TWIN_ADMIN, TWIN_BASE_URL, TEST_DATABASE_URL } from '../helpers/run-cli';

describe('run-cli constants', () => {
  it('point at the canonical twin + gobot paths (foundation §5.1/§6)', () => {
    expect(TWIN_BASE_URL).toBe('http://127.0.0.1:8080/v2');
    expect(TWIN_ADMIN).toBe('http://127.0.0.1:8080/_twin');
    expect(TEST_DATABASE_URL).toContain('5433/spacetraders_test');
    expect(GOBOT_DIR).toBe(path.join(REPO_ROOT, 'gobot'));
    expect(CLI_BIN).toBe(path.join(GOBOT_DIR, 'bin', 'spacetraders'));
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/run-cli.test.ts
```
Expected: module `../helpers/run-cli` fails to load.

- [ ] **Step 3 — implement.** Create `twin/tests/helpers/run-cli.ts` (foundation §5.1):

```ts
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HELPERS_DIR = path.dirname(fileURLToPath(import.meta.url)); // twin/tests/helpers
export const REPO_ROOT   = path.resolve(HELPERS_DIR, '..', '..', '..'); // spacetraders/
export const GOBOT_DIR   = path.join(REPO_ROOT, 'gobot');
export const CLI_BIN     = path.join(GOBOT_DIR, 'bin', 'spacetraders');
export const DAEMON_BIN  = path.join(GOBOT_DIR, 'bin', 'spacetraders-daemon');
export const TWIN_BASE_URL = 'http://127.0.0.1:8080/v2';
export const TWIN_ADMIN    = 'http://127.0.0.1:8080/_twin';
export const TEST_CONFIG   = path.join(REPO_ROOT, 'twin', 'test-config.yaml');
export const TEST_DATABASE_URL =
  'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';

export interface RunCliResult { stdout: string; stderr: string; exitCode: number }

export function runCli(args: string[], opts: { env?: Record<string, string>; timeoutMs?: number } = {}): RunCliResult {
  const res = spawnSync(CLI_BIN, args, {
    cwd: GOBOT_DIR,
    encoding: 'utf8',
    timeout: opts.timeoutMs ?? 30_000,
    env: {
      ...process.env,
      SPACETRADERS_CONFIG: TEST_CONFIG,   // explicit config file wins outright (config.go:64)
      ST_API_BASE_URL: TWIN_BASE_URL,     // the client seam → twin
      DATABASE_URL: TEST_DATABASE_URL,    // overrides database.url (config.go:96)
      ST_ACCOUNT_TOKEN: 'twin-test-account-token', // register only; twin accepts any non-empty
      ...opts.env,
    },
  });
  if (res.error) throw res.error;
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status ?? -1 };
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/run-cli.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/tests/helpers/run-cli.ts twin/tests/skeleton/run-cli.test.ts
rtk git commit -m "test(twin): runCli spawn helper + canonical harness path/URL constants"
```

---

## Task 5 — `capture-x1pz28.sh` + X1-PZ28 fixtures + DSN guard

**Files:** Create `twin/scripts/capture-x1pz28.sh`. Create (by running the script) `twin/fixtures/era2-X1-PZ28/{waypoints,markets,shipyards,register,server-status,meta}.json`. Test `twin/tests/harness/capture-x1pz28.test.ts` (create).

**Capture reality (verified vs prod `localhost:5432/spacetraders`):** the 90 `X1-PZ28` waypoint rows live under the `era_id` holding the most rows (currently era 1, NOT the OPEN era 2) — the script resolves it dynamically, never hardcodes `= 2`. 30 `MARKETPLACE`, 3 `SHIPYARD` (`A2`,`C42`,`H64`). `market_data` is empty for `X1-PZ28` and there is no shipyard table, so markets/shipyards are SYNTHESIZED from a real SpaceTraders reference catalog keyed to the captured topology, shape-faithful to the Go decode targets; `meta.json` records this honestly. `resetDate` is captured from the OPEN era's `universe_reset_date` → `2026-07-05` (supersedes the foundation's illustrative `2026-06-29`).

**Interfaces:** Produces the six fixture files loaded verbatim by `loadColdStartWorld()`, and the golden source for every read-endpoint acceptance. Guard: refusals start `REFUSING TO CAPTURE:` on stderr, exit non-zero; live path forces `PGOPTIONS='-c default_transaction_read_only=on'`.

- [ ] **Step 1 — failing guard test.** Create `twin/tests/harness/capture-x1pz28.test.ts`:

```ts
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const TWIN_DIR = path.resolve(__dirname, '..', '..');
const SCRIPT = path.join(TWIN_DIR, 'scripts', 'capture-x1pz28.sh');

function runDry(env: Record<string, string> = {}) {
  const res = spawnSync('bash', [SCRIPT, '--dry-run'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, CAPTURE_DSN: '', FIXTURE_DIR: '', CAPTURE_SYSTEM: '', EXPECTED_WAYPOINTS: '', ...env },
  });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status };
}

describe('capture-x1pz28.sh — read-only prod-capture DSN guards', () => {
  it('dry-run passes on the default read-only prod DSN and echoes the target', () => {
    const { stdout, stderr, exitCode } = runDry();
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain('X1-PZ28');
    expect(stdout).toContain('localhost:5432/spacetraders');
    expect(stdout).toContain('dry-run: guards passed');
  });
  it('REFUSES the test DB (5433/spacetraders_test) — wrong port', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('5432');
  });
  it('REFUSES a writable DB on the right port (5432/spacetraders_test)', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders_test' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('spacetraders_test');
  });
  it('REFUSES a remote host even on 5432/spacetraders', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@db.example.com:5432/spacetraders' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('localhost');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/capture-x1pz28.test.ts
```
(Add `tests/harness/**` is covered by the default config's include, but these guard tests touch no live services; run them under the unit config OR the default config — either works since the script is spawned directly. The unit config avoids booting the stack.) Expected: FAIL — script missing (`bash: … No such file or directory`).

- [ ] **Step 3 — implement.** Create `twin/scripts/capture-x1pz28.sh`:

```bash
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
```
Then `chmod +x twin/scripts/capture-x1pz28.sh`.

- [ ] **Step 4 — run guard test, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/capture-x1pz28.test.ts
```

- [ ] **Step 5 — run the capture (documented runnable step; needs the read-only prod DB).**
```bash
twin/scripts/capture-x1pz28.sh
```
Post-capture sanity (each must print the exact value):
```bash
cd twin
jq 'length' fixtures/era2-X1-PZ28/waypoints.json   # 90
jq 'length' fixtures/era2-X1-PZ28/markets.json     # 30
jq 'length' fixtures/era2-X1-PZ28/shipyards.json   # 3
jq -r '.headquarters, .startingCredits, .startingFaction' fixtures/era2-X1-PZ28/register.json  # X1-PZ28-A1 / 175000 / COSMIC
jq -r '.resetDate' fixtures/era2-X1-PZ28/server-status.json # 2026-07-05
```

- [ ] **Step 6 — commit.**
```bash
rtk git add twin/scripts/capture-x1pz28.sh twin/tests/harness/capture-x1pz28.test.ts twin/fixtures/era2-X1-PZ28/
rtk git commit -m "feat(twin): capture-x1pz28.sh — read-only X1-PZ28 home-system fixture capture + DSN guard"
```

---

## Task 6 — `launch-test-stack.sh` — boot twin + isolated test daemon

**Files:** Create `twin/scripts/launch-test-stack.sh`. Test `twin/tests/harness/launch-test-stack.test.ts` (create).

**Interfaces:** Consumes `twin/test-config.yaml` (Task 2), the `ST_API_BASE_URL` seam (Task 1), `make -C gobot build-cli/build-daemon`, `npm --prefix twin run start` (Task 3). Produces `launch-test-stack.sh [--dry-run]` — refuses to boot unless the config pins every isolation value, NEVER passes `--force`. Guard refusals start `REFUSING TO LAUNCH:`.

- [ ] **Step 1 — failing test.** Create `twin/tests/harness/launch-test-stack.test.ts`:

```ts
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const TWIN_DIR = path.resolve(__dirname, '..', '..');
const SCRIPT = path.join(TWIN_DIR, 'scripts', 'launch-test-stack.sh');
const REAL_CONFIG = path.join(TWIN_DIR, 'test-config.yaml');
const TEST_DSN = 'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';

function runDry(env: Record<string, string> = {}) {
  const res = spawnSync('bash', [SCRIPT, '--dry-run'], {
    encoding: 'utf8', timeout: 15_000,
    env: { ...process.env, TWIN_TEST_CONFIG: '', TWIN_BASE_URL: '', TEST_DATABASE_URL: '', ...env },
  });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status };
}

describe('launch-test-stack.sh — isolation guards for the --force PID trap', () => {
  it('dry-run passes on the checked-in test-config.yaml and prints the exact env triplet', () => {
    const { stdout, stderr, exitCode } = runDry();
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain(`SPACETRADERS_CONFIG=${REAL_CONFIG}`);
    expect(stdout).toContain('ST_API_BASE_URL=http://127.0.0.1:8080/v2');
    expect(stdout).toContain(`DATABASE_URL=${TEST_DSN}`);
    expect(stdout).toContain('dry-run: guards passed');
  });
  it('REFUSES a config whose pid_file is the PRODUCTION pidfile', () => {
    const tampered = readFileSync(REAL_CONFIG, 'utf8').replace('pid_file: /tmp/spacetraders-daemon-test.pid', 'pid_file: /tmp/spacetraders-daemon.pid');
    const dir = mkdtempSync(path.join(tmpdir(), 'twin-cfg-')); const file = path.join(dir, 'tampered.yaml'); writeFileSync(file, tampered);
    const { stderr, exitCode } = runDry({ TWIN_TEST_CONFIG: file });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO LAUNCH'); expect(stderr).toContain('pid_file');
  });
  it('REFUSES a config with the daemon isolation lines stripped', () => {
    const stripped = readFileSync(REAL_CONFIG, 'utf8').split('\n').filter((l) => !/pid_file|socket_path|address/.test(l)).join('\n');
    const dir = mkdtempSync(path.join(tmpdir(), 'twin-cfg-')); const file = path.join(dir, 'stripped.yaml'); writeFileSync(file, stripped);
    const { stderr, exitCode } = runDry({ TWIN_TEST_CONFIG: file });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO LAUNCH');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/launch-test-stack.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/scripts/launch-test-stack.sh`:

```bash
#!/usr/bin/env bash
#
# launch-test-stack.sh — boot the digital twin + an ISOLATED test daemon.
# Refuses to launch unless test-config.yaml pins every isolation value; NEVER --force.
# Usage: twin/scripts/launch-test-stack.sh [--dry-run]
# Env overrides (empty ⇒ default): TWIN_TEST_CONFIG, TWIN_BASE_URL, TEST_DATABASE_URL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TWIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$TWIN_DIR/.." && pwd)"
GOBOT_DIR="$REPO_ROOT/gobot"
CLI_BIN="$GOBOT_DIR/bin/spacetraders"
DAEMON_BIN="$GOBOT_DIR/bin/spacetraders-daemon"

TWIN_TEST_CONFIG="${TWIN_TEST_CONFIG:-$TWIN_DIR/test-config.yaml}"
TWIN_BASE_URL="${TWIN_BASE_URL:-http://127.0.0.1:8080/v2}"
TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable}"

TEST_PID_FILE="/tmp/spacetraders-daemon-test.pid"
TEST_GRPC_HOST="localhost"; TEST_GRPC_PORT="50062"
TWIN_LOG="/tmp/spacetraders-twin-test.log"; TWIN_PID_FILE="/tmp/spacetraders-twin-test.pid"
DAEMON_LOG="/tmp/spacetraders-daemon-test.log"

DRY_RUN=0; if [ "${1:-}" = "--dry-run" ]; then DRY_RUN=1; fi
fail() { echo "REFUSING TO LAUNCH: $*" >&2; exit 1; }

[ -f "$TWIN_TEST_CONFIG" ] || fail "config not found: $TWIN_TEST_CONFIG"
require_line() {
  grep -Eq "$1" "$TWIN_TEST_CONFIG" || fail "$2 — $TWIN_TEST_CONFIG must contain '$3'.
Without this override a test daemon lands in PRODUCTION's slot, and the next --force boot SIGTERM-kills the production daemon."
}
require_line '^[[:space:]]*pid_file:[[:space:]]*/tmp/spacetraders-daemon-test\.pid[[:space:]]*(#.*)?$' "daemon.pid_file is not the -test pidfile" "pid_file: /tmp/spacetraders-daemon-test.pid"
require_line '^[[:space:]]*socket_path:[[:space:]]*/tmp/spacetraders-daemon-test\.sock[[:space:]]*(#.*)?$' "daemon.socket_path is not the -test socket" "socket_path: /tmp/spacetraders-daemon-test.sock"
require_line '^[[:space:]]*address:[[:space:]]*localhost:50062[[:space:]]*(#.*)?$' "daemon.address is not the test gRPC port" "address: localhost:50062"
require_line '^[[:space:]]*port:[[:space:]]*9092[[:space:]]*(#.*)?$' "metrics.port is not 9092 (prod serves 9090)" "port: 9092"
grep -Fq '5433/spacetraders_test' "$TWIN_TEST_CONFIG" || fail "database.url does not point at spacetraders_test on 5433 — prod (5432/spacetraders) is READ-ONLY."

echo "twin config:   $TWIN_TEST_CONFIG"
echo "daemon bin:    $DAEMON_BIN"
echo "env:           SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG"
echo "env:           ST_API_BASE_URL=$TWIN_BASE_URL"
echo "env:           DATABASE_URL=$TEST_DATABASE_URL"
if [ "$DRY_RUN" = "1" ]; then echo "dry-run: guards passed; nothing launched."; exit 0; fi

if [ ! -x "$CLI_BIN" ] || [ ! -x "$DAEMON_BIN" ]; then echo "building CLI + daemon..."; make -C "$GOBOT_DIR" build-cli build-daemon; fi
if ! (echo > "/dev/tcp/localhost/5433") 2>/dev/null; then
  fail "test Postgres not reachable on localhost:5433. Start it first (docker compose -f twin/docker-compose.test.yml up -d postgres-test). Prod 5432 is READ-ONLY."
fi

if curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1; then echo "twin already serving $TWIN_BASE_URL — reusing it."; else
  [ -d "$TWIN_DIR/node_modules" ] || npm --prefix "$TWIN_DIR" install
  echo "booting twin (npm --prefix $TWIN_DIR run start) -> $TWIN_LOG"
  npm --prefix "$TWIN_DIR" run start >"$TWIN_LOG" 2>&1 &
  echo $! > "$TWIN_PID_FILE"
  i=0; while [ $i -lt 60 ]; do curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1 && break; sleep 0.5; i=$((i + 1)); done
  curl -sf "$TWIN_BASE_URL/" >/dev/null 2>&1 || fail "twin did not become ready on $TWIN_BASE_URL (log: $TWIN_LOG)"
fi

if [ -f "$TEST_PID_FILE" ] && kill -0 "$(cat "$TEST_PID_FILE")" 2>/dev/null; then
  echo "stale test daemon pid $(cat "$TEST_PID_FILE") — SIGTERM via the TEST pidfile only..."
  kill "$(cat "$TEST_PID_FILE")" 2>/dev/null || true
  i=0; while [ $i -lt 20 ] && [ -f "$TEST_PID_FILE" ]; do sleep 0.5; i=$((i + 1)); done
fi
rm -f "$TEST_PID_FILE"

echo "booting isolated test daemon -> $DAEMON_LOG"
( cd "$GOBOT_DIR"; exec env "SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG" "ST_API_BASE_URL=$TWIN_BASE_URL" "DATABASE_URL=$TEST_DATABASE_URL" "$DAEMON_BIN" >"$DAEMON_LOG" 2>&1 ) &
i=0; while [ $i -lt 60 ]; do
  if [ -f "$TEST_PID_FILE" ] && (echo > "/dev/tcp/$TEST_GRPC_HOST/$TEST_GRPC_PORT") 2>/dev/null; then break; fi
  sleep 0.5; i=$((i + 1))
done
[ -f "$TEST_PID_FILE" ] || fail "test daemon never wrote $TEST_PID_FILE (log: $DAEMON_LOG)"
(echo > "/dev/tcp/$TEST_GRPC_HOST/$TEST_GRPC_PORT") 2>/dev/null || fail "test daemon gRPC not accepting on $TEST_GRPC_HOST:$TEST_GRPC_PORT (log: $DAEMON_LOG)"

echo ""; echo "test stack is up:"
echo "  twin:         $TWIN_BASE_URL   (log: $TWIN_LOG)"
echo "  daemon pid:   $(cat "$TEST_PID_FILE")   (log: $DAEMON_LOG)"
echo "  daemon gRPC:  $TEST_GRPC_HOST:$TEST_GRPC_PORT"
echo "  next:         $SCRIPT_DIR/seed-player.sh"
```
Then `chmod +x twin/scripts/launch-test-stack.sh`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/launch-test-stack.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/scripts/launch-test-stack.sh twin/tests/harness/launch-test-stack.test.ts
rtk git commit -m "feat(twin): launch-test-stack.sh — isolated test-daemon boot with --force PID-trap guards"
```

---

## Task 7 — `seed-player.sh` — register TWINAGENT against the twin

**Files:** Create `twin/scripts/seed-player.sh`. Test `twin/tests/harness/seed-player.test.ts` (create).

**Interfaces:** Consumes `test-config.yaml`, the seam, a running test stack (live path only), and `spacetraders player register --new`. Produces `seed-player.sh [--dry-run]` — refuses live-API + non-test-DB targets (`REFUSING TO SEED:`), verifies the minted player id == 1, idempotent (exits 0 on a pre-existing OPEN era).

- [ ] **Step 1 — failing test.** Create `twin/tests/harness/seed-player.test.ts`:

```ts
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const TWIN_DIR = path.resolve(__dirname, '..', '..');
const SCRIPT = path.join(TWIN_DIR, 'scripts', 'seed-player.sh');
const TEST_DSN = 'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';

function runDry(env: Record<string, string> = {}) {
  const res = spawnSync('bash', [SCRIPT, '--dry-run'], {
    encoding: 'utf8', timeout: 15_000,
    env: { ...process.env, TWIN_TEST_CONFIG: '', TWIN_BASE_URL: '', TEST_DATABASE_URL: '', TEST_AGENT: '', TEST_FACTION: '', ST_ACCOUNT_TOKEN: '', ...env },
  });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status };
}

describe('seed-player.sh — guards and invocation shape', () => {
  it('dry-run prints the exact register invocation and env against the twin', () => {
    const { stdout, stderr, exitCode } = runDry();
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain('player register --new --agent TWINAGENT --faction COSMIC');
    expect(stdout).toContain('ST_API_BASE_URL=http://127.0.0.1:8080/v2');
    expect(stdout).toContain('ST_ACCOUNT_TOKEN=twin-test-account-token');
    expect(stdout).toContain(`DATABASE_URL=${TEST_DSN}`);
  });
  it('REFUSES to register against the live SpaceTraders API', () => {
    const { stderr, exitCode } = runDry({ TWIN_BASE_URL: 'https://api.spacetraders.io/v2' });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO SEED'); expect(stderr).toContain('LIVE SpaceTraders API');
  });
  it('REFUSES a DATABASE_URL that is not spacetraders_test', () => {
    const { stderr, exitCode } = runDry({ TEST_DATABASE_URL: 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders' });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO SEED'); expect(stderr).toContain('spacetraders_test');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/seed-player.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/scripts/seed-player.sh`:

```bash
#!/usr/bin/env bash
#
# seed-player.sh — mint TWINAGENT against the RUNNING twin via the bot's own CLI
# (`player register --new`). Refuses live-API + non-test-DB targets; verifies player id 1.
# Usage: twin/scripts/seed-player.sh [--dry-run]
# Env (empty ⇒ default): TWIN_TEST_CONFIG, TWIN_BASE_URL, TEST_DATABASE_URL,
#   TEST_AGENT (TWINAGENT), TEST_FACTION (COSMIC), ST_ACCOUNT_TOKEN (twin-test-account-token)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TWIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"; REPO_ROOT="$(cd "$TWIN_DIR/.." && pwd)"
GOBOT_DIR="$REPO_ROOT/gobot"; CLI_BIN="$GOBOT_DIR/bin/spacetraders"

TWIN_TEST_CONFIG="${TWIN_TEST_CONFIG:-$TWIN_DIR/test-config.yaml}"
TWIN_BASE_URL="${TWIN_BASE_URL:-http://127.0.0.1:8080/v2}"
TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable}"
TEST_AGENT="${TEST_AGENT:-TWINAGENT}"; TEST_FACTION="${TEST_FACTION:-COSMIC}"
ACCOUNT_TOKEN="${ST_ACCOUNT_TOKEN:-twin-test-account-token}"; EXPECTED_PLAYER_ID=1

DRY_RUN=0; if [ "${1:-}" = "--dry-run" ]; then DRY_RUN=1; fi
fail() { echo "REFUSING TO SEED: $*" >&2; exit 1; }

case "$TWIN_BASE_URL" in *spacetraders.io*) fail "base URL '$TWIN_BASE_URL' points at the LIVE SpaceTraders API. Seeding must only hit the local twin (http://127.0.0.1:8080/v2)." ;; esac
case "$TEST_DATABASE_URL" in *spacetraders_test*) : ;; *) fail "DATABASE_URL '$TEST_DATABASE_URL' is not the spacetraders_test DB. Prod (5432/spacetraders) is READ-ONLY." ;; esac
[ -f "$TWIN_TEST_CONFIG" ] || fail "config not found: $TWIN_TEST_CONFIG (run launch-test-stack.sh first)"

echo "register:      $CLI_BIN player register --new --agent $TEST_AGENT --faction $TEST_FACTION"
echo "env:           SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG"
echo "env:           ST_API_BASE_URL=$TWIN_BASE_URL"
echo "env:           DATABASE_URL=$TEST_DATABASE_URL"
echo "env:           ST_ACCOUNT_TOKEN=$ACCOUNT_TOKEN"
if [ "$DRY_RUN" = "1" ]; then echo "dry-run: guards passed; nothing registered."; exit 0; fi

[ -x "$CLI_BIN" ] || make -C "$GOBOT_DIR" build-cli
set +e
OUT="$(cd "$GOBOT_DIR" && env "SPACETRADERS_CONFIG=$TWIN_TEST_CONFIG" "ST_API_BASE_URL=$TWIN_BASE_URL" "DATABASE_URL=$TEST_DATABASE_URL" "ST_ACCOUNT_TOKEN=$ACCOUNT_TOKEN" "$CLI_BIN" player register --new --agent "$TEST_AGENT" --faction "$TEST_FACTION" 2>&1)"
STATUS=$?; set -e
echo "$OUT"
if [ $STATUS -ne 0 ]; then
  if echo "$OUT" | grep -q "an OPEN era"; then echo "test DB already seeded (an OPEN era exists) — nothing to do."; exit 0; fi
  fail "player register --new failed (exit $STATUS). Is the test stack up? Run launch-test-stack.sh first."
fi
PLAYER_ID="$(echo "$OUT" | sed -n 's/.*Player ID:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)"
if [ "$PLAYER_ID" != "$EXPECTED_PLAYER_ID" ]; then
  fail "register minted player id '${PLAYER_ID:-<none>}' but test-config.yaml pins captain.player_id: $EXPECTED_PLAYER_ID. Drop+recreate spacetraders_test, restart the test daemon, reseed."
fi
echo ""; echo "seeded $TEST_AGENT (player id $PLAYER_ID, faction $TEST_FACTION) against $TWIN_BASE_URL."
```
Then `chmod +x twin/scripts/seed-player.sh`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/harness/seed-player.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/scripts/seed-player.sh twin/tests/harness/seed-player.test.ts
rtk git commit -m "feat(twin): seed-player.sh — TWINAGENT registration against the twin with live-API refusal"
```

---

## Task 8 — World loader: `loadColdStartWorld` + `loadRegisterTemplate`

**Files:** Create `twin/src/world/loader.ts`. Test `twin/tests/world/loader.test.ts` (create).

**Reconciliation:** This is the single canonical world module (decision 1). `20b`'s `load.ts`/`loadWorld()` and `30b`'s `register-template.ts` are folded here. `loadColdStartWorld()` returns the PRE-register world (`agent`/`agentToken` null, `ships`/`transits` empty, `shipCounter=0`).

**Interfaces:** Consumes `twin/src/world/types.ts` + the captured fixtures (Task 5). Produces `FIXTURES_DIR`, `interface RegisterTemplate`, `loadRegisterTemplate(dir?)`, `loadColdStartWorld(dir?)`. Consumed by the store (Task 14), the register route (Task 18), and reset.

- [ ] **Step 1 — failing test.** Create `twin/tests/world/loader.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { loadColdStartWorld, loadRegisterTemplate } from '../../src/world/loader';

const HOME_SYSTEM = 'X1-PZ28';

describe('loadColdStartWorld — captured X1-PZ28 snapshot, pre-register', () => {
  it('loads the full 90-waypoint topology with exactly one JUMP_GATE', () => {
    const world = loadColdStartWorld();
    const system = world.systems.get(HOME_SYSTEM);
    expect(system, `system ${HOME_SYSTEM} must be present`).toBeDefined();
    expect(system!.waypoints.size).toBe(90);
    const gates = [...system!.waypoints.values()].filter((w) => w.type === 'JUMP_GATE');
    expect(gates).toHaveLength(1);
  });

  it('exposes non-empty market/shipyard subsets keyed by real waypoint symbols', () => {
    const world = loadColdStartWorld();
    expect(world.markets.size).toBeGreaterThan(0);
    expect(world.shipyards.size).toBeGreaterThan(0);
    const system = world.systems.get(HOME_SYSTEM)!;
    for (const s of world.markets.keys()) expect(system.waypoints.has(s), `market ${s} is a known waypoint`).toBe(true);
    for (const s of world.shipyards.keys()) expect(system.waypoints.has(s), `shipyard ${s} is a known waypoint`).toBe(true);
  });

  it('includes a SHIP_PROBE shipyard listing with numeric engine.speed', () => {
    const world = loadColdStartWorld();
    const probe = [...world.shipyards.values()].flatMap((sy) => sy.ships).find((l) => l.type === 'SHIP_PROBE');
    expect(probe, 'a SHIP_PROBE listing must exist').toBeDefined();
    expect(typeof probe!.engine.speed).toBe('number');
    expect(probe!.engine.speed as number).toBeGreaterThan(0);
  });

  it('returns a PRE-register world (null agent, empty ships/transits, shipCounter 0)', () => {
    const world = loadColdStartWorld();
    expect(world.agent).toBeNull();
    expect(world.agentToken).toBeNull();
    expect(world.ships.size).toBe(0);
    expect(world.transits.size).toBe(0);
    expect(world.shipCounter).toBe(0);
    expect(world.serverStatus.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(typeof world.serverStatus.serverResets.next).toBe('string');
  });
});

describe('loadRegisterTemplate — captured cold-start template', () => {
  it('loads the golden values (2 starting ships with the {AGENT} placeholder)', () => {
    const tpl = loadRegisterTemplate();
    expect(tpl.startingCredits).toBe(175000);
    expect(tpl.headquarters).toBe('X1-PZ28-A1');
    expect(tpl.startingFaction).toBe('COSMIC');
    expect(tpl.ships).toHaveLength(2);
    expect(tpl.ships.map((s) => s.symbol).sort()).toEqual(['{AGENT}-1', '{AGENT}-2']);
    expect(tpl.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/loader.test.ts
```
Expected: `Failed to load url ../../src/world/loader`. (If instead it fails `ENOENT … waypoints.json`, run Task 5's capture first.)

- [ ] **Step 3 — implement.** Create `twin/src/world/loader.ts`:

```ts
// twin/src/world/loader.ts — the single world module for the digital twin.
// Materializes the captured X1-PZ28 snapshot into the foundation world types, and
// (mintToken/registerAgent, added in Tasks 9/10) mints the cold-start agent on top.
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Market, Ship, Shipyard, System, TransitState, Waypoint, World } from './types.js';

const MODULE_DIR = path.dirname(fileURLToPath(import.meta.url));

/** Absolute path to the checked-in captured home-system fixture directory. */
export const FIXTURES_DIR = path.resolve(MODULE_DIR, '../../fixtures/era2-X1-PZ28');

/** The pristine POST /register payload template (fixtures/era2-X1-PZ28/register.json).
 *  Ship symbols carry the "{AGENT}" placeholder registerAgent substitutes. */
export interface RegisterTemplate {
  startingCredits: number;
  headquarters: string;
  startingFaction: string;
  ships: Ship[];
}

function readJson<T>(dir: string, file: string): T {
  return JSON.parse(readFileSync(path.join(dir, file), 'utf8')) as T;
}

export function loadRegisterTemplate(dir: string = FIXTURES_DIR): RegisterTemplate {
  return readJson<RegisterTemplate>(dir, 'register.json');
}

/** Load the PRE-register captured world: serverStatus/systems/markets/shipyards from the
 *  capture; agent/agentToken null; ships/transits empty; shipCounter 0. */
export function loadColdStartWorld(dir: string = FIXTURES_DIR): World {
  const serverStatus = readJson<World['serverStatus']>(dir, 'server-status.json');
  const waypoints = readJson<Waypoint[]>(dir, 'waypoints.json');
  const markets = readJson<Market[]>(dir, 'markets.json');
  const shipyards = readJson<Shipyard[]>(dir, 'shipyards.json');

  const systems = new Map<string, System>();
  for (const wp of waypoints) {
    let system = systems.get(wp.systemSymbol);
    if (!system) {
      system = { symbol: wp.systemSymbol, waypoints: new Map<string, Waypoint>() };
      systems.set(wp.systemSymbol, system);
    }
    system.waypoints.set(wp.symbol, wp);
  }

  const marketMap = new Map<string, Market>();
  for (const m of markets) marketMap.set(m.symbol, m);
  const shipyardMap = new Map<string, Shipyard>();
  for (const sy of shipyards) shipyardMap.set(sy.symbol, sy);

  return {
    serverStatus,
    agent: null,
    agentToken: null,
    ships: new Map<string, Ship>(),
    systems,
    markets: marketMap,
    shipyards: shipyardMap,
    transits: new Map<string, TransitState>(),
    shipCounter: 0,
  };
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/loader.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/world/loader.ts twin/tests/world/loader.test.ts
rtk git commit -m "feat(twin): world loader — loadColdStartWorld/loadRegisterTemplate over captured X1-PZ28 fixtures"
```

---

## Task 9 — `mintToken` — deterministic JWT-shaped agent token

**Files:** Modify `twin/src/world/loader.ts` (add `mintToken`). Test `twin/tests/world/mint-token.test.ts` (create).

**Interfaces:** Produces `mintToken(symbol: string): string` — `header.payload.signature`, each segment `base64url` (no padding), pure function of `symbol`. Determinism lets `POST /_twin/reset` reissue the identical token and lets tests reproduce `players.token`. Consumed by the register route (Task 18) and the store's reset (Task 14).

- [ ] **Step 1 — failing test.** Create `twin/tests/world/mint-token.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { mintToken } from '../../src/world/loader';

describe('mintToken', () => {
  it('mints the exact deterministic token for TWINAGENT', () => {
    expect(mintToken('TWINAGENT')).toBe(
      'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9' +
        '.eyJpZGVudGlmaWVyIjoiVFdJTkFHRU5UIiwidmVyc2lvbiI6InR3aW4ifQ' +
        '.dHdpbi1zaWduYXR1cmUuVFdJTkFHRU5U',
    );
  });
  it('is JWT-shaped: three non-empty base64url segments', () => {
    const parts = mintToken('TWINAGENT').split('.');
    expect(parts).toHaveLength(3);
    for (const p of parts) { expect(p.length).toBeGreaterThan(0); expect(p).toMatch(/^[A-Za-z0-9_-]+$/); }
  });
  it('is deterministic per symbol and distinct across symbols', () => {
    expect(mintToken('TWINAGENT')).toBe(mintToken('TWINAGENT'));
    expect(mintToken('TWINAGENT')).not.toBe(mintToken('OTHERAGENT'));
  });
  it('encodes the agent symbol as the payload identifier', () => {
    const payload = mintToken('TWINAGENT').split('.')[1];
    const json = Buffer.from(payload.replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8');
    expect(JSON.parse(json)).toEqual({ identifier: 'TWINAGENT', version: 'twin' });
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/mint-token.test.ts
```
Expected: `mintToken` is not exported yet.

- [ ] **Step 3 — implement.** Append to `twin/src/world/loader.ts`:

```ts
/** base64url (RFC 4648 §5) with padding stripped. */
function b64url(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Mint the twin's opaque, JWT-shaped agent token. DETERMINISTIC per symbol: the Go
 *  client never decodes it (only ever a Bearer string), and determinism lets reset
 *  reissue the identical token and tests reproduce players.token from the symbol. */
export function mintToken(symbol: string): string {
  const header = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const payload = b64url(JSON.stringify({ identifier: symbol, version: 'twin' }));
  const signature = b64url(`twin-signature.${symbol}`);
  return `${header}.${payload}.${signature}`;
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/mint-token.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/world/loader.ts twin/tests/world/mint-token.test.ts
rtk git commit -m "feat(twin): deterministic JWT-shaped mintToken in the world loader"
```

---

## Task 10 — `registerAgent` — cold-start agent + starting ships builder

**Files:** Modify `twin/src/world/loader.ts` (add `registerAgent`). Test `twin/tests/world/register-agent.test.ts` (create).

**Reconciliation:** Replaces `30b`'s `applyRegister`. Signature `registerAgent(world, { symbol, faction, token }): { agent, ships }` — token is passed IN (minted by the caller), materializes agent + ships into `world`, sets `world.shipCounter = ships.length + 1`, clears `world.transits`. The `POST /register` route mints the token then calls this; `resetWorld` passes the preserved token.

**Interfaces:** Consumes `World`, `Agent`, `Ship` (types), `loadRegisterTemplate` (Task 8). Produces `registerAgent`. Consumed by the store (Task 14) and register route (Task 18).

- [ ] **Step 1 — failing test.** Create `twin/tests/world/register-agent.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { loadColdStartWorld, mintToken, registerAgent } from '../../src/world/loader';

describe('registerAgent — materializes the cold-start agent + starting ships', () => {
  it('mutates the world into cold-start and returns the /register data', () => {
    const world = loadColdStartWorld();
    // pre-seed a ghost transit to prove registerAgent clears in-flight state
    world.transits.set('GHOST', {
      shipSymbol: 'GHOST', originWaypoint: 'X1-PZ28-A1', destinationWaypoint: 'X1-PZ28-B1',
      departureTime: '2026-07-11T00:00:00.000Z', arrival: '2026-07-11T00:10:00.000Z',
    });
    const token = mintToken('TWINAGENT');
    const { agent, ships } = registerAgent(world, { symbol: 'TWINAGENT', faction: 'COSMIC', token });

    expect(agent).toEqual({
      accountId: 'twin-account-TWINAGENT', symbol: 'TWINAGENT',
      headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: 'COSMIC',
    });
    expect(ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(world.agent).toBe(agent);
    expect(world.agentToken).toBe(token);
    expect([...world.ships.keys()].sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(world.ships.get('TWINAGENT-1')?.registration.role).toBe('COMMAND');
    expect(world.ships.get('TWINAGENT-2')?.registration.role).toBe('SATELLITE');
    expect(world.ships.get('TWINAGENT-1')?.nav.status).toBe('DOCKED');
    expect(world.transits.size).toBe(0); // ghost cleared
    expect(world.shipCounter).toBe(3);   // next purchased suffix after -1, -2
  });

  it('defaults faction to the template when the request faction is empty', () => {
    const world = loadColdStartWorld();
    const { agent } = registerAgent(world, { symbol: 'TWINAGENT', faction: '', token: mintToken('TWINAGENT') });
    expect(agent.startingFaction).toBe('COSMIC');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/register-agent.test.ts
```

- [ ] **Step 3 — implement.** Append to `twin/src/world/loader.ts` (and add `Agent` to the type import):

```ts
// add Agent to the existing type import at the top of loader.ts:
//   import type { Agent, Market, Ship, Shipyard, System, TransitState, Waypoint, World } from './types.js';

/** Materialize the cold-start agent + starting ships from register.json into `world`
 *  using the PROVIDED token, and return the /register response data. Clears transits
 *  (cold start ⇒ all ships DOCKED) and sets shipCounter = ships.length + 1. Also used
 *  by POST /_twin/reset with the preserved token. */
export function registerAgent(
  world: World,
  args: { symbol: string; faction: string; token: string },
  template: RegisterTemplate = loadRegisterTemplate(),
): { agent: Agent; ships: Ship[] } {
  const { symbol, token } = args;
  const faction = args.faction || template.startingFaction;

  const agent: Agent = {
    accountId: `twin-account-${symbol}`,
    symbol,
    headquarters: template.headquarters,
    credits: template.startingCredits,
    startingFaction: faction,
  };

  // Deep-replace the "{AGENT}" placeholder (JSON round-trip also deep-clones the
  // shared template so it is never mutated).
  const ships = JSON.parse(JSON.stringify(template.ships).split('{AGENT}').join(symbol)) as Ship[];

  world.agent = agent;
  world.agentToken = token;
  world.ships = new Map(ships.map((s) => [s.symbol, s]));
  world.transits = new Map();
  world.shipCounter = ships.length + 1;

  return { agent, ships };
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/register-agent.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/world/loader.ts twin/tests/world/register-agent.test.ts
rtk git commit -m "feat(twin): registerAgent cold-start builder (token-param, returns agent+ships)"
```

---

## Task 11 — Compressed clock `twin/src/clock.ts`

**Files:** Create `twin/src/clock.ts`. Test `twin/tests/unit/clock.test.ts` (create).

**Reconciliation:** Placed in Wave 1 (not Wave 2) because the skeleton's `GET /_twin/state` (Task 15) consumes `resolveNav`/`getCompression`. Only depends on `types.ts`.

**Interfaces:** Consumes `FlightMode, Ship, TransitState, Waypoint, Rfc3339` (`import type`). Produces `getCompression`, `setCompression`, `parseCompression`, `distance`, `realTravelSeconds`, `fuelCost`, `makeTransit`, `resolveNav`, `makeCooldownExpiration` (foundation §2). Consumed by admin (15), navigate (27), ship reads (Tasks 22/23), ship actions (28).

- [ ] **Step 1 — failing test.** Create `twin/tests/unit/clock.test.ts`:

```ts
import { afterEach, describe, expect, it } from 'vitest';
import type { Ship, TransitState, Waypoint } from '../../src/world/types';
import {
  distance, fuelCost, getCompression, makeCooldownExpiration, makeTransit,
  parseCompression, realTravelSeconds, resolveNav, setCompression,
} from '../../src/clock';

function wp(symbol: string, x: number, y: number): Waypoint {
  return { symbol, type: 'PLANET', systemSymbol: 'X1-PZ28', x, y, traits: [], orbitals: [], isUnderConstruction: false };
}
function baseShip(): Ship {
  return {
    symbol: 'TWINAGENT-1', registration: { role: 'COMMAND' },
    nav: { systemSymbol: 'X1-PZ28', waypointSymbol: 'X1-PZ28-A1', status: 'IN_ORBIT', flightMode: 'CRUISE', route: null },
    fuel: { current: 400, capacity: 400 }, cargo: { capacity: 40, units: 0, inventory: [] }, cooldown: null,
    engine: { speed: 30 }, frame: { symbol: 'FRAME_FRIGATE', moduleSlots: 8, mountingPoints: 5 },
    reactor: { symbol: 'REACTOR_FISSION_I', name: 'Fission Reactor I', powerOutput: 31, requirements: { power: 0, crew: 8, slots: 1 } },
    crew: { current: 57, required: 57, capacity: 80 }, modules: [], mounts: [],
  };
}
afterEach(() => setCompression(100));

describe('distance', () => {
  it('is Euclidean', () => expect(distance({ x: 0, y: 0 }, { x: 3, y: 4 })).toBe(5));
  it('is 0 for the same point', () => expect(distance({ x: 7, y: -2 }, { x: 7, y: -2 })).toBe(0));
});

describe('realTravelSeconds', () => {
  it('is 0 when distance is 0', () => { expect(realTravelSeconds(0, 30, 'CRUISE')).toBe(0); expect(realTravelSeconds(0, 30, 'BURN')).toBe(0); });
  it('matches routing_engine.py per mode', () => {
    expect(realTravelSeconds(10, 30, 'CRUISE')).toBe(10);
    expect(realTravelSeconds(10, 30, 'DRIFT')).toBe(8);
    expect(realTravelSeconds(10, 30, 'BURN')).toBe(5);
    expect(realTravelSeconds(10, 30, 'STEALTH')).toBe(16);
  });
  it('defaults to CRUISE', () => expect(realTravelSeconds(10, 30)).toBe(10));
  it('floors to a minimum of 1s for any non-zero distance', () => expect(realTravelSeconds(1, 100, 'CRUISE')).toBe(1));
  it('clamps engine speed to a minimum of 1', () => expect(realTravelSeconds(10, 0, 'CRUISE')).toBe(310));
});

describe('fuelCost', () => {
  it('is 0 when distance is 0', () => { expect(fuelCost(0, 'CRUISE')).toBe(0); expect(fuelCost(0, 'BURN')).toBe(0); });
  it('matches routing_engine.py per mode', () => {
    expect(fuelCost(10, 'CRUISE')).toBe(10); expect(fuelCost(10, 'BURN')).toBe(20);
    expect(fuelCost(10, 'STEALTH')).toBe(10); expect(fuelCost(10, 'DRIFT')).toBe(1); expect(fuelCost(1000, 'DRIFT')).toBe(3);
  });
  it('defaults to CRUISE, never < 1 for non-zero', () => { expect(fuelCost(10)).toBe(10); expect(fuelCost(0.1, 'CRUISE')).toBe(1); });
});

describe('compression', () => {
  it('parseCompression: default 100 for unset/empty/invalid, honors positives', () => {
    expect(parseCompression(undefined)).toBe(100); expect(parseCompression('')).toBe(100);
    expect(parseCompression('abc')).toBe(100); expect(parseCompression('0')).toBe(100);
    expect(parseCompression('-5')).toBe(100); expect(parseCompression('50')).toBe(50); expect(parseCompression('2.5')).toBe(2.5);
  });
  it('get/set round-trips', () => { setCompression(5); expect(getCompression()).toBe(5); });
  it('setCompression rejects non-positive / non-finite', () => {
    expect(() => setCompression(0)).toThrow(RangeError);
    expect(() => setCompression(-1)).toThrow(RangeError);
    expect(() => setCompression(Number.NaN)).toThrow(RangeError);
  });
});

describe('makeTransit', () => {
  const now = new Date('2026-07-11T00:00:00.000Z');
  it('mints departure=now and arrival=now+realETA/compression', () => {
    setCompression(100);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300), engineSpeed: 30, mode: 'CRUISE', now });
    expect(t.originWaypoint).toBe('X1-PZ28-A1');
    expect(t.destinationWaypoint).toBe('X1-PZ28-B1');
    expect(t.departureTime).toBe('2026-07-11T00:00:00.000Z');
    expect(t.arrival).toBe('2026-07-11T00:00:03.100Z');
  });
  it('samples compression at call time', () => {
    const args = { shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 0, 0), destination: wp('X1-PZ28-B1', 0, 300), engineSpeed: 30, mode: 'CRUISE' as const, now };
    setCompression(10); const slow = makeTransit(args);
    setCompression(100); const fast = makeTransit(args);
    expect(slow.arrival).toBe('2026-07-11T00:00:31.000Z');
    expect(fast.arrival).toBe('2026-07-11T00:00:03.100Z');
  });
  it('guards departure <= arrival for a zero-distance hop', () => {
    setCompression(100);
    const t = makeTransit({ shipSymbol: 'TWINAGENT-1', origin: wp('X1-PZ28-A1', 5, 5), destination: wp('X1-PZ28-A2', 5, 5), engineSpeed: 30, now });
    expect(t.arrival).toBe(t.departureTime);
  });
});

describe('resolveNav', () => {
  function transit(arrival: string): TransitState {
    return { shipSymbol: 'TWINAGENT-1', originWaypoint: 'X1-PZ28-A1', destinationWaypoint: 'X1-PZ28-B1', departureTime: '2026-07-11T00:00:00.000Z', arrival };
  }
  it('returns the ship unchanged with no transit', () => {
    const out = resolveNav(baseShip(), undefined, new Date('2026-07-11T01:00:00.000Z'));
    expect(out.nav.status).toBe('IN_ORBIT'); expect(out.nav.waypointSymbol).toBe('X1-PZ28-A1');
  });
  it('before arrival: IN_TRANSIT at the ORIGIN', () => {
    const out = resolveNav(baseShip(), transit('2026-07-11T00:00:10.000Z'), new Date('2026-07-11T00:00:05.000Z'));
    expect(out.nav.status).toBe('IN_TRANSIT'); expect(out.nav.waypointSymbol).toBe('X1-PZ28-A1');
    expect(out.nav.route).toEqual({ departureTime: '2026-07-11T00:00:00.000Z', arrival: '2026-07-11T00:00:10.000Z' });
  });
  it('at/after arrival: IN_ORBIT at the DESTINATION', () => {
    const at = resolveNav(baseShip(), transit('2026-07-11T00:00:10.000Z'), new Date('2026-07-11T00:00:10.000Z'));
    expect(at.nav.status).toBe('IN_ORBIT'); expect(at.nav.waypointSymbol).toBe('X1-PZ28-B1');
  });
  it('is pure + idempotent post-arrival', () => {
    const ship = baseShip(); const t = transit('2026-07-11T00:00:10.000Z');
    const a = resolveNav(ship, t, new Date('2026-07-11T00:00:20.000Z'));
    const b = resolveNav(ship, t, new Date('2026-07-11T00:00:30.000Z'));
    expect(ship.nav.status).toBe('IN_ORBIT'); expect(a.nav).toEqual(b.nav);
  });
});

describe('makeCooldownExpiration', () => {
  it('is now + realSeconds/compression', () => {
    setCompression(100);
    expect(makeCooldownExpiration(500, new Date('2026-07-11T00:00:00.000Z'))).toBe('2026-07-11T00:00:05.000Z');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/unit/clock.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/clock.ts`:

```ts
// twin/src/clock.ts — compressed-time engine. Travel/fuel mirror routing_engine.py so
// the twin and the bot's planner never disagree about an ETA.
import type { FlightMode, Rfc3339, Ship, TransitState, Waypoint } from './world/types.js';

const DEFAULT_COMPRESSION = 100;
const TRAVEL_MULTIPLIER: Record<FlightMode, number> = { CRUISE: 31, DRIFT: 26, BURN: 15, STEALTH: 50 };
const FUEL_RATE: Record<FlightMode, number> = { CRUISE: 1.0, DRIFT: 0.003, BURN: 2.0, STEALTH: 1.0 };

export function parseCompression(raw: string | undefined): number {
  if (raw === undefined || raw === '') return DEFAULT_COMPRESSION;
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? n : DEFAULT_COMPRESSION;
}

let compression = parseCompression(process.env.TWIN_TIME_COMPRESSION);

export function getCompression(): number { return compression; }
export function setCompression(factor: number): void {
  if (!(factor > 0)) throw new RangeError(`compression must be > 0, got ${factor}`);
  compression = factor;
}

export function distance(a: { x: number; y: number }, b: { x: number; y: number }): number {
  const dx = a.x - b.x; const dy = a.y - b.y;
  return Math.sqrt(dx * dx + dy * dy);
}

export function realTravelSeconds(dist: number, engineSpeed: number, mode: FlightMode = 'CRUISE'): number {
  if (dist === 0) return 0;
  const speed = Math.max(1, engineSpeed);
  return Math.max(1, Math.floor((dist * TRAVEL_MULTIPLIER[mode]) / speed));
}

export function fuelCost(dist: number, mode: FlightMode = 'CRUISE'): number {
  if (dist === 0) return 0;
  return Math.max(1, Math.ceil(dist * FUEL_RATE[mode]));
}

export function makeTransit(args: {
  shipSymbol: string; origin: Waypoint; destination: Waypoint; engineSpeed: number; mode?: FlightMode; now?: Date;
}): TransitState {
  const mode = args.mode ?? 'CRUISE';
  const now = args.now ?? new Date();
  const dist = distance(args.origin, args.destination);
  const realSecs = realTravelSeconds(dist, args.engineSpeed, mode);
  const departureMs = now.getTime();
  const arrivalMs = Math.max(departureMs, departureMs + (realSecs / getCompression()) * 1000);
  return {
    shipSymbol: args.shipSymbol,
    originWaypoint: args.origin.symbol,
    destinationWaypoint: args.destination.symbol,
    departureTime: new Date(departureMs).toISOString(),
    arrival: new Date(arrivalMs).toISOString(),
  };
}

/** The ONLY place nav status/location is computed. Pure — returns a new Ship.
 *  no transit → unchanged; now < arrival → IN_TRANSIT at origin; now >= arrival →
 *  IN_ORBIT at destination (atomic flip). route stays populated in both in-flight cases. */
export function resolveNav(ship: Ship, transit: TransitState | undefined, now: Date = new Date()): Ship {
  if (transit === undefined) return ship;
  const arrived = now.getTime() >= Date.parse(transit.arrival);
  return {
    ...ship,
    nav: {
      ...ship.nav,
      status: arrived ? 'IN_ORBIT' : 'IN_TRANSIT',
      waypointSymbol: arrived ? transit.destinationWaypoint : transit.originWaypoint,
      route: { departureTime: transit.departureTime, arrival: transit.arrival },
    },
  };
}

export function makeCooldownExpiration(realSeconds: number, now: Date = new Date()): Rfc3339 {
  return new Date(now.getTime() + (realSeconds / getCompression()) * 1000).toISOString();
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/unit/clock.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/clock.ts twin/tests/unit/clock.test.ts
rtk git commit -m "feat(twin): compressed-clock module (arrival math + single on-read nav flip)"
```

---

## Task 12 — Error envelope `twin/src/errors.ts`

**Files:** Create `twin/src/errors.ts`. Test `twin/tests/skeleton/errors.test.ts` (create).

**Interfaces:** Produces `ApiError`, `apiError/sendError/notFound/badRequest/unauthorized`, codes `ERR_SHIP_MUST_BE_DOCKED=4214`, `ERR_SHIP_NOT_DOCKED=4244`, `ERR_AGENT_HAS_CONTRACT=4511` (foundation §3). Consumed by every route.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/errors.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import type { FastifyReply } from 'fastify';
import {
  apiError, sendError, notFound, badRequest, unauthorized,
  ERR_AGENT_HAS_CONTRACT, ERR_SHIP_MUST_BE_DOCKED, ERR_SHIP_NOT_DOCKED,
} from '../../src/errors';

function fakeReply() {
  const r = {
    statusCode: 200, payload: undefined as unknown,
    code(c: number) { r.statusCode = c; return r; },
    send(p: unknown) { r.payload = p; return r; },
  };
  return r as typeof r & FastifyReply;
}

describe('errors — SpaceTraders envelope + code constants', () => {
  it('code constants match the Go client', () => {
    expect(ERR_SHIP_MUST_BE_DOCKED).toBe(4214);
    expect(ERR_SHIP_NOT_DOCKED).toBe(4244);
    expect(ERR_AGENT_HAS_CONTRACT).toBe(4511);
  });
  it('apiError omits data when undefined', () => {
    expect(apiError(404, 'Ship X1 not found.')).toEqual({ error: { message: 'Ship X1 not found.', code: 404 } });
  });
  it('apiError includes data when provided', () => {
    expect(apiError(ERR_AGENT_HAS_CONTRACT, 'has contract', { contractId: 'c-1' })).toEqual({ error: { message: 'has contract', code: 4511, data: { contractId: 'c-1' } } });
  });
  it('sendError sets status AND envelope, returns the reply', () => {
    const reply = fakeReply();
    const ret = sendError(reply, 400, ERR_SHIP_MUST_BE_DOCKED, 'Ship must be docked.');
    expect(ret).toBe(reply); expect(reply.statusCode).toBe(400);
    expect(reply.payload).toEqual({ error: { message: 'Ship must be docked.', code: 4214 } });
  });
  it('notFound → 404/404', () => {
    const reply = fakeReply(); notFound(reply, 'Waypoint X1-PZ28-Z9 not found.');
    expect(reply.statusCode).toBe(404);
    expect(reply.payload).toEqual({ error: { message: 'Waypoint X1-PZ28-Z9 not found.', code: 404 } });
  });
  it('badRequest → 400/400', () => {
    const reply = fakeReply(); badRequest(reply, 'compression must be > 0');
    expect(reply.statusCode).toBe(400);
    expect(reply.payload).toEqual({ error: { message: 'compression must be > 0', code: 400 } });
  });
  it('unauthorized → 401 with the default message', () => {
    const reply = fakeReply(); unauthorized(reply);
    expect(reply.statusCode).toBe(401);
    expect(reply.payload).toEqual({ error: { message: 'Missing or invalid agent token.', code: 401 } });
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/errors.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/errors.ts`:

```ts
import type { FastifyReply } from 'fastify';

/** SpaceTraders error envelope — the only error JSON the twin ever emits. */
export interface ApiError {
  error: { message: string; code: number; data?: Record<string, unknown> };
}

export const ERR_AGENT_HAS_CONTRACT = 4511; // errCodeAgentHasContract — HTTP 400
export const ERR_SHIP_MUST_BE_DOCKED = 4214; // errCodeShipMustBeDocked — HTTP 400
export const ERR_SHIP_NOT_DOCKED = 4244; // errCodeShipNotDocked — HTTP 400

export function apiError(code: number, message: string, data?: Record<string, unknown>): ApiError {
  const error: ApiError['error'] = { message, code };
  if (data !== undefined) error.data = data;
  return { error };
}

export function sendError(reply: FastifyReply, httpStatus: number, code: number, message: string, data?: Record<string, unknown>): FastifyReply {
  return reply.code(httpStatus).send(apiError(code, message, data));
}
export function notFound(reply: FastifyReply, message: string): FastifyReply { return sendError(reply, 404, 404, message); }
export function badRequest(reply: FastifyReply, message: string): FastifyReply { return sendError(reply, 400, 400, message); }
export function unauthorized(reply: FastifyReply, message = 'Missing or invalid agent token.'): FastifyReply { return sendError(reply, 401, 401, message); }
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/errors.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/errors.ts twin/tests/skeleton/errors.test.ts
rtk git commit -m "feat(twin): error envelope helper + SpaceTraders code constants (4214/4244/4511)"
```

---

## Task 13 — In-memory world store `twin/src/world/store.ts`

**Files:** Create `twin/src/world/store.ts`. Test `twin/tests/skeleton/world-store.test.ts` (create).

**Interfaces:** Consumes `loadColdStartWorld`, `registerAgent` from `./loader` (Tasks 8/10). Produces `getWorld()`, `setWorld(world)`, `resetWorld()`. `resetWorld()` rebuilds cold-start but PRESERVES the registered agent's symbol/faction/token (so the seeded `players` row stays valid), then replaces the singleton. Consumed by every route handler.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/world-store.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { World, Ship } from '../../src/world/types';

// Hermetic: stub the loader so this exercises ONLY reset/preserve, not fixture I/O.
vi.mock('../../src/world/loader', () => ({
  loadColdStartWorld: (): World => ({
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  }),
  registerAgent: (world: World, args: { symbol: string; faction: string; token: string }) => {
    const agent = { accountId: `twin-account-${args.symbol}`, symbol: args.symbol, headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: args.faction };
    world.agent = agent;
    world.agentToken = args.token;
    world.ships = new Map<string, Ship>([[`${args.symbol}-1`, { symbol: `${args.symbol}-1` } as unknown as Ship]]);
    world.shipCounter = 2;
    return { agent, ships: [...world.ships.values()] };
  },
}));

import { getWorld, setWorld, resetWorld } from '../../src/world/store';

function coldWorld(): World {
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  };
}
afterEach(() => vi.clearAllMocks());

describe('world store — reset preserves agent identity + token', () => {
  it('getWorld returns the injected world without touching the loader', () => {
    setWorld(coldWorld());
    const w = getWorld();
    expect(w.agent).toBeNull(); expect(w.agentToken).toBeNull(); expect(w.ships.size).toBe(0);
  });
  it('resetWorld rebuilds cold-start but re-materializes the SAME agent + token', () => {
    const dirty = coldWorld();
    dirty.agent = { accountId: 'acct-TWINAGENT', symbol: 'TWINAGENT', headquarters: 'X1-PZ28-A1', credits: 42, startingFaction: 'COSMIC' };
    dirty.agentToken = 'jwt-preserve-me'; dirty.ships = new Map();
    setWorld(dirty);
    resetWorld();
    const w = getWorld();
    expect(w.agent?.symbol).toBe('TWINAGENT');
    expect(w.agent?.startingFaction).toBe('COSMIC');
    expect(w.agentToken).toBe('jwt-preserve-me');
    expect(w.agent?.credits).toBe(175000);
    expect(w.ships.size).toBe(1);
  });
  it('resetWorld on a never-registered world stays cold', () => {
    setWorld(coldWorld()); resetWorld();
    const w = getWorld();
    expect(w.agent).toBeNull(); expect(w.agentToken).toBeNull(); expect(w.ships.size).toBe(0);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/world-store.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/world/store.ts`:

```ts
import type { World } from './types.js';
import { loadColdStartWorld, registerAgent } from './loader.js';

/** The single in-memory world every route reads/mutates. Lazily built on first access. */
let current: World | null = null;

export function getWorld(): World {
  if (current === null) current = loadColdStartWorld();
  return current;
}

/** Replace the live world outright (buildServer({ world }) and tests). */
export function setWorld(world: World): void { current = world; }

/** POST /_twin/reset: rebuild cold-start from fixtures, PRESERVING the registered
 *  agent's symbol/faction/token so the seeded players row stays valid. Replaces the
 *  singleton — safe because no route captures a reference (all call getWorld()). */
export function resetWorld(): void {
  const prev = getWorld();
  const prevSymbol = prev.agent?.symbol ?? null;
  const prevFaction = prev.agent?.startingFaction ?? null;
  const prevToken = prev.agentToken;

  const fresh = loadColdStartWorld();
  if (prevSymbol !== null && prevToken !== null) {
    registerAgent(fresh, { symbol: prevSymbol, faction: prevFaction ?? 'COSMIC', token: prevToken });
  }
  current = fresh;
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/world-store.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/world/store.ts twin/tests/skeleton/world-store.test.ts
rtk git commit -m "feat(twin): in-memory world store with token-preserving reset"
```

---

## Task 14 — `buildServer()` + `GET /v2/` server status + `main.ts`

**Files:** Create `twin/src/routes/server-status.ts`, `twin/src/server.ts`, `twin/src/main.ts`. Test `twin/tests/skeleton/server.test.ts` (create).

**Interfaces:** Produces `serverStatusRoutes(app)` (the route-registration exemplar — serves `GET /` under the `/v2` prefix, UNWRAPPED `world.serverStatus`); `buildServer(opts?: { world?: World })` with the marked `/v2` extension block every endpoint task adds one line to; `start()` (listens on 127.0.0.1:8080). `main.ts` invokes `start()` for `npm run start`.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/server.test.ts`:

```ts
import { afterEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';

function coldWorld(): World {
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  };
}
let app: FastifyInstance;
afterEach(async () => { if (app) await app.close(); });

describe('buildServer — GET /v2/ server status (unwrapped)', () => {
  it('returns the UNWRAPPED server-status shape', async () => {
    app = buildServer({ world: coldWorld() });
    const res = await app.inject({ method: 'GET', url: '/v2/' });
    expect(res.statusCode).toBe(200);
    const body = res.json();
    expect(body).toEqual({ resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } });
    expect(body).not.toHaveProperty('data');
    expect(body.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });
  it('ignoreTrailingSlash: GET /v2 also resolves', async () => {
    app = buildServer({ world: coldWorld() });
    const res = await app.inject({ method: 'GET', url: '/v2' });
    expect(res.statusCode).toBe(200);
    expect(res.json().resetDate).toBe('2026-07-05');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/server.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/routes/server-status.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';

/** GET /v2/ — UNWRAPPED server status (server_status.go:19-25). Exemplar of the
 *  route-registration pattern every endpoint task copies. */
export async function serverStatusRoutes(app: FastifyInstance): Promise<void> {
  app.get('/', async () => getWorld().serverStatus);
}
```

Create `twin/src/server.ts`:

```ts
import Fastify, { type FastifyInstance } from 'fastify';
import type { World } from './world/types.js';
import { setWorld } from './world/store.js';
import { serverStatusRoutes } from './routes/server-status.js';

export interface BuildServerOptions { world?: World }

/** Compose the twin: the /v2 SpaceTraders API surface + the /_twin admin namespace.
 *  Every endpoint task adds its `await xxxRoutes(v2)` line in the marked block below. */
export function buildServer(opts: BuildServerOptions = {}): FastifyInstance {
  if (opts.world) setWorld(opts.world);

  const app = Fastify({ logger: false, ignoreTrailingSlash: true });

  app.register(
    async (v2) => {
      await serverStatusRoutes(v2);
      // ─── endpoint tasks register their /v2 route plugins here ─────────────
      // await registerRoutes(v2);       // Task 17  POST /register
      // await agentRoutes(v2);          // Task 18  GET /my/agent
      // await shipRoutes(v2);           // Task 20  GET /my/ships[/:s]
      // await waypointRoutes(v2);       // Task 21  GET /systems/:s/waypoints[/:w]
      // await marketRoutes(v2);         // Task 22  GET …/market
      // await shipyardRoutes(v2);       // Task 23  GET …/shipyard
      // await shipNavigateRoutes(v2);   // Task 24  POST …/navigate
      // await shipActionRoutes(v2);     // Task 25  POST …/orbit|dock|refuel
      // await myShipsPurchaseRoutes(v2);// Task 27  POST /my/ships
    },
    { prefix: '/v2' },
  );

  // /_twin admin namespace (Task 15 adds adminRoutes; Task 28 adds testAdminRoutes).
  // app.register(adminRoutes, { prefix: '/_twin' });
  // app.register(testAdminRoutes, { prefix: '/_twin' });

  return app;
}

/** Boot helper for `npm run start` / launch-test-stack.sh. */
export async function start(): Promise<FastifyInstance> {
  const app = buildServer();
  await app.listen({ port: 8080, host: '127.0.0.1' });
  return app;
}
```

Create `twin/src/main.ts`:

```ts
import { start } from './server.js';

start().catch((err) => {
  console.error('twin failed to start:', err);
  process.exit(1);
});
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/server.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/server-status.ts twin/src/server.ts twin/src/main.ts twin/tests/skeleton/server.test.ts
rtk git commit -m "feat(twin): buildServer() + GET /v2/ server status + route-registration pattern"
```

---

## Task 15 — `/_twin` admin routes: reset / state / time-compression

**Files:** Create `twin/src/routes/admin.ts`. Modify `twin/src/server.ts` (register `adminRoutes` under `/_twin`). Test `twin/tests/skeleton/admin.test.ts` (create).

**Interfaces:** Consumes `getWorld`, `resetWorld` (store); `getCompression`, `setCompression`, `resolveNav` (clock); `badRequest` (errors). Produces `TwinStateSummary`, `TwinState`, `adminRoutes(app)` registering `POST /reset`, `GET /state`, `POST /time-compression` (foundation §5.4). Consumed by every acceptance test.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/admin.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World, Ship, Waypoint, System, Market, Shipyard } from '../../src/world/types';

vi.mock('../../src/world/loader', () => ({
  loadColdStartWorld: (): World => ({
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
  }),
  registerAgent: (world: World, args: { symbol: string; faction: string; token: string }) => {
    const agent = { accountId: `twin-account-${args.symbol}`, symbol: args.symbol, headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: args.faction };
    world.agent = agent; world.agentToken = args.token;
    world.ships = new Map<string, Ship>([[`${args.symbol}-1`, { symbol: `${args.symbol}-1` } as unknown as Ship]]);
    world.shipCounter = 2;
    return { agent, ships: [...world.ships.values()] };
  },
}));

import { buildServer } from '../../src/server';

function registeredWorld(): World {
  const waypoints = new Map<string, Waypoint>([
    ['X1-PZ28-A1', { symbol: 'X1-PZ28-A1' } as unknown as Waypoint],
    ['X1-PZ28-A2', { symbol: 'X1-PZ28-A2' } as unknown as Waypoint],
  ]);
  const systems = new Map<string, System>([['X1-PZ28', { symbol: 'X1-PZ28', waypoints }]]);
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: { accountId: 'acct-TWINAGENT', symbol: 'TWINAGENT', headquarters: 'X1-PZ28-A1', credits: 175000, startingFaction: 'COSMIC' },
    agentToken: 'jwt-preserve-me',
    ships: new Map<string, Ship>([['TWINAGENT-1', { symbol: 'TWINAGENT-1' } as unknown as Ship]]),
    systems,
    markets: new Map<string, Market>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', exports: [], imports: [], exchange: [], tradeGoods: [] }]]),
    shipyards: new Map<string, Shipyard>([['X1-PZ28-A1', { symbol: 'X1-PZ28-A1', shipTypes: [], ships: [], transactions: [], modificationsFee: 0 }]]),
    transits: new Map(), shipCounter: 2,
  };
}

let app: FastifyInstance;
afterEach(async () => { if (app) await app.close(); vi.clearAllMocks(); });

describe('/_twin admin routes', () => {
  it('GET /_twin/state returns the foundation TwinState shape', async () => {
    app = buildServer({ world: registeredWorld() });
    const res = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(res.statusCode).toBe(200);
    const s = res.json();
    expect(s.agent.symbol).toBe('TWINAGENT'); expect(s.agent.credits).toBe(175000);
    expect(Array.isArray(s.ships)).toBe(true); expect(s.ships).toHaveLength(1);
    expect(Array.isArray(s.transits)).toBe(true); expect(s.transits).toHaveLength(0);
    expect(s.markets['X1-PZ28-A1'].symbol).toBe('X1-PZ28-A1');
    expect(s.shipyards['X1-PZ28-A1'].symbol).toBe('X1-PZ28-A1');
    expect(s.waypointCount).toBe(2);
    expect(typeof s.compression).toBe('number'); expect(s.compression).toBeGreaterThan(0);
    expect(s.now).toMatch(/^\d{4}-\d{2}-\d{2}T/);
  });
  it('POST /_twin/time-compression validates >0 and updates the live factor', async () => {
    app = buildServer({ world: registeredWorld() });
    const ok = await app.inject({ method: 'POST', url: '/_twin/time-compression', payload: { compression: 250 } });
    expect(ok.statusCode).toBe(200);
    expect(ok.json()).toEqual({ ok: true, compression: 250 });
    const st = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(st.json().compression).toBe(250);
  });
  it('POST /_twin/time-compression rejects <= 0 with the error envelope', async () => {
    app = buildServer({ world: registeredWorld() });
    const res = await app.inject({ method: 'POST', url: '/_twin/time-compression', payload: { compression: 0 } });
    expect(res.statusCode).toBe(400);
    expect(res.json().error.code).toBe(400);
  });
  it('POST /_twin/reset rebuilds cold-start but preserves agent symbol + token', async () => {
    const dirty = registeredWorld(); dirty.agent!.credits = 3; dirty.ships = new Map();
    app = buildServer({ world: dirty });
    const res = await app.inject({ method: 'POST', url: '/_twin/reset' });
    expect(res.statusCode).toBe(200);
    const body = res.json();
    expect(body.ok).toBe(true);
    expect(body.world.agent.symbol).toBe('TWINAGENT');
    expect(body.world.shipCount).toBe(1);
    const st = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(st.json().agent.credits).toBe(175000);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/admin.test.ts
```
Expected: `GET /_twin/state` 404 (namespace not registered).

- [ ] **Step 3 — implement.** Create `twin/src/routes/admin.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import type { Agent, Market, Shipyard, Ship, TransitState } from '../world/types.js';
import { getWorld, resetWorld } from '../world/store.js';
import { getCompression, setCompression, resolveNav } from '../clock.js';
import { badRequest } from '../errors.js';

export interface TwinStateSummary { agent: Agent | null; shipCount: number; compression: number }

export interface TwinState {
  compression: number;
  agent: Agent | null;
  ships: Ship[];               // nav ALREADY passed through resolveNav
  transits: TransitState[];    // in-flight only
  markets: Record<string, Market>;
  shipyards: Record<string, Shipyard>;
  waypointCount: number;
  now: string;
}

function summarize(): TwinStateSummary {
  const w = getWorld();
  return { agent: w.agent, shipCount: w.ships.size, compression: getCompression() };
}

export async function adminRoutes(app: FastifyInstance): Promise<void> {
  app.post('/reset', async () => {
    resetWorld();
    return { ok: true, world: summarize() };
  });

  app.get('/state', async () => {
    const w = getWorld();
    const now = new Date();
    const ships = [...w.ships.values()].map((ship) => resolveNav(ship, w.transits.get(ship.symbol), now));
    const transits = [...w.transits.values()].filter((t) => new Date(t.arrival) > now);
    let waypointCount = 0;
    for (const sys of w.systems.values()) waypointCount += sys.waypoints.size;
    const state: TwinState = {
      compression: getCompression(),
      agent: w.agent, ships, transits,
      markets: Object.fromEntries(w.markets),
      shipyards: Object.fromEntries(w.shipyards),
      waypointCount, now: now.toISOString(),
    };
    return state;
  });

  app.post<{ Body: { compression?: unknown } }>('/time-compression', async (req, reply) => {
    const raw = req.body?.compression;
    const factor = typeof raw === 'number' ? raw : Number(raw);
    if (!Number.isFinite(factor) || factor <= 0) {
      return badRequest(reply, `compression must be a number > 0, got ${JSON.stringify(raw)}`);
    }
    setCompression(factor);
    return { ok: true, compression: factor };
  });
}
```

In `twin/src/server.ts`, add `import { adminRoutes } from './routes/admin.js';` and replace the commented admin registration with the live one before `return app;`:

```ts
  app.register(adminRoutes, { prefix: '/_twin' });
  // app.register(testAdminRoutes, { prefix: '/_twin' }); // Task 28

  return app;
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/admin.test.ts
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/skeleton/
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/admin.ts twin/src/server.ts twin/tests/skeleton/admin.test.ts
rtk git commit -m "feat(twin): /_twin admin routes — reset (token-preserving), state, time-compression"
```

---

## Task 16 — Test harness: `daemon.ts` lifecycle + `global-setup.ts`

**Files:** Create `twin/tests/helpers/daemon.ts`, `twin/tests/global-setup.ts`. Test `twin/tests/skeleton/harness-smoke.test.ts` (create — a live smoke run under the default config that just asserts `GET /_twin/state` after globalSetup).

**Interfaces:** Consumes `buildServer` (Task 14), the scripts/config (Tasks 2/6/7), `run-cli.ts` constants. Produces:
- `daemon.ts`: `startTestDaemon(extraEnv?)`, `stopTestDaemon()`, `restartTestDaemon(extraEnv?)` — spawn/kill the isolated test daemon on the -test slot, waiting pidfile + gRPC-ready; `extraEnv` overlaid last (lets the E2E point `SPACETRADERS_CONFIG` at the bootstrap config).
- `global-setup.ts`: default-config `globalSetup` that boots the in-process twin, ensures test Postgres on :5433, boots the test daemon (AutoMigrate), seeds `TWINAGENT` via `player register`, and returns a teardown. Consumed by every CLI-driven acceptance test.

- [ ] **Step 1 — failing test.** Create `twin/tests/skeleton/harness-smoke.test.ts`:

```ts
// This smoke test runs under the DEFAULT vitest config (globalSetup boots the stack).
// It is excluded from the unit config; run it via `rtk npx vitest run tests/skeleton/harness-smoke.test.ts`
// only once the twin server + scripts exist and a test Postgres is up on :5433.
import { describe, expect, it } from 'vitest';
import { TWIN_ADMIN } from '../helpers/run-cli';

describe('harness globalSetup smoke', () => {
  it('the twin is serving and the world holds the seeded agent', async () => {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    expect(res.status).toBe(200);
    const s = (await res.json()) as { agent: { symbol: string } | null };
    expect(s.agent?.symbol).toBe('TWINAGENT');
  });
});
```

Note: `harness-smoke.test.ts` lives under `tests/skeleton/**` which the DEFAULT config excludes. Move it (or run it explicitly) under the default config: add a one-off include override on the CLI, or place it at `tests/harness-smoke.test.ts`. Canonical: place the file at **`twin/tests/harness-smoke.test.ts`** so the default config's `tests/**/*.test.ts` include + globalSetup picks it up. (The path in this task is corrected to `twin/tests/harness-smoke.test.ts`.)

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/harness-smoke.test.ts
```
Expected: globalSetup module missing / twin not booted → the run errors before assertions.

- [ ] **Step 3 — implement.** Create `twin/tests/helpers/daemon.ts`:

```ts
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import net from 'node:net';
import { DAEMON_BIN, GOBOT_DIR, TEST_CONFIG, TEST_DATABASE_URL, TWIN_BASE_URL } from './run-cli.js';

const TEST_PID_FILE = '/tmp/spacetraders-daemon-test.pid';
const GRPC_HOST = 'localhost';
const GRPC_PORT = 50062;

let daemon: ChildProcess | undefined;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function tcpOpen(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ host, port }, () => { sock.destroy(); resolve(true); });
    sock.on('error', () => { sock.destroy(); resolve(false); });
    sock.setTimeout(500, () => { sock.destroy(); resolve(false); });
  });
}

async function waitReady(timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(TEST_PID_FILE) && (await tcpOpen(GRPC_HOST, GRPC_PORT))) return;
    await sleep(300);
  }
  throw new Error(`test daemon not ready within ${timeoutMs}ms (pidfile ${TEST_PID_FILE} / gRPC ${GRPC_HOST}:${GRPC_PORT})`);
}

/** Spawn the isolated test daemon on the -test slot. `extraEnv` overlays LAST. */
export async function startTestDaemon(extraEnv: Record<string, string> = {}): Promise<void> {
  daemon = spawn(DAEMON_BIN, [], {
    cwd: GOBOT_DIR,
    stdio: 'ignore',
    env: {
      ...process.env,
      SPACETRADERS_CONFIG: TEST_CONFIG,
      ST_API_BASE_URL: TWIN_BASE_URL,
      DATABASE_URL: TEST_DATABASE_URL,
      ...extraEnv,
    },
  });
  daemon.unref?.();
  await waitReady();
}

async function waitPidfileGone(timeoutMs = 20_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!existsSync(TEST_PID_FILE)) return;
    await sleep(250);
  }
}

/** SIGTERM the test daemon via the -test pidfile (never --force, never prod). */
export async function stopTestDaemon(): Promise<void> {
  if (daemon && !daemon.killed) daemon.kill('SIGTERM');
  daemon = undefined;
  await waitPidfileGone();
}

export async function restartTestDaemon(extraEnv: Record<string, string> = {}): Promise<void> {
  await stopTestDaemon();
  await startTestDaemon(extraEnv);
}
```

Create `twin/tests/global-setup.ts`:

```ts
import net from 'node:net';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../src/server.js';
import { runCli, TWIN_BASE_URL } from './helpers/run-cli.js';
import { startTestDaemon, stopTestDaemon } from './helpers/daemon.js';

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function tcpOpen(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ host, port }, () => { sock.destroy(); resolve(true); });
    sock.on('error', () => { sock.destroy(); resolve(false); });
    sock.setTimeout(500, () => { sock.destroy(); resolve(false); });
  });
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  // 1. Boot the twin in-process (fixtures loaded lazily by the store).
  const app: FastifyInstance = buildServer();
  await app.listen({ port: 8080, host: '127.0.0.1' });
  {
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      try { if ((await fetch(`${TWIN_BASE_URL}/`)).status === 200) break; } catch { /* not ready */ }
      await sleep(200);
    }
  }

  // 2. Ensure the test Postgres is reachable on :5433 (fail fast with a hint).
  if (!(await tcpOpen('localhost', 5433))) {
    await app.close();
    throw new Error('test Postgres not reachable on localhost:5433 — start it first: docker compose -f twin/docker-compose.test.yml up -d postgres-test');
  }

  // 3. Boot the isolated test daemon (AutoMigrate on first boot).
  await startTestDaemon();

  // 4. Seed TWINAGENT through the hardened CLI (idempotent: skip if an OPEN era exists).
  const reg = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
  if (reg.exitCode !== 0 && !/an OPEN era/.test(`${reg.stdout}\n${reg.stderr}`)) {
    await stopTestDaemon(); await app.close();
    throw new Error(`globalSetup seed failed (exit ${reg.exitCode}): ${reg.stderr}`);
  }

  // Teardown.
  return async () => {
    await stopTestDaemon();
    await app.close();
  };
}
```

- [ ] **Step 4 — run, expect PASS.** (Requires the test Postgres up on :5433 and the CLI/daemon built.)
```bash
docker compose -f twin/docker-compose.test.yml up -d postgres-test  # or: createdb, once Task 33 lands
make -C gobot build-cli build-daemon
cd twin && rtk npx vitest run tests/harness-smoke.test.ts
```
Expected: PASS — `GET /_twin/state` reports agent `TWINAGENT`.

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/tests/helpers/daemon.ts twin/tests/global-setup.ts twin/tests/harness-smoke.test.ts
rtk git commit -m "test(twin): globalSetup (boot twin+daemon, seed TWINAGENT) + daemon lifecycle helpers"
```

---

## Task 17 — `POST /v2/register` route + CLI acceptance

**Files:** Create `twin/src/routes/register.ts`. Modify `twin/src/server.ts` (add `await registerRoutes(v2)`). Test `twin/tests/unit/register-route.test.ts` (in-process, unit config) and `twin/tests/register.test.ts` (CLI acceptance, default config).

**Reconciliation:** Plugin `registerRoutes(app)` reading `getWorld()` (not the `30b` `registerRoute(app, world)`). Mints the token via `mintToken`, materializes via `registerAgent`, replies `201 { data: { token, agent, ships } }`.

**Interfaces:** Consumes `getWorld` (store), `mintToken`, `registerAgent` (loader), `unauthorized`, `badRequest` (errors). Wire contract (register.go): request `{ symbol, faction }` + `Authorization: Bearer <accountToken>`; twin does not validate the account token (any non-empty passes).

- [ ] **Step 1a — failing in-process test.** Create `twin/tests/unit/register-route.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../../src/server';
import { mintToken } from '../../src/world/loader';

describe('POST /v2/register (buildServer wiring)', () => {
  let app: FastifyInstance;
  beforeEach(async () => { app = buildServer(); await app.ready(); });
  afterEach(async () => { await app.close(); });

  it('mints the cold-start agent and returns { data: { token, agent, ships } }', async () => {
    const res = await app.inject({
      method: 'POST', url: '/v2/register',
      headers: { authorization: 'Bearer twin-test-account-token' },
      payload: { symbol: 'TWINAGENT', faction: 'COSMIC' },
    });
    expect(res.statusCode).toBe(201);
    const body = res.json() as { data: { token: string; agent: { symbol: string; credits: number; headquarters: string; startingFaction: string }; ships: Array<{ symbol: string; registration: { role: string } }> } };
    expect(body.data.token).toBe(mintToken('TWINAGENT'));
    expect(body.data.agent).toMatchObject({ symbol: 'TWINAGENT', credits: 175000, headquarters: 'X1-PZ28-A1', startingFaction: 'COSMIC' });
    expect(body.data.ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(body.data.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });

  it('mutates the world (GET /_twin/state reports the cold-start agent + 2 ships)', async () => {
    await app.inject({ method: 'POST', url: '/v2/register', headers: { authorization: 'Bearer x' }, payload: { symbol: 'TWINAGENT', faction: 'COSMIC' } });
    const state = (await app.inject({ method: 'GET', url: '/_twin/state' })).json() as { agent: { symbol: string; credits: number } | null; ships: unknown[] };
    expect(state.agent?.symbol).toBe('TWINAGENT');
    expect(state.agent?.credits).toBe(175000);
    expect(state.ships).toHaveLength(2);
  });

  it('rejects a request with no Authorization header (401)', async () => {
    const res = await app.inject({ method: 'POST', url: '/v2/register', payload: { symbol: 'TWINAGENT', faction: 'COSMIC' } });
    expect(res.statusCode).toBe(401);
    expect((res.json() as { error: { code: number } }).error.code).toBe(401);
  });
});
```

- [ ] **Step 1b — CLI acceptance test.** Create `twin/tests/register.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { beforeAll, describe, expect, it } from 'vitest';
import { REPO_ROOT, TEST_DATABASE_URL, TWIN_ADMIN, runCli } from './helpers/run-cli';
import { mintToken } from '../src/world/loader';

const RESET_DATE = (JSON.parse(
  readFileSync(path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'server-status.json'), 'utf8'),
) as { resetDate: string }).resetDate; // captured golden (e.g. 2026-07-05)
const ERA_NAME = `twinagent-${RESET_DATE}`;

function psql(sql: string): string {
  const res = spawnSync('psql', [TEST_DATABASE_URL, '-tA', '-c', sql], { encoding: 'utf8', timeout: 10_000 });
  if (res.status !== 0) throw new Error(`psql failed (exit ${res.status}): ${res.stderr}`);
  return (res.stdout ?? '').trim();
}

interface TwinState { agent: { symbol: string; credits: number; headquarters: string; startingFaction: string } | null; ships: Array<{ symbol: string; registration: { role: string } }>; }
async function twinState(): Promise<TwinState> {
  const res = await fetch(`${TWIN_ADMIN}/state`);
  expect(res.status).toBe(200);
  return (await res.json()) as TwinState;
}

describe('POST /v2/register via `spacetraders player register --new`', () => {
  // Fresh-DB slate so the era guard does not reject; RESTART IDENTITY re-mints id 1.
  beforeAll(() => {
    const res = spawnSync('psql', [TEST_DATABASE_URL, '-c', 'TRUNCATE players, eras RESTART IDENTITY CASCADE;'], { encoding: 'utf8', timeout: 10_000 });
    if (res.status !== 0) throw new Error(`beforeAll TRUNCATE failed (exit ${res.status}): ${res.stderr}`);
  });

  it('registers the cold-start agent: CLI + DB (contract) and world (behavior)', async () => {
    const r = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
    expect(r.exitCode, r.stderr).toBe(0);
    expect(r.stdout).toContain('✓ New agent registered');
    expect(r.stdout).toContain('Agent Symbol: TWINAGENT');
    expect(r.stdout).toContain('Player ID:    1');
    expect(r.stdout).toContain(`Era:          ${ERA_NAME}`);

    const playerRow = psql("SELECT id, agent_symbol, token FROM players WHERE agent_symbol = 'TWINAGENT';");
    const [id, symbol, token] = playerRow.split('|');
    expect(id).toBe('1'); expect(symbol).toBe('TWINAGENT'); expect(token).toBe(mintToken('TWINAGENT'));

    const eraRow = psql("SELECT name, closed_at FROM eras WHERE agent_symbol = 'TWINAGENT';");
    const [eraName, closedAt] = eraRow.split('|');
    expect(eraName).toBe(ERA_NAME); expect(closedAt).toBe('');

    const state = await twinState();
    expect(state.agent).toMatchObject({ symbol: 'TWINAGENT', credits: 175000, headquarters: 'X1-PZ28-A1', startingFaction: 'COSMIC' });
    expect(state.ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(state.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/unit/register-route.test.ts
```
Expected: `POST /v2/register` 404 (route unwired).

- [ ] **Step 3 — implement.** Create `twin/src/routes/register.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { mintToken, registerAgent } from '../world/loader.js';
import { badRequest, unauthorized } from '../errors.js';

/** POST /v2/register — mint the cold-start agent. Request { symbol, faction } + Bearer
 *  <accountToken> (not validated; any non-empty passes). Reply 201 { data: { token, agent, ships } }. */
export async function registerRoutes(app: FastifyInstance): Promise<void> {
  app.post('/register', async (req, reply) => {
    const auth = (req.headers.authorization ?? '').trim();
    if (auth === '') return unauthorized(reply, 'Account token required.');
    const body = (req.body ?? {}) as { symbol?: string; faction?: string };
    if (!body.symbol || body.symbol.trim() === '') return badRequest(reply, 'symbol is required.');
    const faction = body.faction && body.faction.trim() !== '' ? body.faction : 'COSMIC';
    const world = getWorld();
    const token = mintToken(body.symbol);
    const { agent, ships } = registerAgent(world, { symbol: body.symbol, faction, token });
    return reply.code(201).send({ data: { token, agent, ships } });
  });
}
```

In `twin/src/server.ts`, add `import { registerRoutes } from './routes/register.js';` and uncomment `await registerRoutes(v2);` in the `/v2` block.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/unit/register-route.test.ts
cd twin && rtk npx vitest run tests/register.test.ts   # CLI acceptance (default config; stack up)
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/register.ts twin/src/server.ts twin/tests/unit/register-route.test.ts twin/tests/register.test.ts
rtk git commit -m "feat(twin): POST /v2/register route (mintToken + registerAgent) + CLI acceptance"
```

---

## Task 18 — `GET /v2/my/agent` (`{ data: Agent }`) + bearer-auth guard

**Files:** Create `twin/src/routes/agent.ts`. Modify `twin/src/server.ts` (add `await agentRoutes(v2)`). Test `twin/tests/agent.test.ts`, `twin/tests/agent-auth.test.ts` (create).

**Reconciliation:** Plugin `agentRoutes(app)` (named export, not `30c`'s default `agentRoute`) reading `getWorld()`. Bearer-guarded from the start (30c.2 + 30c.3 merged): `Authorization: Bearer <token>` must equal `world.agentToken`; missing/mismatch → 401 envelope.

**Interfaces:** Consumes `getWorld` (store), `unauthorized` (errors), `runCli`, `TWIN_BASE_URL`, `TWIN_ADMIN` (helpers). Golden cold-start agent (from `register.json`): symbol `TWINAGENT`, HQ `X1-PZ28-A1`, credits 175000, faction `COSMIC`. Driven by `spacetraders player info`.

- [ ] **Step 1a — contract test.** Create `twin/tests/agent.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, TWIN_ADMIN } from './helpers/run-cli';

const AGENT = 'TWINAGENT'; const HQ = 'X1-PZ28-A1'; const CREDITS = 175000; const FACTION = 'COSMIC';

function agentToken(): string {
  const { stdout, stderr, exitCode } = runCli(['player', 'info', '--agent', AGENT, '--show-token']);
  expect(exitCode, `player info --show-token failed:\n${stderr}\n${stdout}`).toBe(0);
  const m = /Token:\s*(\S+)/.exec(stdout);
  if (!m) throw new Error(`no token in player info output:\n${stdout}`);
  return m[1];
}

describe('GET /v2/my/agent — agent treasury (player info)', () => {
  beforeEach(async () => {
    const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
    expect(res.status).toBe(200);
  });

  it('player info prints live credits + symbol decoded from GET /my/agent', () => {
    const { stdout, stderr, exitCode } = runCli(['player', 'info', '--agent', AGENT]);
    expect(exitCode, `stderr:\n${stderr}\nstdout:\n${stdout}`).toBe(0);
    expect(stdout).toMatch(new RegExp(`Agent Symbol: +${AGENT}`));
    expect(stdout).toMatch(new RegExp(`Credits: +${CREDITS}`));
  });

  it('GET /my/agent returns { data: Agent } field-for-field, matching /_twin/state', async () => {
    const token = agentToken();
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: `Bearer ${token}` } });
    expect(res.status).toBe(200);
    const a = ((await res.json()) as { data: { accountId: string; symbol: string; headquarters: string; credits: number; startingFaction: string } }).data;
    expect(a.symbol).toBe(AGENT); expect(a.headquarters).toBe(HQ); expect(a.credits).toBe(CREDITS); expect(a.startingFaction).toBe(FACTION);
    expect(typeof a.accountId).toBe('string'); expect(a.accountId.length).toBeGreaterThan(0);
    const state = (await (await fetch(`${TWIN_ADMIN}/state`)).json()) as { agent: typeof a };
    expect(a).toEqual(state.agent);
  });
});
```

- [ ] **Step 1b — auth test.** Create `twin/tests/agent-auth.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL } from './helpers/run-cli';

const AGENT = 'TWINAGENT';
function agentToken(): string {
  const { stdout, exitCode } = runCli(['player', 'info', '--agent', AGENT, '--show-token']);
  expect(exitCode).toBe(0);
  return /Token:\s*(\S+)/.exec(stdout)![1];
}

describe('GET /v2/my/agent — bearer auth guard', () => {
  it('401 + envelope when the Authorization header is missing', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`);
    expect(res.status).toBe(401);
    const body = (await res.json()) as { error: { message: string; code: number }; data?: unknown };
    expect(body.data).toBeUndefined(); expect(body.error.code).toBe(401);
    expect(body.error.message.length).toBeGreaterThan(0);
  });
  it('401 when the bearer token does not match world.agentToken', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: 'Bearer not-the-agent-token' } });
    expect(res.status).toBe(401);
    expect(((await res.json()) as { error: { code: number } }).error.code).toBe(401);
  });
  it('200 with the correct bearer token', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: `Bearer ${agentToken()}` } });
    expect(res.status).toBe(200);
    expect(((await res.json()) as { data: { symbol: string } }).data.symbol).toBe(AGENT);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/agent.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/routes/agent.ts`:

```ts
import type { FastifyInstance, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { unauthorized } from '../errors.js';

function bearerToken(request: FastifyRequest): string | null {
  const header = request.headers.authorization;
  if (typeof header !== 'string') return null;
  const match = /^Bearer\s+(.+)$/.exec(header);
  return match ? match[1] : null;
}

/** GET /v2/my/agent — { data: Agent }. Bearer token must equal world.agentToken. */
export async function agentRoutes(app: FastifyInstance): Promise<void> {
  app.get('/my/agent', async (request, reply) => {
    const world = getWorld();
    const token = bearerToken(request);
    if (world.agentToken === null || token !== world.agentToken) {
      return unauthorized(reply, 'Missing or invalid agent token.');
    }
    return reply.status(200).send({ data: world.agent });
  });
}
```

In `twin/src/server.ts`, add `import { agentRoutes } from './routes/agent.js';` and uncomment `await agentRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/agent.test.ts tests/agent-auth.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/agent.ts twin/src/server.ts twin/tests/agent.test.ts twin/tests/agent-auth.test.ts
rtk git commit -m "feat(twin): GET /v2/my/agent — { data: Agent } treasury endpoint + bearer-auth guard"
```

---

## Task 19 — `universe status` acceptance for `GET /v2/`

**Files:** Test `twin/tests/server-status.test.ts` (create). No new route — `GET /v2/` is served by the skeleton's `serverStatusRoutes` (Task 14).

**Reconciliation:** `30c.1`'s duplicate `status.ts`/`statusRoute` is DROPPED. This task keeps only its CLI acceptance, reading golden values from the committed `server-status.json` (not hardcoding `2026-06-29`).

**Interfaces:** Consumes `runCli`, `TWIN_BASE_URL`, `REPO_ROOT`. Driven by `spacetraders universe status`.

- [ ] **Step 1 — test.** Create `twin/tests/server-status.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from './helpers/run-cli';

const golden = JSON.parse(
  readFileSync(path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'server-status.json'), 'utf8'),
) as { resetDate: string; serverResets: { next: string; frequency: string } };

describe('GET /v2/ — server status (universe status)', () => {
  it('universe status parses GET / and reports in sync (exit 0)', () => {
    const { stdout, stderr, exitCode } = runCli(['universe', 'status']);
    expect(exitCode, `stderr:\n${stderr}\nstdout:\n${stdout}`).toBe(0);
    expect(stdout).toMatch(new RegExp(`Server resetDate +${golden.resetDate}`));
    expect(stdout).toMatch(new RegExp(`Next reset +${golden.serverResets.next.replace(/[.]/g, '\\.')} \\(${golden.serverResets.frequency}\\)`));
    expect(stdout).toMatch(/State +in sync/);
  });

  it('serves GET /v2/ UNWRAPPED with a bare-date resetDate and RFC3339 next reset', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data?: unknown; resetDate: string; serverResets: { next: string; frequency: string } };
    expect(body.data).toBeUndefined();
    expect(body.resetDate).toBe(golden.resetDate);
    expect(body.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(body.serverResets).toEqual(golden.serverResets);
    expect(Number.isNaN(Date.parse(body.serverResets.next))).toBe(false);
  });
});
```

- [ ] **Step 2 — run, expect PASS** (the route already exists from the skeleton; this is a green acceptance that pins the contract):
```bash
cd twin && rtk npx vitest run tests/server-status.test.ts
```
If it fails, the seeded era's `UniverseResetDate` disagrees with the fixture — re-seed against the current fixture (both derive from the same `GET /`).

- [ ] **Step 3 — commit.**
```bash
rtk git add twin/tests/server-status.test.ts
rtk git commit -m "test(twin): universe status acceptance for GET /v2/ (fixture-golden, in-sync)"
```

---

## Task 20 — `GET /v2/my/ships` (paginated) + `GET /v2/my/ships/:s`

**Files:** Create `twin/src/routes/ships.ts`. Modify `twin/src/server.ts` (add `await shipRoutes(v2)`). Test `twin/tests/ships/list.test.ts`, `twin/tests/ships/show.test.ts` (create).

**Reconciliation:** `shipRoutes(app)` plugin reading `getWorld()`, relative paths `/my/ships` + `/my/ships/:symbol`. Both handlers pass every ship's `nav` through `resolveNav` (invariant 3) and Bearer-auth against `world.agentToken`.

**Driver reality:** `ship list` reads the daemon cache filled by the paginated `GET /my/ships` sync on daemon boot — the list test calls `restartTestDaemon()` so the boot sync repopulates; exactly two rows proves the empty past-end page (HTTP 200) terminates the loop. `ship refresh` forces a fresh `GET /my/ships/{s}`.

- [ ] **Step 1a — list test.** Create `twin/tests/ships/list.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, runCli } from '../helpers/run-cli';
import { restartTestDaemon } from '../helpers/daemon';
import type { Agent, Ship } from '../../src/world/types';

const AGENT = 'TWINAGENT';
const FIXTURE_DIR = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28');
interface RegisterTemplate { headquarters: string; ships: Ship[] }
const tpl = JSON.parse(readFileSync(path.join(FIXTURE_DIR, 'register.json'), 'utf8')) as RegisterTemplate;
function expectedStartingShips(agent: string): Ship[] {
  return tpl.ships.map((s) => ({ ...s, symbol: s.symbol.replace('{AGENT}', agent) }));
}
interface TwinState { ships: Ship[]; agent: Agent | null }
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  expect(res.status, 'POST /_twin/reset').toBe(200);
}
async function twinState(): Promise<TwinState> {
  const res = await fetch(`${TWIN_ADMIN}/state`); expect(res.status).toBe(200); return (await res.json()) as TwinState;
}
function listShipsJson(): Array<Record<string, unknown>> {
  const res = runCli(['ship', 'list', '--json', '--agent', AGENT]);
  if (res.exitCode !== 0) throw new Error(`ship list failed (exit ${res.exitCode}): ${res.stderr}`);
  const out = res.stdout.trim();
  if (out === '' || out.startsWith('No ships found')) return [];
  return JSON.parse(out) as Array<Record<string, unknown>>;
}
async function listShipsAfterSync(): Promise<Array<Record<string, unknown>>> {
  for (let i = 0; i < 12; i++) { const rows = listShipsJson(); if (rows.length >= 2) return rows; await new Promise((r) => setTimeout(r, 500)); }
  return listShipsJson();
}

describe('GET /v2/my/ships — paginated fleet snapshot', () => {
  beforeEach(async () => { await resetWorld(); });
  it('serves both starting hulls to the real client fleet sync (page 1 full, page 2 empty/200)', async () => {
    await restartTestDaemon();
    const rows = await listShipsAfterSync();
    expect(rows.length, `ship list rows: ${JSON.stringify(rows)}`).toBe(2);
    const bySymbol = Object.fromEntries(rows.map((r) => [r.symbol as string, r]));
    for (const exp of expectedStartingShips(AGENT)) {
      const row = bySymbol[exp.symbol];
      expect(row, `${exp.symbol} missing`).toBeTruthy();
      expect(row.location).toBe(tpl.headquarters);
      expect(row.navStatus).toBe('DOCKED');
      expect(row.fuelCurrent).toBe(exp.fuel.current);
      expect(row.fuelCapacity).toBe(exp.fuel.capacity);
      expect(row.cargoUnits).toBe(exp.cargo.units);
      expect(row.cargoCapacity).toBe(exp.cargo.capacity);
      expect(row.engineSpeed).toBe(exp.engine.speed);
    }
    const state = await twinState();
    expect(state.ships.length).toBe(2);
    expect(state.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  }, 90_000);
});
```

- [ ] **Step 1b — show test.** Create `twin/tests/ships/show.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, runCli } from '../helpers/run-cli';
import type { Agent, Ship } from '../../src/world/types';

const AGENT = 'TWINAGENT';
const FIXTURE_DIR = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28');
interface RegisterTemplate { headquarters: string; ships: Ship[] }
const tpl = JSON.parse(readFileSync(path.join(FIXTURE_DIR, 'register.json'), 'utf8')) as RegisterTemplate;
function expectedStartingShips(agent: string): Ship[] { return tpl.ships.map((s) => ({ ...s, symbol: s.symbol.replace('{AGENT}', agent) })); }
interface TwinState { ships: Ship[]; agent: Agent | null }
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  expect(res.status).toBe(200);
}
async function twinState(): Promise<TwinState> { const res = await fetch(`${TWIN_ADMIN}/state`); expect(res.status).toBe(200); return (await res.json()) as TwinState; }
function escapeRe(s: string): string { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

describe('GET /v2/my/ships/{s} — single-ship state', () => {
  beforeEach(async () => { await resetWorld(); });
  it('ship refresh drives a fresh GET /my/ships/{s}, decoded field-for-field', async () => {
    const [cmd] = expectedStartingShips(AGENT);
    const res = runCli(['ship', 'refresh', '--ship', cmd.symbol, '--agent', AGENT]);
    expect(res.exitCode, res.stderr).toBe(0);
    const out = res.stdout;
    expect(out).toMatch(new RegExp(`Ship Symbol:\\s+${escapeRe(cmd.symbol)}\\b`));
    expect(out).toMatch(new RegExp(`Location:\\s+${escapeRe(tpl.headquarters)}\\b`));
    expect(out).toMatch(/Nav Status:\s+DOCKED\b/);
    expect(out).toMatch(new RegExp(`Fuel:\\s+${cmd.fuel.current} / ${cmd.fuel.capacity}\\b`));
    expect(out).toMatch(new RegExp(`Cargo:\\s+${cmd.cargo.units} / ${cmd.cargo.capacity} units`));
    expect(out).toMatch(new RegExp(`Engine Speed:\\s+${cmd.engine.speed}\\b`));
    const state = await twinState();
    const s = state.ships.find((x) => x.symbol === cmd.symbol) as Ship;
    expect(s.nav.systemSymbol).toBe('X1-PZ28'); expect(s.nav.waypointSymbol).toBe(tpl.headquarters);
    expect(s.nav.status).toBe('DOCKED'); expect(s.nav.flightMode).toBe('CRUISE');
    expect(s.cargo.inventory).toEqual(cmd.cargo.inventory); expect(s.frame.symbol).toBe(cmd.frame.symbol);
  });
  it('404s for an unknown ship', () => {
    const res = runCli(['ship', 'refresh', '--ship', `${AGENT}-404`, '--agent', AGENT]);
    expect(res.exitCode, 'refresh of a nonexistent ship must fail').not.toBe(0);
    expect(`${res.stdout}\n${res.stderr}`).toMatch(/not found|404/i);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/ships/list.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/routes/ships.ts`:

```ts
import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { resolveNav } from '../clock.js';
import { notFound, unauthorized } from '../errors.js';
import type { Ship } from '../world/types.js';

const DEFAULT_LIMIT = 20;
const MAX_LIMIT = 20;

function authFailed(request: FastifyRequest, reply: FastifyReply): boolean {
  const world = getWorld();
  const header = request.headers.authorization;
  const token = typeof header === 'string' && header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
  if (!world.agentToken || token !== world.agentToken) { unauthorized(reply, 'Invalid or missing agent token.'); return true; }
  return false;
}
function intParam(raw: unknown, def: number, min: number, max: number): number {
  const n = Number.parseInt(typeof raw === 'string' ? raw : '', 10);
  if (!Number.isFinite(n) || n < min) return def;
  return n > max ? max : n;
}

export async function shipRoutes(app: FastifyInstance): Promise<void> {
  // GET /my/ships?page&limit — paginated; a page past the end returns { data: [], meta } HTTP 200.
  app.get('/my/ships', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = new Date();
    const q = request.query as Record<string, unknown>;
    const page = intParam(q.page, 1, 1, Number.MAX_SAFE_INTEGER);
    const limit = intParam(q.limit, DEFAULT_LIMIT, 1, MAX_LIMIT);
    const all: Ship[] = [...world.ships.values()]
      .sort((a, b) => a.symbol.localeCompare(b.symbol))
      .map((s) => resolveNav(s, world.transits.get(s.symbol), now));
    const start = (page - 1) * limit;
    const data = all.slice(start, start + limit);
    return reply.send({ data, meta: { total: all.length, page, limit } });
  });

  // GET /my/ships/:symbol — single ship with on-read arrival flip; 404 for unknown symbols.
  app.get('/my/ships/:symbol', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = new Date();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    return reply.send({ data: resolveNav(ship, world.transits.get(symbol), now) });
  });
}
```

In `twin/src/server.ts`, add `import { shipRoutes } from './routes/ships.js';` and uncomment `await shipRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/ships/
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/ships.ts twin/src/server.ts twin/tests/ships/
rtk git commit -m "feat(twin): GET /my/ships (paginated, empty past-end page) + GET /my/ships/{s} (shipDTO, resolveNav, 404)"
```

---

## Task 21 — `GET /v2/systems/:s/waypoints[/:w]`

**Files:** Create `twin/src/routes/waypoints.ts`. Modify `twin/src/server.ts` (add `await waypointRoutes(v2)`). Test `twin/tests/endpoints/waypoints-list.test.ts`, `twin/tests/endpoints/waypoint-detail.test.ts` (create).

**Interfaces:** `waypointRoutes: FastifyPluginAsync` reading `getWorld()`. List → `{ data: Waypoint[], meta }` (limit clamped [1,20], default 10; page past end → `[]`); detail → `{ data: Waypoint }`; 404 envelope on unknown system/waypoint. Golden values READ from `waypoints.json`. Driven by `spacetraders waypoint list --system X1-PZ28` / `waypoint get --waypoint <w>` (daemon-mediated, pass `--player-id 1`).

- [ ] **Step 1a — list test.** Create `twin/tests/endpoints/waypoints-list.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const WAYPOINTS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'waypoints.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenWaypoint { symbol: string; type: string; systemSymbol: string; x: number; y: number; traits: Array<{ symbol: string; name: string; description: string }>; orbitals: Array<{ symbol: string }>; isUnderConstruction: boolean }
function loadWaypoints(): GoldenWaypoint[] { return JSON.parse(readFileSync(WAYPOINTS_FIXTURE, 'utf8')) as GoldenWaypoint[]; }
function waypointRows(stdout: string): string[][] {
  return stdout.split('\n').map((l) => l.trimEnd()).filter((l) => /^X1-PZ28-\S/.test(l)).map((l) => l.split(/\s{2,}/));
}

describe('GET /v2/systems/{s}/waypoints — wire shape vs captured golden', () => {
  it('serves the full captured topology across limit=20 pages', async () => {
    const wps = loadWaypoints(); const total = wps.length; const limit = 20; const lastPage = Math.ceil(total / limit);
    const p1 = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=1&limit=${limit}`);
    expect(p1.status).toBe(200);
    const b1 = (await p1.json()) as { data: GoldenWaypoint[]; meta: { total: number; page: number; limit: number } };
    expect(b1.meta).toEqual({ total, page: 1, limit }); expect(b1.data).toHaveLength(limit);
    const past = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=${lastPage + 1}&limit=${limit}`);
    expect(((await past.json()) as { data: GoldenWaypoint[] }).data).toHaveLength(0);
    const wire = new Map<string, GoldenWaypoint>();
    for (let page = 1; page <= lastPage; page++) {
      const r = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=${page}&limit=${limit}`);
      for (const w of ((await r.json()) as { data: GoldenWaypoint[] }).data) {
        expect(wire.has(w.symbol), `duplicate ${w.symbol}`).toBe(false); wire.set(w.symbol, w);
      }
    }
    expect(wire.size).toBe(total);
    for (const gold of wps) expect(wire.get(gold.symbol)).toEqual(gold);
  });
  it('404s an unknown system with the error envelope', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/systems/X1-NOPE/waypoints?page=1&limit=20`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain('X1-NOPE');
  });
});

describe('`spacetraders waypoint list` — Go ListWaypoints round-trip', () => {
  it('surfaces every captured waypoint incl. the JUMP_GATE; filters resolve to fixture subsets', () => {
    const wps = loadWaypoints();
    const gate = wps.find((w) => w.type === 'JUMP_GATE')!;
    const { stdout, stderr, exitCode } = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    const rows = waypointRows(stdout);
    expect(rows).toHaveLength(wps.length);
    const gateRow = rows.find((c) => c[0] === gate.symbol)!;
    expect(gateRow[1]).toBe('JUMP_GATE'); expect(gateRow[2]).toBe(String(Math.round(gate.x))); expect(gateRow[3]).toBe(String(Math.round(gate.y)));
    const gateSymbols = wps.filter((w) => w.type === 'JUMP_GATE').map((w) => w.symbol).sort();
    const gates = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--type', 'JUMP_GATE', '--player-id', '1']);
    expect(gates.exitCode, gates.stderr).toBe(0);
    expect(waypointRows(gates.stdout).map((c) => c[0]).sort()).toEqual(gateSymbols);
    const shipyardSymbols = wps.filter((w) => w.traits.some((t) => t.symbol === 'SHIPYARD')).map((w) => w.symbol).sort();
    const yards = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--trait', 'SHIPYARD', '--player-id', '1']);
    expect(yards.exitCode, yards.stderr).toBe(0);
    expect(waypointRows(yards.stdout).map((c) => c[0]).sort()).toEqual(shipyardSymbols);
  });
});
```

- [ ] **Step 1b — detail test.** Create `twin/tests/endpoints/waypoint-detail.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const WAYPOINTS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'waypoints.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenWaypoint { symbol: string; type: string; systemSymbol: string; x: number; y: number; traits: Array<{ symbol: string; name: string; description: string }>; orbitals: Array<{ symbol: string }>; isUnderConstruction: boolean }
function jumpGate(): GoldenWaypoint {
  const g = (JSON.parse(readFileSync(WAYPOINTS_FIXTURE, 'utf8')) as GoldenWaypoint[]).find((w) => w.type === 'JUMP_GATE');
  if (!g) throw new Error('fixture invariant: no JUMP_GATE in X1-PZ28');
  return g;
}

describe('GET /v2/systems/{s}/waypoints/{w} — thin decode target', () => {
  it('returns { data: Waypoint } for the JUMP_GATE, isUnderConstruction per capture', async () => {
    const gate = jumpGate();
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${gate.symbol}`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: GoldenWaypoint };
    expect(body.data.symbol).toBe(gate.symbol);
    expect(typeof body.data.isUnderConstruction).toBe('boolean');
    expect(body.data).toEqual(gate);
  });
  it('404s an unknown waypoint with the error envelope', async () => {
    const missing = `${HOME_SYSTEM}-NOSUCHWP`;
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${missing}`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain(missing);
  });
});

describe('`spacetraders waypoint get` — detail command', () => {
  it('prints the JUMP_GATE type + coordinates round-tripped', () => {
    const gate = jumpGate();
    const { stdout, stderr, exitCode } = runCli(['waypoint', 'get', '--waypoint', gate.symbol, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toMatch(new RegExp(`Waypoint:\\s+${gate.symbol}`));
    expect(stdout).toMatch(/Type:\s+JUMP_GATE/);
    expect(stdout).toContain(`(${Math.round(gate.x)}, ${Math.round(gate.y)})`);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/endpoints/waypoints-list.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/routes/waypoints.ts`:

```ts
import type { FastifyPluginAsync } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

function clampInt(raw: string | undefined, def: number, min: number, max: number): number {
  const n = raw === undefined ? Number.NaN : Number.parseInt(raw, 10);
  if (!Number.isFinite(n)) return def;
  return Math.min(Math.max(n, min), max);
}
const DEFAULT_LIMIT = 10;
const MAX_LIMIT = 20;

export const waypointRoutes: FastifyPluginAsync = async (app) => {
  app.get<{ Params: { systemSymbol: string }; Querystring: { page?: string; limit?: string } }>(
    '/systems/:systemSymbol/waypoints',
    async (req, reply) => {
      const { systemSymbol } = req.params;
      const system = getWorld().systems.get(systemSymbol);
      if (!system) return notFound(reply, `System ${systemSymbol} not found.`);
      const limit = clampInt(req.query.limit, DEFAULT_LIMIT, 1, MAX_LIMIT);
      const page = clampInt(req.query.page, 1, 1, Number.MAX_SAFE_INTEGER);
      const all = [...system.waypoints.values()].sort((a, b) => (a.symbol < b.symbol ? -1 : a.symbol > b.symbol ? 1 : 0));
      const start = (page - 1) * limit;
      return reply.send({ data: all.slice(start, start + limit), meta: { total: all.length, page, limit } });
    },
  );

  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol',
    async (req, reply) => {
      const { systemSymbol, waypointSymbol } = req.params;
      const waypoint = getWorld().systems.get(systemSymbol)?.waypoints.get(waypointSymbol);
      if (!waypoint) return notFound(reply, `Waypoint ${waypointSymbol} not found.`);
      return reply.send({ data: waypoint });
    },
  );
};
```

In `twin/src/server.ts`, add `import { waypointRoutes } from './routes/waypoints.js';` and uncomment `await waypointRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/endpoints/waypoints-list.test.ts tests/endpoints/waypoint-detail.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/waypoints.ts twin/src/server.ts twin/tests/endpoints/waypoints-list.test.ts twin/tests/endpoints/waypoint-detail.test.ts
rtk git commit -m "feat(twin): GET .../waypoints list + detail (captured X1-PZ28 topology, paginated, 404)"
```

---

## Task 22 — `GET /v2/systems/:s/waypoints/:w/market`

**Files:** Create `twin/src/routes/market.ts`. Modify `twin/src/server.ts` (add `await marketRoutes(v2)`). Test `twin/tests/market.test.ts` (create).

**Reconciliation:** `30b`/`40b`'s `registerMarketRoutes(app, world)` → `marketRoutes(app)` plugin reading `getWorld()`, relative path. **The crux:** the Go client derives EXPORT/IMPORT/EXCHANGE by which array a good is in — the twin returns the captured `Market` unchanged; the test proves the CLI classifies every good correctly. Driven by `workflow scout-markets` (single market at the ship's location = zero navigation) + `market find --json` read-back.

- [ ] **Step 1 — test.** Create `twin/tests/market.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, TWIN_BASE_URL, runCli } from './helpers/run-cli';

const SYSTEM = 'X1-PZ28'; const HQ = 'X1-PZ28-A1'; const SCOUT = 'TWINAGENT-2'; const AGENT = 'TWINAGENT';
const MARKETS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'markets.json');
interface FixtureTradeGood { symbol: string; supply: string; activity: string; sellPrice: number; purchasePrice: number; tradeVolume: number }
interface FixtureMarket { symbol: string; exports: Array<{ symbol: string }>; imports: Array<{ symbol: string }>; exchange: Array<{ symbol: string }>; tradeGoods: FixtureTradeGood[] }
interface FindRow { WaypointSymbol: string; TradeType: string; PurchasePrice: number; SellPrice: number; Supply: string; Activity: string; TradeVolume: number; LastUpdated: string }
function loadHqMarket(): FixtureMarket {
  const all = JSON.parse(readFileSync(MARKETS_FIXTURE, 'utf8')) as FixtureMarket[];
  const m = all.find((x) => x.symbol === HQ); if (!m) throw new Error(`fixture markets.json has no market for ${HQ}`); return m;
}
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  if (!res.ok) throw new Error(`/_twin/reset failed: ${res.status}`);
}
function marketFindRows(good: string): FindRow[] {
  const { stdout, exitCode } = runCli(['market', 'find', '--good', good, '--system', SYSTEM, '--agent', AGENT, '--json']);
  if (exitCode !== 0) return [];
  const parsed = JSON.parse(stdout.trim() || '[]'); return Array.isArray(parsed) ? (parsed as FindRow[]) : [];
}
const hqRow = (good: string) => marketFindRows(good).find((r) => r.WaypointSymbol === HQ);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

describe('GET /systems/{s}/waypoints/{w}/market — goods classified by array membership', () => {
  beforeEach(async () => { await resetWorld(); });
  it('serves HQ market so the Go client classifies each good and round-trips prices', async () => {
    const market = loadHqMarket();
    expect(market.exports.length).toBeGreaterThan(0);
    expect(market.imports.length).toBeGreaterThan(0);
    expect(market.exchange.length).toBeGreaterThan(0);
    const exportSym = market.exports[0].symbol, importSym = market.imports[0].symbol, exchangeSym = market.exchange[0].symbol;

    const res = await fetch(`${TWIN_BASE_URL}/systems/${SYSTEM}/waypoints/${HQ}/market`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: FixtureMarket };
    expect(body.data.symbol).toBe(HQ);
    const ex = new Set(body.data.exports.map((g) => g.symbol)), im = new Set(body.data.imports.map((g) => g.symbol)), xc = new Set(body.data.exchange.map((g) => g.symbol));
    for (const g of body.data.tradeGoods) {
      const memberships = (ex.has(g.symbol) ? 1 : 0) + (im.has(g.symbol) ? 1 : 0) + (xc.has(g.symbol) ? 1 : 0);
      expect(memberships, `${g.symbol} must be in exactly one array`).toBe(1);
    }

    expect(runCli(['ship', 'list', '--agent', AGENT]).exitCode, 'warm fleet sync').toBe(0);
    const beforeScan = Date.now();
    const launch = runCli(['workflow', 'scout-markets', '--ships', SCOUT, '--system', SYSTEM, '--markets', HQ, '--iterations', '1', '--agent', AGENT]);
    expect(launch.exitCode, launch.stderr).toBe(0);
    const deadline = Date.now() + 100_000; let landed = false;
    while (Date.now() < deadline) { const r = hqRow(exportSym); if (r && Date.parse(r.LastUpdated) >= beforeScan - 1000) { landed = true; break; } await sleep(1000); }
    expect(landed, `fresh scan for ${HQ} never landed`).toBe(true);

    expect(hqRow(exportSym)?.TradeType).toBe('EXPORT');
    expect(hqRow(importSym)?.TradeType).toBe('IMPORT');
    expect(hqRow(exchangeSym)?.TradeType).toBe('EXCHANGE');
    for (const sym of [exportSym, importSym, exchangeSym]) {
      const tg = market.tradeGoods.find((g) => g.symbol === sym)!; const r = hqRow(sym)!;
      expect(r.PurchasePrice).toBe(tg.purchasePrice); expect(r.SellPrice).toBe(tg.sellPrice);
      expect(r.Supply).toBe(tg.supply); expect(r.Activity).toBe(tg.activity); expect(r.TradeVolume).toBe(tg.tradeVolume);
    }
  }, 115_000);
});
```
Optional speed-up: add `scouting: { tour_start_jitter_max_seconds: 1 }` to `twin/test-config.yaml` (the test is correct at the default jitter either way).

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/market.test.ts
```
Expected: fast fail at `GET market must be 200` (route missing → 404).

- [ ] **Step 3 — implement.** Create `twin/src/routes/market.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

/** GET /v2/systems/:s/waypoints/:w/market — { data: Market }. The world's Market
 *  already partitions goods into exports/imports/exchange (captured verbatim); the
 *  twin returns it unchanged. Markets are keyed by waypoint symbol (globally unique). */
export async function marketRoutes(app: FastifyInstance): Promise<void> {
  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol/market',
    async (request, reply) => {
      const { waypointSymbol } = request.params;
      const market = getWorld().markets.get(waypointSymbol);
      if (!market) return notFound(reply, `Market not found at waypoint ${waypointSymbol}.`);
      return reply.send({ data: market });
    },
  );
}
```

In `twin/src/server.ts`, add `import { marketRoutes } from './routes/market.js';` and uncomment `await marketRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/market.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/market.ts twin/src/server.ts twin/tests/market.test.ts
rtk git commit -m "feat(twin): GET .../market — serve captured market; CLI classifies goods by array membership"
```

---

## Task 23 — `GET /v2/systems/:s/waypoints/:w/shipyard` (+ 404 guard)

**Files:** Create `twin/src/routes/shipyard.ts`. Modify `twin/src/server.ts` (add `await shipyardRoutes(v2)`). Test `twin/tests/endpoints/shipyard.test.ts`, `twin/tests/endpoints/shipyard.errors.test.ts` (create).

**Interfaces:** `shipyardRoutes: FastifyPluginAsync` reading `getWorld()`; `{ data: Shipyard }` for a known shipyard waypoint (field-for-field golden incl. `engine.speed`, `shipTypes[]`, `modificationsFee`), 404 envelope otherwise. Driven by `spacetraders shipyard list <sys> <wp> --player-id 1` (surfaces `SHIP_PROBE` at its golden `purchasePrice`).

- [ ] **Step 1a — happy-path test.** Create `twin/tests/endpoints/shipyard.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const SHIPYARDS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'shipyards.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenListing { type: string; name: string; description: string; purchasePrice: number; engine: { speed: number } & Record<string, unknown> } 
interface GoldenShipyard { symbol: string; shipTypes: Array<{ type: string }>; ships: GoldenListing[]; transactions: unknown[]; modificationsFee: number }
function loadProbeShipyard(): { shipyard: GoldenShipyard; probe: GoldenListing } {
  for (const s of JSON.parse(readFileSync(SHIPYARDS_FIXTURE, 'utf8')) as GoldenShipyard[]) {
    const probe = s.ships.find((x) => x.type === 'SHIP_PROBE'); if (probe) return { shipyard: s, probe };
  }
  throw new Error('fixture invariant: no SHIP_PROBE listing in any X1-PZ28 shipyard');
}

describe('GET /v2/systems/{s}/waypoints/{w}/shipyard — wire shape vs golden', () => {
  it('returns the captured shipyard field-for-field incl. SHIP_PROBE engine.speed + modificationsFee', async () => {
    const { shipyard, probe } = loadProbeShipyard();
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${shipyard.symbol}/shipyard`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: GoldenShipyard };
    expect(body.data.symbol).toBe(shipyard.symbol);
    expect(body.data.shipTypes.map((t) => t.type)).toContain('SHIP_PROBE');
    expect(body.data.modificationsFee).toBe(shipyard.modificationsFee);
    expect(Array.isArray(body.data.transactions)).toBe(true);
    const listing = body.data.ships.find((s) => s.type === 'SHIP_PROBE')!;
    expect(listing.purchasePrice).toBe(probe.purchasePrice);
    expect(typeof listing.engine.speed).toBe('number'); expect(listing.engine.speed).toBe(probe.engine.speed);
    expect(body.data).toEqual(shipyard);
  });
});

describe('`spacetraders shipyard list` — Go GetShipyard round-trip', () => {
  it('surfaces the SHIP_PROBE at the golden purchasePrice + the shipyard symbol', () => {
    const { shipyard, probe } = loadProbeShipyard();
    const { stdout, stderr, exitCode } = runCli(['shipyard', 'list', HOME_SYSTEM, shipyard.symbol, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain(`Shipyard: ${shipyard.symbol}`);
    expect(stdout).toContain('SHIP_PROBE');
    expect(stdout).toContain(probe.name);
    expect(stdout).toContain(String(probe.purchasePrice));
    if (shipyard.modificationsFee > 0) expect(stdout).toContain(`Modification Fee: ${shipyard.modificationsFee} credits`);
  });
});
```

- [ ] **Step 1b — 404 test.** Create `twin/tests/endpoints/shipyard.errors.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL } from '../helpers/run-cli';

const HOME_SYSTEM = 'X1-PZ28'; const NO_SHIPYARD_WP = 'X1-PZ28-NOSHIPYARD';

describe('GET /v2/systems/{s}/waypoints/{w}/shipyard — not-a-shipyard waypoint', () => {
  it('returns a 404 error envelope naming the waypoint', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${NO_SHIPYARD_WP}/shipyard`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain(NO_SHIPYARD_WP);
  });
  it('`shipyard list` exits non-zero (Go client surfaces the 404)', () => {
    const { stderr, exitCode } = runCli(['shipyard', 'list', HOME_SYSTEM, NO_SHIPYARD_WP, '--player-id', '1']);
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('failed to get shipyard');
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/endpoints/shipyard.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/routes/shipyard.ts`:

```ts
import type { FastifyPluginAsync } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

/** GET /v2/systems/:s/waypoints/:w/shipyard — { data: Shipyard } (captured verbatim);
 *  404 envelope for a waypoint with no shipyard. Keyed by waypoint symbol. */
export const shipyardRoutes: FastifyPluginAsync = async (app) => {
  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol/shipyard',
    async (req, reply) => {
      const { waypointSymbol } = req.params;
      const shipyard = getWorld().shipyards.get(waypointSymbol);
      if (!shipyard) return notFound(reply, `Shipyard not found at waypoint ${waypointSymbol}.`);
      return reply.send({ data: shipyard });
    },
  );
};
```

In `twin/src/server.ts`, add `import { shipyardRoutes } from './routes/shipyard.js';` and uncomment `await shipyardRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/endpoints/shipyard.test.ts tests/endpoints/shipyard.errors.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/shipyard.ts twin/src/server.ts twin/tests/endpoints/shipyard.test.ts twin/tests/endpoints/shipyard.errors.test.ts
rtk git commit -m "feat(twin): GET .../shipyard endpoint — captured listings incl. SHIP_PROBE engine.speed + 404 guard"
```

---

## Task 24 — `POST /v2/my/ships/:s/navigate` (compressed transit + on-read flip)

**Files:** Create `twin/src/routes/ships-navigate.ts`. Modify `twin/src/server.ts` (add `await shipNavigateRoutes(v2)`). Test `twin/tests/endpoints/navigate.test.ts` (create).

**Reconciliation:** `50a`'s `shipNavigateRoutes(world)` factory → `shipNavigateRoutes(app)` plugin reading `getWorld()`, relative path. Fuel debited eagerly at POST; a `TransitState` stored in `world.transits`; the RESPONSE nav points at the DESTINATION with `IN_TRANSIT` (client.go:242) — deliberately different from `resolveNav`'s origin-during-transit contract for GET reads. **Do not "unify" these.**

**Interfaces:** Consumes `distance`, `fuelCost`, `makeTransit` (clock); `notFound`, `badRequest`, `unauthorized`, `sendError`, `ERR_SHIP_NOT_DOCKED` (errors); `getWorld` (store). Response `{ data: { fuel: { current, capacity, consumed: { amount } }, nav } }` (client.go:213). Driven by `spacetraders ship navigate`. Acceptance gated after the sibling orbit + waypoints + ship-read endpoints exist.

- [ ] **Step 1 — test.** Create `twin/tests/endpoints/navigate.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { Ship, TransitState, Waypoint } from '../../src/world/types';
import { distance, fuelCost, realTravelSeconds } from '../../src/clock';
import { TWIN_ADMIN, TWIN_BASE_URL, runCli } from '../helpers/run-cli';

const SYSTEM = 'X1-PZ28'; const SHIP = 'TWINAGENT-1';
interface TwinState { compression: number; ships: Ship[]; transits: TransitState[] }
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
async function twinState(): Promise<TwinState> { const res = await fetch(`${TWIN_ADMIN}/state`); if (!res.ok) throw new Error(`state ${res.status}`); return (await res.json()) as TwinState; }
async function resetWorld(): Promise<void> { const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' }); if (!res.ok) throw new Error(`reset ${res.status}`); }
async function setCompression(compression: number): Promise<void> { const res = await fetch(`${TWIN_ADMIN}/time-compression`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ compression }) }); if (!res.ok) throw new Error(`compression ${res.status}`); }
async function fetchAllWaypoints(system: string): Promise<Waypoint[]> {
  const all: Waypoint[] = [];
  for (let page = 1; page <= 20; page++) {
    const res = await fetch(`${TWIN_BASE_URL}/systems/${system}/waypoints?limit=20&page=${page}`);
    const body = (await res.json()) as { data: Waypoint[] }; if (!body.data || body.data.length === 0) break; all.push(...body.data);
  }
  return all;
}
async function pollState<T>(pick: (st: TwinState) => T | undefined, timeoutMs: number, intervalMs: number, failMsg: string): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) { const hit = pick(await twinState()); if (hit !== undefined) return hit; await sleep(intervalMs); }
  throw new Error(failMsg);
}

describe('POST /my/ships/{s}/navigate — compressed transit + on-read flip', () => {
  beforeEach(async () => { await resetWorld(); });
  afterEach(async () => { await setCompression(100); });
  it('debits fuel eagerly, reports IN_TRANSIT, then flips to IN_ORBIT at the destination', async () => {
    const ship0 = (await twinState()).ships.find((s) => s.symbol === SHIP)!;
    const originSymbol = ship0.nav.waypointSymbol, speed = ship0.engine.speed, fuel0 = ship0.fuel.current;
    const wps = await fetchAllWaypoints(SYSTEM);
    const origin = wps.find((w) => w.symbol === originSymbol)!;
    const MIN_DIST = 10;
    const candidate = wps.filter((w) => w.symbol !== originSymbol).map((w) => ({ w, d: distance(origin, w) }))
      .filter((c) => c.d >= MIN_DIST && Math.ceil(c.d) <= fuel0 * 0.9).sort((a, b) => a.d - b.d)[0];
    expect(candidate, 'a fuel-feasible, observable destination exists').toBeTruthy();
    const DEST = candidate.w.symbol, dist = candidate.d;
    const realETA = realTravelSeconds(dist, speed, 'CRUISE');
    await setCompression(Math.max(1, Math.round(realETA / 6)));

    const res = runCli(['ship', 'navigate', '--ship', SHIP, '--destination', DEST, '--player-id', '1']);
    expect(res.exitCode, res.stderr).toBe(0);
    expect(res.stdout).toContain('Navigation started');

    const seen = await pollState(
      (st) => { const sh = st.ships.find((s) => s.symbol === SHIP); return sh && sh.nav.status === 'IN_TRANSIT' ? { sh, st } : undefined; },
      8000, 150, `ship ${SHIP} never reached IN_TRANSIT — navigate did not mint a transit`,
    );
    const transit = seen.st.transits.find((t) => t.shipSymbol === SHIP)!;
    expect(transit.destinationWaypoint).toBe(DEST);
    expect(seen.sh!.fuel.current).toBe(fuel0 - fuelCost(dist, 'CRUISE'));

    const arrived = await pollState(
      (st) => { const sh = st.ships.find((s) => s.symbol === SHIP); return sh && sh.nav.status === 'IN_ORBIT' && sh.nav.waypointSymbol === DEST ? sh : undefined; },
      20000, 200, `ship ${SHIP} never flipped to IN_ORBIT@${DEST}`,
    );
    expect(arrived.nav.status).toBe('IN_ORBIT'); expect(arrived.nav.waypointSymbol).toBe(DEST);
  }, 45_000);
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/endpoints/navigate.test.ts
```
Expected: IN_TRANSIT poll times out (route unregistered → daemon can't mint a transit).

- [ ] **Step 3 — implement.** Create `twin/src/routes/ships-navigate.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import type { FlightMode, Waypoint, World } from '../world/types.js';
import { getWorld } from '../world/store.js';
import { distance, fuelCost, makeTransit } from '../clock.js';
import { ERR_SHIP_NOT_DOCKED, badRequest, notFound, sendError, unauthorized } from '../errors.js';

function findWaypoint(world: World, symbol: string): Waypoint | undefined {
  const systemSymbol = symbol.split('-').slice(0, 2).join('-');
  const direct = world.systems.get(systemSymbol)?.waypoints.get(symbol);
  if (direct !== undefined) return direct;
  for (const system of world.systems.values()) { const wp = system.waypoints.get(symbol); if (wp !== undefined) return wp; }
  return undefined;
}

export async function shipNavigateRoutes(app: FastifyInstance): Promise<void> {
  app.post('/my/ships/:symbol/navigate', async (request, reply) => {
    const world = getWorld();
    const header = request.headers.authorization ?? '';
    const token = header.startsWith('Bearer ') ? header.slice(7) : '';
    if (world.agentToken === null || token !== world.agentToken) return unauthorized(reply);

    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (ship === undefined) return notFound(reply, `Ship ${symbol} not found.`);

    const body = (request.body ?? {}) as { waypointSymbol?: string };
    const destinationSymbol = body.waypointSymbol;
    if (!destinationSymbol) return badRequest(reply, 'waypointSymbol is required.');

    // Real API requires orbit to navigate; the daemon orbits first. Block only a docked
    // hull with no active transit.
    if (!world.transits.has(symbol) && ship.nav.status === 'DOCKED') {
      return sendError(reply, 400, ERR_SHIP_NOT_DOCKED, 'Ship must be in orbit to navigate.');
    }

    const origin = findWaypoint(world, ship.nav.waypointSymbol);
    const destination = findWaypoint(world, destinationSymbol);
    if (destination === undefined) return notFound(reply, `Waypoint ${destinationSymbol} not found.`);
    if (origin === undefined) return notFound(reply, `Waypoint ${ship.nav.waypointSymbol} not found.`);

    const mode = (ship.nav.flightMode ?? 'CRUISE') as FlightMode;
    const dist = distance(origin, destination);
    const consumed = fuelCost(dist, mode);

    ship.fuel.current = Math.max(0, ship.fuel.current - consumed); // eager debit
    const transit = makeTransit({ shipSymbol: symbol, origin, destination, engineSpeed: ship.engine.speed, mode });
    world.transits.set(symbol, transit);

    // Response nav points at the DESTINATION with IN_TRANSIT (client.go:213/242).
    return reply.send({
      data: {
        fuel: { current: ship.fuel.current, capacity: ship.fuel.capacity, consumed: { amount: consumed } },
        nav: {
          systemSymbol: ship.nav.systemSymbol,
          waypointSymbol: destinationSymbol,
          status: 'IN_TRANSIT',
          flightMode: mode,
          route: { departureTime: transit.departureTime, arrival: transit.arrival },
        },
      },
    });
  });
}
```

In `twin/src/server.ts`, add `import { shipNavigateRoutes } from './routes/ships-navigate.js';` and uncomment `await shipNavigateRoutes(v2);`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/endpoints/navigate.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/ships-navigate.ts twin/src/server.ts twin/tests/endpoints/navigate.test.ts
rtk git commit -m "feat(twin): POST /my/ships/{s}/navigate — eager fuel debit + compressed transit + on-read IN_ORBIT flip"
```

---

## Task 25 — `POST /v2/my/ships/:s/{orbit,dock,refuel}` + test-admin seams

**Files:** Create `twin/src/routes/ship-actions.ts`, `twin/src/routes/twin-test-admin.ts`, `twin/tests/helpers/twin-admin.ts`. Modify `twin/src/server.ts`. Test `twin/tests/ship-orbit.test.ts`, `twin/tests/ship-dock.test.ts`, `twin/tests/ship-refuel.test.ts`, `twin/tests/ship-refuel-not-docked.test.ts` (create).

**Reconciliation:** `50b`'s `registerShipActionRoutes(app, world)` → `shipActionRoutes(app)` plugin (relative paths, `getWorld()` at handler entry); `registerTwinTestAdminRoutes(app, world)` → `testAdminRoutes(app)` plugin under `/_twin` (relative paths `/ships/:symbol/fuel`, `/agent-token`). Build sub-route by sub-route: orbit → dock → refuel → not-docked guard, red→green→commit each.

**Ground truth:** orbit/dock/refuel are daemon-mediated and ASYNC (CLI prints `✓ … operation started`, exits 0) — behavior asserted via `GET /_twin/state`, contract via `ship refresh`. Each test `ship refresh`es after `reset` to force `EnsureInOrbit`/`EnsureDocked` to fire the real twin call. HQ `X1-PZ28-A1` has the `MARKETPLACE` trait so the daemon treats it as a fuel station. Cold-start ships launch full → the `POST /_twin/ships/:s/fuel` drain seam makes refuel do real work. The 4214 not-docked branch is unreachable via CLI (daemon auto-docks) → verified by a direct auth'd `fetch` using `GET /_twin/agent-token`.

- [ ] **Step 1 — helper + tests.** Create `twin/tests/helpers/twin-admin.ts`:

```ts
import { TWIN_ADMIN } from './run-cli.js';
import type { Agent, Market, Ship } from '../../src/world/types.js';

export interface TwinState {
  compression: number; agent: Agent | null; ships: Ship[]; transits: unknown[];
  markets: Record<string, Market>; shipyards: Record<string, unknown>; waypointCount: number; now: string;
}
export async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  if (!res.ok) throw new Error(`POST /_twin/reset -> ${res.status}`);
}
export async function getState(): Promise<TwinState> {
  const res = await fetch(`${TWIN_ADMIN}/state`); if (!res.ok) throw new Error(`GET /_twin/state -> ${res.status}`); return (await res.json()) as TwinState;
}
export function findShip(state: TwinState, symbol: string): Ship {
  const ship = state.ships.find((s) => s.symbol === symbol); if (!ship) throw new Error(`ship ${symbol} not present`); return ship;
}
export async function waitForShip(symbol: string, pred: (s: Ship) => boolean, timeoutMs = 8000): Promise<Ship> {
  const deadline = Date.now() + timeoutMs; let last: Ship | undefined;
  for (;;) {
    last = (await getState()).ships.find((s) => s.symbol === symbol);
    if (last && pred(last)) return last;
    if (Date.now() >= deadline) throw new Error(`waitForShip(${symbol}) timed out; last nav=${JSON.stringify(last?.nav ?? null)} fuel=${JSON.stringify(last?.fuel ?? null)}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}
export async function setShipFuel(symbol: string, current: number): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/ships/${symbol}/fuel`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ current }) });
  if (!res.ok) throw new Error(`POST /_twin/ships/${symbol}/fuel -> ${res.status}`);
}
export function fuelUnitPriceFromState(state: TwinState, waypointSymbol: string): number {
  const fuel = state.markets[waypointSymbol]?.tradeGoods?.find((g) => g.symbol === 'FUEL');
  return fuel && fuel.purchasePrice > 0 ? fuel.purchasePrice : 72;
}
export async function getAgentToken(): Promise<string> {
  const res = await fetch(`${TWIN_ADMIN}/agent-token`); if (!res.ok) throw new Error(`GET /_twin/agent-token -> ${res.status}`);
  const body = (await res.json()) as { token: string | null }; if (!body.token) throw new Error('twin has no agent token'); return body.token;
}
```

Create `twin/tests/ship-orbit.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli } from './helpers/run-cli';
import { findShip, getState, resetWorld, waitForShip } from './helpers/twin-admin';
const AGENT = 'TWINAGENT'; const SHIP = 'TWINAGENT-1'; const SYSTEM = 'X1-PZ28'; const HQ = 'X1-PZ28-A1';
describe('POST /my/ships/:s/orbit — via `spacetraders ship orbit`', () => {
  beforeEach(async () => {
    await resetWorld();
    const r = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]);
    expect(r.exitCode, r.stderr).toBe(0); expect(r.stdout).toMatch(/Nav Status:\s+DOCKED/);
  });
  it('flips the ship to IN_ORBIT and the CLI reads it back', async () => {
    const before = findShip(await getState(), SHIP); const fuelBefore = before.fuel.current;
    const orbit = runCli(['ship', 'orbit', '--ship', SHIP, '--agent', AGENT]);
    expect(orbit.exitCode, orbit.stderr).toBe(0); expect(orbit.stdout).toContain('✓ Orbit operation started');
    const after = await waitForShip(SHIP, (s) => s.nav.status === 'IN_ORBIT');
    expect(after.nav.waypointSymbol).toBe(HQ); expect(after.nav.systemSymbol).toBe(SYSTEM); expect(after.fuel.current).toBe(fuelBefore);
    const refreshed = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]);
    expect(refreshed.exitCode, refreshed.stderr).toBe(0); expect(refreshed.stdout).toMatch(/Nav Status:\s+IN_ORBIT/);
  });
});
```

Create `twin/tests/ship-dock.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli } from './helpers/run-cli';
import { findShip, getState, resetWorld, waitForShip } from './helpers/twin-admin';
const AGENT = 'TWINAGENT'; const SHIP = 'TWINAGENT-1'; const HQ = 'X1-PZ28-A1';
describe('POST /my/ships/:s/dock — via `spacetraders ship dock`', () => {
  beforeEach(async () => {
    await resetWorld();
    const r = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]);
    expect(r.exitCode, r.stderr).toBe(0); expect(r.stdout).toMatch(/Nav Status:\s+DOCKED/);
  });
  it('flips the ship to DOCKED and the CLI reads it back', async () => {
    expect(runCli(['ship', 'orbit', '--ship', SHIP, '--agent', AGENT]).exitCode).toBe(0);
    await waitForShip(SHIP, (s) => s.nav.status === 'IN_ORBIT');
    const rr = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]); expect(rr.stdout).toMatch(/Nav Status:\s+IN_ORBIT/);
    const dock = runCli(['ship', 'dock', '--ship', SHIP, '--agent', AGENT]);
    expect(dock.exitCode, dock.stderr).toBe(0); expect(dock.stdout).toContain('✓ Dock operation started');
    const after = await waitForShip(SHIP, (s) => s.nav.status === 'DOCKED');
    expect(after.nav.waypointSymbol).toBe(HQ);
    expect(findShip(await getState(), SHIP).nav.status).toBe('DOCKED');
    expect(runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]).stdout).toMatch(/Nav Status:\s+DOCKED/);
  });
});
```

Create `twin/tests/ship-refuel.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli } from './helpers/run-cli';
import { findShip, fuelUnitPriceFromState, getState, resetWorld, setShipFuel, waitForShip } from './helpers/twin-admin';
const AGENT = 'TWINAGENT'; const SHIP = 'TWINAGENT-1'; const HQ = 'X1-PZ28-A1';
describe('POST /my/ships/:s/refuel — via `spacetraders ship refuel`', () => {
  beforeEach(async () => {
    await resetWorld();
    const r = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]); expect(r.exitCode, r.stderr).toBe(0); expect(r.stdout).toMatch(/Nav Status:\s+DOCKED/);
  });
  it('fills the tank to capacity and charges credits by totalPrice', async () => {
    const s0 = await getState(); const before = findShip(s0, SHIP); const capacity = before.fuel.capacity;
    expect(capacity).toBeGreaterThan(0);
    const units = Math.min(50, capacity); const drainTo = capacity - units;
    await setShipFuel(SHIP, drainTo);
    expect(findShip(await getState(), SHIP).fuel.current).toBe(drainTo);
    const perUnit = fuelUnitPriceFromState(s0, HQ); const expectedTotal = units * perUnit; const creditsBefore = s0.agent!.credits;
    const refuel = runCli(['ship', 'refuel', '--ship', SHIP, '--agent', AGENT]);
    expect(refuel.exitCode, refuel.stderr).toBe(0); expect(refuel.stdout).toContain('✓ Refuel operation started');
    const filled = await waitForShip(SHIP, (s) => s.fuel.current === s.fuel.capacity);
    expect(filled.fuel.current).toBe(capacity);
    expect(expectedTotal).toBeGreaterThan(0);
    expect((await getState()).agent!.credits).toBe(creditsBefore - expectedTotal);
    expect(runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]).stdout).toMatch(new RegExp(`Fuel:\\s+${capacity}\\s*/\\s*${capacity}`));
  });
});
```

Create `twin/tests/ship-refuel-not-docked.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL } from './helpers/run-cli';
import { findShip, getAgentToken, getState, resetWorld, waitForShip } from './helpers/twin-admin';
const AGENT = 'TWINAGENT'; const SHIP = 'TWINAGENT-1';
describe('POST /my/ships/:s/refuel — not-docked contract (direct; CLI auto-docks)', () => {
  beforeEach(async () => {
    await resetWorld();
    const r = runCli(['ship', 'refresh', '--ship', SHIP, '--agent', AGENT]); expect(r.exitCode, r.stderr).toBe(0); expect(r.stdout).toMatch(/Nav Status:\s+DOCKED/);
  });
  it('rejects refuel while IN_ORBIT with 400 + code 4214 and no side effects', async () => {
    expect(runCli(['ship', 'orbit', '--ship', SHIP, '--agent', AGENT]).exitCode).toBe(0);
    const orbiting = await waitForShip(SHIP, (s) => s.nav.status === 'IN_ORBIT');
    const fuelBefore = orbiting.fuel.current; const creditsBefore = (await getState()).agent!.credits;
    const token = await getAgentToken();
    const res = await fetch(`${TWIN_BASE_URL}/my/ships/${SHIP}/refuel`, { method: 'POST', headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' }, body: '{}' });
    expect(res.status).toBe(400);
    expect(((await res.json()) as { error?: { code?: number } }).error?.code).toBe(4214);
    const after = await getState();
    expect(findShip(after, SHIP).fuel.current).toBe(fuelBefore);
    expect(after.agent!.credits).toBe(creditsBefore);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/ship-orbit.test.ts
```
Expected: `waitForShip(TWINAGENT-1)` times out (no `/orbit` route → daemon container 404s).

- [ ] **Step 3 — implement.** Create `twin/src/routes/ship-actions.ts`:

```ts
import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { resolveNav } from '../clock.js';
import { ERR_SHIP_MUST_BE_DOCKED, notFound, sendError, unauthorized } from '../errors.js';
import type { Ship, World } from '../world/types.js';

function authed(request: FastifyRequest, reply: FastifyReply, world: World): boolean {
  const auth = request.headers.authorization;
  if (!world.agentToken || auth !== `Bearer ${world.agentToken}`) { unauthorized(reply); return false; }
  return true;
}

/** Settle any stored transit onto the canonical ship via resolveNav (foundation §2). */
function settle(world: World, symbol: string, ship: Ship, now: Date): Ship {
  const transit = world.transits.get(symbol);
  const resolved = resolveNav(ship, transit, now);
  ship.nav.systemSymbol = resolved.nav.systemSymbol;
  ship.nav.waypointSymbol = resolved.nav.waypointSymbol;
  ship.nav.status = resolved.nav.status;
  ship.nav.flightMode = resolved.nav.flightMode;
  ship.nav.route = resolved.nav.route ?? null;
  if (transit && resolved.nav.status !== 'IN_TRANSIT') world.transits.delete(symbol);
  return ship;
}

const FUEL_FALLBACK_PRICE = 72;
function fuelUnitPrice(world: World, waypointSymbol: string): number {
  const fuel = world.markets.get(waypointSymbol)?.tradeGoods.find((g) => g.symbol === 'FUEL');
  return fuel && fuel.purchasePrice > 0 ? fuel.purchasePrice : FUEL_FALLBACK_PRICE;
}

export async function shipActionRoutes(app: FastifyInstance): Promise<void> {
  // POST /my/ships/:symbol/orbit -> { data: { nav } } status IN_ORBIT.
  app.post('/my/ships/:symbol/orbit', async (request, reply) => {
    const world = getWorld();
    if (!authed(request, reply, world)) return reply;
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    settle(world, symbol, ship, new Date());
    world.transits.delete(symbol);
    ship.nav.status = 'IN_ORBIT'; ship.nav.route = null;
    return reply.send({ data: { nav: ship.nav } });
  });

  // POST /my/ships/:symbol/dock -> { data: { nav } } status DOCKED.
  app.post('/my/ships/:symbol/dock', async (request, reply) => {
    const world = getWorld();
    if (!authed(request, reply, world)) return reply;
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    settle(world, symbol, ship, new Date());
    world.transits.delete(symbol);
    ship.nav.status = 'DOCKED'; ship.nav.route = null;
    return reply.send({ data: { nav: ship.nav } });
  });

  // POST /my/ships/:symbol/refuel -> { data: { agent, fuel, transaction } } (client.go:287-304).
  app.post('/my/ships/:symbol/refuel', async (request, reply) => {
    const world = getWorld();
    if (!authed(request, reply, world)) return reply;
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    const agent = world.agent;
    if (!agent) return notFound(reply, 'No agent registered.');

    settle(world, symbol, ship, new Date());
    if (ship.nav.status !== 'DOCKED') {
      return sendError(reply, 400, ERR_SHIP_MUST_BE_DOCKED, 'Ship must be docked to refuel.');
    }

    const capacity = ship.fuel.capacity; const before = ship.fuel.current;
    const body = (request.body ?? {}) as { units?: number };
    const room = Math.max(0, capacity - before);
    const units = typeof body.units === 'number' ? Math.max(0, Math.min(body.units, room)) : room;
    const totalPrice = units * fuelUnitPrice(world, ship.nav.waypointSymbol);
    ship.fuel.current = before + units;
    agent.credits -= totalPrice;
    return reply.send({ data: { agent: { credits: agent.credits }, fuel: { current: ship.fuel.current, capacity }, transaction: { units, totalPrice } } });
  });
}
```

Create `twin/src/routes/twin-test-admin.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { badRequest, notFound } from '../errors.js';

/** Test-only control routes under /_twin (never part of the /v2 API contract). */
export async function testAdminRoutes(app: FastifyInstance): Promise<void> {
  // POST /_twin/ships/:symbol/fuel { current } — drain/set a ship's tank.
  app.post('/ships/:symbol/fuel', async (request, reply) => {
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    const body = (request.body ?? {}) as { current?: number };
    if (typeof body.current !== 'number' || body.current < 0) return badRequest(reply, 'current must be a number >= 0');
    ship.fuel.current = Math.min(body.current, ship.fuel.capacity);
    return reply.send({ ok: true, fuel: { current: ship.fuel.current, capacity: ship.fuel.capacity } });
  });

  // GET /_twin/agent-token — expose the minted token for direct auth'd contract calls.
  app.get('/agent-token', async (_request, reply) => {
    return reply.send({ token: getWorld().agentToken });
  });
}
```

In `twin/src/server.ts`: add `import { shipActionRoutes } from './routes/ship-actions.js';` and `import { testAdminRoutes } from './routes/twin-test-admin.js';`; uncomment `await shipActionRoutes(v2);` in the `/v2` block, and register the test-admin sibling: `app.register(testAdminRoutes, { prefix: '/_twin' });` next to `adminRoutes`.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/ship-orbit.test.ts tests/ship-dock.test.ts tests/ship-refuel.test.ts tests/ship-refuel-not-docked.test.ts
```
Expected: `Tests  4 passed`.

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/ship-actions.ts twin/src/routes/twin-test-admin.ts twin/tests/helpers/twin-admin.ts twin/src/server.ts twin/tests/ship-orbit.test.ts twin/tests/ship-dock.test.ts twin/tests/ship-refuel.test.ts twin/tests/ship-refuel-not-docked.test.ts
rtk git commit -m "feat(twin): POST /my/ships/:s/{orbit,dock,refuel} (+4214 guard) + /_twin fuel-drain & agent-token seams"
```

---

## Task 26 — Pure purchase mutation `applyPurchaseShip` + `mintShipFromListing`

**Files:** Create `twin/src/world/purchase.ts`. Test `twin/tests/world/purchase.test.ts` (create; unit config).

**Interfaces:** Consumes `World, Ship, Agent, Shipyard, ShipyardListing, Envelope` (types); `apiError, ApiError` (errors). Produces `applyPurchaseShip(world, body, now): PurchaseResult`, `mintShipFromListing(symbol, shipType, waypointSymbol, listing): Ship`, types `PurchaseResult/PurchaseData/PurchaseTransaction/PurchaseBody`. `world.shipCounter` is 3 after register (next purchased suffix). The transaction echoes the requested `shipType` (batch_purchase_ships.go:252 money-integrity floor).

- [ ] **Step 1 — test.** Create `twin/tests/world/purchase.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import type { Agent, Envelope, Ship, Shipyard, ShipyardListing, World } from '../../src/world/types';
import type { ApiError as ErrEnvelope } from '../../src/errors';
import { applyPurchaseShip, mintShipFromListing, type PurchaseData } from '../../src/world/purchase';

const A2 = 'X1-PZ28-A2'; const PROBE_PRICE = 24_800; const PROBE_SPEED = 3; const START_CREDITS = 175_000;
function probeListing(): ShipyardListing {
  return {
    type: 'SHIP_PROBE', name: 'Probe', description: 'A small reconnaissance ship.', purchasePrice: PROBE_PRICE,
    frame: { symbol: 'FRAME_PROBE', moduleSlots: 0, mountingPoints: 0, fuelCapacity: 0, requirements: { power: 1, crew: 0, slots: 0 } },
    reactor: { symbol: 'REACTOR_SOLAR_I', name: 'Solar Reactor I', powerOutput: 3, requirements: { power: 0, crew: 0, slots: 0 } },
    engine: { symbol: 'ENGINE_IMPULSE_DRIVE_I', name: 'Impulse Drive I', speed: PROBE_SPEED, requirements: { power: 1, crew: 0, slots: 0 } },
    modules: [], mounts: [],
  };
}
function makeTestWorld(): World {
  const shipyard: Shipyard = { symbol: A2, shipTypes: [{ type: 'SHIP_PROBE' }], ships: [probeListing()], transactions: [], modificationsFee: 0 };
  const agent: Agent = { accountId: 'acct-twin', symbol: 'TWINAGENT', headquarters: 'X1-PZ28-A1', credits: START_CREDITS, startingFaction: 'COSMIC' };
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent, agentToken: 'twin-jwt-token',
    ships: new Map<string, Ship>([['TWINAGENT-1', {} as Ship], ['TWINAGENT-2', {} as Ship]]),
    systems: new Map(), markets: new Map(), shipyards: new Map<string, Shipyard>([[A2, shipyard]]), transits: new Map(), shipCounter: 3,
  };
}

describe('mintShipFromListing', () => {
  it('emits a non-empty symbol and present nav/fuel/cargo/engine', () => {
    const ship = mintShipFromListing('TWINAGENT-3', 'SHIP_PROBE', A2, probeListing());
    expect(ship.symbol).toBe('TWINAGENT-3'); expect(ship.registration.role).toBe('SATELLITE');
    expect(ship.nav).toMatchObject({ systemSymbol: 'X1-PZ28', waypointSymbol: A2, status: 'DOCKED', flightMode: 'CRUISE' });
    expect(ship.nav.route).toBeNull(); expect(ship.engine.speed).toBe(PROBE_SPEED); expect(ship.frame.symbol).toBe('FRAME_PROBE'); expect(ship.cooldown).toBeNull();
  });
});

describe('applyPurchaseShip', () => {
  let world: World;
  beforeEach(() => { world = makeTestWorld(); });
  it('mints the probe, debits credits, returns the exact decode-target envelope', () => {
    const now = new Date('2026-07-11T18:04:05.123Z');
    const r = applyPurchaseShip(world, { shipType: 'SHIP_PROBE', waypointSymbol: A2 }, now);
    expect(r.status).toBe(201);
    const data = (r.json as Envelope<PurchaseData>).data;
    expect(data.agent.credits).toBe(START_CREDITS - PROBE_PRICE);
    expect(data.transaction).toEqual({ waypointSymbol: A2, shipSymbol: 'TWINAGENT-3', shipType: 'SHIP_PROBE', price: PROBE_PRICE, agentSymbol: 'TWINAGENT', timestamp: '2026-07-11T18:04:05.123Z' });
    expect(data.ship.symbol).toBe('TWINAGENT-3'); expect(data.ship.registration.role).toBe('SATELLITE');
    expect(world.ships.size).toBe(3); expect(world.agent?.credits).toBe(START_CREDITS - PROBE_PRICE); expect(world.shipCounter).toBe(4);
  });
  it('404 when the waypoint has no shipyard (world untouched)', () => {
    const r = applyPurchaseShip(world, { shipType: 'SHIP_PROBE', waypointSymbol: 'X1-PZ28-A1' }, new Date());
    expect(r.status).toBe(404); expect((r.json as ErrEnvelope).error.code).toBe(404);
    expect(world.ships.size).toBe(2); expect(world.agent?.credits).toBe(START_CREDITS);
  });
  it('400 when the shipyard does not sell the type', () => {
    const r = applyPurchaseShip(world, { shipType: 'SHIP_MINING_DRONE', waypointSymbol: A2 }, new Date());
    expect(r.status).toBe(400); expect(world.ships.size).toBe(2);
  });
  it('400 when the agent cannot afford it', () => {
    world.agent!.credits = 10;
    const r = applyPurchaseShip(world, { shipType: 'SHIP_PROBE', waypointSymbol: A2 }, new Date());
    expect(r.status).toBe(400); expect((r.json as ErrEnvelope).error.message).toContain('Insufficient');
    expect(world.ships.size).toBe(2); expect(world.agent!.credits).toBe(10);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/purchase.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/src/world/purchase.ts`:

```ts
import type { Agent, Envelope, Ship, ShipyardListing, World } from './types.js';
import { apiError, type ApiError } from '../errors.js';

export interface PurchaseTransaction { waypointSymbol: string; shipSymbol: string; shipType: string; price: number; agentSymbol: string; timestamp: string }
export interface PurchaseData { agent: Agent; ship: Ship; transaction: PurchaseTransaction }
export interface PurchaseBody { shipType?: unknown; waypointSymbol?: unknown }
export interface PurchaseResult { status: number; json: Envelope<PurchaseData> | ApiError }

const ROLE_BY_SHIP_TYPE: Record<string, string> = {
  SHIP_PROBE: 'SATELLITE', SHIP_MINING_DRONE: 'EXCAVATOR', SHIP_SIPHON_DRONE: 'EXCAVATOR', SHIP_ORE_HOUND: 'EXCAVATOR',
  SHIP_SURVEYOR: 'SURVEYOR', SHIP_INTERCEPTOR: 'INTERCEPTOR', SHIP_LIGHT_HAULER: 'HAULER', SHIP_HEAVY_FREIGHTER: 'HAULER',
  SHIP_LIGHT_SHUTTLE: 'TRANSPORT', SHIP_COMMAND_FRIGATE: 'COMMAND', SHIP_EXPLORER: 'COMMAND', SHIP_REFINING_FREIGHTER: 'REFINERY',
};

function num(v: unknown, fallback = 0): number { return typeof v === 'number' && Number.isFinite(v) ? v : fallback; }
function str(v: unknown, fallback = ''): string { return typeof v === 'string' ? v : fallback; }
function requirements(o: Record<string, unknown>): { power: number; crew: number; slots: number } {
  const r = (o.requirements ?? {}) as Record<string, unknown>;
  return { power: num(r.power), crew: num(r.crew), slots: num(r.slots) };
}

/** Build a FULL Ship from a shipyard listing (satisfies convertShipData's presence guard). */
export function mintShipFromListing(symbol: string, shipType: string, waypointSymbol: string, listing: ShipyardListing): Ship {
  const systemSymbol = waypointSymbol.split('-').slice(0, 2).join('-');
  const frame = (listing.frame ?? {}) as Record<string, unknown>;
  const reactor = (listing.reactor ?? {}) as Record<string, unknown>;
  const engine = (listing.engine ?? {}) as Record<string, unknown>;
  const crew = ((listing as Record<string, unknown>).crew ?? {}) as Record<string, unknown>;
  const modules = (listing.modules ?? []) as Array<Record<string, unknown>>;
  const mounts = (listing.mounts ?? []) as Array<Record<string, unknown>>;
  const fuelCapacity = num(frame.fuelCapacity);
  const cargoCapacity = modules.reduce((sum, m) => sum + num(m.capacity), 0);
  return {
    symbol,
    registration: { role: ROLE_BY_SHIP_TYPE[shipType] ?? shipType },
    nav: { systemSymbol, waypointSymbol, status: 'DOCKED', flightMode: 'CRUISE', route: null },
    fuel: { current: fuelCapacity, capacity: fuelCapacity },
    cargo: { capacity: cargoCapacity, units: 0, inventory: [] },
    cooldown: null,
    engine: { speed: num(engine.speed) },
    frame: { symbol: str(frame.symbol), moduleSlots: num(frame.moduleSlots), mountingPoints: num(frame.mountingPoints) },
    reactor: { symbol: str(reactor.symbol), name: str(reactor.name), powerOutput: num(reactor.powerOutput), requirements: requirements(reactor) },
    crew: { current: num(crew.required), required: num(crew.required), capacity: num(crew.capacity) },
    modules: modules.map((m) => ({ symbol: str(m.symbol), capacity: num(m.capacity), range: num(m.range), requirements: requirements(m) })),
    mounts: mounts.map((m) => ({ symbol: str(m.symbol), name: str(m.name), strength: num(m.strength), deposits: Array.isArray(m.deposits) ? (m.deposits as string[]) : [], requirements: requirements(m) })),
  };
}

/** Pure POST /v2/my/ships. Success mutates world (mints ship, debits credits, bumps
 *  shipCounter) and returns 201 + the client.go:1205 envelope. Guard failures mutate
 *  nothing: no shipyard → 404; type not sold → 400; can't afford → 400. Bearer auth is
 *  enforced by the route wrapper before this runs (world.agent non-null). */
export function applyPurchaseShip(world: World, body: PurchaseBody, now: Date): PurchaseResult {
  const shipType = str(body.shipType);
  const waypointSymbol = str(body.waypointSymbol);
  const shipyard = world.shipyards.get(waypointSymbol);
  if (!shipyard) return { status: 404, json: apiError(404, `Waypoint ${waypointSymbol} has no shipyard.`) };
  const listing = shipyard.ships.find((s) => s.type === shipType);
  if (!listing) return { status: 400, json: apiError(400, `Shipyard ${waypointSymbol} does not sell ${shipType}.`) };
  const price = listing.purchasePrice;
  const agent = world.agent as Agent;
  if (agent.credits < price) return { status: 400, json: apiError(400, `Insufficient credits: have ${agent.credits}, need ${price} for ${shipType}.`) };

  const symbol = `${agent.symbol}-${world.shipCounter}`;
  const ship = mintShipFromListing(symbol, shipType, waypointSymbol, listing);
  world.ships.set(symbol, ship);
  world.shipCounter += 1;
  agent.credits -= price;
  const transaction: PurchaseTransaction = { waypointSymbol, shipSymbol: symbol, shipType, price, agentSymbol: agent.symbol, timestamp: now.toISOString() };
  return { status: 201, json: { data: { agent: { ...agent }, ship, transaction } } };
}
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/world/purchase.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/world/purchase.ts twin/tests/world/purchase.test.ts
rtk git commit -m "feat(twin): applyPurchaseShip + mintShipFromListing — POST /my/ships decode-target + error envelopes"
```

---

## Task 27 — `POST /v2/my/ships` (buy) route + `shipyard purchase` acceptance

**Files:** Create `twin/src/routes/my-ships-purchase.ts`. Modify `twin/src/server.ts` (add `await myShipsPurchaseRoutes(v2)`). Test `twin/tests/endpoints/my-ships-purchase.test.ts` (create).

**Reconciliation:** `myShipsPurchaseRoutes(app)` plugin reading `getWorld()`, relative path `/my/ships`. **Dependency:** the CLI happy-path drives the daemon's purchase container through sibling endpoints (`GET /my/ships/:s`, `POST …/orbit|navigate|dock`, `GET …/shipyard`, `GET /my/agent`, `GET …/waypoints/:w`) — DAG-order after Tasks 18–25. Pin `--waypoint X1-PZ28-A2` (A2 is at HQ's coords → distance-0 hop). The 401 leg needs only the skeleton + register.

**Interfaces:** Consumes `applyPurchaseShip`, `PurchaseBody` (Task 26); `unauthorized` (errors); `getWorld`. The daemon driver is `spacetraders shipyard purchase` (async container; behavior polled via `GET /_twin/state`).

- [ ] **Step 1 — test.** Create `twin/tests/endpoints/my-ships-purchase.test.ts`:

```ts
import { beforeEach, describe, expect, it } from 'vitest';
import { runCli, TWIN_ADMIN, TWIN_BASE_URL } from '../helpers/run-cli';

const A2 = 'X1-PZ28-A2';
interface StateResp {
  agent: { symbol: string; credits: number } | null;
  ships: Array<{ symbol: string; registration: { role: string }; nav: { waypointSymbol: string } }>;
  shipyards: Record<string, { ships: Array<{ type: string; purchasePrice: number }> }>;
}
async function twinReset(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  if (!res.ok) throw new Error(`/_twin/reset failed: ${res.status}`);
}
async function twinState(): Promise<StateResp> { const res = await fetch(`${TWIN_ADMIN}/state`); if (!res.ok) throw new Error(`/_twin/state failed: ${res.status}`); return (await res.json()) as StateResp; }
async function waitForShipCount(target: number, timeoutMs = 20_000): Promise<StateResp> {
  const deadline = Date.now() + timeoutMs; let last = await twinState();
  while (last.ships.length < target && Date.now() < deadline) { await new Promise((r) => setTimeout(r, 300)); last = await twinState(); }
  return last;
}

describe('POST /v2/my/ships (buy probe)', () => {
  beforeEach(async () => { await twinReset(); });
  it('rejects a bad bearer token with 401 + envelope and no world change', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/ships`, { method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer not-the-real-token' }, body: JSON.stringify({ shipType: 'SHIP_PROBE', waypointSymbol: A2 }) });
    expect(res.status).toBe(401);
    expect(((await res.json()) as { error?: { code?: number } }).error?.code).toBe(401);
    expect((await twinState()).ships.length).toBe(2);
  });
  it('shipyard purchase mints a SHIP_PROBE at A2, grows the fleet, debits the price', async () => {
    const before = await twinState();
    const initialCount = before.ships.length; const initialCredits = before.agent!.credits;
    const price = before.shipyards[A2].ships.find((s) => s.type === 'SHIP_PROBE')!.purchasePrice;
    const res = runCli(['shipyard', 'purchase', '--ship', 'TWINAGENT-1', '--type', 'SHIP_PROBE', '--quantity', '1', '--waypoint', A2, '--agent', 'TWINAGENT'], { timeoutMs: 30_000 });
    expect(res.exitCode, res.stderr).toBe(0);
    expect(res.stdout).toContain('Ship purchase started successfully');
    const after = await waitForShipCount(initialCount + 1);
    expect(after.ships.length).toBe(initialCount + 1);
    const bought = after.ships.find((s) => !before.ships.some((b) => b.symbol === s.symbol))!;
    expect(bought.registration.role).toBe('SATELLITE');
    expect(bought.nav.waypointSymbol).toBe(A2);
    expect(after.agent!.credits).toBe(initialCredits - price);
  });
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/endpoints/my-ships-purchase.test.ts
```
Expected: 401 leg fails 404→401 (route missing); happy-path leg count stays 2.

- [ ] **Step 3 — implement.** Create `twin/src/routes/my-ships-purchase.ts`:

```ts
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { unauthorized } from '../errors.js';
import { applyPurchaseShip, type PurchaseBody } from '../world/purchase.js';

/** POST /v2/my/ships — purchase a ship at a shipyard (client.go:1197/1205). Bearer
 *  token must equal world.agentToken; applyPurchaseShip owns all business guards. */
export async function myShipsPurchaseRoutes(app: FastifyInstance): Promise<void> {
  app.post('/my/ships', async (req, reply) => {
    const world = getWorld();
    const header = req.headers.authorization ?? '';
    const token = header.startsWith('Bearer ') ? header.slice('Bearer '.length) : '';
    if (!world.agentToken || token !== world.agentToken) return unauthorized(reply, 'Invalid agent token.');
    const result = applyPurchaseShip(world, (req.body ?? {}) as PurchaseBody, new Date());
    return reply.status(result.status).send(result.json);
  });
}
```

In `twin/src/server.ts`, add `import { myShipsPurchaseRoutes } from './routes/my-ships-purchase.js';` and uncomment `await myShipsPurchaseRoutes(v2);`.

> **Route order note:** `POST /my/ships` (this route) and `GET /my/ships` (Task 20) share the `/my/ships` path on different methods — no conflict. Ensure `POST /my/ships` is registered in the same `/v2` scope.

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run tests/endpoints/my-ships-purchase.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/src/routes/my-ships-purchase.ts twin/src/server.ts twin/tests/endpoints/my-ships-purchase.test.ts
rtk git commit -m "feat(twin): POST /my/ships buy route + shipyard-purchase end-to-end contract/behavior test"
```

---

## Task 28 — Bootstrap-tuned E2E daemon config + Go isolation-parity guard

**Files:** Create `twin/test-config.bootstrap.yaml`. Test `gobot/internal/infrastructure/config/bootstrap_e2e_config_test.go` (create).

**Why:** the standing bootstrap coordinator ticks every `bootstrap.tick_seconds` (default 300 s), and viper binds only `metrics.*` nested env keys (never `ST_BOOTSTRAP_*`), so the fast tick can only travel via a config FILE. This is a SIBLING of `test-config.yaml` with byte-identical isolation values + a fast `[bootstrap]` section, selected for the E2E daemon via `SPACETRADERS_CONFIG`.

**Interfaces:** Produces the config with `tick_seconds: 2`, `probe_target: 3`, `coverage_bar: 2.0` (> 1 so `CoverageFraction()` ≤ 1 never clears it → the arc PARKS in DATA, never buys haulers), `reserve_margin: 0.9`.

- [ ] **Step 1 — failing test.** Create `gobot/internal/infrastructure/config/bootstrap_e2e_config_test.go`:

```go
package config

import (
	"path/filepath"
	"testing"
)

// TestBootstrapE2EConfigMatchesIsolationAndTunesBootstrap pins the Slice-1 DATA E2E
// daemon config: a SIBLING of test-config.yaml with byte-identical isolation values
// (the --force PID trap) + a fast [bootstrap] section (unreachable via env).
func TestBootstrapE2EConfigMatchesIsolationAndTunesBootstrap(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ST_METRICS_PORT", "")
	base := filepath.Join("..", "..", "..", "..", "twin", "test-config.yaml")
	e2e := filepath.Join("..", "..", "..", "..", "twin", "test-config.bootstrap.yaml")

	baseCfg, err := LoadConfig(base)
	if err != nil { t.Fatalf("LoadConfig(%s) failed: %v", base, err) }
	cfg, err := LoadConfig(e2e)
	if err != nil { t.Fatalf("LoadConfig(%s) failed: %v", e2e, err) }

	parity := []struct{ name, got, want string }{
		{"daemon.pid_file", cfg.Daemon.PIDFile, baseCfg.Daemon.PIDFile},
		{"daemon.socket_path", cfg.Daemon.SocketPath, baseCfg.Daemon.SocketPath},
		{"daemon.address", cfg.Daemon.Address, baseCfg.Daemon.Address},
		{"database.url", cfg.Database.URL, baseCfg.Database.URL},
	}
	for _, c := range parity {
		if c.got != c.want { t.Errorf("%s = %q, want %q (parity with test-config.yaml)", c.name, c.got, c.want) }
	}
	if cfg.Metrics.Port != baseCfg.Metrics.Port { t.Errorf("metrics.port = %d, want %d", cfg.Metrics.Port, baseCfg.Metrics.Port) }
	if cfg.Captain.PlayerID != baseCfg.Captain.PlayerID { t.Errorf("captain.player_id = %d, want %d", cfg.Captain.PlayerID, baseCfg.Captain.PlayerID) }
	if cfg.Daemon.PIDFile != "/tmp/spacetraders-daemon-test.pid" { t.Errorf("daemon.pid_file = %q, want the -test pidfile", cfg.Daemon.PIDFile) }

	if cfg.Bootstrap.TickSeconds <= 0 || cfg.Bootstrap.TickSeconds > 5 { t.Errorf("bootstrap.tick_seconds = %d, want 1..5s", cfg.Bootstrap.TickSeconds) }
	if cfg.Bootstrap.ProbeTarget != 3 { t.Errorf("bootstrap.probe_target = %d, want 3", cfg.Bootstrap.ProbeTarget) }
	if cfg.Bootstrap.CoverageBar <= 1.0 { t.Errorf("bootstrap.coverage_bar = %v, want > 1.0 so the arc PARKS in DATA", cfg.Bootstrap.CoverageBar) }
	if cfg.Bootstrap.ReserveMargin < 0.8 { t.Errorf("bootstrap.reserve_margin = %v, want >= 0.8", cfg.Bootstrap.ReserveMargin) }
}
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd gobot && rtk go test ./internal/infrastructure/config/ -run TestBootstrapE2EConfigMatchesIsolationAndTunesBootstrap -v
```

- [ ] **Step 3 — implement.** Create `twin/test-config.bootstrap.yaml`:

```yaml
# twin/test-config.bootstrap.yaml — the Slice-1 DATA E2E daemon config. SIBLING of
# twin/test-config.yaml: everything above [bootstrap] is byte-identical isolation
# (the --force PID trap); the [bootstrap] knobs cannot be delivered via env.
database:
  type: postgres
  url: postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable
api:
  base_url: http://127.0.0.1:8080/v2
  rate_limit:
    requests: 10
    burst: 30
daemon:
  pid_file: /tmp/spacetraders-daemon-test.pid # NOT /tmp/spacetraders-daemon.pid (prod)
  socket_path: /tmp/spacetraders-daemon-test.sock
  address: localhost:50062 # prod gRPC is localhost:50052
metrics:
  enabled: true
  port: 9092 # prod daemon serves 9090
captain:
  player_id: 1
bootstrap:
  tick_seconds: 2       # fast ramp — prod default is 300s
  probe_target: 3       # the DATA acceptance target: 3 probes scouting
  coverage_bar: 2.0     # > 1 so CoverageFraction() (<=1) never clears it → the arc PARKS in DATA
  reserve_margin: 0.9   # capital gate never blocks two probe buys from 175000 cr
  probe_ship_type: SHIP_PROBE
logging:
  level: info
  format: text
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd gobot && rtk go test ./internal/infrastructure/config/ -run TestBootstrapE2EConfigMatchesIsolationAndTunesBootstrap -v
cd gobot && rtk go test ./internal/infrastructure/config/
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/test-config.bootstrap.yaml gobot/internal/infrastructure/config/bootstrap_e2e_config_test.go
rtk git commit -m "feat(twin): bootstrap DATA-e2e daemon config (fast tick, DATA-parked) + Go isolation-parity guard"
```

---

## Task 29 — Slice-1 DATA end-to-end acceptance (`workflow bootstrap`)

**Files:** Create `twin/tests/helpers/bootstrap-e2e.ts`, `twin/tests/e2e/bootstrap-data.test.ts`. Modify `twin/package.json` (add dev deps `pg` + `@types/pg`).

**Depends on everything (all endpoints green).** Boots the bootstrap-tuned daemon, launches the coordinator, watches it ramp 1→3 probes, restarts the daemon mid-ramp, and confirms it converges to exactly 3 assigned probes with the 2-probe spend and no double-buy. **SUT invariant:** a purchased `SHIP_PROBE` has `registration.role === "SATELLITE"` (Task 26 mints this) — else `ProbeCount` never increments and the coordinator loops forever.

**Interfaces:** Consumes `runCli` + constants, `startTestDaemon/stopTestDaemon/restartTestDaemon` (Task 16), `POST /_twin/reset`/`GET /_twin/state`, and Task 28's config.

- [ ] **Step 1 — test.** Create `twin/tests/e2e/bootstrap-data.test.ts`:

```ts
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { runCli } from '../helpers/run-cli';
import { restartTestDaemon, startTestDaemon, stopTestDaemon } from '../helpers/daemon';
import {
  STARTING_CREDITS, bootstrapDaemonEnv, pollUntil, resetDaemonState, resetTwin,
  scoutSatellites, shipListRows, sleep, twinState,
} from '../helpers/bootstrap-e2e';

const CONVERGE_MS = 180_000;
const TICK_MS = 2_000;

describe('Slice-1 DATA acceptance — reaches 3 scouting probes, restart-idempotent', () => {
  beforeAll(async () => {
    await stopTestDaemon();
    await resetTwin();
    await resetDaemonState();
    await startTestDaemon(bootstrapDaemonEnv());
  }, 120_000);
  afterAll(async () => {
    await stopTestDaemon();
    await startTestDaemon().catch(() => undefined);
  }, 120_000);

  it('ramps to 3 assigned probes, no double-buy across a mid-purchase restart', async () => {
    const cold = await twinState();
    expect(cold.agent?.credits).toBe(STARTING_CREDITS);
    expect(scoutSatellites(cold)).toHaveLength(1);
    const probePrices = Object.values(cold.shipyards).flatMap((y) => y.ships).filter((s) => s.type === 'SHIP_PROBE').map((s) => s.purchasePrice);
    expect(probePrices.length).toBeGreaterThan(0);
    const minProbe = Math.min(...probePrices); const maxProbe = Math.max(...probePrices);

    const launch = runCli(['workflow', 'bootstrap', '--agent', 'TWINAGENT']);
    expect(launch.exitCode, launch.stderr).toBe(0);
    expect(launch.stdout).toContain('Captain bootstrap coordinator started');

    const midCount = await pollUntil(
      async () => { const n = scoutSatellites(await twinState()).length; return n >= 2 ? n : null; },
      { timeoutMs: CONVERGE_MS, intervalMs: 250, label: 'first probe buy (>=2 SATELLITE hulls)' },
    );
    expect(midCount).toBeGreaterThanOrEqual(2);

    await restartTestDaemon(bootstrapDaemonEnv());

    const assignedRows = await pollUntil(
      async () => { const sats = shipListRows().filter((r) => r.role === 'SATELLITE' && r.assignment !== '-'); return sats.length === 3 ? sats : null; },
      { timeoutMs: CONVERGE_MS, intervalMs: 1_000, label: '3 SATELLITE probes all assigned' },
    );
    expect(assignedRows).toHaveLength(3);

    const converged = await twinState();
    expect(scoutSatellites(converged)).toHaveLength(3);
    const convergedCredits = converged.agent!.credits;
    const spend = STARTING_CREDITS - convergedCredits;
    expect(spend).toBeGreaterThanOrEqual(2 * minProbe);
    expect(spend).toBeLessThanOrEqual(2 * maxProbe);

    await sleep(TICK_MS * 4);
    const settled = await twinState();
    expect(scoutSatellites(settled)).toHaveLength(3);
    expect(settled.agent!.credits).toBe(convergedCredits);
  }, CONVERGE_MS * 2 + 60_000);
});
```

- [ ] **Step 2 — run, expect FAIL.**
```bash
cd twin && rtk npx vitest run tests/e2e/bootstrap-data.test.ts
```
Expected: `Failed to resolve import "../helpers/bootstrap-e2e"`.

- [ ] **Step 3 — implement.** Add deps then create the helper:
```bash
rtk npm --prefix twin install --save-dev pg @types/pg
```

Create `twin/tests/helpers/bootstrap-e2e.ts`:

```ts
import path from 'node:path';
import { Client } from 'pg';
import { REPO_ROOT, TEST_DATABASE_URL, TWIN_ADMIN, TWIN_BASE_URL, runCli } from './run-cli.js';
import type { Agent, Ship, Shipyard } from '../../src/world/types.js';

export const BOOTSTRAP_E2E_CONFIG = path.join(REPO_ROOT, 'twin', 'test-config.bootstrap.yaml');
export const STARTING_CREDITS = 175_000;

/** Env overlay pointing a test daemon at the bootstrap-tuned config (overlaid LAST). */
export function bootstrapDaemonEnv(): Record<string, string> {
  return { SPACETRADERS_CONFIG: BOOTSTRAP_E2E_CONFIG, ST_API_BASE_URL: TWIN_BASE_URL, DATABASE_URL: TEST_DATABASE_URL };
}
export const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

export interface TwinStateView { agent: Agent | null; ships: Ship[]; shipyards: Record<string, Shipyard> }
export async function twinState(): Promise<TwinStateView> {
  const res = await fetch(`${TWIN_ADMIN}/state`); if (!res.ok) throw new Error(`GET /_twin/state -> ${res.status}`); return (await res.json()) as TwinStateView;
}
export async function resetTwin(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  if (!res.ok) throw new Error(`POST /_twin/reset -> ${res.status}`);
}
export function scoutSatellites(s: TwinStateView): Ship[] { return s.ships.filter((sh) => sh.registration.role === 'SATELLITE'); }

export interface ShipListRow { symbol: string; location: string; navStatus: string; role: string; fleet: string; assignment: string }
export function shipListRows(): ShipListRow[] {
  const r = runCli(['ship', 'list', '--agent', 'TWINAGENT', '--json']);
  if (r.exitCode !== 0) throw new Error(`ship list --json failed (${r.exitCode}): ${r.stderr}`);
  return JSON.parse(r.stdout) as ShipListRow[];
}

/** Truncate the daemon's mutable world tables (SyncAllFromAPI never prunes). Keeps
 *  players/eras. MUST run only while the daemon is DOWN. */
export async function resetDaemonState(): Promise<void> {
  const client = new Client({ connectionString: TEST_DATABASE_URL });
  await client.connect();
  try { await client.query('TRUNCATE containers, container_logs, ships RESTART IDENTITY CASCADE'); }
  finally { await client.end(); }
}

export interface PollOpts { timeoutMs: number; intervalMs: number; label: string }
export async function pollUntil<T>(fn: () => Promise<T | null> | T | null, opts: PollOpts): Promise<T> {
  const deadline = Date.now() + opts.timeoutMs; let last: T | null = null;
  while (Date.now() < deadline) { last = await fn(); if (last !== null) return last; await sleep(opts.intervalMs); }
  throw new Error(`pollUntil timed out after ${opts.timeoutMs}ms waiting for: ${opts.label} (last=${JSON.stringify(last)})`);
}
```

- [ ] **Step 4 — run, expect PASS** (Sections/Tasks 1–27 green; Task 28 committed):
```bash
cd twin && rtk npx vitest run tests/e2e/bootstrap-data.test.ts
```
If it fails at `first probe buy`: the purchase mints probes with a role other than `SATELLITE`, or the purchase/shipyard endpoints aren't green. If it fails at `3 SATELLITE probes all assigned`: the coordinator didn't recover after the restart or scout-assignment isn't reaching the twin.

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/tests/helpers/bootstrap-e2e.ts twin/tests/e2e/bootstrap-data.test.ts twin/package.json twin/package-lock.json
rtk git commit -m "test(twin): Slice-1 DATA end-to-end — workflow bootstrap reaches 3 scouting probes, restart-idempotent"
```

---

## Task 30 — `twin/docker-compose.test.yml` — parallel, collision-free stack

**Files:** Create `twin/docker-compose.test.yml`. Test `twin/tests/observability/docker-compose.test.ts` (create).

**Interfaces:** Produces the manual observability stack; `postgres-test` (container `spacetraders-twin-postgres`, DB `spacetraders_test`, host 5433) doubles as THE canonical test Postgres consumed by the daemon/globalSetup. Prometheus 9093, Grafana 3001, distinct volumes `twin-*-data`, project `spacetraders-twin`. Reuses the gobot dashboards read-only.

- [ ] **Step 1 — test.** Create `twin/tests/observability/docker-compose.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
const TWIN_DIR = path.resolve(__dirname, '..', '..');
const COMPOSE = path.join(TWIN_DIR, 'docker-compose.test.yml');
const compose = () => readFileSync(COMPOSE, 'utf8');
describe('docker-compose.test.yml — parallel isolation from production', () => {
  it('names containers spacetraders-twin-* (never prod)', () => {
    const c = compose();
    expect(c).toContain('container_name: spacetraders-twin-prometheus');
    expect(c).toContain('container_name: spacetraders-twin-grafana');
    expect(c).toContain('container_name: spacetraders-twin-postgres');
    expect(c).not.toMatch(/container_name:\s*spacetraders-prometheus\b/);
  });
  it('remaps host ports 9093/3001/5433, binds none of 9091/3000/5432', () => {
    const c = compose();
    expect(c).toContain('"9093:9090"'); expect(c).toContain('"3001:3000"'); expect(c).toContain('"5433:5432"');
    expect(c).not.toMatch(/"9091:\d+"/); expect(c).not.toMatch(/"3000:3000"/); expect(c).not.toMatch(/"5432:5432"/);
  });
  it('uses distinct twin-*-data volumes', () => {
    const c = compose();
    for (const v of ['twin-prometheus-data', 'twin-grafana-data', 'twin-postgres-data']) expect(c.match(new RegExp(v, 'g'))?.length ?? 0).toBeGreaterThanOrEqual(2);
  });
  it('feeds Prometheus the test scrape config and reuses prod dashboards', () => {
    const c = compose();
    expect(c).toContain('./configs/prometheus.test.yml:/etc/prometheus/prometheus.yml:ro');
    expect(c).toContain('../gobot/configs/grafana/dashboards:/var/lib/grafana/dashboards:ro');
    expect(c).toContain('../gobot/configs/grafana/provisioning/dashboards:/etc/grafana/provisioning/dashboards:ro');
    expect(c).toContain('./configs/grafana/provisioning/datasources:/etc/grafana/provisioning/datasources:ro');
  });
  it('the test Postgres is spacetraders_test', () => {
    const c = compose();
    expect(c).toContain('POSTGRES_DB: spacetraders_test'); expect(c).toContain('POSTGRES_USER: spacetraders');
  });
});
```

- [ ] **Step 2 — run, expect FAIL** (`ENOENT`).
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/docker-compose.test.ts
```
(Observability guard tests are pure file reads — run them under the unit config to avoid booting the stack. Add `tests/observability/**` to the unit config's `include` if not already matched, or run with the default config once the stack exists.)

- [ ] **Step 3 — implement.** Create `twin/docker-compose.test.yml`:

```yaml
# twin/docker-compose.test.yml — PARALLEL observability stack for the digital twin.
# Runs alongside gobot/docker-compose.metrics.yml with zero collisions. postgres-test
# doubles as THE canonical test DB (spacetraders_test on host 5433).
name: spacetraders-twin
services:
  postgres-test:
    image: postgres:16-alpine
    container_name: spacetraders-twin-postgres
    environment:
      POSTGRES_DB: spacetraders_test
      POSTGRES_USER: spacetraders
      POSTGRES_PASSWORD: dev_password
    ports:
      - "5433:5432"
    volumes:
      - twin-postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U spacetraders -d spacetraders_test"]
      interval: 5s
      timeout: 5s
      retries: 10
    restart: unless-stopped
    networks: [twin-metrics]
  prometheus-test:
    image: prom/prometheus:latest
    container_name: spacetraders-twin-prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--storage.tsdb.retention.time=3d'
      - '--web.console.libraries=/usr/share/prometheus/console_libraries'
      - '--web.console.templates=/usr/share/prometheus/consoles'
    ports:
      - "9093:9090"
    volumes:
      - ./configs/prometheus.test.yml:/etc/prometheus/prometheus.yml:ro
      - twin-prometheus-data:/prometheus
    extra_hosts:
      - "host.docker.internal:host-gateway"
    restart: unless-stopped
    networks: [twin-metrics]
  grafana-test:
    image: grafana/grafana:latest
    container_name: spacetraders-twin-grafana
    environment:
      - GF_SECURITY_ADMIN_USER=admin
      - GF_SECURITY_ADMIN_PASSWORD=admin
      - GF_USERS_ALLOW_SIGN_UP=false
      - GF_SERVER_ROOT_URL=http://localhost:3001
    ports:
      - "3001:3000"
    volumes:
      - twin-grafana-data:/var/lib/grafana
      - ./configs/grafana/provisioning/datasources:/etc/grafana/provisioning/datasources:ro
      - ../gobot/configs/grafana/provisioning/dashboards:/etc/grafana/provisioning/dashboards:ro
      - ../gobot/configs/grafana/dashboards:/var/lib/grafana/dashboards:ro
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on: [prometheus-test, postgres-test]
    restart: unless-stopped
    networks: [twin-metrics]
networks:
  twin-metrics:
    driver: bridge
volumes:
  twin-prometheus-data: { driver: local }
  twin-grafana-data: { driver: local }
  twin-postgres-data: { driver: local }
```

- [ ] **Step 4 — run + structural validation.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/docker-compose.test.ts
docker compose -f twin/docker-compose.test.yml config -q && echo "exit=$?"
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/docker-compose.test.yml twin/tests/observability/docker-compose.test.ts
rtk git commit -m "feat(twin): docker-compose.test.yml — parallel Prometheus/Grafana/Postgres, collision-free"
```

---

## Task 31 — `twin/configs/prometheus.test.yml`

**Files:** Create `twin/configs/prometheus.test.yml`. Test `twin/tests/observability/prometheus-config.test.ts` (create).

**Interfaces:** Scrapes `host.docker.internal:9092` (the test daemon; never prod 9090); keeps only `spacetraders_*` series; retains `job_name: 'spacetraders-daemon'`.

- [ ] **Step 1 — test.** Create `twin/tests/observability/prometheus-config.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
const TWIN_DIR = path.resolve(__dirname, '..', '..');
const prom = () => readFileSync(path.join(TWIN_DIR, 'configs', 'prometheus.test.yml'), 'utf8');
describe('prometheus.test.yml — scrapes the test daemon, never prod', () => {
  it('scrapes 9092 via host.docker.internal', () => expect(prom()).toContain("targets: ['host.docker.internal:9092']"));
  it('never scrapes host.docker.internal:9090', () => expect(prom()).not.toContain('host.docker.internal:9090'));
  it('keeps only spacetraders_* series and mounts no rule files', () => {
    const p = prom(); expect(p).toContain("regex: 'spacetraders_.*'"); expect(p).toContain('action: keep'); expect(p).not.toContain('rule_files');
  });
  it("retains job_name 'spacetraders-daemon'", () => expect(prom()).toContain("job_name: 'spacetraders-daemon'"));
});
```

- [ ] **Step 2 — run, expect FAIL** (`ENOENT`).
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/prometheus-config.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/configs/prometheus.test.yml`:

```yaml
# twin/configs/prometheus.test.yml — scrape config for the ISOLATED twin stack.
# Copied from gobot/configs/prometheus/prometheus.yml with the target changed to the
# TEST daemon's metrics port 9092 (prod serves 9090). No alerting rule files.
global:
  scrape_interval: 15s
  evaluation_interval: 15s
  external_labels:
    cluster: 'spacetraders'
    environment: 'twin-test'
scrape_configs:
  - job_name: 'spacetraders-daemon'
    static_configs:
      - targets: ['host.docker.internal:9092']
        labels:
          service: 'daemon'
          instance: 'twin-test'
    scrape_timeout: 10s
    metric_relabel_configs:
      - source_labels: [__name__]
        regex: 'spacetraders_.*'
        action: keep
```

- [ ] **Step 4 — run + optional promtool.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/prometheus-config.test.ts
docker run --rm -v "$(pwd)/twin/configs:/cfg:ro" --entrypoint promtool prom/prometheus:latest check config /cfg/prometheus.test.yml; echo "exit=$?"
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/configs/prometheus.test.yml twin/tests/observability/prometheus-config.test.ts
rtk git commit -m "feat(twin): prometheus.test.yml — scrape the isolated test daemon on 9092 (not prod 9090)"
```

---

## Task 32 — `twin/configs/grafana/provisioning/datasources/datasource.test.yml`

**Files:** Create `twin/configs/grafana/provisioning/datasources/datasource.test.yml`. Test `twin/tests/observability/grafana-datasource.test.ts` (create).

**Interfaces:** Keeps the dashboards' fixed uids `prometheus`/`postgresql`; redirects Prometheus → `prometheus-test:9090` and PostgreSQL → `postgres-test:5432`/`spacetraders_test`, so the reused dashboards render on test data with zero edits.

- [ ] **Step 1 — test.** Create `twin/tests/observability/grafana-datasource.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
const TWIN_DIR = path.resolve(__dirname, '..', '..');
const ds = () => readFileSync(path.join(TWIN_DIR, 'configs', 'grafana', 'provisioning', 'datasources', 'datasource.test.yml'), 'utf8');
describe('datasource.test.yml — reused dashboards resolve against TEST data', () => {
  it('keeps the exact uids the gobot dashboards bind to', () => { const d = ds(); expect(d).toContain('uid: prometheus'); expect(d).toContain('uid: postgresql'); });
  it('points Prometheus at the twin test Prometheus', () => expect(ds()).toContain('url: http://prometheus-test:9090'));
  it('points PostgreSQL at spacetraders_test on the twin postgres service', () => {
    const d = ds(); expect(d).toContain('url: postgres-test:5432'); expect(d).toContain('database: spacetraders_test'); expect(d).not.toContain('database: spacetraders\n');
  });
});
```

- [ ] **Step 2 — run, expect FAIL** (`ENOENT`).
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/grafana-datasource.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/configs/grafana/provisioning/datasources/datasource.test.yml`:

```yaml
# twin/configs/grafana/provisioning/datasources/datasource.test.yml
# TEST-ONLY datasources. Keeps the reused dashboards' fixed uids (prometheus/postgresql)
# but redirects them at the TEST stack. Zero dashboard edits.
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    uid: prometheus
    url: http://prometheus-test:9090
    isDefault: true
    editable: false
    jsonData:
      timeInterval: 15s
      httpMethod: POST
  - name: PostgreSQL
    type: postgres
    access: proxy
    uid: postgresql
    url: postgres-test:5432
    database: spacetraders_test
    user: spacetraders
    editable: false
    secureJsonData:
      password: dev_password
    jsonData:
      database: spacetraders_test
      sslmode: disable
      maxOpenConns: 5
      maxIdleConns: 2
      connMaxLifetime: 14400
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/grafana-datasource.test.ts
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/configs/grafana/provisioning/datasources/datasource.test.yml twin/tests/observability/grafana-datasource.test.ts
rtk git commit -m "feat(twin): grafana test datasources — reuse gobot dashboards (fixed uids) on spacetraders_test data"
```

---

## Task 33 — `twin/README.md` — full local run recipe

**Files:** Create `twin/README.md`. Test `twin/tests/observability/readme.test.ts` (create).

**Reconciliation:** the recipe's capture step is `twin/scripts/capture-x1pz28.sh` (Task 5), NOT the foundation's illustrative `capture-topology.ts`/`capture-markets.sh`. The guard's `STEPS` array is updated to match.

- [ ] **Step 1 — test.** Create `twin/tests/observability/readme.test.ts`:

```ts
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
const TWIN_DIR = path.resolve(__dirname, '..', '..');
const readme = () => readFileSync(path.join(TWIN_DIR, 'README.md'), 'utf8');
const STEPS = [
  'twin/scripts/capture-x1pz28.sh',
  'twin/scripts/launch-test-stack.sh',
  'twin/scripts/seed-player.sh',
  'rtk vitest run',
  'docker compose -f twin/docker-compose.test.yml up -d prometheus-test grafana-test',
];
describe('twin/README.md — full local run recipe, documented + in order', () => {
  it('documents each recipe step by its real command/script name', () => {
    const r = readme(); for (const step of STEPS) expect(r, `missing: ${step}`).toContain(step);
  });
  it('orders capture -> launch -> seed -> vitest -> compose up', () => {
    const r = readme(); const idx = STEPS.map((s) => r.indexOf(s));
    expect(idx.every((i) => i >= 0)).toBe(true);
    for (let i = 1; i < idx.length; i++) expect(idx[i]).toBeGreaterThan(idx[i - 1]);
  });
  it('documents the isolated observability ports (9093/3001/5433)', () => {
    const r = readme(); expect(r).toContain('9093'); expect(r).toContain('3001'); expect(r).toContain('5433');
  });
});
```

- [ ] **Step 2 — run, expect FAIL** (`ENOENT`).
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/readme.test.ts
```

- [ ] **Step 3 — implement.** Create `twin/README.md`:

```markdown
# SpaceTraders Digital Twin

A fast, disposable, deterministic reimplementation of the SpaceTraders v2 API
(Node + TypeScript + Fastify) with ship travel compressed 100x. It drives the
already-hardened `spacetraders` CLI and daemon end-to-end without mutating the live game.

Design: `docs/superpowers/specs/2026-07-11-spacetraders-digital-twin-design.md`.

## Prerequisites

- Node >= 22 and `npm --prefix twin install`.
- Go toolchain: `make -C gobot build-cli build-daemon`.
- Docker (test Postgres + observability): `docker --version`.
- Read-only access to the prod Postgres for the one-time topology capture.

## The full local run recipe

Run these in order from the repo root.

### 1. Capture the home-system fixtures (one-time)

Snapshot the real era-2 X1-PZ28 world into `twin/fixtures/era2-X1-PZ28/` (topology
read-only from the prod `waypoints` table; markets/shipyards synthesized).

```bash
twin/scripts/capture-x1pz28.sh   # prod DB is READ-ONLY (capture only)
```

### 2. Bring up the test Postgres, then launch twin + isolated test daemon

```bash
docker compose -f twin/docker-compose.test.yml up -d postgres-test
twin/scripts/launch-test-stack.sh
```

`launch-test-stack.sh` boots the twin on `http://127.0.0.1:8080/v2` and an isolated
test daemon (pidfile `/tmp/spacetraders-daemon-test.pid`, gRPC `localhost:50062`,
metrics `:9092`) — never `--force`. It auto-migrates the empty `spacetraders_test` schema.

### 3. Seed the cold-start agent

```bash
twin/scripts/seed-player.sh
```

### 4. Run the acceptance suite

```bash
rtk vitest run
```

The Slice-4 test drives `workflow bootstrap` to the DATA-phase acceptance (3 probes
scouting, restart-idempotent). The daemon emits `spacetraders_*` metrics on `:9092`.

### 5. Bring up observability to view the run

```bash
docker compose -f twin/docker-compose.test.yml up -d prometheus-test grafana-test
```

Open Grafana at <http://localhost:3001> (admin/admin) — the same dashboards as
production, now reading `spacetraders_test` and the test daemon's metrics.

## Observability stack — parallel and collision-free

`twin/docker-compose.test.yml` runs alongside `gobot/docker-compose.metrics.yml`; nothing collides.

| Service          | Container                     | Host port | Prod host port |
| ---------------- | ----------------------------- | --------- | -------------- |
| Prometheus       | `spacetraders-twin-prometheus`| `9093`    | `9091`         |
| Grafana          | `spacetraders-twin-grafana`   | `3001`    | `3000`         |
| Postgres (test)  | `spacetraders-twin-postgres`  | `5433`    | `5432`         |

- Prometheus scrapes the test daemon on `:9092` (prod daemon serves `9090`).
- Dashboards are reused verbatim from `gobot/configs/grafana/dashboards`; a test-only
  datasource file keeps their fixed uids and redirects them at the test stack.
- Named volumes are `twin-*-data`, so `down -v` never touches production data.

## Safety

The production daemon runs with `--force`, which SIGTERM-kills whatever PID sits in the
configured `daemon.pid_file`. The test stack is isolated on every axis (pidfile, socket,
gRPC, metrics, database, container names, host ports, volumes). See `twin/test-config.yaml`
and `twin/scripts/launch-test-stack.sh`.
```

- [ ] **Step 4 — run, expect PASS.**
```bash
cd twin && rtk npx vitest run --config vitest.unit.config.ts tests/observability/
```

- [ ] **Step 5 — commit.**
```bash
rtk git add twin/README.md twin/tests/observability/readme.test.ts
rtk git commit -m "docs(twin): README — full capture->launch->seed->vitest->compose-up run recipe"
```

---

## Self-review report

Performed against the writing-plans self-review checklist.

### 1. Spec coverage — every spec section maps to a task

| Spec section | Task(s) |
|---|---|
| Base-URL seam (bot-side) | 1 |
| Serving & admin namespace (`/v2`, `/_twin`) | 14, 15 |
| Endpoint surface — `GET /` | 14 (route), 19 (acceptance) |
| `POST /register` | 8/9/10 (builder), 17 (route) |
| `GET /my/agent` | 18 |
| `GET /my/ships[/{s}]` | 20 |
| `POST /my/ships` (buy) | 26, 27 |
| `navigate` / `orbit` / `dock` / `refuel` | 24, 25 |
| `GET /systems/{s}/waypoints[/{w}]` | 21 |
| `…/market` · `…/shipyard` | 22, 23 |
| Response fidelity (Go decode targets) | 3 (types), all route tasks |
| Errors envelope + codes 4214/4244/4511 | 12 |
| World model + home-system capture (X1-PZ28) | 5 (capture), 8 (loader) |
| Compressed clock (lazy on-read flip) | 11, 24 |
| Test daemon & isolation (`--force` trap) | 2, 6, 7, 16 |
| Fixture seeding via `player register` | 7, 16, 17 |
| Observability (parallel stack) | 30, 31, 32, 33 |
| Testing strategy (acceptance-test-first through CLI) | every route task |
| Slice-4 DATA end-to-end | 28, 29 |

**Gaps found + filled:** the source sections referenced a "scaffold section" and a "world-model/types section" that no section actually created — Task 3 (package.json/tsconfig/vitest configs + `types.ts`) and Task 4 (`run-cli.ts`) fill these. The `daemon.ts` lifecycle helpers and `global-setup.ts` (foundation §5.3, described but uncoded) are filled by Task 16. No spec endpoint is unmapped.

### 2. Placeholder scan — no TBD/TODO/"similar to Task N"

Every task's failing test, implementation, run commands, and commit are complete and runnable. The one deliberately-deferred item from the source (`50c`'s "add error handling"-style prose) is replaced by concrete guards in Task 26 (`applyPurchaseShip` returns explicit 404/400 envelopes with messages). No "similar to Task N" cross-references remain; where a route reuses a pattern (e.g. bearer auth) the code is written out in full per file.

### 3. Type & helper consistency across all tasks — fixes applied

- **World module unified** to `twin/src/world/loader.ts` (`loadColdStartWorld`, `loadRegisterTemplate`, `RegisterTemplate`, `mintToken`, `registerAgent`, `FIXTURES_DIR`). Dropped: `20b`'s `load.ts`/`loadWorld`, `30b`'s `cold-start.ts`/`applyRegister` and `register-template.ts`.
- **Register mechanism unified:** `registerAgent(world, {symbol,faction,token}) → {agent,ships}` + `mintToken(symbol)`; the route mints then materializes; reset re-materializes with the preserved token. Replaces `30b`'s `applyRegister` (which minted internally).
- **Server architecture unified** to the store-singleton: every route is a plugin reading `getWorld()`, registered under one `/v2` scope with relative paths; `/_twin` plugins are siblings. Rewrote `30b`/`40b`/`50a`/`50b`'s "pass `world`" registrars (`registerRoute`, `registerMarketRoutes`, `shipNavigateRoutes(world)`, `registerShipActionRoutes`, `registerTwinTestAdminRoutes`) to `registerRoutes`, `marketRoutes`, `shipNavigateRoutes(app)`, `shipActionRoutes`, `testAdminRoutes` — and rewrote full `/v2/...`/`/_twin/...` path strings to relative.
- **`GET /v2/` implemented once** (skeleton `serverStatusRoutes`); `30c`'s duplicate `status.ts`/`statusRoute` dropped, its CLI acceptance kept as Task 19.
- **`shipCounter`** = 0 in `loadColdStartWorld`, set to `ships.length + 1` (=3) by `registerAgent` — reconciled `20b` (which set it at load) vs `30a`/`50c`.
- **Capture script** = `twin/scripts/capture-x1pz28.sh` everywhere; `60b`'s README/guard references to `capture-topology.ts`/`capture-markets.sh` rewritten.
- **`resetDate`** = captured `2026-07-05` (not the illustrative `2026-06-29`); every fixture-reading acceptance (Tasks 17, 19) now READS `server-status.json` for its golden instead of hardcoding, while hermetic unit tests keep self-contained literals.
- **Vitest configs:** `vitest.config.ts` (globalSetup, CLI acceptance) vs `vitest.unit.config.ts` (pure/in-process) — replaces `30b`'s ad-hoc `vitest.unit.config.ts` with a single canonical pair, and the loader/skeleton/clock/purchase unit tests are scoped to the unit config.
- **Helper names** aligned to the foundation: `runCli`/`TWIN_BASE_URL`/`TWIN_ADMIN`/`TEST_DATABASE_URL` (Task 4), `resetWorld`/`getState`/`findShip`/`waitForShip`/`setShipFuel`/`fuelUnitPriceFromState`/`getAgentToken` (Task 25), `startTestDaemon`/`stopTestDaemon`/`restartTestDaemon` (Task 16, consumed identically by Tasks 20 and 29).

















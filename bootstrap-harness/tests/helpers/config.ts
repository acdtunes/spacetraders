import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Self-contained harness config. Depends on NO other package's source. The API target is a
// runtime URL string only (env-overridable) — the harness assumes nothing about whether any
// API server (twin, mock, or the real game) exists at author/typecheck time.
//
// ── PARALLEL PER-WORKER ISOLATION (st-drm.22) ────────────────────────────────────────────────
// vitest can run the e2e specs across N forks concurrently, each fork driving its OWN isolated
// stack (twin + daemon + Postgres DB) on DISJOINT resources. Isolation is keyed on the fork slot:
//   twin port  = 8080 + 10*W      DB   = <base>_W        socket = /tmp/st-daemon-harness-W.sock
//   gRPC       = 50072 + W        metrics port = 9092 + W
// where W is the 1-based fork slot. Per-worker mode is OFF by default; it activates ONLY when the
// parallel runner sets HARNESS_PARALLEL=1 (vitest.config.ts). Without it, EVERY value below is
// today's single-stack default (:8080 / spacetraders_test / harness.sock), so manual `vitest run`
// invocations and the concurrently-running GATE stack are completely unaffected.
//
// WHY VITEST_POOL_ID, not VITEST_WORKER_ID: under vitest, VITEST_WORKER_ID is a MONOTONIC per-file
// counter (1,2,3,… up to the file count — verified empirically), so it is NOT bounded by maxForks
// and cannot key a fixed pool of pre-created DBs. VITEST_POOL_ID is the reused fork SLOT, bounded
// [1..maxForks] and stable for the life of a fork — exactly the per-worker key we want. We read
// POOL_ID first, fall back to WORKER_ID, and only engage per-worker mode under HARNESS_PARALLEL.

const HELPERS_DIR = path.dirname(fileURLToPath(import.meta.url)); // bootstrap-harness/tests/helpers
export const HARNESS_ROOT = path.resolve(HELPERS_DIR, '..', '..'); // bootstrap-harness/
export const REPO_ROOT = path.resolve(HELPERS_DIR, '..', '..', '..'); // spacetraders/
export const TWIN_DIR = path.join(REPO_ROOT, 'twin');
// Env-overridable so the harness can run against a gobot build that carries the ST_API_BASE_URL
// seam (e.g. a feature-branch worktree) without the seam having to land on the main branch yet.
export const GOBOT_DIR = process.env.HARNESS_GOBOT_DIR ?? path.join(REPO_ROOT, 'gobot');
export const CLI_BIN = path.join(GOBOT_DIR, 'bin', 'spacetraders');
export const DAEMON_BIN = path.join(GOBOT_DIR, 'bin', 'spacetraders-daemon');

// The isolated daemon config lives INSIDE the harness (self-contained; see tests/fixtures/).
const BASE_TEST_CONFIG = path.join(HARNESS_ROOT, 'tests', 'fixtures', 'test-config.yaml');

// ── single-stack defaults (byte-identical to the historical values) ──────────────────────────
const SINGLE_API_BASE_URL = 'http://127.0.0.1:8080/v2';
const SINGLE_ADMIN_URL = 'http://127.0.0.1:8080/_twin';
const SINGLE_METRICS_URL = 'http://127.0.0.1:9092/metrics';
const SINGLE_DB_URL =
  'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';
const SINGLE_SOCKET = '/tmp/spacetraders-daemon-harness.sock';

// ── Postgres base for PER-WORKER DBs (parallel mode only) ────────────────────────────────────
// The real harness Postgres is on :5434 (5433 is dead; the single-stack default above is
// vestigial and manual runs override it via HARNESS_TEST_DATABASE_URL). In parallel mode
// HARNESS_TEST_DATABASE_URL, if set, is treated as the BASE (server+credentials); the per-worker
// suffix `_W` is ALWAYS applied on top so isolation cannot be accidentally collapsed to one DB.
const PG_BASE_URL =
  process.env.HARNESS_TEST_DATABASE_URL ??
  'postgresql://spacetraders:dev_password@localhost:5434/spacetraders_test?sslmode=disable';
const PG_BASE_DBNAME = new URL(PG_BASE_URL).pathname.replace(/^\//, '') || 'spacetraders_test';

function withDbName(name: string): string {
  const u = new URL(PG_BASE_URL);
  u.pathname = '/' + name;
  return u.toString();
}
export function workerDbName(w: number): string {
  return `${PG_BASE_DBNAME}_${w}`;
}
export const TEMPLATE_DB_NAME = `${PG_BASE_DBNAME}_template`;
export function templateDbUrl(): string {
  return withDbName(TEMPLATE_DB_NAME);
}
/** Maintenance-DB DSN (the `postgres` db) for CREATE/DROP DATABASE — never the harness DBs. */
export function pgMaintenanceUrl(): string {
  return withDbName('postgres');
}

// ── worker resource derivation (pure; the single source of truth for globalSetup + the fork) ──
export interface WorkerResources {
  workerId: number;
  twinPort: number;
  apiBaseUrl: string;
  adminUrl: string;
  metricsPort: number;
  metricsUrl: string;
  grpcAddress: string;
  socket: string;
  pidFile: string;
  dbUrl: string;
  dbName: string;
  configPath: string;
}

export function workerResources(w: number): WorkerResources {
  const twinPort = 8080 + 10 * w;
  const metricsPort = 9092 + w;
  return {
    workerId: w,
    twinPort,
    apiBaseUrl: `http://127.0.0.1:${twinPort}/v2`,
    adminUrl: `http://127.0.0.1:${twinPort}/_twin`,
    metricsPort,
    metricsUrl: `http://127.0.0.1:${metricsPort}/metrics`,
    grpcAddress: `localhost:${50072 + w}`,
    socket: `/tmp/st-daemon-harness-${w}.sock`,
    pidFile: `/tmp/st-daemon-harness-${w}.pid`,
    dbUrl: withDbName(workerDbName(w)),
    dbName: workerDbName(w),
    configPath: `/tmp/st-harness-config-${w}.yaml`,
  };
}

// ── resource math (Admiral's explicit concern) ───────────────────────────────────────────────
// Each fork boots a daemon whose Postgres pool opens up to POOL_MAX_OPEN connections. N forks ×
// pool must stay well under Postgres max_connections AND leave headroom for the GATE stack + psql.
export const POOL_MAX_OPEN = 5; // must match test-config.yaml database.pool.max_open
const PG_HEADROOM = 25; // reserved for the concurrent GATE stack + admin/psql connections
export const PG_MAX_CONNECTIONS = Number(process.env.HARNESS_PG_MAX_CONNECTIONS ?? 100); // SHOW max_connections
// Highest fork count whose pools fit under (max_connections − headroom): floor((100−25)/5)=15.
export const CONNECTION_CAP = Math.max(1, Math.floor((PG_MAX_CONNECTIONS - PG_HEADROOM) / POOL_MAX_OPEN));
// Leave 2 cores for the OS + the concurrent GATE agent; never exceed the connection cap.
const CPU_CAP = Math.min((os.cpus().length || 4) - 2, 6);
const REQUESTED_WORKERS = Number(process.env.HARNESS_MAX_WORKERS ?? Math.min(CPU_CAP, CONNECTION_CAP));
/** Bounded fork count. Tunable via HARNESS_MAX_WORKERS but hard-capped by CONNECTION_CAP so a
 *  large request can never exhaust Postgres out from under the GATE stack. */
export const MAX_WORKERS = Math.max(1, Math.min(REQUESTED_WORKERS, CONNECTION_CAP));

// ── active context: which stack does THIS process talk to? ───────────────────────────────────
export const HARNESS_PARALLEL = process.env.HARNESS_PARALLEL === '1';
const rawWorker = Number(process.env.VITEST_POOL_ID ?? process.env.VITEST_WORKER_ID);
const activeWorkerId =
  HARNESS_PARALLEL && Number.isInteger(rawWorker) && rawWorker >= 1 ? rawWorker : null;
/** This fork's 1-based worker id, or null in single-stack mode. */
export const WORKER_ID: number | null = activeWorkerId;
const active = activeWorkerId === null ? null : workerResources(activeWorkerId);

// Runtime targets. In single-stack mode HARNESS_* overrides win over the defaults (unchanged
// behaviour). In parallel mode the per-worker derivation is authoritative for the isolation
// fields — a per-field URL/socket override would collapse every fork onto one stack — while
// HARNESS_TEST_DATABASE_URL is still honoured as the DB base (server+creds) via PG_BASE_URL.
export const API_BASE_URL = active?.apiBaseUrl ?? process.env.HARNESS_API_BASE_URL ?? SINGLE_API_BASE_URL;
export const ADMIN_URL = active?.adminUrl ?? process.env.HARNESS_ADMIN_URL ?? SINGLE_ADMIN_URL;
export const METRICS_URL = active?.metricsUrl ?? process.env.HARNESS_METRICS_URL ?? SINGLE_METRICS_URL;
export const TEST_DATABASE_URL =
  active?.dbUrl ?? process.env.HARNESS_TEST_DATABASE_URL ?? SINGLE_DB_URL;
export const TEST_DAEMON_SOCKET = active?.socket ?? SINGLE_SOCKET;
export const TEST_CONFIG = active?.configPath ?? BASE_TEST_CONFIG;

// ── per-worker daemon config file generation (used by globalSetup) ────────────────────────────
// The daemon reads daemon.socket_path/pid_file/address + metrics.port from its config FILE (viper
// binds ST_METRICS_PORT and DATABASE_URL as env, but not the daemon.* keys), so each fork needs a
// distinct config file. We derive it from the checked-in fixture by swapping only the isolation
// scalars — every other field (pool, rate limits, bootstrap cadence, captain.player_id) is
// inherited verbatim, so the generated config tracks the fixture automatically.
export function renderConfig(o: {
  socket: string;
  pidFile: string;
  grpcAddress: string;
  metricsPort: number;
  dbUrl: string;
}): string {
  let text = readFileSync(BASE_TEST_CONFIG, 'utf8');
  text = text.replace(/^(\s*socket_path:\s*).*$/m, `$1${o.socket}`);
  text = text.replace(/^(\s*pid_file:\s*).*$/m, `$1${o.pidFile}`);
  text = text.replace(/^(\s*address:\s*).*$/m, `$1${o.grpcAddress}`);
  text = text.replace(/^(\s*port:\s*).*$/m, `$1${o.metricsPort}`); // metrics.port is the only `port:` key
  text = text.replace(/^(\s+)url:\s*.*$/m, `$1url: ${o.dbUrl}`); // database.url (api uses base_url:)
  return text;
}
export function renderWorkerConfig(w: number): string {
  const r = workerResources(w);
  return renderConfig({
    socket: r.socket,
    pidFile: r.pidFile,
    grpcAddress: r.grpcAddress,
    metricsPort: r.metricsPort,
    dbUrl: r.dbUrl,
  });
}

export interface RunCliResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

// Run the spacetraders CLI against the isolated test daemon + API target. The env seams
// (SPACETRADERS_CONFIG, ST_API_BASE_URL, DATABASE_URL) point every invocation at the test stack.
export function runCli(
  args: string[],
  opts: { env?: Record<string, string>; timeoutMs?: number } = {},
): RunCliResult {
  // Daemon-mediated commands (workflow bootstrap, ship list/refresh, container ...) dial the daemon
  // over its Unix socket; the CLI's `--socket` flag DEFAULTS to the PRODUCTION socket
  // (/tmp/spacetraders-daemon.sock). Without this injection, every such command hits the running
  // production daemon instead of the isolated harness daemon — e.g. `workflow bootstrap` launches a
  // spurious BOOTSTRAP_COORDINATOR on prod. Force the test daemon's socket (harmless global flag on
  // direct-client commands like `player register`).
  const finalArgs = args.includes('--socket') ? args : [...args, '--socket', TEST_DAEMON_SOCKET];
  const res = spawnSync(CLI_BIN, finalArgs, {
    cwd: GOBOT_DIR,
    encoding: 'utf8',
    timeout: opts.timeoutMs ?? 30_000,
    env: {
      ...process.env,
      SPACETRADERS_CONFIG: TEST_CONFIG,
      ST_API_BASE_URL: API_BASE_URL,
      DATABASE_URL: TEST_DATABASE_URL,
      ST_ACCOUNT_TOKEN: process.env.HARNESS_ACCOUNT_TOKEN ?? 'harness-test-account-token',
      ...opts.env,
    },
  });
  if (res.error) throw res.error;
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status ?? -1 };
}

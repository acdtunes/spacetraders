import { spawn, spawnSync } from 'node:child_process';
import { existsSync, openSync, writeFileSync, rmSync } from 'node:fs';
import {
  DAEMON_BIN,
  GOBOT_DIR,
  MAX_WORKERS,
  POOL_MAX_OPEN,
  PG_MAX_CONNECTIONS,
  CONNECTION_CAP,
  TEMPLATE_DB_NAME,
  templateDbUrl,
  pgMaintenanceUrl,
  workerResources,
  workerDbName,
  renderConfig,
  renderWorkerConfig,
} from '../helpers/config';

// globalSetup (st-drm.22) — runs ONCE in the main process before any fork spawns. It:
//   1. migrates a template DB once (throwaway daemon → AutoMigrate),
//   2. clones a fully-migrated spacetraders_test_W per worker (fast TEMPLATE copy),
//   3. writes each fork's daemon config file.
// globalTeardown drops the cloned + template DBs, removes generated configs, and reaps stray twins.
//
// The per-worker TWINS are started inside each fork by tests/setup/worker-setup.ts — NOT here —
// because a twin must live in the fork that drives it. This file only prepares shared DB state.

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

// Throwaway migration-daemon slot — collision-free with the GATE stack (:8080 / gRPC 50072 /
// metrics 9092) and every worker slot (8090+ / 50073+ / 9093+).
const TMPL_SOCKET = '/tmp/st-daemon-harness-tmpl.sock';
const TMPL_PIDFILE = '/tmp/st-daemon-harness-tmpl.pid';
const TMPL_CONFIG = '/tmp/st-harness-config-tmpl.yaml';
const TMPL_GRPC = 'localhost:50071';
const TMPL_METRICS = 9091;
const TMPL_DAEMON_LOG = '/tmp/st-harness-tmpl-daemon.log';

function psql(dsn: string, sql: string, timeoutMs = 30_000): { ok: boolean; out: string; err: string } {
  const res = spawnSync('psql', [dsn, '-v', 'ON_ERROR_STOP=1', '-tAc', sql], {
    encoding: 'utf8',
    timeout: timeoutMs,
  });
  return { ok: (res.status ?? -1) === 0, out: res.stdout ?? '', err: res.stderr ?? String(res.error ?? '') };
}

function terminateConnections(dbName: string): void {
  // Detach any lingering sessions so DROP / CREATE ... TEMPLATE won't fail with "database is
  // being accessed by other users". Best-effort: a fresh DB simply has none.
  psql(
    pgMaintenanceUrl(),
    `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${dbName}' AND pid<>pg_backend_pid()`,
  );
}

function dropDatabase(dbName: string): void {
  terminateConnections(dbName);
  const r = psql(pgMaintenanceUrl(), `DROP DATABASE IF EXISTS "${dbName}"`);
  if (!r.ok) throw new Error(`DROP DATABASE ${dbName} failed: ${r.err}`);
}

function createDatabase(dbName: string, template?: string): void {
  if (template) terminateConnections(template);
  const tmpl = template ? ` TEMPLATE "${template}"` : '';
  const r = psql(pgMaintenanceUrl(), `CREATE DATABASE "${dbName}"${tmpl}`);
  if (!r.ok) throw new Error(`CREATE DATABASE ${dbName}${tmpl} failed: ${r.err}`);
}

async function waitForFile(file: string, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(file)) return true;
    await sleep(150);
  }
  return false;
}

// Migrate the template DB by booting a throwaway daemon against it. AutoMigrate runs synchronously
// on boot and FULLY completes before the daemon creates its unix socket, so the socket appearing is
// a reliable "schema migrated" signal — no twin required (startup ship-sync is non-fatal; we point
// the API at a dead port so it never touches the GATE twin).
async function migrateTemplate(): Promise<void> {
  dropDatabase(TEMPLATE_DB_NAME);
  createDatabase(TEMPLATE_DB_NAME);

  writeFileSync(
    TMPL_CONFIG,
    renderConfig({
      socket: TMPL_SOCKET,
      pidFile: TMPL_PIDFILE,
      grpcAddress: TMPL_GRPC,
      metricsPort: TMPL_METRICS,
      dbUrl: templateDbUrl(),
    }),
  );
  rmSync(TMPL_SOCKET, { force: true });
  rmSync(TMPL_PIDFILE, { force: true });

  const logFd = openSync(TMPL_DAEMON_LOG, 'w');
  const proc = spawn(DAEMON_BIN, ['--force'], {
    cwd: GOBOT_DIR,
    env: {
      ...process.env,
      SPACETRADERS_CONFIG: TMPL_CONFIG,
      DATABASE_URL: templateDbUrl(),
      ST_API_BASE_URL: 'http://127.0.0.1:1/v2', // dead port: startup sync fails fast, never hits GATE
    },
    stdio: ['ignore', logFd, logFd],
  });

  const stop = async (): Promise<void> => {
    if (proc.exitCode !== null) return;
    await new Promise<void>((resolve) => {
      proc.once('exit', () => resolve());
      proc.kill('SIGTERM');
      setTimeout(() => {
        if (proc.exitCode === null) proc.kill('SIGKILL');
        resolve();
      }, 5_000).unref();
    });
  };

  try {
    const migrated = await waitForFile(TMPL_SOCKET, 60_000);
    if (!migrated) {
      throw new Error(`template migration daemon never created ${TMPL_SOCKET} (see ${TMPL_DAEMON_LOG})`);
    }
    // Confirm the schema is actually present (defence in depth beyond the socket signal).
    const check = psql(templateDbUrl(), `SELECT to_regclass('public.ships') IS NOT NULL`);
    if (!check.ok || check.out.trim() !== 't') {
      throw new Error(`template ${TEMPLATE_DB_NAME} has no 'ships' table after migration (see ${TMPL_DAEMON_LOG})`);
    }
    // Ensure the template carries NO seeded player/era, so each clone registers TWINAGENT fresh
    // (a preserved token in a clone whose twin has none would 401 every daemon call).
    const wipe = psql(templateDbUrl(), 'TRUNCATE players, eras RESTART IDENTITY CASCADE');
    if (!wipe.ok) throw new Error(`template player/era truncate failed: ${wipe.err}`);
  } finally {
    await stop();
    rmSync(TMPL_SOCKET, { force: true });
    rmSync(TMPL_PIDFILE, { force: true });
  }
  // Give Postgres a beat to release the daemon's pool before cloning (CREATE ... TEMPLATE needs
  // zero sessions on the source); terminateConnections in createDatabase is the hard guarantee.
  await sleep(300);
}

export async function setup(): Promise<void> {
  // Reachability preflight (fail fast with a hint) against the maintenance DB — never a harness DB.
  const ping = psql(pgMaintenanceUrl(), 'SELECT 1', 8_000);
  if (!ping.ok) {
    throw new Error(
      `harness Postgres not reachable (${pgMaintenanceUrl().replace(/:[^:@/]*@/, ':***@')}): ${ping.err}\n` +
        `Start it: docker compose -f twin/docker-compose.test.yml up -d postgres-test`,
    );
  }

  // Observed max_connections vs the planned pool load, for the record.
  const mc = psql(pgMaintenanceUrl(), 'SHOW max_connections');
  const observed = mc.ok ? Number(mc.out.trim()) : PG_MAX_CONNECTIONS;
  const planned = MAX_WORKERS * POOL_MAX_OPEN;
  // eslint-disable-next-line no-console
  console.log(
    `[harness] parallel setup: MAX_WORKERS=${MAX_WORKERS} × pool max_open=${POOL_MAX_OPEN} = ${planned} conns; ` +
      `Postgres max_connections=${observed}; connection cap=${CONNECTION_CAP} (headroom reserved for the GATE stack).`,
  );

  await migrateTemplate();

  for (let w = 1; w <= MAX_WORKERS; w++) {
    const name = workerDbName(w);
    dropDatabase(name);
    createDatabase(name, TEMPLATE_DB_NAME);
    writeFileSync(workerResources(w).configPath, renderWorkerConfig(w));
  }
  // eslint-disable-next-line no-console
  console.log(`[harness] provisioned ${MAX_WORKERS} isolated worker DB(s) + config(s) from template ${TEMPLATE_DB_NAME}.`);
}

export async function teardown(): Promise<void> {
  // Reap any twin a fork failed to kill (best effort, by pidfile).
  for (let w = 1; w <= MAX_WORKERS; w++) {
    const pidFile = `/tmp/st-twin-harness-${w}.pid`;
    if (existsSync(pidFile)) {
      try {
        const pid = Number(spawnSync('cat', [pidFile], { encoding: 'utf8' }).stdout?.trim());
        if (Number.isInteger(pid) && pid > 0) process.kill(pid, 'SIGKILL');
      } catch {
        /* already gone */
      }
      rmSync(pidFile, { force: true });
    }
  }

  for (let w = 1; w <= MAX_WORKERS; w++) {
    try {
      dropDatabase(workerDbName(w));
    } catch {
      /* leave it for the next run's DROP IF EXISTS */
    }
    rmSync(workerResources(w).configPath, { force: true });
  }
  try {
    dropDatabase(TEMPLATE_DB_NAME);
  } catch {
    /* best effort */
  }
  rmSync(TMPL_CONFIG, { force: true });
}

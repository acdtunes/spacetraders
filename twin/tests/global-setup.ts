import net from 'node:net';
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { runCli, TWIN_BASE_URL, TEST_DATABASE_URL } from './helpers/run-cli.js';
import { startTestDaemon, stopTestDaemon } from './helpers/daemon.js';

// Derive the test Postgres port from the (env-overridable) DSN so the reachability
// check and its hint stay in lock-step with run-cli's TEST_DATABASE_URL.
const TEST_DB_PORT = Number(new URL(TEST_DATABASE_URL).port) || 5434;

const TWIN_DIR = path.resolve(fileURLToPath(new URL('.', import.meta.url)), '..');
const TSX_BIN = path.join(TWIN_DIR, 'node_modules', '.bin', 'tsx');
const TWIN_ENTRY = path.join(TWIN_DIR, 'src', 'main.ts');

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function tcpOpen(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ host, port }, () => { sock.destroy(); resolve(true); });
    sock.on('error', () => { sock.destroy(); resolve(false); });
    sock.setTimeout(500, () => { sock.destroy(); resolve(false); });
  });
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  // 1. Boot the twin as a SEPARATE CHILD PROCESS — NOT in-process.
  //    The seed step below uses spawnSync (`player register`), which blocks this
  //    globalSetup event loop. An in-process twin (buildServer().listen on the same
  //    loop) could not answer the CLI's POST /register while the loop is blocked →
  //    deadlock. A child process has its own loop and responds normally.
  const twin: ChildProcess = spawn(TSX_BIN, [TWIN_ENTRY], {
    cwd: TWIN_DIR,
    env: { ...process.env },
    stdio: 'ignore',
  });
  const stopTwin = async (): Promise<void> => {
    if (!twin.killed) twin.kill('SIGTERM');
  };
  {
    const deadline = Date.now() + 15_000;
    let up = false;
    while (Date.now() < deadline) {
      try { if ((await fetch(`${TWIN_BASE_URL}/`)).status === 200) { up = true; break; } } catch { /* not ready */ }
      await sleep(200);
    }
    if (!up) { await stopTwin(); throw new Error('twin child process did not answer GET /v2/ on :8080 within 15s'); }
  }

  // 2. Ensure the test Postgres is reachable (fail fast with a hint).
  if (!(await tcpOpen('localhost', TEST_DB_PORT))) {
    await stopTwin();
    throw new Error(`test Postgres not reachable on localhost:${TEST_DB_PORT} — start it first: docker compose -f twin/docker-compose.test.yml up -d postgres-test`);
  }

  // 3. Boot the isolated test daemon (AutoMigrate on first boot).
  await startTestDaemon();

  // 3b. Clean-slate the daemon's player/era rows. resetDaemonDb/AutoMigrate PERSIST the
  //     players row across runs, but the twin is a FRESH in-memory process each run
  //     (world.agentToken=null until a POST /register actually executes). If we let
  //     `player register --new` skip on an existing OPEN era, the daemon keeps its persisted
  //     token while the fresh twin has none → every daemon-mediated GET /my/ships 401s
  //     ("Invalid or missing agent token"). Truncating forces register to run fully and
  //     re-seed BOTH the daemon row and the twin's world.agentToken with the same token.
  {
    const wipe = spawnSync('psql', [TEST_DATABASE_URL, '-v', 'ON_ERROR_STOP=1', '-c', 'TRUNCATE players, eras RESTART IDENTITY CASCADE;'], { encoding: 'utf8', timeout: 10_000 });
    if (wipe.status !== 0) {
      await stopTestDaemon(); await stopTwin();
      throw new Error(`globalSetup player/era truncate failed (exit ${wipe.status}): ${wipe.stderr}`);
    }
  }

  // 4. Seed TWINAGENT through the hardened CLI (now always runs fresh against the clean slate).
  const reg = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
  if (reg.exitCode !== 0 && !/an OPEN era/.test(`${reg.stdout}\n${reg.stderr}`)) {
    await stopTestDaemon(); await stopTwin();
    throw new Error(`globalSetup seed failed (exit ${reg.exitCode}): ${reg.stderr}`);
  }

  // Teardown.
  return async () => {
    await stopTestDaemon();
    await stopTwin();
  };
}

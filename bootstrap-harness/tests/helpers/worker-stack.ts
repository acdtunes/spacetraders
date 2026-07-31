import { spawn, type ChildProcess } from 'node:child_process';
import { writeFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { WORKER_ID, TWIN_DIR, API_BASE_URL, workerResources, runCli } from './config';

// Per-worker twin lifecycle (st-drm.22). Each vitest fork drives its OWN twin on a disjoint port;
// this module boots it exactly once per fork and reaps it on exit.
//
// SINGLETON DISCIPLINE: vitest re-runs setupFiles for every test file, but caches the modules they
// import. So the start-once guard MUST live HERE (an imported module, evaluated once per fork), not
// in the setup file's top level (which re-executes per file — verified empirically). The setup file
// simply `await`s ensureWorkerStackReady(); the first call boots the stack, later calls return the
// same resolved promise.
//
// In single-stack mode (WORKER_ID === null) this is a no-op: the externally-launched stack on :8080
// owns the twin, and we must never touch it.

const TWIN_ENTRY = path.join(TWIN_DIR, 'src', 'main.ts');

let readyPromise: Promise<void> | undefined;
let twinProc: ChildProcess | undefined;
let twinPidFile: string | undefined;

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function bootWorkerStack(w: number): Promise<void> {
  const res = workerResources(w);
  twinPidFile = `/tmp/st-twin-harness-${w}.pid`;

  // 1. Boot THIS worker's twin as a child process (its own event loop, so a blocking spawnSync
  //    `player register` in this fork cannot deadlock it). Fast-run knobs: compression 200 +
  //    50ms travel floor. INVARIANT (st-drm.8): TWIN_MIN_TRAVEL_MS (50) >= the daemon's
  //    ST_CLOCK_DRIFT_BUFFER_MS clamp (50), which daemon.ts injects — 50 >= 50 holds.
  //    Launch node directly with the tsx loader (NOT the `.bin/tsx` shim): the shim spawns the
  //    real node as a CHILD, so proc.pid would be the launcher and killing it orphans the twin
  //    (verified). `node --import tsx` runs in-process, so proc.pid IS the twin and a plain kill
  //    reaps it. `tsx` resolves from twin/node_modules via cwd.
  const proc = spawn(process.execPath, ['--import', 'tsx', TWIN_ENTRY], {
    cwd: TWIN_DIR,
    env: {
      ...process.env,
      TWIN_PORT: String(res.twinPort),
      TWIN_TIME_COMPRESSION: '200',
      TWIN_MIN_TRAVEL_MS: '50',
    },
    stdio: 'ignore',
  });
  twinProc = proc;
  try {
    writeFileSync(twinPidFile, String(proc.pid ?? ''));
  } catch {
    /* pidfile is only for the globalTeardown sweep; not fatal if it can't be written */
  }

  // 2. Health-wait: the twin serves GET /v2/ (server-status) once listening.
  {
    const deadline = Date.now() + 20_000;
    let up = false;
    let lastErr = '';
    while (Date.now() < deadline) {
      if (proc.exitCode !== null) {
        throw new Error(`worker ${w} twin exited early (code ${proc.exitCode}) before answering on :${res.twinPort}`);
      }
      try {
        if ((await fetch(`${API_BASE_URL}/`)).status === 200) {
          up = true;
          break;
        }
      } catch (e) {
        lastErr = String(e);
      }
      await sleep(200);
    }
    if (!up) {
      await stopWorkerStack();
      throw new Error(`worker ${w} twin did not answer GET ${API_BASE_URL}/ within 20s (${lastErr})`);
    }
  }

  // 3. Seed TWINAGENT: `player register --new` writes players/eras/token into spacetraders_test_W
  //    (already migrated — the DB was cloned from the migrated template) AND sets the twin's
  //    in-memory world.agentToken. POST /_twin/reset preserves that token across every later
  //    scenario (world/store.ts), so one registration per fork is sufficient.
  const reg = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
  if (reg.exitCode !== 0 && !/an OPEN era/.test(`${reg.stdout}\n${reg.stderr}`)) {
    await stopWorkerStack();
    throw new Error(`worker ${w} TWINAGENT register failed (exit ${reg.exitCode}): ${reg.stderr || reg.stdout}`);
  }
}

async function stopWorkerStack(): Promise<void> {
  const proc = twinProc;
  twinProc = undefined;
  if (twinPidFile) {
    try {
      rmSync(twinPidFile, { force: true });
    } catch {
      /* best effort */
    }
  }
  if (!proc || proc.exitCode !== null) return;
  await new Promise<void>((resolve) => {
    proc.once('exit', () => resolve());
    proc.kill('SIGTERM');
    // Fallback if SIGTERM is ignored.
    setTimeout(() => {
      if (proc.exitCode === null) proc.kill('SIGKILL');
      resolve();
    }, 3_000).unref();
  });
}

// Reap the twin when this fork exits. 'exit' is sync-only, so issue a plain kill; SIGTERM/SIGINT
// get the graceful path. globalTeardown additionally sweeps any survivor by pidfile.
function killSync(): void {
  if (twinProc && twinProc.exitCode === null) {
    try {
      twinProc.kill('SIGKILL');
    } catch {
      /* ignore */
    }
  }
  if (twinPidFile) {
    try {
      rmSync(twinPidFile, { force: true });
    } catch {
      /* ignore */
    }
  }
}
process.once('exit', killSync);
process.once('SIGTERM', () => {
  killSync();
  process.exit(0);
});
process.once('SIGINT', () => {
  killSync();
  process.exit(0);
});

/** Boot this fork's isolated twin exactly once (no-op in single-stack mode). Idempotent. */
export function ensureWorkerStackReady(): Promise<void> {
  if (WORKER_ID === null) return Promise.resolve();
  readyPromise ??= bootWorkerStack(WORKER_ID);
  return readyPromise;
}

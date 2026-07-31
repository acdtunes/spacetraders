import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync, rmSync } from 'node:fs';
import net from 'node:net';
import { DAEMON_BIN, GOBOT_DIR, TEST_CONFIG, TEST_DATABASE_URL, TWIN_BASE_URL } from './run-cli.js';

const TEST_PID_FILE = '/tmp/spacetraders-daemon-test.pid';
// The daemon's gRPC server binds a UNIX SOCKET (net.Listen("unix", socket_path)) and the CLI dials
// unix:<socket_path>. The `daemon.address` config field is NOT the gRPC listener, so readiness is
// the socket file existing AND accepting a connection. Must match daemon.socket_path in test-config.yaml.
const TEST_SOCKET = '/tmp/spacetraders-daemon-test.sock';

let daemon: ChildProcess | undefined;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function unixOpen(socketPath: string): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ path: socketPath }, () => { sock.destroy(); resolve(true); });
    sock.on('error', () => { sock.destroy(); resolve(false); });
    sock.setTimeout(500, () => { sock.destroy(); resolve(false); });
  });
}

async function waitReady(timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(TEST_PID_FILE) && existsSync(TEST_SOCKET) && (await unixOpen(TEST_SOCKET))) return;
    await sleep(300);
  }
  throw new Error(`test daemon not ready within ${timeoutMs}ms (pidfile ${TEST_PID_FILE} / socket ${TEST_SOCKET})`);
}

/** Spawn the isolated test daemon on the -test slot. `extraEnv` overlays LAST.
 *  Uses --force so a fresh boot evicts any prior test daemon and reclaims a stale
 *  -test pidfile/socket left by a previous run/restart (a lingering unix-socket file
 *  otherwise blocks the new daemon's bind → its startup fleet-sync never runs).
 *  --force targets ONLY the -test pidfile from test-config.yaml, never production. */
export async function startTestDaemon(extraEnv: Record<string, string> = {}): Promise<void> {
  daemon = spawn(DAEMON_BIN, ['--force'], {
    cwd: GOBOT_DIR,
    stdio: 'ignore',
    env: {
      ...process.env,
      SPACETRADERS_CONFIG: TEST_CONFIG,
      ST_API_BASE_URL: TWIN_BASE_URL,
      DATABASE_URL: TEST_DATABASE_URL,
      // Shrink the daemon's arrival/cooldown clock-drift clamp from its 1s prod default to 50ms
      // so compressed twin arrivals resolve fast. INVARIANT (st-drm.8): this MUST stay <= the
      // twin's TWIN_MIN_TRAVEL_MS floor (default 1000ms here), else the daemon could miss an
      // arrival inside its own tolerance. Overridable via extraEnv.
      ST_CLOCK_DRIFT_BUFFER_MS: '50',
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

/** SIGTERM the test daemon, then GUARANTEE it is dead (SIGKILL fallback). */
export async function stopTestDaemon(): Promise<void> {
  const pid = daemon?.pid;
  if (daemon && !daemon.killed) daemon.kill('SIGTERM');
  daemon = undefined;
  await waitPidfileGone();
  // SIGTERM + pidfile-gone does NOT confirm the process exited — a slow/ignored SIGTERM would
  // leak an orphaned daemon (reparented to PID 1) holding the test Postgres/gRPC sockets, which
  // then collides with the next run. Confirm exit and SIGKILL if still alive.
  if (pid !== undefined) {
    try { process.kill(pid, 0); process.kill(pid, 'SIGKILL'); } catch { /* already gone */ }
  }
  // Remove any lingering -test pidfile/socket so the next boot's socket bind + startup
  // fleet-sync are not blocked by a stale file (the daemon does not always unlink on SIGTERM).
  try { rmSync(TEST_PID_FILE, { force: true }); } catch { /* ignore */ }
  try { rmSync(TEST_SOCKET, { force: true }); } catch { /* ignore */ }
}

export async function restartTestDaemon(extraEnv: Record<string, string> = {}): Promise<void> {
  await stopTestDaemon();
  await startTestDaemon(extraEnv);
}

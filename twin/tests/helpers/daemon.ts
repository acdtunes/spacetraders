import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
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

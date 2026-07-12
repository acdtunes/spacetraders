import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Self-contained harness config. Depends on NO other package's source. The API target is a
// runtime URL string only (env-overridable) — the harness assumes nothing about whether any
// API server (twin, mock, or the real game) exists at author/typecheck time.

const HELPERS_DIR = path.dirname(fileURLToPath(import.meta.url)); // bootstrap-harness/tests/helpers
export const HARNESS_ROOT = path.resolve(HELPERS_DIR, '..', '..'); // bootstrap-harness/
export const REPO_ROOT = path.resolve(HELPERS_DIR, '..', '..', '..'); // spacetraders/
export const GOBOT_DIR = path.join(REPO_ROOT, 'gobot');
export const CLI_BIN = path.join(GOBOT_DIR, 'bin', 'spacetraders');
export const DAEMON_BIN = path.join(GOBOT_DIR, 'bin', 'spacetraders-daemon');

// Runtime targets — the API base URL the daemon is pointed at, the admin namespace, the daemon's
// metrics endpoint. ALL env-overridable so the harness binds to whatever serves the API contract.
export const API_BASE_URL = process.env.HARNESS_API_BASE_URL ?? 'http://127.0.0.1:8080/v2';
export const ADMIN_URL = process.env.HARNESS_ADMIN_URL ?? 'http://127.0.0.1:8080/_twin';
export const METRICS_URL = process.env.HARNESS_METRICS_URL ?? 'http://127.0.0.1:9092/metrics';
export const TEST_DATABASE_URL =
  process.env.HARNESS_TEST_DATABASE_URL ??
  'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';

// The isolated daemon config lives INSIDE the harness (self-contained; see tests/fixtures/).
export const TEST_CONFIG = path.join(HARNESS_ROOT, 'tests', 'fixtures', 'test-config.yaml');
export const TEST_DAEMON_SOCKET = '/tmp/spacetraders-daemon-harness.sock';

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
  const res = spawnSync(CLI_BIN, args, {
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

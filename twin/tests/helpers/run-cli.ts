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
  process.env.TWIN_TEST_DATABASE_URL ??
  'postgresql://spacetraders:dev_password@localhost:5434/spacetraders_test?sslmode=disable';
// The daemon-mediated CLI commands (ship list/show/refresh/navigate, shipyard, scout) dial the
// daemon over its Unix socket; the `--socket` global flag DEFAULTS to the PRODUCTION socket
// (/tmp/spacetraders-daemon.sock). Without overriding it, those commands hit the prod daemon
// (which has no TWINAGENT) instead of the isolated test daemon. Must match daemon.socket_path in
// test-config.yaml. Direct-client commands (player register, universe status) accept the global
// flag and harmlessly ignore it.
export const TEST_SOCKET =
  process.env.TWIN_TEST_SOCKET ?? '/tmp/spacetraders-daemon-test.sock';

export interface RunCliResult { stdout: string; stderr: string; exitCode: number }

export function runCli(args: string[], opts: { env?: Record<string, string>; timeoutMs?: number } = {}): RunCliResult {
  const finalArgs = args.includes('--socket') ? args : [...args, '--socket', TEST_SOCKET];
  const res = spawnSync(CLI_BIN, finalArgs, {
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

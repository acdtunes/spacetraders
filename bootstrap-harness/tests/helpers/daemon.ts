import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import {
  DAEMON_BIN,
  GOBOT_DIR,
  TEST_CONFIG,
  API_BASE_URL,
  TEST_DATABASE_URL,
  TEST_DAEMON_SOCKET,
} from './config';

const env = (configPath: string) => ({
  ...process.env,
  SPACETRADERS_CONFIG: configPath,
  ST_API_BASE_URL: API_BASE_URL,
  DATABASE_URL: TEST_DATABASE_URL,
});

export interface DaemonHandle {
  proc: ChildProcess;
  stop(): Promise<void>;
}

async function waitForSocket(timeoutMs = 20_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(TEST_DAEMON_SOCKET)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`test daemon socket ${TEST_DAEMON_SOCKET} never appeared`);
}

export async function startTestDaemon(opts: { configPath?: string } = {}): Promise<DaemonHandle> {
  // --force SIGTERM-evicts any prior daemon on the TEST pidfile (isolated by test-config.yaml).
  const proc = spawn(DAEMON_BIN, ['--force'], {
    cwd: GOBOT_DIR,
    env: env(opts.configPath ?? TEST_CONFIG),
    stdio: 'ignore',
  });
  await waitForSocket();
  const stop = () =>
    new Promise<void>((resolve) => {
      if (proc.exitCode !== null) return resolve();
      proc.once('exit', () => resolve());
      proc.kill('SIGTERM');
    });
  return { proc, stop };
}

export async function resetDaemonDb(): Promise<void> {
  // Truncate every daemon-owned table but preserve the seeded players row + migration ledger.
  const sql = `DO $$ DECLARE r RECORD; BEGIN
    FOR r IN SELECT tablename FROM pg_tables WHERE schemaname='public'
             AND tablename NOT IN ('players','schema_migrations','goose_db_version') LOOP
      EXECUTE 'TRUNCATE TABLE public.' || quote_ident(r.tablename) || ' RESTART IDENTITY CASCADE';
    END LOOP; END $$;`;
  const res = spawnSync('psql', [TEST_DATABASE_URL, '-v', 'ON_ERROR_STOP=1', '-c', sql], {
    encoding: 'utf8',
  });
  if (res.status !== 0) throw new Error(`resetDaemonDb failed: ${res.stderr}`);
}

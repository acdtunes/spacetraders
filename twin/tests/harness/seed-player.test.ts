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

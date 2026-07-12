import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const TWIN_DIR = path.resolve(__dirname, '..', '..');
const SCRIPT = path.join(TWIN_DIR, 'scripts', 'launch-test-stack.sh');
const REAL_CONFIG = path.join(TWIN_DIR, 'test-config.yaml');
const TEST_DSN = 'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable';

function runDry(env: Record<string, string> = {}) {
  const res = spawnSync('bash', [SCRIPT, '--dry-run'], {
    encoding: 'utf8', timeout: 15_000,
    env: { ...process.env, TWIN_TEST_CONFIG: '', TWIN_BASE_URL: '', TEST_DATABASE_URL: '', ...env },
  });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status };
}

describe('launch-test-stack.sh — isolation guards for the --force PID trap', () => {
  it('dry-run passes on the checked-in test-config.yaml and prints the exact env triplet', () => {
    const { stdout, stderr, exitCode } = runDry();
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain(`SPACETRADERS_CONFIG=${REAL_CONFIG}`);
    expect(stdout).toContain('ST_API_BASE_URL=http://127.0.0.1:8080/v2');
    expect(stdout).toContain(`DATABASE_URL=${TEST_DSN}`);
    expect(stdout).toContain('dry-run: guards passed');
  });
  it('REFUSES a config whose pid_file is the PRODUCTION pidfile', () => {
    const tampered = readFileSync(REAL_CONFIG, 'utf8').replace('pid_file: /tmp/spacetraders-daemon-test.pid', 'pid_file: /tmp/spacetraders-daemon.pid');
    const dir = mkdtempSync(path.join(tmpdir(), 'twin-cfg-')); const file = path.join(dir, 'tampered.yaml'); writeFileSync(file, tampered);
    const { stderr, exitCode } = runDry({ TWIN_TEST_CONFIG: file });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO LAUNCH'); expect(stderr).toContain('pid_file');
  });
  it('REFUSES a config with the daemon isolation lines stripped', () => {
    const stripped = readFileSync(REAL_CONFIG, 'utf8').split('\n').filter((l) => !/pid_file|socket_path|address/.test(l)).join('\n');
    const dir = mkdtempSync(path.join(tmpdir(), 'twin-cfg-')); const file = path.join(dir, 'stripped.yaml'); writeFileSync(file, stripped);
    const { stderr, exitCode } = runDry({ TWIN_TEST_CONFIG: file });
    expect(exitCode).not.toBe(0); expect(stderr).toContain('REFUSING TO LAUNCH');
  });
});

import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const TWIN_DIR = path.resolve(__dirname, '..', '..');
const SCRIPT = path.join(TWIN_DIR, 'scripts', 'capture-x1pz28.sh');

function runDry(env: Record<string, string> = {}) {
  const res = spawnSync('bash', [SCRIPT, '--dry-run'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, CAPTURE_DSN: '', FIXTURE_DIR: '', CAPTURE_SYSTEM: '', EXPECTED_WAYPOINTS: '', ...env },
  });
  return { stdout: res.stdout ?? '', stderr: res.stderr ?? '', exitCode: res.status };
}

describe('capture-x1pz28.sh — read-only prod-capture DSN guards', () => {
  it('dry-run passes on the default read-only prod DSN and echoes the target', () => {
    const { stdout, stderr, exitCode } = runDry();
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain('X1-PZ28');
    expect(stdout).toContain('localhost:5432/spacetraders');
    expect(stdout).toContain('dry-run: guards passed');
  });
  it('REFUSES the test DB (5433/spacetraders_test) — wrong port', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('5432');
  });
  it('REFUSES a writable DB on the right port (5432/spacetraders_test)', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders_test' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('spacetraders_test');
  });
  it('REFUSES a remote host even on 5432/spacetraders', () => {
    const { stderr, exitCode } = runDry({ CAPTURE_DSN: 'postgresql://spacetraders:dev_password@db.example.com:5432/spacetraders' });
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('REFUSING TO CAPTURE');
    expect(stderr).toContain('localhost');
  });
});

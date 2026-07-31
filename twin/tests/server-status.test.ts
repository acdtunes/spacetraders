import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from './helpers/run-cli';

const golden = JSON.parse(
  readFileSync(path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'server-status.json'), 'utf8'),
) as { resetDate: string; serverResets: { next: string; frequency: string } };

describe('GET /v2/ — server status (universe status)', () => {
  it('universe status parses GET / and reports in sync (exit 0)', () => {
    const { stdout, stderr, exitCode } = runCli(['universe', 'status']);
    expect(exitCode, `stderr:\n${stderr}\nstdout:\n${stdout}`).toBe(0);
    expect(stdout).toMatch(new RegExp(`Server resetDate +${golden.resetDate}`));
    expect(stdout).toMatch(new RegExp(`Next reset +${golden.serverResets.next.replace(/[.]/g, '\\.')} \\(${golden.serverResets.frequency}\\)`));
    expect(stdout).toMatch(/State +in sync/);
  });

  it('serves GET /v2/ UNWRAPPED with a bare-date resetDate and RFC3339 next reset', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data?: unknown; resetDate: string; serverResets: { next: string; frequency: string } };
    expect(body.data).toBeUndefined();
    expect(body.resetDate).toBe(golden.resetDate);
    expect(body.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(body.serverResets).toEqual(golden.serverResets);
    expect(Number.isNaN(Date.parse(body.serverResets.next))).toBe(false);
  });
});

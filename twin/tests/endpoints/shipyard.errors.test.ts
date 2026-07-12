import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL } from '../helpers/run-cli';

const HOME_SYSTEM = 'X1-PZ28'; const NO_SHIPYARD_WP = 'X1-PZ28-NOSHIPYARD';

describe('GET /v2/systems/{s}/waypoints/{w}/shipyard — not-a-shipyard waypoint', () => {
  it('returns a 404 error envelope naming the waypoint', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${NO_SHIPYARD_WP}/shipyard`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain(NO_SHIPYARD_WP);
  });
  it('`shipyard list` exits non-zero (Go client surfaces the 404)', () => {
    const { stderr, exitCode } = runCli(['shipyard', 'list', HOME_SYSTEM, NO_SHIPYARD_WP, '--player-id', '1']);
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain('failed to get shipyard');
  });
});

// This smoke test runs under the DEFAULT vitest config (globalSetup boots the stack).
// It is excluded from the unit config; run it via `rtk npx vitest run tests/skeleton/harness-smoke.test.ts`
// only once the twin server + scripts exist and a test Postgres is up on :5433.
import { describe, expect, it } from 'vitest';
import { TWIN_ADMIN } from './helpers/run-cli';

describe('harness globalSetup smoke', () => {
  it('the twin is serving and the world holds the seeded agent', async () => {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    expect(res.status).toBe(200);
    const s = (await res.json()) as { agent: { symbol: string } | null };
    expect(s.agent?.symbol).toBe('TWINAGENT');
  });
});

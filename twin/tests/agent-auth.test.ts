import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL } from './helpers/run-cli';

const AGENT = 'TWINAGENT';
function agentToken(): string {
  const { stdout, exitCode } = runCli(['player', 'info', '--agent', AGENT, '--show-token']);
  expect(exitCode).toBe(0);
  return /Token:\s*(\S+)/.exec(stdout)![1];
}

describe('GET /v2/my/agent — bearer auth guard', () => {
  it('401 + envelope when the Authorization header is missing', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`);
    expect(res.status).toBe(401);
    const body = (await res.json()) as { error: { message: string; code: number }; data?: unknown };
    expect(body.data).toBeUndefined(); expect(body.error.code).toBe(401);
    expect(body.error.message.length).toBeGreaterThan(0);
  });
  it('401 when the bearer token does not match world.agentToken', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: 'Bearer not-the-agent-token' } });
    expect(res.status).toBe(401);
    expect(((await res.json()) as { error: { code: number } }).error.code).toBe(401);
  });
  it('200 with the correct bearer token', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: `Bearer ${agentToken()}` } });
    expect(res.status).toBe(200);
    expect(((await res.json()) as { data: { symbol: string } }).data.symbol).toBe(AGENT);
  });
});

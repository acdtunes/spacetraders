import { beforeEach, describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, TWIN_ADMIN } from './helpers/run-cli';

const AGENT = 'TWINAGENT'; const HQ = 'X1-PZ28-A1'; const CREDITS = 175000; const FACTION = 'COSMIC';

function agentToken(): string {
  const { stdout, stderr, exitCode } = runCli(['player', 'info', '--agent', AGENT, '--show-token']);
  expect(exitCode, `player info --show-token failed:\n${stderr}\n${stdout}`).toBe(0);
  const m = /Token:\s*(\S+)/.exec(stdout);
  if (!m) throw new Error(`no token in player info output:\n${stdout}`);
  return m[1];
}

describe('GET /v2/my/agent — agent treasury (player info)', () => {
  beforeEach(async () => {
    const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
    expect(res.status).toBe(200);
  });

  it('player info prints live credits + symbol decoded from GET /my/agent', () => {
    const { stdout, stderr, exitCode } = runCli(['player', 'info', '--agent', AGENT]);
    expect(exitCode, `stderr:\n${stderr}\nstdout:\n${stdout}`).toBe(0);
    expect(stdout).toMatch(new RegExp(`Agent Symbol: +${AGENT}`));
    expect(stdout).toMatch(new RegExp(`Credits: +${CREDITS}`));
  });

  it('GET /my/agent returns { data: Agent } field-for-field, matching /_twin/state', async () => {
    const token = agentToken();
    const res = await fetch(`${TWIN_BASE_URL}/my/agent`, { headers: { Authorization: `Bearer ${token}` } });
    expect(res.status).toBe(200);
    const a = ((await res.json()) as { data: { accountId: string; symbol: string; headquarters: string; credits: number; startingFaction: string } }).data;
    expect(a.symbol).toBe(AGENT); expect(a.headquarters).toBe(HQ); expect(a.credits).toBe(CREDITS); expect(a.startingFaction).toBe(FACTION);
    expect(typeof a.accountId).toBe('string'); expect(a.accountId.length).toBeGreaterThan(0);
    const state = (await (await fetch(`${TWIN_ADMIN}/state`)).json()) as { agent: typeof a };
    expect(a).toEqual(state.agent);
  });
});

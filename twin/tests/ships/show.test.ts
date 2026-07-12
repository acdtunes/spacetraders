import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, runCli } from '../helpers/run-cli';
import type { Agent, Ship } from '../../src/world/types';

const AGENT = 'TWINAGENT';
const FIXTURE_DIR = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28');
interface RegisterTemplate { headquarters: string; ships: Ship[] }
const tpl = JSON.parse(readFileSync(path.join(FIXTURE_DIR, 'register.json'), 'utf8')) as RegisterTemplate;
function expectedStartingShips(agent: string): Ship[] { return tpl.ships.map((s) => ({ ...s, symbol: s.symbol.replace('{AGENT}', agent) })); }
interface TwinState { ships: Ship[]; agent: Agent | null }
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  expect(res.status).toBe(200);
}
async function twinState(): Promise<TwinState> { const res = await fetch(`${TWIN_ADMIN}/state`); expect(res.status).toBe(200); return (await res.json()) as TwinState; }
function escapeRe(s: string): string { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

describe('GET /v2/my/ships/{s} — single-ship state', () => {
  beforeEach(async () => { await resetWorld(); });
  it('ship refresh drives a fresh GET /my/ships/{s}, decoded field-for-field', async () => {
    const [cmd] = expectedStartingShips(AGENT);
    const res = runCli(['ship', 'refresh', '--ship', cmd.symbol, '--agent', AGENT]);
    expect(res.exitCode, res.stderr).toBe(0);
    const out = res.stdout;
    expect(out).toMatch(new RegExp(`Ship Symbol:\\s+${escapeRe(cmd.symbol)}\\b`));
    expect(out).toMatch(new RegExp(`Location:\\s+${escapeRe(tpl.headquarters)}\\b`));
    expect(out).toMatch(/Nav Status:\s+DOCKED\b/);
    expect(out).toMatch(new RegExp(`Fuel:\\s+${cmd.fuel.current} / ${cmd.fuel.capacity}\\b`));
    expect(out).toMatch(new RegExp(`Cargo:\\s+${cmd.cargo.units} / ${cmd.cargo.capacity} units`));
    expect(out).toMatch(new RegExp(`Engine Speed:\\s+${cmd.engine.speed}\\b`));
    const state = await twinState();
    const s = state.ships.find((x) => x.symbol === cmd.symbol) as Ship;
    expect(s.nav.systemSymbol).toBe('X1-PZ28'); expect(s.nav.waypointSymbol).toBe(tpl.headquarters);
    expect(s.nav.status).toBe('DOCKED'); expect(s.nav.flightMode).toBe('CRUISE');
    expect(s.cargo.inventory).toEqual(cmd.cargo.inventory); expect(s.frame.symbol).toBe(cmd.frame.symbol);
  });
  it('404s for an unknown ship', () => {
    const res = runCli(['ship', 'refresh', '--ship', `${AGENT}-404`, '--agent', AGENT]);
    expect(res.exitCode, 'refresh of a nonexistent ship must fail').not.toBe(0);
    expect(`${res.stdout}\n${res.stderr}`).toMatch(/not found|404/i);
  });
});

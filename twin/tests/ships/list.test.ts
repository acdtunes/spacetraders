import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, runCli } from '../helpers/run-cli';
import { restartTestDaemon } from '../helpers/daemon';
import type { Agent, Ship } from '../../src/world/types';

const AGENT = 'TWINAGENT';
const FIXTURE_DIR = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28');
interface RegisterTemplate { headquarters: string; ships: Ship[] }
const tpl = JSON.parse(readFileSync(path.join(FIXTURE_DIR, 'register.json'), 'utf8')) as RegisterTemplate;
function expectedStartingShips(agent: string): Ship[] {
  return tpl.ships.map((s) => ({ ...s, symbol: s.symbol.replace('{AGENT}', agent) }));
}
interface TwinState { ships: Ship[]; agent: Agent | null }
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  expect(res.status, 'POST /_twin/reset').toBe(200);
}
async function twinState(): Promise<TwinState> {
  const res = await fetch(`${TWIN_ADMIN}/state`); expect(res.status).toBe(200); return (await res.json()) as TwinState;
}
function listShipsJson(): Array<Record<string, unknown>> {
  const res = runCli(['ship', 'list', '--json', '--agent', AGENT]);
  if (res.exitCode !== 0) throw new Error(`ship list failed (exit ${res.exitCode}): ${res.stderr}`);
  const out = res.stdout.trim();
  if (out === '' || out.startsWith('No ships found')) return [];
  return JSON.parse(out) as Array<Record<string, unknown>>;
}
async function listShipsAfterSync(): Promise<Array<Record<string, unknown>>> {
  for (let i = 0; i < 12; i++) { const rows = listShipsJson(); if (rows.length >= 2) return rows; await new Promise((r) => setTimeout(r, 500)); }
  return listShipsJson();
}

describe('GET /v2/my/ships — paginated fleet snapshot', () => {
  beforeEach(async () => { await resetWorld(); });
  it('serves both starting hulls to the real client fleet sync (page 1 full, page 2 empty/200)', async () => {
    await restartTestDaemon();
    const rows = await listShipsAfterSync();
    expect(rows.length, `ship list rows: ${JSON.stringify(rows)}`).toBe(2);
    const bySymbol = Object.fromEntries(rows.map((r) => [r.symbol as string, r]));
    for (const exp of expectedStartingShips(AGENT)) {
      const row = bySymbol[exp.symbol];
      expect(row, `${exp.symbol} missing`).toBeTruthy();
      expect(row.location).toBe(tpl.headquarters);
      expect(row.navStatus).toBe('DOCKED');
      expect(row.fuelCurrent).toBe(exp.fuel.current);
      expect(row.fuelCapacity).toBe(exp.fuel.capacity);
      expect(row.cargoUnits).toBe(exp.cargo.units);
      expect(row.cargoCapacity).toBe(exp.cargo.capacity);
      expect(row.engineSpeed).toBe(exp.engine.speed);
    }
    const state = await twinState();
    expect(state.ships.length).toBe(2);
    expect(state.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  }, 90_000);
});

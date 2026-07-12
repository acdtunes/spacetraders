import { readFileSync } from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { beforeAll, describe, expect, it } from 'vitest';
import { REPO_ROOT, TEST_DATABASE_URL, TWIN_ADMIN, runCli } from './helpers/run-cli';
import { mintToken } from '../src/world/loader';

const RESET_DATE = (JSON.parse(
  readFileSync(path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'server-status.json'), 'utf8'),
) as { resetDate: string }).resetDate; // captured golden (e.g. 2026-07-05)
const ERA_NAME = `twinagent-${RESET_DATE}`;

function psql(sql: string): string {
  const res = spawnSync('psql', [TEST_DATABASE_URL, '-tA', '-c', sql], { encoding: 'utf8', timeout: 10_000 });
  if (res.status !== 0) throw new Error(`psql failed (exit ${res.status}): ${res.stderr}`);
  return (res.stdout ?? '').trim();
}

interface TwinState { agent: { symbol: string; credits: number; headquarters: string; startingFaction: string } | null; ships: Array<{ symbol: string; registration: { role: string } }>; }
async function twinState(): Promise<TwinState> {
  const res = await fetch(`${TWIN_ADMIN}/state`);
  expect(res.status).toBe(200);
  return (await res.json()) as TwinState;
}

describe('POST /v2/register via `spacetraders player register --new`', () => {
  // Fresh-DB slate so the era guard does not reject; RESTART IDENTITY re-mints id 1.
  beforeAll(() => {
    const res = spawnSync('psql', [TEST_DATABASE_URL, '-c', 'TRUNCATE players, eras RESTART IDENTITY CASCADE;'], { encoding: 'utf8', timeout: 10_000 });
    if (res.status !== 0) throw new Error(`beforeAll TRUNCATE failed (exit ${res.status}): ${res.stderr}`);
  });

  it('registers the cold-start agent: CLI + DB (contract) and world (behavior)', async () => {
    const r = runCli(['player', 'register', '--new', '--agent', 'TWINAGENT', '--faction', 'COSMIC']);
    expect(r.exitCode, r.stderr).toBe(0);
    expect(r.stdout).toContain('✓ New agent registered');
    expect(r.stdout).toContain('Agent Symbol: TWINAGENT');
    expect(r.stdout).toContain('Player ID:    1');
    expect(r.stdout).toContain(`Era:          ${ERA_NAME}`);

    const playerRow = psql("SELECT id, agent_symbol, token FROM players WHERE agent_symbol = 'TWINAGENT';");
    const [id, symbol, token] = playerRow.split('|');
    expect(id).toBe('1'); expect(symbol).toBe('TWINAGENT'); expect(token).toBe(mintToken('TWINAGENT'));

    const eraRow = psql("SELECT name, closed_at FROM eras WHERE agent_symbol = 'TWINAGENT';");
    const [eraName, closedAt] = eraRow.split('|');
    expect(eraName).toBe(ERA_NAME); expect(closedAt).toBe('');

    const state = await twinState();
    expect(state.agent).toMatchObject({ symbol: 'TWINAGENT', credits: 175000, headquarters: 'X1-PZ28-A1', startingFaction: 'COSMIC' });
    expect(state.ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(state.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });
});

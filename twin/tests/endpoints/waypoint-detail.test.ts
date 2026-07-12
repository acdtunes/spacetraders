import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const WAYPOINTS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'waypoints.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenWaypoint { symbol: string; type: string; systemSymbol: string; x: number; y: number; traits: Array<{ symbol: string; name: string; description: string }>; orbitals: Array<{ symbol: string }>; isUnderConstruction: boolean }
function jumpGate(): GoldenWaypoint {
  const g = (JSON.parse(readFileSync(WAYPOINTS_FIXTURE, 'utf8')) as GoldenWaypoint[]).find((w) => w.type === 'JUMP_GATE');
  if (!g) throw new Error('fixture invariant: no JUMP_GATE in X1-PZ28');
  return g;
}

describe('GET /v2/systems/{s}/waypoints/{w} — thin decode target', () => {
  it('returns { data: Waypoint } for the JUMP_GATE, isUnderConstruction per capture', async () => {
    const gate = jumpGate();
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${gate.symbol}`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: GoldenWaypoint };
    expect(body.data.symbol).toBe(gate.symbol);
    expect(typeof body.data.isUnderConstruction).toBe('boolean');
    expect(body.data).toEqual(gate);
  });
  it('404s an unknown waypoint with the error envelope', async () => {
    const missing = `${HOME_SYSTEM}-NOSUCHWP`;
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${missing}`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain(missing);
  });
});

describe('`spacetraders waypoint get` — detail command', () => {
  it('prints the JUMP_GATE type + coordinates round-tripped', () => {
    const gate = jumpGate();
    const { stdout, stderr, exitCode } = runCli(['waypoint', 'get', '--waypoint', gate.symbol, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toMatch(new RegExp(`Waypoint:\\s+${gate.symbol}`));
    expect(stdout).toMatch(/Type:\s+JUMP_GATE/);
    expect(stdout).toContain(`(${Math.round(gate.x)}, ${Math.round(gate.y)})`);
  });
});

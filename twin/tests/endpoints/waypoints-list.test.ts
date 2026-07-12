import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const WAYPOINTS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'waypoints.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenWaypoint { symbol: string; type: string; systemSymbol: string; x: number; y: number; traits: Array<{ symbol: string; name: string; description: string }>; orbitals: Array<{ symbol: string }>; isUnderConstruction: boolean }
function loadWaypoints(): GoldenWaypoint[] { return JSON.parse(readFileSync(WAYPOINTS_FIXTURE, 'utf8')) as GoldenWaypoint[]; }
function waypointRows(stdout: string): string[][] {
  return stdout.split('\n').map((l) => l.trimEnd()).filter((l) => /^X1-PZ28-\S/.test(l)).map((l) => l.split(/\s{2,}/));
}

describe('GET /v2/systems/{s}/waypoints — wire shape vs captured golden', () => {
  it('serves the full captured topology across limit=20 pages', async () => {
    const wps = loadWaypoints(); const total = wps.length; const limit = 20; const lastPage = Math.ceil(total / limit);
    const p1 = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=1&limit=${limit}`);
    expect(p1.status).toBe(200);
    const b1 = (await p1.json()) as { data: GoldenWaypoint[]; meta: { total: number; page: number; limit: number } };
    expect(b1.meta).toEqual({ total, page: 1, limit }); expect(b1.data).toHaveLength(limit);
    const past = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=${lastPage + 1}&limit=${limit}`);
    expect(((await past.json()) as { data: GoldenWaypoint[] }).data).toHaveLength(0);
    const wire = new Map<string, GoldenWaypoint>();
    for (let page = 1; page <= lastPage; page++) {
      const r = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints?page=${page}&limit=${limit}`);
      for (const w of ((await r.json()) as { data: GoldenWaypoint[] }).data) {
        expect(wire.has(w.symbol), `duplicate ${w.symbol}`).toBe(false); wire.set(w.symbol, w);
      }
    }
    expect(wire.size).toBe(total);
    for (const gold of wps) expect(wire.get(gold.symbol)).toEqual(gold);
  });
  it('404s an unknown system with the error envelope', async () => {
    const res = await fetch(`${TWIN_BASE_URL}/systems/X1-NOPE/waypoints?page=1&limit=20`);
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404); expect(body.error!.message).toContain('X1-NOPE');
  });
});

describe('`spacetraders waypoint list` — Go ListWaypoints round-trip', () => {
  it('surfaces every captured waypoint incl. the JUMP_GATE; filters resolve to fixture subsets', () => {
    const wps = loadWaypoints();
    const gate = wps.find((w) => w.type === 'JUMP_GATE')!;
    const { stdout, stderr, exitCode } = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    const rows = waypointRows(stdout);
    expect(rows).toHaveLength(wps.length);
    const gateRow = rows.find((c) => c[0] === gate.symbol)!;
    expect(gateRow[1]).toBe('JUMP_GATE'); expect(gateRow[2]).toBe(String(Math.round(gate.x))); expect(gateRow[3]).toBe(String(Math.round(gate.y)));
    const gateSymbols = wps.filter((w) => w.type === 'JUMP_GATE').map((w) => w.symbol).sort();
    const gates = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--type', 'JUMP_GATE', '--player-id', '1']);
    expect(gates.exitCode, gates.stderr).toBe(0);
    expect(waypointRows(gates.stdout).map((c) => c[0]).sort()).toEqual(gateSymbols);
    const shipyardSymbols = wps.filter((w) => w.traits.some((t) => t.symbol === 'SHIPYARD')).map((w) => w.symbol).sort();
    const yards = runCli(['waypoint', 'list', '--system', HOME_SYSTEM, '--trait', 'SHIPYARD', '--player-id', '1']);
    expect(yards.exitCode, yards.stderr).toBe(0);
    expect(waypointRows(yards.stdout).map((c) => c[0]).sort()).toEqual(shipyardSymbols);
  });
});

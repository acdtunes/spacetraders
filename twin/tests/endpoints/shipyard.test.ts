import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { runCli, TWIN_BASE_URL, REPO_ROOT } from '../helpers/run-cli';

const SHIPYARDS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'shipyards.json');
const HOME_SYSTEM = 'X1-PZ28';
interface GoldenListing { type: string; name: string; description: string; purchasePrice: number; engine: { speed: number } & Record<string, unknown> }
interface GoldenShipyard { symbol: string; shipTypes: Array<{ type: string }>; ships: GoldenListing[]; transactions: unknown[]; modificationsFee: number }
function loadProbeShipyard(): { shipyard: GoldenShipyard; probe: GoldenListing } {
  for (const s of JSON.parse(readFileSync(SHIPYARDS_FIXTURE, 'utf8')) as GoldenShipyard[]) {
    const probe = s.ships.find((x) => x.type === 'SHIP_PROBE'); if (probe) return { shipyard: s, probe };
  }
  throw new Error('fixture invariant: no SHIP_PROBE listing in any X1-PZ28 shipyard');
}

describe('GET /v2/systems/{s}/waypoints/{w}/shipyard — wire shape vs golden', () => {
  it('returns the captured shipyard field-for-field incl. SHIP_PROBE engine.speed + modificationsFee', async () => {
    const { shipyard, probe } = loadProbeShipyard();
    const res = await fetch(`${TWIN_BASE_URL}/systems/${HOME_SYSTEM}/waypoints/${shipyard.symbol}/shipyard`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: GoldenShipyard };
    expect(body.data.symbol).toBe(shipyard.symbol);
    expect(body.data.shipTypes.map((t) => t.type)).toContain('SHIP_PROBE');
    expect(body.data.modificationsFee).toBe(shipyard.modificationsFee);
    expect(Array.isArray(body.data.transactions)).toBe(true);
    const listing = body.data.ships.find((s) => s.type === 'SHIP_PROBE')!;
    expect(listing.purchasePrice).toBe(probe.purchasePrice);
    expect(typeof listing.engine.speed).toBe('number'); expect(listing.engine.speed).toBe(probe.engine.speed);
    expect(body.data).toEqual(shipyard);
  });
});

describe('`spacetraders shipyard list` — Go GetShipyard round-trip', () => {
  it('surfaces the SHIP_PROBE at the golden purchasePrice + the shipyard symbol', () => {
    const { shipyard, probe } = loadProbeShipyard();
    const { stdout, stderr, exitCode } = runCli(['shipyard', 'list', HOME_SYSTEM, shipyard.symbol, '--player-id', '1']);
    expect(exitCode, stderr).toBe(0);
    expect(stdout).toContain(`Shipyard: ${shipyard.symbol}`);
    expect(stdout).toContain('SHIP_PROBE');
    expect(stdout).toContain(probe.name);
    expect(stdout).toContain(String(probe.purchasePrice));
    if (shipyard.modificationsFee > 0) expect(stdout).toContain(`Modification Fee: ${shipyard.modificationsFee} credits`);
  });
});

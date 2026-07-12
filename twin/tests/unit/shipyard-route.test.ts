import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../../src/server';
import { loadColdStartWorld } from '../../src/world/loader';

// Hermetic Fastify-inject proof of Task 23 (GET .../shipyard). Runs under vitest.unit.config
// (no live stack). The CLI-acceptance proof lives in tests/endpoints/shipyard.test.ts and
// tests/endpoints/shipyard.errors.test.ts, driven at the wave barrier once the test Postgres
// + daemon are up.
const HOME_SYSTEM = 'X1-PZ28';
const MODULE_DIR = path.dirname(fileURLToPath(import.meta.url)); // twin/tests/unit
const SHIPYARDS_FIXTURE = path.resolve(MODULE_DIR, '../../fixtures/era2-X1-PZ28/shipyards.json');

interface GoldenListing { type: string; name: string; description: string; purchasePrice: number; engine: { speed: number } & Record<string, unknown> }
interface GoldenShipyard { symbol: string; shipTypes: Array<{ type: string }>; ships: GoldenListing[]; transactions: unknown[]; modificationsFee: number }

function loadShipyards(): GoldenShipyard[] {
  return JSON.parse(readFileSync(SHIPYARDS_FIXTURE, 'utf8')) as GoldenShipyard[];
}
// Every X1-PZ28 shipyard should list a SHIP_PROBE; pick the first from the committed
// fixture, never hardcoded (reconciliation decision 8).
function loadProbeShipyard(): { shipyard: GoldenShipyard; probe: GoldenListing } {
  for (const s of loadShipyards()) {
    const probe = s.ships.find((x) => x.type === 'SHIP_PROBE');
    if (probe) return { shipyard: s, probe };
  }
  throw new Error('fixture invariant: no SHIP_PROBE listing in any X1-PZ28 shipyard');
}

describe('GET /v2/systems/{s}/waypoints/{w}/shipyard (buildServer wiring)', () => {
  let app: FastifyInstance;
  beforeEach(async () => { app = buildServer({ world: loadColdStartWorld() }); await app.ready(); });
  afterEach(async () => { await app.close(); });

  it('serves the captured shipyard field-for-field incl. SHIP_PROBE engine.speed + modificationsFee', async () => {
    const { shipyard, probe } = loadProbeShipyard();
    const res = await app.inject({ method: 'GET', url: `/v2/systems/${HOME_SYSTEM}/waypoints/${shipyard.symbol}/shipyard` });
    expect(res.statusCode).toBe(200);
    const body = res.json() as { data: GoldenShipyard };
    expect(body.data.symbol).toBe(shipyard.symbol);
    expect(body.data.shipTypes.map((t) => t.type)).toContain('SHIP_PROBE');
    expect(body.data.modificationsFee).toBe(shipyard.modificationsFee);
    expect(Array.isArray(body.data.transactions)).toBe(true);
    const listing = body.data.ships.find((s) => s.type === 'SHIP_PROBE')!;
    expect(listing.purchasePrice).toBe(probe.purchasePrice);
    expect(typeof listing.engine.speed).toBe('number');
    expect(listing.engine.speed).toBe(probe.engine.speed);
    // Every captured field is preserved; the response ADDS the spec-required ShipyardShip fields
    // (symbol/supply/crew) and the deep condition/integrity/quality/description on frame/reactor/engine,
    // so this is toMatchObject (superset), not a verbatim toEqual.
    expect(body.data).toMatchObject(shipyard);
  });

  it('404s a waypoint with no shipyard, naming the waypoint in the error envelope', async () => {
    const missing = `${HOME_SYSTEM}-NOSHIPYARD`;
    const res = await app.inject({ method: 'GET', url: `/v2/systems/${HOME_SYSTEM}/waypoints/${missing}/shipyard` });
    expect(res.statusCode).toBe(404);
    const body = res.json() as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404);
    expect(body.error!.message).toContain(missing);
  });
});

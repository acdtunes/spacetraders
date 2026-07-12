import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../../src/server';
import { loadColdStartWorld } from '../../src/world/loader';

// Hermetic Fastify-inject proof of Task 22 (GET .../market). Runs under vitest.unit.config
// (no live stack). The CLI-acceptance proof lives in tests/market.test.ts and is driven at
// the wave barrier once the test Postgres + daemon are up.
const HOME_SYSTEM = 'X1-PZ28';
const MODULE_DIR = path.dirname(fileURLToPath(import.meta.url)); // twin/tests/unit
const MARKETS_FIXTURE = path.resolve(MODULE_DIR, '../../fixtures/era2-X1-PZ28/markets.json');

interface Good { symbol: string }
interface FixtureTradeGood { symbol: string; supply: string; activity: string; sellPrice: number; purchasePrice: number; tradeVolume: number }
interface FixtureMarket { symbol: string; exports: Good[]; imports: Good[]; exchange: Good[]; tradeGoods: FixtureTradeGood[] }

function loadMarkets(): FixtureMarket[] {
  return JSON.parse(readFileSync(MARKETS_FIXTURE, 'utf8')) as FixtureMarket[];
}
// The Go client derives EXPORT/IMPORT/EXCHANGE purely from which array a good is in, so
// exercise a market that populates all three. Chosen from the committed fixture, never
// hardcoded (reconciliation decision 8).
function richMarket(): FixtureMarket {
  const m = loadMarkets()
    .filter((x) => x.exports.length > 0 && x.imports.length > 0 && x.exchange.length > 0)
    .sort((a, b) => (a.symbol < b.symbol ? -1 : a.symbol > b.symbol ? 1 : 0))[0];
  if (!m) throw new Error('fixture invariant: no market populates exports+imports+exchange');
  return m;
}

describe('GET /v2/systems/{s}/waypoints/{w}/market (buildServer wiring)', () => {
  let app: FastifyInstance;
  beforeEach(async () => { app = buildServer({ world: loadColdStartWorld() }); await app.ready(); });
  afterEach(async () => { await app.close(); });

  it('serves the captured market verbatim so each good stays in its exports/imports/exchange array', async () => {
    const gold = richMarket();
    const res = await app.inject({ method: 'GET', url: `/v2/systems/${HOME_SYSTEM}/waypoints/${gold.symbol}/market` });
    expect(res.statusCode).toBe(200);
    const body = res.json() as { data: FixtureMarket };
    expect(body.data.symbol).toBe(gold.symbol);
    expect(body.data).toEqual(gold); // exports/imports/exchange + tradeGoods pass through unchanged
    const ex = new Set(body.data.exports.map((g) => g.symbol));
    const im = new Set(body.data.imports.map((g) => g.symbol));
    const xc = new Set(body.data.exchange.map((g) => g.symbol));
    expect(body.data.tradeGoods.length).toBeGreaterThan(0);
    for (const g of body.data.tradeGoods) {
      const memberships = (ex.has(g.symbol) ? 1 : 0) + (im.has(g.symbol) ? 1 : 0) + (xc.has(g.symbol) ? 1 : 0);
      expect(memberships, `${g.symbol} must be in exactly one array`).toBe(1);
    }
  });

  it('404s an unknown waypoint with the error envelope', async () => {
    const missing = `${HOME_SYSTEM}-NOSUCHWP`;
    const res = await app.inject({ method: 'GET', url: `/v2/systems/${HOME_SYSTEM}/waypoints/${missing}/market` });
    expect(res.statusCode).toBe(404);
    const body = res.json() as { error?: { message: string; code: number } };
    expect(body.error!.code).toBe(404);
    expect(body.error!.message).toContain(missing);
  });
});

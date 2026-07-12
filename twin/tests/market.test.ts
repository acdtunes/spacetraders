import { readFileSync } from 'node:fs';
import path from 'node:path';
import { beforeEach, describe, expect, it } from 'vitest';
import { REPO_ROOT, TWIN_ADMIN, TWIN_BASE_URL, runCli } from './helpers/run-cli';

const SYSTEM = 'X1-PZ28'; const HQ = 'X1-PZ28-A1'; const SCOUT = 'TWINAGENT-2'; const AGENT = 'TWINAGENT';
const MARKETS_FIXTURE = path.join(REPO_ROOT, 'twin', 'fixtures', 'era2-X1-PZ28', 'markets.json');
interface FixtureTradeGood { symbol: string; supply: string; activity: string; sellPrice: number; purchasePrice: number; tradeVolume: number }
interface FixtureMarket { symbol: string; exports: Array<{ symbol: string }>; imports: Array<{ symbol: string }>; exchange: Array<{ symbol: string }>; tradeGoods: FixtureTradeGood[] }
interface FindRow { WaypointSymbol: string; TradeType: string; PurchasePrice: number; SellPrice: number; Supply: string; Activity: string; TradeVolume: number; LastUpdated: string }
function loadHqMarket(): FixtureMarket {
  const all = JSON.parse(readFileSync(MARKETS_FIXTURE, 'utf8')) as FixtureMarket[];
  const m = all.find((x) => x.symbol === HQ); if (!m) throw new Error(`fixture markets.json has no market for ${HQ}`); return m;
}
async function resetWorld(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  if (!res.ok) throw new Error(`/_twin/reset failed: ${res.status}`);
}
function marketFindRows(good: string): FindRow[] {
  const { stdout, exitCode } = runCli(['market', 'find', '--good', good, '--system', SYSTEM, '--agent', AGENT, '--json']);
  if (exitCode !== 0) return [];
  const parsed = JSON.parse(stdout.trim() || '[]'); return Array.isArray(parsed) ? (parsed as FindRow[]) : [];
}
const hqRow = (good: string) => marketFindRows(good).find((r) => r.WaypointSymbol === HQ);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

describe('GET /systems/{s}/waypoints/{w}/market — goods classified by array membership', () => {
  beforeEach(async () => { await resetWorld(); });
  it('serves HQ market so the Go client classifies each good and round-trips prices', async () => {
    const market = loadHqMarket();
    expect(market.exports.length).toBeGreaterThan(0);
    expect(market.imports.length).toBeGreaterThan(0);
    expect(market.exchange.length).toBeGreaterThan(0);
    const exportSym = market.exports[0].symbol, importSym = market.imports[0].symbol, exchangeSym = market.exchange[0].symbol;

    const res = await fetch(`${TWIN_BASE_URL}/systems/${SYSTEM}/waypoints/${HQ}/market`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { data: FixtureMarket };
    expect(body.data.symbol).toBe(HQ);
    const ex = new Set(body.data.exports.map((g) => g.symbol)), im = new Set(body.data.imports.map((g) => g.symbol)), xc = new Set(body.data.exchange.map((g) => g.symbol));
    for (const g of body.data.tradeGoods) {
      const memberships = (ex.has(g.symbol) ? 1 : 0) + (im.has(g.symbol) ? 1 : 0) + (xc.has(g.symbol) ? 1 : 0);
      expect(memberships, `${g.symbol} must be in exactly one array`).toBe(1);
    }

    expect(runCli(['ship', 'list', '--agent', AGENT]).exitCode, 'warm fleet sync').toBe(0);
    const beforeScan = Date.now();
    const launch = runCli(['workflow', 'scout-markets', '--ships', SCOUT, '--system', SYSTEM, '--markets', HQ, '--iterations', '1', '--agent', AGENT]);
    expect(launch.exitCode, launch.stderr).toBe(0);
    const deadline = Date.now() + 100_000; let landed = false;
    while (Date.now() < deadline) { const r = hqRow(exportSym); if (r && Date.parse(r.LastUpdated) >= beforeScan - 1000) { landed = true; break; } await sleep(1000); }
    expect(landed, `fresh scan for ${HQ} never landed`).toBe(true);

    expect(hqRow(exportSym)?.TradeType).toBe('EXPORT');
    expect(hqRow(importSym)?.TradeType).toBe('IMPORT');
    expect(hqRow(exchangeSym)?.TradeType).toBe('EXCHANGE');
    for (const sym of [exportSym, importSym, exchangeSym]) {
      const tg = market.tradeGoods.find((g) => g.symbol === sym)!; const r = hqRow(sym)!;
      expect(r.PurchasePrice).toBe(tg.purchasePrice); expect(r.SellPrice).toBe(tg.sellPrice);
      expect(r.Supply).toBe(tg.supply); expect(r.Activity).toBe(tg.activity); expect(r.TradeVolume).toBe(tg.tradeVolume);
    }
  }, 115_000);
});

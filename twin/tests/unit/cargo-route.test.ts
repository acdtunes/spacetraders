import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of the cargo-trade /v2 surface: buy/sell IRON_ORE at a docked market
// (C42: purchasePrice 46, sellPrice 40). Docked-gate = 4244; good-not-sold = 4602; over-sell = 4218.
// Prices are API-faithful (buy costs purchasePrice, sell yields sellPrice).

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const SHIP = 'TWINAGENT-1';
const MARKET = 'X1-PZ28-C42'; // sells IRON_ORE @ purchase 46 / sell 40

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  w.agent!.credits = 1_000_000;
  return w;
}

let app: FastifyInstance;
let world: World;
beforeEach(() => {
  resetClock(); setNow(FROZEN_NOW); setClockMode('frozen');
  world = baseWorld(); app = buildServer({ world });
});
afterEach(async () => { if (app) await app.close(); });

const purchase = (payload: unknown, ship = SHIP) => app.inject({ method: 'POST', url: `/v2/my/ships/${ship}/purchase`, headers: AUTH, payload });
const sell = (payload: unknown, ship = SHIP) => app.inject({ method: 'POST', url: `/v2/my/ships/${ship}/sell`, headers: AUTH, payload });

function placeShip(waypoint: string, status: 'DOCKED' | 'IN_ORBIT', cargo: Array<{ symbol: string; units: number }> = []): void {
  const ship = world.ships.get(SHIP)!;
  ship.nav = { ...ship.nav, waypointSymbol: waypoint, status, route: null };
  world.transits.delete(SHIP);
  ship.cargo = { ...ship.cargo, capacity: 40, units: cargo.reduce((a, c) => a + c.units, 0), inventory: cargo.map((c) => ({ ...c })) };
}

describe('cargo route — purchase', () => {
  it('buys units at the docked market, adds cargo and debits credits at purchasePrice', async () => {
    placeShip(MARKET, 'DOCKED');
    const res = await purchase({ symbol: 'IRON_ORE', units: 10 });
    expect(res.statusCode).toBe(200);
    const d = res.json().data;
    expect(d.cargo.units).toBe(10);
    expect(d.agent.credits).toBe(1_000_000 - 10 * 46);
    expect(d.transaction).toMatchObject({ type: 'PURCHASE', units: 10, pricePerUnit: 46, totalPrice: 460 });
  });

  it('rejects a purchase while not docked (4244) and a good not sold here (4602)', async () => {
    placeShip(MARKET, 'IN_ORBIT');
    expect((await purchase({ symbol: 'IRON_ORE', units: 10 })).json().error.code).toBe(4244);
    placeShip(MARKET, 'DOCKED');
    expect((await purchase({ symbol: 'NONEXISTENT_GOOD', units: 5 })).json().error.code).toBe(4602);
  });

  it('rejects a purchase that exceeds cargo capacity (4217)', async () => {
    placeShip(MARKET, 'DOCKED'); // capacity 40
    expect((await purchase({ symbol: 'IRON_ORE', units: 41 })).json().error.code).toBe(4217);
  });
});

describe('cargo route — sell', () => {
  it('sells held cargo into the docked market, removes cargo and credits at sellPrice', async () => {
    placeShip(MARKET, 'DOCKED', [{ symbol: 'IRON_ORE', units: 10 }]);
    const res = await sell({ symbol: 'IRON_ORE', units: 10 });
    expect(res.statusCode).toBe(200);
    const d = res.json().data;
    expect(d.cargo.units).toBe(0);
    expect(d.agent.credits).toBe(1_000_000 + 10 * 40);
    expect(d.transaction).toMatchObject({ type: 'SELL', units: 10, pricePerUnit: 40, totalPrice: 400 });
  });

  it('rejects selling more than held (4218)', async () => {
    placeShip(MARKET, 'DOCKED', [{ symbol: 'IRON_ORE', units: 3 }]);
    expect((await sell({ symbol: 'IRON_ORE', units: 10 })).json().error.code).toBe(4218);
  });

  it('401s unauthenticated', async () => {
    placeShip(MARKET, 'DOCKED', [{ symbol: 'IRON_ORE', units: 3 }]);
    expect((await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/sell`, payload: { symbol: 'IRON_ORE', units: 1 } })).statusCode).toBe(401);
  });
});

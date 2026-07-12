import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// SHIP-ACTIONS READ-BACK — in-process (Fastify-inject) teeth for the live-stack ship-actions
// scenarios 4 (refuel) & 5 (purchase). The live suite (tests/acceptance/ship-actions.e2e.test.ts)
// drives the daemon read-back chain; here we assert the SAME OBSERVABLES straight off the twin's
// /v2 surface — GET /my/ships (roster), GET /my/ships/:s (fuel + reconciled nav), GET /my/agent
// (credits) — the exact endpoints the daemon re-syncs from on `ship refresh` / `ship list`.
//
// These lock what could otherwise silently no-op on the twin side and read as a live RED:
//   • Scenario 4: a refuel that adds no fuel, or is free (no credit debit).
//   • Scenario 5: a purchase that adds no hull, mis-sites it, or is free (no credit debit).
// The complementary navigate-fuel.test.ts already proves the voyage BURN; this proves the FILL.
//
// Ground truth (fixtures/era2-X1-PZ28): TWINAGENT-1 = COMMAND frigate (tank 400/400) & TWINAGENT-2
// = probe, both DOCKED at A1; agent holds 175,000 credits. A1 & A2 markets sell FUEL @72
// (1 market unit = 100 ship-fuel → ceil(72/100)=1 credit/unit). A2 shipyard sells SHIP_PROBE @24,680.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };

const FRIGATE = 'TWINAGENT-1';
const PROBE_BUYER = 'TWINAGENT-2';
const HOME = 'X1-PZ28-A1';   // cold-start waypoint; its market sells FUEL
const YARD = 'X1-PZ28-A2';   // shipyard; sells SHIP_PROBE @ 24,680; market also sells FUEL
const PROBE_TYPE = 'SHIP_PROBE';
const CAPACITY = 400;        // frigate tank capacity (fixture)
const DRAINED = 100;         // a post-voyage quarter tank — below capacity (the refuel pre-check's teeth)

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

function getShip(symbol: string) {
  return app.inject({ method: 'GET', url: `/v2/my/ships/${symbol}`, headers: AUTH });
}
function getRoster() {
  return app.inject({ method: 'GET', url: '/v2/my/ships', headers: AUTH });
}
async function credits(): Promise<number> {
  const res = await app.inject({ method: 'GET', url: '/v2/my/agent', headers: AUTH });
  expect(res.statusCode, 'agent read-back must succeed').toBe(200);
  return res.json().data.credits as number;
}

// ── Scenario 4 analogue: refuel a drained tank ──────────────────────────────────────────────────
describe('POST /v2/my/ships/:s/refuel — read-back fills the tank to capacity and debits the agent', () => {
  it('restores fuel current→capacity (strictly above the drained level) and charges market price', async () => {
    // ── Given: a frigate DOCKED at a FUEL market with a tank drained by a prior voyage. The BURN is
    //    proven real by navigate-fuel.test.ts; here it is the arranged precondition refuel restores. ──
    const world = baseWorld();
    const frigate = world.ships.get(FRIGATE)!;
    frigate.fuel = { current: DRAINED, capacity: CAPACITY };
    app = buildServer({ world });

    // pre-check (mirrors the live scenario's fuelBefore<capacity teeth): the drain is observable
    // through the very GET the daemon re-syncs from.
    const before = await getShip(FRIGATE);
    expect(before.statusCode).toBe(200);
    expect(before.json().data.nav.status, 'must be docked at the fuel market to refuel').toBe('DOCKED');
    expect(before.json().data.nav.waypointSymbol).toBe(HOME);
    expect(before.json().data.fuel.current as number, 'the voyage burned fuel — tank below capacity').toBeLessThan(CAPACITY);
    const creditsBefore = await credits();

    // ── When: the pilot refuels at the docked market ──
    const res = await app.inject({ method: 'POST', url: `/v2/my/ships/${FRIGATE}/refuel`, headers: AUTH, payload: {} });
    expect(res.statusCode, `refuel failed: ${res.body}`).toBe(200);
    const txn = res.json().data.transaction as { tradeSymbol: string; type: string; totalPrice: number };

    // real-API contract: refuel is a FUEL PURCHASE with a positive price at a 72-credit market
    expect(txn.tradeSymbol).toBe('FUEL');
    expect(txn.type).toBe('PURCHASE');
    expect(txn.totalPrice, 'refuelling a drained tank is not free').toBeGreaterThan(0);

    // ── Then: the read-back tank is FULL and strictly above the drained level ──
    const after = await getShip(FRIGATE);
    expect(after.statusCode).toBe(200);
    const fuel = after.json().data.fuel as { current: number; capacity: number };
    expect(fuel.current, 'refuel must fill the tank to capacity').toBe(fuel.capacity);
    expect(fuel.current, 'teeth: fuel strictly increased vs the drained level').toBeGreaterThan(DRAINED);

    // ── ... and the agent paid exactly the transaction price (non-circular: debit == quoted price) ──
    expect(await credits(), 'refuel debit must equal the quoted transaction price').toBe(creditsBefore - txn.totalPrice);
    expect(await credits(), 'refuelling is not free — credits dropped').toBeLessThan(creditsBefore);
  });
});

// ── Scenario 5 analogue: expand the fleet at a shipyard ──────────────────────────────────────────
describe('POST /v2/my/ships — read-back grows the roster by one reconciled hull and debits the agent', () => {
  it('adds exactly one new hull DOCKED at the shipyard and charges its purchase price', async () => {
    // ── Given: the cold-start roster (two hulls) and a funded agent ──
    app = buildServer({ world: baseWorld() });
    const rosterBefore = getRosterSymbols(await getRoster());
    expect(rosterBefore.length, 'cold start has two hulls').toBe(2);
    const creditsBefore = await credits();
    expect(creditsBefore, 'agent must afford the probe').toBeGreaterThan(0);

    // ── When: buy a SHIP_PROBE at the adjacent shipyard A2 ──
    const res = await app.inject({
      method: 'POST', url: '/v2/my/ships', headers: AUTH,
      payload: { shipType: PROBE_TYPE, waypointSymbol: YARD },
    });
    expect(res.statusCode, `purchase failed: ${res.body}`).toBe(201);
    const price = res.json().data.transaction.price as number;
    const newSymbol = res.json().data.ship.symbol as string;
    expect(price, 'a shipyard purchase is not free').toBeGreaterThan(0);

    // ── Then: the roster read-back grows by exactly one brand-new hull ──
    const rosterAfter = getRosterSymbols(await getRoster());
    expect(rosterAfter.length, 'the purchased hull must appear in the roster').toBe(rosterBefore.length + 1);
    const added = rosterAfter.filter((s) => !rosterBefore.includes(s));
    expect(added, 'exactly one brand-new hull was added').toEqual([newSymbol]);

    // ── Teeth: the new hull reconciles as a real probe DOCKED at the shipyard, not a phantom row ──
    const bought = await getShip(newSymbol);
    expect(bought.statusCode).toBe(200);
    expect(bought.json().data.nav.waypointSymbol, 'the new hull sits at the shipyard').toBe(YARD);
    expect(bought.json().data.nav.status, 'a freshly bought hull is docked').toBe('DOCKED');

    // ── Teeth: a real ECONOMIC transaction — the agent paid exactly the purchase price ──
    expect(await credits(), 'buying a probe charged the agent exactly its price').toBe(creditsBefore - price);
    expect(await credits(), 'credits must drop').toBeLessThan(creditsBefore);

    // untouched buyer stays put (the purchase turns on the roster, not a phantom move of the buyer)
    const buyer = await getShip(PROBE_BUYER);
    expect(buyer.statusCode).toBe(200);
  });
});

function getRosterSymbols(res: Awaited<ReturnType<typeof getRoster>>): string[] {
  expect(res.statusCode, 'roster read-back must succeed').toBe(200);
  return (res.json().data as Array<{ symbol: string }>).map((s) => s.symbol);
}

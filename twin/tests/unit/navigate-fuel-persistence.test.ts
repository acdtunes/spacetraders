import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setCompression, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// NAVIGATE FUEL BURN — DURABILITY THROUGH ARRIVAL SETTLE.
//
// navigate-fuel.test.ts already proves the burn lands on the stored ship when re-GET IMMEDIATELY
// (still IN_TRANSIT). This is the assertion that suite lacked: re-GET AFTER the (compressed) arrival
// AND after the arrival-settle path runs (POST /orbit → settleArrival commits the arrival into stored
// nav). The live-stack symptom was GET /my/ships showing a full 400/400 tank after a voyage; the
// burn must be durably reduced at departure and NOT be resurrected by the arrival settle. It isn't
// — settleArrival only commits nav, never fuel — and this locks that forever.
//
// The observed live "−1 credit with only a `navigate` log entry" is NOT a lost burn: it is the
// daemon's post-arrival auto-refuel buying ONE market unit (100 ship-fuel) to top up the 50 burned
// here — the twin's refuel is correct (test 2 proves a full-tank refuel is free), so a −1 can only be
// a real >0-fuel top-up, never a phantom 0-unit charge.
//
// Ground truth (fixtures/era2-X1-PZ28): TWINAGENT-1 = COMMAND frigate, 400/400, CRUISE, DOCKED at
// A1 (17,-21), engine speed 30. A1→F55 (53,-55) = 49.518 → round 50 fuel in CRUISE. Compression is
// dialled high so the compressed ETA floors at TWIN_MIN_TRAVEL_MS (1000ms) — a ~1s real wait.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const FRIGATE = 'TWINAGENT-1';
const A1 = 'X1-PZ28-A1';
const F55 = 'X1-PZ28-F55';
const FULL = 400;
const CRUISE_BURN_TO_F55 = 50; // round(49.518)

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); setCompression(1000); });
afterEach(async () => { setCompression(20); if (app) await app.close(); });

function navigate(symbol: string, waypointSymbol: string) {
  return app.inject({ method: 'POST', url: `/v2/my/ships/${symbol}/navigate`, headers: AUTH, payload: { waypointSymbol } });
}
function orbit(symbol: string) {
  return app.inject({ method: 'POST', url: `/v2/my/ships/${symbol}/orbit`, headers: AUTH });
}
function refuel(symbol: string) {
  return app.inject({ method: 'POST', url: `/v2/my/ships/${symbol}/refuel`, headers: AUTH });
}
async function getShip(symbol: string) {
  const res = await app.inject({ method: 'GET', url: `/v2/my/ships/${symbol}`, headers: AUTH });
  expect(res.statusCode).toBe(200);
  return (res.json() as { data: { nav: { status: string; waypointSymbol: string }; fuel: { current: number } } }).data;
}
/** Poll the ship on the REAL wall clock until it is observed arrived (IN_ORBIT), or the budget lapses. */
async function pollArrived(symbol: string): Promise<{ nav: { status: string; waypointSymbol: string }; fuel: { current: number } }> {
  for (let i = 0; i < 30; i++) {
    const ship = await getShip(symbol);
    if (ship.nav.status === 'IN_ORBIT') return ship;
    await new Promise((r) => setTimeout(r, 150));
  }
  return getShip(symbol);
}

describe('POST /v2/my/ships/:symbol/navigate — the burn persists through arrival settle', () => {
  it('re-GET after arrival + settle shows the drained tank, never a resurrected full tank', async () => {
    app = buildServer({ world: baseWorld() });
    const expected = FULL - CRUISE_BURN_TO_F55; // 350

    const res = await navigate(FRIGATE, F55);
    expect(res.statusCode).toBe(200);
    expect((res.json() as { data: { fuel: { current: number } } }).data.fuel.current).toBe(expected);

    // in transit (on-read), the tank already reads drained
    const arrived = await pollArrived(FRIGATE);
    expect(arrived.nav.status, 'the compressed voyage must complete within the poll budget').toBe('IN_ORBIT');
    expect(arrived.nav.waypointSymbol, 'arrived at the destination').toBe(F55);
    expect(arrived.fuel.current, 'the departure burn is still on the stored ship after arrival').toBe(expected);

    // POST /orbit runs settleArrival — the arrival-settle path that commits nav into the stored ship.
    // It must NOT touch fuel: the tank stays drained.
    expect((await orbit(FRIGATE)).statusCode).toBe(200);
    const settled = await getShip(FRIGATE);
    expect(settled.nav.status).toBe('IN_ORBIT');
    expect(settled.nav.waypointSymbol).toBe(F55);
    expect(settled.fuel.current, 'settleArrival commits nav only — the burn survives it').toBe(expected);
  });
});

describe('POST /v2/my/ships/:symbol/refuel — a full tank is a free 0-unit no-op', () => {
  it('charges 0 credits and buys 0 market units when nothing is missing (0 units must cost 0)', async () => {
    app = buildServer({ world: baseWorld() });
    const before = await getShip(FRIGATE);
    expect(before.fuel.current, 'frigate starts with a full tank at A1').toBe(FULL);

    const res = await refuel(FRIGATE);
    expect(res.statusCode).toBe(200);
    const data = (res.json() as { data: { agent: { credits: number }; transaction: { units: number; totalPrice: number } } }).data;
    expect(data.transaction.units, 'a full tank needs 0 market units').toBe(0);
    expect(data.transaction.totalPrice, '0 units must cost 0 credits — never a phantom 1-credit charge').toBe(0);
    expect(data.agent.credits, 'a free refuel leaves the treasury untouched').toBe(175_000);
  });
});

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ── ACCEPTANCE — the contract economy as a STORY (in-process /v2 wire via buildServer().inject) ──────
//
// The contract lifecycle has no low-level gobot CLI (only the excluded batch-contract automation), so
// the highest feasible acceptance level is the real /v2 wire driven in-process. The per-endpoint UNITS
// (twin/tests/unit/contracts-route.test.ts) each assert one mutation against that mutation's OWN echoed
// response. This test proves the CUMULATIVE, PERSISTED behaviour of the whole negotiate → accept →
// deliver → fulfill journey by reading OBSERVABLE state back through an INDEPENDENT observation path —
// a fresh GET — after every mutation:
//     GET /my/agent          -> the treasury balance (the running credit total)
//     GET /my/contracts/:id   -> accepted / fulfilled / unitsFulfilled progress
//     GET /my/ships/:symbol   -> the ship's remaining cargo
//
// Teeth: the treasury is walked 1,000,000 → 1,020,000 → 1,100,000 and the hold 60 → 30 → 0, each value
// re-observed via a SEPARATE GET. A mutation that silently no-op'd would break the running total and
// fail HERE even if its own response happened to look right. Two negative checkpoints prove a premature
// fulfill neither pays nor flips `fulfilled`.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const SHIP = 'TWINAGENT-1';
const DELIVER_TO = 'X1-PZ28-A1'; // the deterministic procurement fixture's deliverable destination

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

// ── driving-port mutations (POST through the /v2 wire) ───────────────────────────────────────────────
const negotiate = (ship = SHIP) => app.inject({ method: 'POST', url: `/v2/my/ships/${ship}/negotiate/contract`, headers: AUTH });
const accept = (id: string) => app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/accept`, headers: AUTH });
const deliver = (id: string, units: number) =>
  app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/deliver`, headers: AUTH, payload: { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units } });
const fulfill = (id: string) => app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/fulfill`, headers: AUTH });

// ── observation path — independent GETs. Reading state back HERE (not from the mutation response) is
// what gives the story its teeth: it proves the effect PERSISTED into world state. ───────────────────
async function observedCredits(): Promise<number> {
  const res = await app.inject({ method: 'GET', url: '/v2/my/agent', headers: AUTH });
  expect(res.statusCode).toBe(200);
  return res.json().data.credits as number;
}
async function observedContract(id: string) {
  const res = await app.inject({ method: 'GET', url: `/v2/my/contracts/${id}`, headers: AUTH });
  expect(res.statusCode).toBe(200);
  return res.json().data;
}
async function observedCargoUnits(ship = SHIP): Promise<number> {
  const res = await app.inject({ method: 'GET', url: `/v2/my/ships/${ship}`, headers: AUTH });
  expect(res.statusCode).toBe(200);
  return res.json().data.cargo.units as number;
}

/** Dock SHIP at `waypoint` (clearing any transit) and load its hold — copied from
 *  twin/tests/unit/contracts-route.test.ts's dockShipAt (the delivery setup). */
function dockShipAt(waypoint: string, cargo: Array<{ symbol: string; units: number }> = []): void {
  const ship = world.ships.get(SHIP)!;
  ship.nav = { ...ship.nav, waypointSymbol: waypoint, status: 'DOCKED', route: null };
  world.transits.delete(SHIP);
  ship.cargo = {
    ...ship.cargo,
    capacity: Math.max(ship.cargo.capacity, 100),
    units: cargo.reduce((a, c) => a + c.units, 0),
    inventory: cargo.map((c) => ({ ...c })),
  };
}

describe('CONTRACT lifecycle — the whole economy, proven by state re-observed after each step', () => {
  it('walks credits 1,000,000 → 1,020,000 → 1,100,000 and the hold 60 → 0, and a premature fulfill is a no-op', async () => {
    // ── Given a fresh agent with a known treasury and a ship — observed at baseline, not assumed ──
    expect(await observedCredits()).toBe(1_000_000);

    // ── When the agent negotiates a contract ──
    const negRes = await negotiate();
    expect(negRes.statusCode).toBe(201);
    const id = negRes.json().data.contract.id as string;

    // ── Then a contract exists with the deterministic procurement terms, un-accepted and un-fulfilled ──
    const minted = await observedContract(id);
    expect(minted.accepted).toBe(false);
    expect(minted.fulfilled).toBe(false);
    expect(minted.terms.payment).toEqual({ onAccepted: 20_000, onFulfilled: 80_000 });
    expect(minted.terms.deliver).toEqual([
      { tradeSymbol: 'IRON_ORE', destinationSymbol: DELIVER_TO, unitsRequired: 60, unitsFulfilled: 0 },
    ]);
    // …and negotiating alone pays nothing: the running total is still at baseline.
    expect(await observedCredits()).toBe(1_000_000);

    // ── When the agent accepts the contract ──
    expect((await accept(id)).statusCode).toBe(200);

    // ── Then the treasury is credited onAccepted and the contract reads back accepted (both persisted) ──
    expect(await observedCredits()).toBe(1_020_000);
    const afterAccept = await observedContract(id);
    expect(afterAccept.accepted).toBe(true);
    expect(afterAccept.fulfilled).toBe(false);

    // ── When the agent tries to fulfill BEFORE delivering anything ──
    const prematureEmpty = await fulfill(id);
    // ── Then it is rejected — and, the teeth, it pays NOTHING and does NOT flip fulfilled ──
    expect(prematureEmpty.statusCode).toBe(400);
    expect(prematureEmpty.json().error.code).toBe(4502); // deliverables not yet met
    expect(await observedCredits()).toBe(1_020_000);            // unchanged
    expect((await observedContract(id)).fulfilled).toBe(false); // still open

    // ── Given the ship is DOCKED at the deliverable destination holding 60 IRON_ORE ──
    dockShipAt(DELIVER_TO, [{ symbol: 'IRON_ORE', units: 60 }]);
    expect(await observedCargoUnits()).toBe(60);

    // ── When the agent delivers 30 of the 60 units (a partial delivery) ──
    expect((await deliver(id, 30)).statusCode).toBe(200);
    // ── Then progress advances to 30 and the hold drops by 30, both re-observed ──
    expect((await observedContract(id)).terms.deliver[0].unitsFulfilled).toBe(30);
    expect(await observedCargoUnits()).toBe(30);

    // ── When the agent tries to fulfill with the delivery only PARTIALLY complete ──
    const prematurePartial = await fulfill(id);
    // ── Then it is still rejected, still pays nothing, and progress is untouched (running total intact) ──
    expect(prematurePartial.statusCode).toBe(400);
    expect(prematurePartial.json().error.code).toBe(4502);
    expect(await observedCredits()).toBe(1_020_000);
    const stillPartial = await observedContract(id);
    expect(stillPartial.fulfilled).toBe(false);
    expect(stillPartial.terms.deliver[0].unitsFulfilled).toBe(30);

    // ── When the agent delivers the remaining 30 units ──
    expect((await deliver(id, 30)).statusCode).toBe(200);
    // ── Then the deliverable is fully met (60) and the hold is empty — 60 total units consumed ──
    expect((await observedContract(id)).terms.deliver[0].unitsFulfilled).toBe(60);
    expect(await observedCargoUnits()).toBe(0);

    // ── When the agent fulfills the now-complete contract ──
    expect((await fulfill(id)).statusCode).toBe(200);
    // ── Then the treasury is credited onFulfilled (total 1,100,000) and the contract reads back fulfilled ──
    expect(await observedCredits()).toBe(1_100_000);
    expect((await observedContract(id)).fulfilled).toBe(true);

    // ── And the active-contract slot is freed: a fresh negotiation now SUCCEEDS with a new contract id ──
    const next = await negotiate();
    expect(next.statusCode).toBe(201);
    expect(next.json().data.contract.id).not.toBe(id);
  });
});

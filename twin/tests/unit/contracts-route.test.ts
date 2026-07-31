import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of the INCOME contract /v2 surface: negotiate mints the deterministic
// IRON_ORE→A1 procurement fixture (one-active guard = 4511), accept pays onAccepted, deliver moves
// cargo (docked-at-destination gate = 4510), fulfill pays onFulfilled once every line is met (else
// 4502) and frees the next negotiation.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const SHIP = 'TWINAGENT-1';

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

const negotiate = (ship = SHIP) => app.inject({ method: 'POST', url: `/v2/my/ships/${ship}/negotiate/contract`, headers: AUTH });
const getContract = (id: string) => app.inject({ method: 'GET', url: `/v2/my/contracts/${id}`, headers: AUTH });
const accept = (id: string) => app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/accept`, headers: AUTH });
const deliver = (id: string, payload: unknown) => app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/deliver`, headers: AUTH, payload });
const fulfill = (id: string) => app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/fulfill`, headers: AUTH });

/** Dock SHIP at `waypoint` (clearing any transit) and optionally load cargo — the deliver setup. */
function dockShipAt(waypoint: string, cargo: Array<{ symbol: string; units: number }> = []): void {
  const ship = world.ships.get(SHIP)!;
  ship.nav = { ...ship.nav, waypointSymbol: waypoint, status: 'DOCKED', route: null };
  world.transits.delete(SHIP);
  ship.cargo = { ...ship.cargo, capacity: Math.max(ship.cargo.capacity, 100), units: cargo.reduce((a, c) => a + c.units, 0), inventory: cargo.map((c) => ({ ...c })) };
}

describe('INCOME contract routes — negotiate / get / accept', () => {
  it('negotiate mints the deterministic IRON_ORE→A1 procurement contract', async () => {
    const res = await negotiate();
    expect(res.statusCode).toBe(201);
    const c = res.json().data.contract;
    expect(c.accepted).toBe(false);
    expect(c.fulfilled).toBe(false);
    expect(c.terms.payment).toEqual({ onAccepted: 20_000, onFulfilled: 80_000 });
    expect(c.terms.deliver).toEqual([{ tradeSymbol: 'IRON_ORE', destinationSymbol: 'X1-PZ28-A1', unitsRequired: 60, unitsFulfilled: 0 }]);
    expect(typeof c.terms.deadline).toBe('string');
  });

  it('one-active guard: a second negotiate returns 4511 with the active contract id', async () => {
    const first = (await negotiate()).json().data.contract;
    const res = await negotiate();
    expect(res.statusCode).toBe(400);
    expect(res.json().error.code).toBe(4511);
    expect(res.json().error.data.contractId).toBe(first.id);
  });

  it('GET returns the contract as the data payload; unknown id 404s', async () => {
    const id = (await negotiate()).json().data.contract.id;
    const res = await getContract(id);
    expect(res.statusCode).toBe(200);
    expect(res.json().data.id).toBe(id);
    expect((await getContract('contract-999')).statusCode).toBe(404);
  });

  it('accept flips accepted and pays onAccepted into the treasury', async () => {
    const id = (await negotiate()).json().data.contract.id;
    const res = await accept(id);
    expect(res.statusCode).toBe(200);
    expect(res.json().data.contract.accepted).toBe(true);
    expect(res.json().data.agent.credits).toBe(1_020_000);
    // re-accept is rejected (payment credited exactly once)
    expect((await accept(id)).statusCode).toBe(400);
  });

  it('negotiate 404s for an unknown ship and 401s unauthenticated', async () => {
    expect((await app.inject({ method: 'POST', url: '/v2/my/ships/NOPE-9/negotiate/contract', headers: AUTH })).statusCode).toBe(404);
    expect((await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/negotiate/contract` })).statusCode).toBe(401);
  });
});

describe('INCOME contract routes — deliver / fulfill (the economy pair)', () => {
  it('deliver moves cargo into the deliverable and empties the hold', async () => {
    const id = (await negotiate()).json().data.contract.id;
    await accept(id);
    dockShipAt('X1-PZ28-A1', [{ symbol: 'IRON_ORE', units: 60 }]);
    const res = await deliver(id, { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units: 60 });
    expect(res.statusCode).toBe(200);
    expect(res.json().data.contract.terms.deliver[0].unitsFulfilled).toBe(60);
    expect(res.json().data.cargo.units).toBe(0);
  });

  it('deliver while not docked at the destination is rejected 4510', async () => {
    const id = (await negotiate()).json().data.contract.id;
    await accept(id);
    dockShipAt('X1-PZ28-C42', [{ symbol: 'IRON_ORE', units: 60 }]); // docked, but NOT at A1
    const res = await deliver(id, { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units: 60 });
    expect(res.statusCode).toBe(400);
    expect(res.json().error.code).toBe(4510);
  });

  it('fulfill pays onFulfilled once every line is met and frees the next negotiation', async () => {
    const id = (await negotiate()).json().data.contract.id;
    await accept(id);
    // fulfill before delivery is met -> 4502
    expect((await fulfill(id)).json().error.code).toBe(4502);

    dockShipAt('X1-PZ28-A1', [{ symbol: 'IRON_ORE', units: 60 }]);
    await deliver(id, { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units: 60 });
    const res = await fulfill(id);
    expect(res.statusCode).toBe(200);
    expect(res.json().data.contract.fulfilled).toBe(true);
    expect(res.json().data.agent.credits).toBe(1_100_000); // 1M + 20k accept + 80k fulfill

    // active slot cleared -> a fresh negotiate succeeds (new id)
    const next = await negotiate();
    expect(next.statusCode).toBe(201);
    expect(next.json().data.contract.id).not.toBe(id);
  });
});

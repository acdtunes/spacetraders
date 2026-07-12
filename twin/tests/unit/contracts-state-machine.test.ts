import { beforeEach, describe, expect, it } from 'vitest';
import type { Contract, World } from '../../src/world/types';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';
import { ContractError } from '../../src/errors';
import {
  acceptContract, deliverToContract, fulfillContract, negotiateContract, serializeContract,
} from '../../src/world/contracts';

// Pure world-model proof of the CONTRACT state machine (negotiate/accept/deliver/fulfill) — the
// helpers the /v2 contract routes consume. The world clock is pinned FROZEN so the fixture's
// derived deadline is deterministic. Fidelity anchors (api-fidelity-spec st-drm.3 + client.go
// parseContractData): canonical shape carries terms.deliver (NOT "deliveries"); ONE-active guard
// -> 4511; fulfill only when every deliverable is met; accept/fulfill move credits by the payment.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const DEADLINE_7D = '2026-07-18T00:00:00.000Z'; // FROZEN_NOW + 7 days (the fixture deadline)
const START_CREDITS = 175_000;                   // register.json startingCredits
const CMD = 'TWINAGENT-1';                       // COMMAND hull from register.json

function seededWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: 'tok-1' });
  return w;
}

/** Load a ship's hold with `units` of `symbol` (bypasses capacity — the state machine reads the
 *  inventory, capacity gating is the route's concern). */
function giveCargo(world: World, shipSymbol: string, symbol: string, units: number): void {
  const ship = world.ships.get(shipSymbol)!;
  ship.cargo = { ...ship.cargo, units, inventory: [{ symbol, name: symbol, description: symbol, units }] };
}

function cargoUnits(world: World, shipSymbol: string, symbol: string): number {
  const item = world.ships.get(shipSymbol)!.cargo.inventory.find((i) => i.symbol === symbol);
  return item ? item.units : 0;
}

function catchError(fn: () => unknown): ContractError {
  try { fn(); } catch (e) { return e as ContractError; }
  throw new Error('expected the call to throw a ContractError, but it did not');
}

beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });

describe('cold defaults', () => {
  it('loadColdStartWorld seeds an empty contracts map and null activeContractId', () => {
    const w = loadColdStartWorld();
    expect(w.contracts instanceof Map).toBe(true);
    expect(w.contracts.size).toBe(0);
    expect(w.activeContractId).toBeNull();
  });
});

describe('negotiateContract — fixture template + serializer', () => {
  it('creates the deterministic PROCUREMENT fixture, stores it, and sets activeContractId', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });

    expect(c.factionSymbol).toBe('COSMIC');
    expect(c.type).toBe('PROCUREMENT');
    expect(c.accepted).toBe(false);
    expect(c.fulfilled).toBe(false);
    expect(c.terms.payment).toEqual({ onAccepted: 20_000, onFulfilled: 80_000 });
    expect(c.terms.deadline).toBe(DEADLINE_7D);
    expect(c.terms.deliver).toEqual([
      { tradeSymbol: 'IRON_ORE', destinationSymbol: 'X1-PZ28-A1', unitsRequired: 60, unitsFulfilled: 0 },
    ]);
    // deliverable good is buyable in the seeded X1-PZ28 markets
    expect(w.markets.get('X1-PZ28-C42')?.tradeGoods.some((g) => g.symbol === 'IRON_ORE')).toBe(true);

    expect(w.activeContractId).toBe(c.id);
    expect(w.contracts.get(c.id)).toBe(c);
  });

  it('serializeContract returns the canonical shape (deliver key, not deliveries) as a copy', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    const wire = serializeContract(c) as Contract & { terms: { deliveries?: unknown } };

    expect(Object.keys(wire).sort()).toEqual(['accepted', 'factionSymbol', 'fulfilled', 'id', 'terms', 'type']);
    expect(Object.keys(wire.terms).sort()).toEqual(['deadline', 'deliver', 'payment']);
    expect(wire.terms.deliveries).toBeUndefined();
    expect(wire).toEqual({
      id: c.id,
      factionSymbol: 'COSMIC',
      type: 'PROCUREMENT',
      accepted: false,
      fulfilled: false,
      terms: {
        deadline: DEADLINE_7D,
        payment: { onAccepted: 20_000, onFulfilled: 80_000 },
        deliver: [{ tradeSymbol: 'IRON_ORE', destinationSymbol: 'X1-PZ28-A1', unitsRequired: 60, unitsFulfilled: 0 }],
      },
    });
    // mutating the wire copy must not corrupt stored world state
    wire.terms.deliver[0].unitsFulfilled = 999;
    expect(w.contracts.get(c.id)!.terms.deliver[0].unitsFulfilled).toBe(0);
  });
});

describe('acceptContract — credits', () => {
  it('flips accepted true and pays onAccepted into the treasury', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    expect(w.agent!.credits).toBe(START_CREDITS);

    const accepted = acceptContract(w, c.id);
    expect(accepted.accepted).toBe(true);
    expect(w.agent!.credits).toBe(START_CREDITS + 20_000);
  });

  it('rejects a double-accept with 4501 (no second payment)', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    acceptContract(w, c.id);
    const err = catchError(() => acceptContract(w, c.id));
    expect(err).toBeInstanceOf(ContractError);
    expect(err.code).toBe(4501);
    expect(w.agent!.credits).toBe(START_CREDITS + 20_000); // unchanged
  });
});

describe('deliverToContract — cargo + units', () => {
  it('moves cargo out of the hold and into unitsFulfilled, capped at unitsRequired', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    acceptContract(w, c.id);
    giveCargo(w, CMD, 'IRON_ORE', 100);

    const after = deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 100 });
    expect(after.terms.deliver[0].unitsFulfilled).toBe(60);  // capped at unitsRequired
    expect(cargoUnits(w, CMD, 'IRON_ORE')).toBe(40);         // only the delivered 60 left the hold
    expect(w.ships.get(CMD)!.cargo.units).toBe(40);
  });

  it('accumulates across partial deliveries and empties the hold slot when it hits zero', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    acceptContract(w, c.id);
    giveCargo(w, CMD, 'IRON_ORE', 60);

    deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 40 });
    expect(w.contracts.get(c.id)!.terms.deliver[0].unitsFulfilled).toBe(40);
    expect(cargoUnits(w, CMD, 'IRON_ORE')).toBe(20);

    deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 40 }); // only 20 remain
    expect(w.contracts.get(c.id)!.terms.deliver[0].unitsFulfilled).toBe(60);
    expect(cargoUnits(w, CMD, 'IRON_ORE')).toBe(0);
    expect(w.ships.get(CMD)!.cargo.inventory.find((i) => i.symbol === 'IRON_ORE')).toBeUndefined();
  });

  it('rejects delivering to an unaccepted contract (4505) and an off-terms good (4508)', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    giveCargo(w, CMD, 'IRON_ORE', 60);
    expect(catchError(() => deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 10 })).code).toBe(4505);

    acceptContract(w, c.id);
    expect(catchError(() => deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'COPPER_ORE', units: 10 })).code).toBe(4508);
  });
});

describe('fulfillContract — gating + payment', () => {
  it('refuses (4502) until every deliverable is met, then pays onFulfilled and clears activeContractId', () => {
    const w = seededWorld();
    const c = negotiateContract(w, { shipSymbol: CMD });
    acceptContract(w, c.id);
    giveCargo(w, CMD, 'IRON_ORE', 60);

    deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 30 }); // 30/60 — short
    const gated = catchError(() => fulfillContract(w, c.id));
    expect(gated).toBeInstanceOf(ContractError);
    expect(gated.code).toBe(4502);
    expect(w.contracts.get(c.id)!.fulfilled).toBe(false);
    expect(w.activeContractId).toBe(c.id);

    deliverToContract(w, { id: c.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 30 }); // now 60/60
    const creditsBefore = w.agent!.credits;
    const done = fulfillContract(w, c.id);
    expect(done.fulfilled).toBe(true);
    expect(w.agent!.credits).toBe(creditsBefore + 80_000);
    expect(w.activeContractId).toBeNull();
  });
});

describe('negotiateContract — ONE-ACTIVE guard (4511)', () => {
  it('blocks a second negotiation while one is active, and allows a new one after fulfillment', () => {
    const w = seededWorld();
    const c1 = negotiateContract(w, { shipSymbol: CMD });

    const err = catchError(() => negotiateContract(w, { shipSymbol: CMD }));
    expect(err).toBeInstanceOf(ContractError);
    expect(err.code).toBe(4511);
    expect(err.data).toEqual({ contractId: c1.id });
    expect(w.activeContractId).toBe(c1.id); // unchanged, no second contract created
    expect(w.contracts.size).toBe(1);

    // fulfill c1, then a fresh negotiation succeeds with a distinct id
    acceptContract(w, c1.id);
    giveCargo(w, CMD, 'IRON_ORE', 60);
    deliverToContract(w, { id: c1.id, shipSymbol: CMD, tradeSymbol: 'IRON_ORE', units: 60 });
    fulfillContract(w, c1.id);

    const c2 = negotiateContract(w, { shipSymbol: CMD });
    expect(c2.id).not.toBe(c1.id);
    expect(w.activeContractId).toBe(c2.id);
    expect(w.contracts.size).toBe(2);
  });
});

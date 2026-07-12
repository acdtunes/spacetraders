// twin/src/world/contracts.ts — the CONTRACT entity serializer + world state machine.
//
// serializeContract emits the CANONICAL SpaceTraders contract object (field-for-field the shape
// the Go client decodes, client.go parseContractData:1295 — deliverables key "deliver", a single
// terms.deadline). The state-machine helpers (negotiate/accept/deliver/fulfill) mutate the world's
// contracts map + activeContractId and move credits/cargo; the /v2 contract routes consume them and
// map any thrown ContractError onto the error envelope (errors.ts sendContractError).
//
// Fidelity anchors (api-fidelity-spec st-drm.3): ONE-active-max (negotiate -> 4511); fulfill only
// once every deliverable line is met; accept pays onAccepted, fulfill pays onFulfilled. Location /
// docked-at-destination checks (4510) are the ROUTE's concern — the world helpers are pure.
import type { Contract, Ship, World } from './types.js';
import { getNow } from '../clock.js';
import {
  ContractError,
  ERR_AGENT_HAS_CONTRACT,
  ERR_CONTRACT_ACCEPT_CONFLICT,
  ERR_CONTRACT_ALREADY_FULFILLED,
  ERR_CONTRACT_DELIVER_TERMS,
  ERR_CONTRACT_DELIVERY_NOT_MET,
  ERR_CONTRACT_NOT_ACCEPTED,
} from '../errors.js';

/** The deterministic PROCUREMENT contract fixture every negotiation mints. The deliverable good
 *  (IRON_ORE) is buyable in the seeded X1-PZ28 markets (e.g. X1-PZ28-C42); delivery is to the
 *  home-system HQ waypoint (present in the captured topology). Payment and the unit target are
 *  fixed, and the deadline is derived from the WORLD clock at negotiate time, so the whole state
 *  machine is fully deterministic under the frozen harness clock. */
const FIXTURE = {
  factionSymbol: 'COSMIC',
  type: 'PROCUREMENT',
  tradeSymbol: 'IRON_ORE',
  destinationSymbol: 'X1-PZ28-A1',
  unitsRequired: 60,
  onAccepted: 20_000,
  onFulfilled: 80_000,
  deadlineMs: 7 * 24 * 60 * 60 * 1000, // 7 days after negotiate (world clock)
} as const;

/** Return the CANONICAL contract object — the exact shape parseContractData decodes. Deep-copied so
 *  a caller mutating the wire object never corrupts stored world state. The deliverables key is
 *  "deliver" (NOT "deliveries") and terms carries a single `deadline`. */
export function serializeContract(c: Contract): Contract {
  return {
    id: c.id,
    factionSymbol: c.factionSymbol,
    type: c.type,
    accepted: c.accepted,
    fulfilled: c.fulfilled,
    terms: {
      deadline: c.terms.deadline,
      payment: { onAccepted: c.terms.payment.onAccepted, onFulfilled: c.terms.payment.onFulfilled },
      deliver: c.terms.deliver.map((d) => ({
        tradeSymbol: d.tradeSymbol,
        destinationSymbol: d.destinationSymbol,
        unitsRequired: d.unitsRequired,
        unitsFulfilled: d.unitsFulfilled,
      })),
    },
  };
}

/** Negotiate a new contract from the deterministic fixture, store it, and mark it active. ONE-ACTIVE
 *  guard: an active, not-yet-fulfilled contract makes this throw 4511 (data.contractId = the active
 *  id) so the route can surface the existing contract. `shipSymbol` is accepted for real-API symmetry
 *  (negotiate is POST /my/ships/{ship}/negotiate/contract) but the fixture is ship-independent; the
 *  route validates the ship is docked (4214/4244) before calling this. */
export function negotiateContract(world: World, _opts: { shipSymbol: string }): Contract {
  const activeId = world.activeContractId;
  if (activeId !== null) {
    const active = world.contracts.get(activeId);
    if (active && !active.fulfilled) {
      throw new ContractError(ERR_AGENT_HAS_CONTRACT, 'Agent already has an active contract.', {
        data: { contractId: active.id },
      });
    }
  }

  const id = `contract-${world.contracts.size + 1}`;
  const deadline = new Date(getNow().getTime() + FIXTURE.deadlineMs).toISOString();
  const contract: Contract = {
    id,
    factionSymbol: FIXTURE.factionSymbol,
    type: FIXTURE.type,
    accepted: false,
    fulfilled: false,
    terms: {
      deadline,
      payment: { onAccepted: FIXTURE.onAccepted, onFulfilled: FIXTURE.onFulfilled },
      deliver: [{
        tradeSymbol: FIXTURE.tradeSymbol,
        destinationSymbol: FIXTURE.destinationSymbol,
        unitsRequired: FIXTURE.unitsRequired,
        unitsFulfilled: 0,
      }],
    },
  };

  world.contracts.set(id, contract);
  world.activeContractId = id;
  return contract;
}

/** Accept a contract: flip accepted true and pay onAccepted into the treasury. Rejects a re-accept
 *  (4501) so the payment is credited exactly once; rejects accepting a fulfilled contract (4504). */
export function acceptContract(world: World, id: string): Contract {
  const contract = requireContract(world, id);
  if (contract.fulfilled) {
    throw new ContractError(ERR_CONTRACT_ALREADY_FULFILLED, `Contract ${id} is already fulfilled.`);
  }
  if (contract.accepted) {
    throw new ContractError(ERR_CONTRACT_ACCEPT_CONFLICT, `Contract ${id} is already accepted.`);
  }
  contract.accepted = true;
  if (world.agent) world.agent.credits += contract.terms.payment.onAccepted;
  return contract;
}

/** Deliver cargo toward a contract: move up to `units` of `tradeSymbol` out of the ship's hold and
 *  into the matching deliverable's unitsFulfilled, clamped by both the units still in the hold and
 *  the units still required (so unitsFulfilled never exceeds unitsRequired). Requires the contract
 *  accepted (4505) and not fulfilled (4504), and the good to be on the terms (4508). */
export function deliverToContract(
  world: World,
  args: { id: string; shipSymbol: string; tradeSymbol: string; units: number },
): Contract {
  const contract = requireContract(world, args.id);
  if (contract.fulfilled) {
    throw new ContractError(ERR_CONTRACT_ALREADY_FULFILLED, `Contract ${args.id} is already fulfilled.`);
  }
  if (!contract.accepted) {
    throw new ContractError(ERR_CONTRACT_NOT_ACCEPTED, `Contract ${args.id} has not been accepted.`);
  }
  const deliverable = contract.terms.deliver.find((d) => d.tradeSymbol === args.tradeSymbol);
  if (!deliverable) {
    throw new ContractError(ERR_CONTRACT_DELIVER_TERMS, `Contract ${args.id} does not require ${args.tradeSymbol}.`);
  }
  const ship = world.ships.get(args.shipSymbol);
  if (!ship) {
    throw new ContractError(404, `Ship ${args.shipSymbol} not found.`, { httpStatus: 404 });
  }

  const remaining = deliverable.unitsRequired - deliverable.unitsFulfilled;
  const held = cargoUnitsOf(ship, args.tradeSymbol);
  const requested = Math.max(0, Math.trunc(args.units));
  const moved = Math.min(requested, held, remaining);
  if (moved > 0) {
    decrementCargo(ship, args.tradeSymbol, moved);
    deliverable.unitsFulfilled += moved;
  }
  return contract;
}

/** Fulfill a contract: only once EVERY deliverable line is met (else 4502). Pays onFulfilled into the
 *  treasury, flips fulfilled true, and clears activeContractId (freeing the next negotiation).
 *  Requires the contract accepted (4505) and not already fulfilled (4504). */
export function fulfillContract(world: World, id: string): Contract {
  const contract = requireContract(world, id);
  if (contract.fulfilled) {
    throw new ContractError(ERR_CONTRACT_ALREADY_FULFILLED, `Contract ${id} is already fulfilled.`);
  }
  if (!contract.accepted) {
    throw new ContractError(ERR_CONTRACT_NOT_ACCEPTED, `Contract ${id} has not been accepted.`);
  }
  const unmet = contract.terms.deliver.some((d) => d.unitsFulfilled < d.unitsRequired);
  if (unmet) {
    throw new ContractError(ERR_CONTRACT_DELIVERY_NOT_MET, `Contract ${id} deliverables are not yet met.`);
  }
  contract.fulfilled = true;
  if (world.agent) world.agent.credits += contract.terms.payment.onFulfilled;
  if (world.activeContractId === id) world.activeContractId = null;
  return contract;
}

// ─── internals ───────────────────────────────────────────────────────────────────
function requireContract(world: World, id: string): Contract {
  const contract = world.contracts.get(id);
  if (!contract) throw new ContractError(404, `Contract ${id} not found.`, { httpStatus: 404 });
  return contract;
}

function cargoUnitsOf(ship: Ship, symbol: string): number {
  const item = ship.cargo.inventory.find((i) => i.symbol === symbol);
  return item ? item.units : 0;
}

/** Remove `units` of `symbol` from the hold, drop the slot when it hits zero, and keep cargo.units
 *  (the total) consistent. */
function decrementCargo(ship: Ship, symbol: string, units: number): void {
  const item = ship.cargo.inventory.find((i) => i.symbol === symbol);
  if (!item) return;
  item.units -= units;
  ship.cargo.units = Math.max(0, ship.cargo.units - units);
  if (item.units <= 0) {
    ship.cargo.inventory = ship.cargo.inventory.filter((i) => i.symbol !== symbol);
  }
}

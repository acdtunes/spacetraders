// twin/src/routes/contracts.ts — the INCOME-phase contract /v2 surface. Thin HTTP wiring over the
// world state machine (world/contracts.ts): each handler validates the request, calls the matching
// helper, and maps any thrown ContractError onto the SpaceTraders error envelope (sendContractError).
//
// Response shapes are field-for-field the real API (what the Go client decodes):
//   POST /my/ships/:s/negotiate/contract -> { data: { contract } }
//   GET  /my/contracts/:id               -> { data: <contract> }           (the contract IS data)
//   POST /my/contracts/:id/accept        -> { data: { agent, contract } }
//   POST /my/contracts/:id/deliver       -> { data: { contract, cargo } }  (ship cargo after delivery)
//   POST /my/contracts/:id/fulfill       -> { data: { agent, contract } }
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { badRequest, notFound, sendContractError, sendError, ContractError, ERR_CONTRACT_DELIVER_INVALID_LOC } from '../errors.js';
import {
  serializeContract,
  negotiateContract,
  acceptContract,
  deliverToContract,
  fulfillContract,
} from '../world/contracts.js';
import { serializeAgent, serializeCargo } from '../world/serialize.js';
import { authFailed } from './auth.js';
import { settleArrival } from './ships.js';

export async function contractRoutes(app: FastifyInstance): Promise<void> {
  // POST /my/ships/:symbol/negotiate/contract — mint a contract from the deterministic fixture. The
  // ONE-ACTIVE guard surfaces as 4511 (data.contractId) when an unfulfilled contract already exists.
  app.post('/my/ships/:symbol/negotiate/contract', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    if (!world.ships.has(symbol)) return notFound(reply, `Ship ${symbol} not found.`);
    try {
      const contract = negotiateContract(world, { shipSymbol: symbol });
      return reply.code(201).send({ data: { contract: serializeContract(contract) } });
    } catch (err) {
      if (err instanceof ContractError) return sendContractError(reply, err);
      throw err;
    }
  });

  // GET /my/contracts/:id — the contract itself is the `data` payload; 404 for an unknown id.
  app.get('/my/contracts/:id', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { id } = request.params as { id: string };
    const contract = world.contracts.get(id);
    if (!contract) return notFound(reply, `Contract ${id} not found.`);
    return reply.send({ data: serializeContract(contract) });
  });

  // POST /my/contracts/:id/accept — flip accepted, pay onAccepted into the treasury.
  app.post('/my/contracts/:id/accept', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { id } = request.params as { id: string };
    try {
      const contract = acceptContract(world, id);
      return reply.send({ data: { agent: serializeAgent(world), contract: serializeContract(contract) } });
    } catch (err) {
      if (err instanceof ContractError) return sendContractError(reply, err);
      throw err;
    }
  });

  // POST /my/contracts/:id/deliver — move cargo from the ship into the deliverable. Requires the ship
  // DOCKED at the deliverable's destinationSymbol (4510). Returns the updated contract + ship cargo.
  app.post('/my/contracts/:id/deliver', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { id } = request.params as { id: string };
    const body = (request.body ?? {}) as { shipSymbol?: unknown; tradeSymbol?: unknown; units?: unknown };
    const shipSymbol = typeof body.shipSymbol === 'string' ? body.shipSymbol.trim() : '';
    const tradeSymbol = typeof body.tradeSymbol === 'string' ? body.tradeSymbol.trim() : '';
    const units = Number(body.units);
    if (shipSymbol === '') return badRequest(reply, 'shipSymbol is required.');
    if (tradeSymbol === '') return badRequest(reply, 'tradeSymbol is required.');
    if (!Number.isFinite(units) || units <= 0) return badRequest(reply, 'units must be a positive number.');

    const ship = world.ships.get(shipSymbol);
    if (!ship) return notFound(reply, `Ship ${shipSymbol} not found.`);
    settleArrival(world, ship, shipSymbol);

    const contract = world.contracts.get(id);
    // Location gate (real-API 4510): the ship must be docked at the deliverable's destination. Only
    // enforced when the contract + deliverable are known; unknown-contract falls through to the helper's 404.
    const deliverable = contract?.terms.deliver.find((d) => d.tradeSymbol === tradeSymbol);
    if (deliverable) {
      const atDest = ship.nav.status === 'DOCKED' && ship.nav.waypointSymbol === deliverable.destinationSymbol;
      if (!atDest) {
        return sendError(reply, 400, ERR_CONTRACT_DELIVER_INVALID_LOC,
          `Ship ${shipSymbol} must be docked at ${deliverable.destinationSymbol} to deliver.`);
      }
    }

    try {
      const updated = deliverToContract(world, { id, shipSymbol, tradeSymbol, units });
      return reply.send({ data: { contract: serializeContract(updated), cargo: serializeCargo(ship) } });
    } catch (err) {
      if (err instanceof ContractError) return sendContractError(reply, err);
      throw err;
    }
  });

  // POST /my/contracts/:id/fulfill — only once every deliverable is met (4502); pays onFulfilled and
  // clears the active-contract slot (freeing the next negotiation).
  app.post('/my/contracts/:id/fulfill', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { id } = request.params as { id: string };
    try {
      const contract = fulfillContract(world, id);
      return reply.send({ data: { agent: serializeAgent(world), contract: serializeContract(contract) } });
    } catch (err) {
      if (err instanceof ContractError) return sendContractError(reply, err);
      throw err;
    }
  });
}

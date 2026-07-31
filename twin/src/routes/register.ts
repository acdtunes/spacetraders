import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { mintToken, registerAgent } from '../world/loader.js';
import { buildStartingContract, serializeContract } from '../world/contracts.js';
import { buildFaction, serializeAgent, serializeShip } from '../world/serialize.js';
import { badRequest, unauthorized } from '../errors.js';

/** POST /v2/register — mint the cold-start agent. Request { symbol, faction } + Bearer
 *  <accountToken> (not validated; any non-empty passes). Reply 201 with the spec's register data
 *  { token, agent, faction, contract, ship, ships } (the spec requires singular `ship` in addition
 *  to the `ships` array; both are served for conformance). */
export async function registerRoutes(app: FastifyInstance): Promise<void> {
  app.post('/register', async (req, reply) => {
    const auth = (req.headers.authorization ?? '').trim();
    if (auth === '') return unauthorized(reply, 'Account token required.');
    const body = (req.body ?? {}) as { symbol?: string; faction?: string };
    if (!body.symbol || body.symbol.trim() === '') return badRequest(reply, 'symbol is required.');
    const faction = body.faction && body.faction.trim() !== '' ? body.faction : 'COSMIC';
    const world = getWorld();
    const token = mintToken(body.symbol);
    const { ships } = registerAgent(world, { symbol: body.symbol, faction, token });
    const serializedShips = ships.map((s) => serializeShip(world, s));
    return reply.code(201).send({
      data: {
        token,
        agent: serializeAgent(world),
        faction: buildFaction(faction),
        contract: serializeContract(buildStartingContract(world)),
        ship: serializedShips[0],
        ships: serializedShips,
      },
    });
  });
}

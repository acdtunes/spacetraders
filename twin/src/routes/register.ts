import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { mintToken, registerAgent } from '../world/loader.js';
import { badRequest, unauthorized } from '../errors.js';

/** POST /v2/register — mint the cold-start agent. Request { symbol, faction } + Bearer
 *  <accountToken> (not validated; any non-empty passes). Reply 201 { data: { token, agent, ships } }. */
export async function registerRoutes(app: FastifyInstance): Promise<void> {
  app.post('/register', async (req, reply) => {
    const auth = (req.headers.authorization ?? '').trim();
    if (auth === '') return unauthorized(reply, 'Account token required.');
    const body = (req.body ?? {}) as { symbol?: string; faction?: string };
    if (!body.symbol || body.symbol.trim() === '') return badRequest(reply, 'symbol is required.');
    const faction = body.faction && body.faction.trim() !== '' ? body.faction : 'COSMIC';
    const world = getWorld();
    const token = mintToken(body.symbol);
    const { agent, ships } = registerAgent(world, { symbol: body.symbol, faction, token });
    return reply.code(201).send({ data: { token, agent, ships } });
  });
}

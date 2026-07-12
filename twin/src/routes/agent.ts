import type { FastifyInstance, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { serializeAgent } from '../world/serialize.js';
import { unauthorized } from '../errors.js';

function bearerToken(request: FastifyRequest): string | null {
  const header = request.headers.authorization;
  if (typeof header !== 'string') return null;
  const match = /^Bearer\s+(.+)$/.exec(header);
  return match ? match[1] : null;
}

/** GET /v2/my/agent — { data: Agent }. Bearer token must equal world.agentToken. */
export async function agentRoutes(app: FastifyInstance): Promise<void> {
  app.get('/my/agent', async (request, reply) => {
    const world = getWorld();
    const token = bearerToken(request);
    if (world.agentToken === null || token !== world.agentToken) {
      return unauthorized(reply, 'Missing or invalid agent token.');
    }
    return reply.status(200).send({ data: serializeAgent(world) });
  });
}

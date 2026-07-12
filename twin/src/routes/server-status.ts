import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';

/** GET /v2/ — UNWRAPPED server status (server_status.go:19-25). Exemplar of the
 *  route-registration pattern every endpoint task copies. */
export async function serverStatusRoutes(app: FastifyInstance): Promise<void> {
  app.get('/', async () => getWorld().serverStatus);
}

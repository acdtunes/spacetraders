import Fastify, { type FastifyInstance } from 'fastify';
import type { World } from './world/types.js';
import { setWorld } from './world/store.js';
import { consumeFault } from './world/faults.js';
import { sendError } from './errors.js';
import { serverStatusRoutes } from './routes/server-status.js';
import { registerRoutes } from './routes/register.js';
import { agentRoutes } from './routes/agent.js';
import { shipRoutes } from './routes/ships.js';
import { waypointRoutes } from './routes/waypoints.js';
import { marketRoutes } from './routes/market.js';
import { shipyardRoutes } from './routes/shipyard.js';
import { adminRoutes } from './routes/admin.js';

export interface BuildServerOptions { world?: World }

/** request.url minus the /v2 prefix and any query string: "/v2/my/ships?page=2" -> "/my/ships".
 *  Matches the "METHOD /path" shape POST /_twin/fault arms (path relative to /v2). */
function pathWithinV2(url: string): string {
  const noQuery = url.split('?')[0] ?? url;
  const stripped = noQuery.replace(/^\/v2(?=\/|$)/, '');
  return stripped === '' ? '/' : stripped;
}

/** Compose the twin: the /v2 SpaceTraders API surface + the /_twin admin namespace.
 *  Every endpoint task adds its `await xxxRoutes(v2)` line in the marked block below. */
export function buildServer(opts: BuildServerOptions = {}): FastifyInstance {
  if (opts.world) setWorld(opts.world);

  const app = Fastify({ logger: false, ignoreTrailingSlash: true });

  app.register(
    async (v2) => {
      // POST /_twin/fault arms this: checked on EVERY /v2 request, ahead of every route
      // handler below. /_twin is a separate top-level registration, so it is never faulted.
      v2.addHook('preHandler', async (request, reply) => {
        const path = pathWithinV2(request.url);
        const code = consumeFault(request.method, path);
        if (code !== null) return sendError(reply, code, code, `Injected fault: ${request.method} ${path}`);
      });

      await serverStatusRoutes(v2);
      // ─── endpoint tasks register their /v2 route plugins here ─────────────
      await registerRoutes(v2);          // Task 17  POST /register
      await agentRoutes(v2);             // Task 18  GET /my/agent
      await shipRoutes(v2);              // Task 20  GET /my/ships[/:s]
      await waypointRoutes(v2);          // Task 21  GET /systems/:s/waypoints[/:w]
      await marketRoutes(v2);            // Task 22  GET …/market
      await shipyardRoutes(v2);          // Task 23  GET …/shipyard
      // await shipNavigateRoutes(v2);   // Task 24  POST …/navigate
      // await shipActionRoutes(v2);     // Task 25  POST …/orbit|dock|refuel
      // await myShipsPurchaseRoutes(v2);// Task 27  POST /my/ships
    },
    { prefix: '/v2' },
  );

  // /_twin admin namespace (Task 15 adds adminRoutes; Task 28 adds testAdminRoutes).
  app.register(adminRoutes, { prefix: '/_twin' });
  // app.register(testAdminRoutes, { prefix: '/_twin' }); // Task 28

  return app;
}

/** Boot helper for `npm run start` / launch-test-stack.sh. */
export async function start(): Promise<FastifyInstance> {
  const app = buildServer();
  await app.listen({ port: 8080, host: '127.0.0.1' });
  return app;
}

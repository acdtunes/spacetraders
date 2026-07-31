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
import { contractRoutes } from './routes/contracts.js';
import { cargoRoutes } from './routes/cargo.js';
import { constructionRoutes } from './routes/construction.js';
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
      await contractRoutes(v2);          // INCOME   POST …/negotiate/contract; …/contracts/:id[/accept|deliver|fulfill]
      await cargoRoutes(v2);             // INCOME   POST /my/ships/:s/purchase|sell
      await constructionRoutes(v2);      // GATE     GET …/construction; POST …/construction/supply
      // navigate / orbit / dock / refuel / PATCH nav / POST /my/ships (purchase) live inside
      // shipRoutes (routes/ships.ts) — registered above; there are no separate route modules.
    },
    { prefix: '/v2' },
  );

  // /_twin admin namespace (Task 15 adds adminRoutes; Task 28 adds testAdminRoutes).
  app.register(adminRoutes, { prefix: '/_twin' });
  // app.register(testAdminRoutes, { prefix: '/_twin' }); // Task 28

  return app;
}

/** Boot helper for `npm run start` / launch-test-stack.sh.
 *  Port/host are env-overridable so the bootstrap-harness can run N twins in parallel, one per
 *  vitest worker, on disjoint ports (TWIN_PORT = 8080 + 10*workerId). Unset => the historical
 *  :8080 single-stack default, so npm run start / launch-test-stack.sh are byte-for-byte unchanged. */
export async function start(): Promise<FastifyInstance> {
  const app = buildServer();
  const port = Number(process.env.TWIN_PORT ?? process.env.PORT ?? 8080);
  const host = process.env.TWIN_HOST ?? '127.0.0.1';
  await app.listen({ port, host });
  return app;
}

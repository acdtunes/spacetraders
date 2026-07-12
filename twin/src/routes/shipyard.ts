import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

/** GET /v2/systems/:s/waypoints/:w/shipyard — { data: Shipyard } (captured verbatim);
 *  404 envelope for a waypoint with no shipyard. Keyed by waypoint symbol. */
export async function shipyardRoutes(app: FastifyInstance): Promise<void> {
  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol/shipyard',
    async (req, reply) => {
      const { waypointSymbol } = req.params;
      const shipyard = getWorld().shipyards.get(waypointSymbol);
      if (!shipyard) return notFound(reply, `Shipyard not found at waypoint ${waypointSymbol}.`);
      return reply.send({ data: shipyard });
    },
  );
}

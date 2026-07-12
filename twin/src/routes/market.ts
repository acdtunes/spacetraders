import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

/** GET /v2/systems/:s/waypoints/:w/market — { data: Market }. The world's Market
 *  already partitions goods into exports/imports/exchange (captured verbatim); the
 *  twin returns it unchanged. Markets are keyed by waypoint symbol (globally unique). */
export async function marketRoutes(app: FastifyInstance): Promise<void> {
  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol/market',
    async (request, reply) => {
      const { waypointSymbol } = request.params;
      const market = getWorld().markets.get(waypointSymbol);
      if (!market) return notFound(reply, `Market not found at waypoint ${waypointSymbol}.`);
      return reply.send({ data: market });
    },
  );
}

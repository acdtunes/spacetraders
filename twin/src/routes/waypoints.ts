import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { notFound } from '../errors.js';

function clampInt(raw: string | undefined, def: number, min: number, max: number): number {
  const n = raw === undefined ? Number.NaN : Number.parseInt(raw, 10);
  if (!Number.isFinite(n)) return def;
  return Math.min(Math.max(n, min), max);
}
const DEFAULT_LIMIT = 10;
const MAX_LIMIT = 20;

export async function waypointRoutes(app: FastifyInstance): Promise<void> {
  app.get<{ Params: { systemSymbol: string }; Querystring: { page?: string; limit?: string } }>(
    '/systems/:systemSymbol/waypoints',
    async (req, reply) => {
      const { systemSymbol } = req.params;
      const system = getWorld().systems.get(systemSymbol);
      if (!system) return notFound(reply, `System ${systemSymbol} not found.`);
      const limit = clampInt(req.query.limit, DEFAULT_LIMIT, 1, MAX_LIMIT);
      const page = clampInt(req.query.page, 1, 1, Number.MAX_SAFE_INTEGER);
      const all = [...system.waypoints.values()].sort((a, b) => (a.symbol < b.symbol ? -1 : a.symbol > b.symbol ? 1 : 0));
      const start = (page - 1) * limit;
      return reply.send({ data: all.slice(start, start + limit), meta: { total: all.length, page, limit } });
    },
  );

  app.get<{ Params: { systemSymbol: string; waypointSymbol: string } }>(
    '/systems/:systemSymbol/waypoints/:waypointSymbol',
    async (req, reply) => {
      const { systemSymbol, waypointSymbol } = req.params;
      const waypoint = getWorld().systems.get(systemSymbol)?.waypoints.get(waypointSymbol);
      if (!waypoint) return notFound(reply, `Waypoint ${waypointSymbol} not found.`);
      return reply.send({ data: waypoint });
    },
  );
}

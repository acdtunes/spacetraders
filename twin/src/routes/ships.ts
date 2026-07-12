import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { getNow, resolveNav } from '../clock.js';
import { notFound, unauthorized } from '../errors.js';
import type { Ship } from '../world/types.js';

const DEFAULT_LIMIT = 20;
const MAX_LIMIT = 20;

function authFailed(request: FastifyRequest, reply: FastifyReply): boolean {
  const world = getWorld();
  const header = request.headers.authorization;
  const token = typeof header === 'string' && header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
  if (!world.agentToken || token !== world.agentToken) { unauthorized(reply, 'Invalid or missing agent token.'); return true; }
  return false;
}
function intParam(raw: unknown, def: number, min: number, max: number): number {
  const n = Number.parseInt(typeof raw === 'string' ? raw : '', 10);
  if (!Number.isFinite(n) || n < min) return def;
  return n > max ? max : n;
}

export async function shipRoutes(app: FastifyInstance): Promise<void> {
  // GET /my/ships?page&limit — paginated; a page past the end returns { data: [], meta } HTTP 200.
  app.get('/my/ships', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = getNow();
    const q = request.query as Record<string, unknown>;
    const page = intParam(q.page, 1, 1, Number.MAX_SAFE_INTEGER);
    const limit = intParam(q.limit, DEFAULT_LIMIT, 1, MAX_LIMIT);
    const all: Ship[] = [...world.ships.values()]
      .sort((a, b) => a.symbol.localeCompare(b.symbol))
      .map((s) => resolveNav(s, world.transits.get(s.symbol), now));
    const start = (page - 1) * limit;
    const data = all.slice(start, start + limit);
    return reply.send({ data, meta: { total: all.length, page, limit } });
  });

  // GET /my/ships/:symbol — single ship with on-read arrival flip; 404 for unknown symbols.
  app.get('/my/ships/:symbol', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = getNow();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    return reply.send({ data: resolveNav(ship, world.transits.get(symbol), now) });
  });
}

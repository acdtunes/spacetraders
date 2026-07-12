import type { FastifyInstance } from 'fastify';
import type { Agent, Market, Shipyard, Ship, TransitState } from '../world/types.js';
import { getWorld, resetWorld } from '../world/store.js';
import { getCompression, setCompression, resolveNav } from '../clock.js';
import { badRequest } from '../errors.js';

export interface TwinStateSummary { agent: Agent | null; shipCount: number; compression: number }

export interface TwinState {
  compression: number;
  agent: Agent | null;
  ships: Ship[];               // nav ALREADY passed through resolveNav
  transits: TransitState[];    // in-flight only
  markets: Record<string, Market>;
  shipyards: Record<string, Shipyard>;
  waypointCount: number;
  now: string;
}

function summarize(): TwinStateSummary {
  const w = getWorld();
  return { agent: w.agent, shipCount: w.ships.size, compression: getCompression() };
}

export async function adminRoutes(app: FastifyInstance): Promise<void> {
  app.post('/reset', async () => {
    resetWorld();
    return { ok: true, world: summarize() };
  });

  app.get('/state', async () => {
    const w = getWorld();
    const now = new Date();
    const ships = [...w.ships.values()].map((ship) => resolveNav(ship, w.transits.get(ship.symbol), now));
    const transits = [...w.transits.values()].filter((t) => new Date(t.arrival) > now);
    let waypointCount = 0;
    for (const sys of w.systems.values()) waypointCount += sys.waypoints.size;
    const state: TwinState = {
      compression: getCompression(),
      agent: w.agent, ships, transits,
      markets: Object.fromEntries(w.markets),
      shipyards: Object.fromEntries(w.shipyards),
      waypointCount, now: now.toISOString(),
    };
    return state;
  });

  app.post<{ Body: { compression?: unknown } }>('/time-compression', async (req, reply) => {
    const raw = req.body?.compression;
    const factor = typeof raw === 'number' ? raw : Number(raw);
    if (!Number.isFinite(factor) || factor <= 0) {
      return badRequest(reply, `compression must be a number > 0, got ${JSON.stringify(raw)}`);
    }
    setCompression(factor);
    return { ok: true, compression: factor };
  });
}

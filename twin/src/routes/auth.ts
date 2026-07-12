import type { FastifyReply, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { unauthorized } from '../errors.js';

/** Shared agent-token gate for the authenticated /v2 route plugins (contracts, cargo, construction).
 *  Returns true when auth FAILED (and has already sent the 401), so a handler can `if (authFailed(...))
 *  return reply;` exactly as the ships routes do. The Bearer token must equal world.agentToken; a
 *  world with no registered agent (agentToken unset) rejects every request. */
export function authFailed(request: FastifyRequest, reply: FastifyReply): boolean {
  const world = getWorld();
  const header = request.headers.authorization;
  const token =
    typeof header === 'string' && header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
  if (!world.agentToken || token !== world.agentToken) {
    unauthorized(reply, 'Invalid or missing agent token.');
    return true;
  }
  return false;
}

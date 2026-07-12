import type { FastifyReply } from 'fastify';

/** SpaceTraders error envelope — the only error JSON the twin ever emits. */
export interface ApiError {
  error: { message: string; code: number; data?: Record<string, unknown> };
}

export const ERR_AGENT_HAS_CONTRACT = 4511; // errCodeAgentHasContract — HTTP 400
export const ERR_SHIP_MUST_BE_DOCKED = 4214; // errCodeShipMustBeDocked — HTTP 400
export const ERR_SHIP_NOT_DOCKED = 4244; // errCodeShipNotDocked — HTTP 400

export function apiError(code: number, message: string, data?: Record<string, unknown>): ApiError {
  const error: ApiError['error'] = { message, code };
  if (data !== undefined) error.data = data;
  return { error };
}

export function sendError(reply: FastifyReply, httpStatus: number, code: number, message: string, data?: Record<string, unknown>): FastifyReply {
  return reply.code(httpStatus).send(apiError(code, message, data));
}
export function notFound(reply: FastifyReply, message: string): FastifyReply { return sendError(reply, 404, 404, message); }
export function badRequest(reply: FastifyReply, message: string): FastifyReply { return sendError(reply, 400, 400, message); }
export function unauthorized(reply: FastifyReply, message = 'Missing or invalid agent token.'): FastifyReply { return sendError(reply, 401, 401, message); }

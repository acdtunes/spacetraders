import type { FastifyReply } from 'fastify';

/** SpaceTraders error envelope — the only error JSON the twin ever emits. */
export interface ApiError {
  error: { message: string; code: number; data?: Record<string, unknown> };
}

export const ERR_AGENT_HAS_CONTRACT = 4511; // errCodeAgentHasContract — HTTP 400 (one-active guard)
export const ERR_SHIP_MUST_BE_DOCKED = 4214; // errCodeShipMustBeDocked — HTTP 400
export const ERR_SHIP_NOT_DOCKED = 4244; // errCodeShipNotDocked — HTTP 400

// ─── Contract error codes (45xx / 4700) — all HTTP 400 (validation/state) ─────────────
export const ERR_CONTRACT_ACCEPT_CONFLICT = 4501;      // accept an already-accepted contract
export const ERR_CONTRACT_DELIVERY_NOT_MET = 4502;     // fulfill before every deliverable is met
export const ERR_CONTRACT_ACCEPT_DEADLINE = 4503;      // accept past the accept-deadline
export const ERR_CONTRACT_ALREADY_FULFILLED = 4504;    // accept/deliver/fulfill on a fulfilled contract
export const ERR_CONTRACT_NOT_ACCEPTED = 4505;         // deliver/fulfill before acceptance
export const ERR_CONTRACT_DELIVER_TERMS = 4508;        // deliver a good the contract does not require
export const ERR_CONTRACT_DELIVER_FULFILLED = 4509;    // deliver to an already-complete deliverable line
export const ERR_CONTRACT_DELIVER_INVALID_LOC = 4510;  // deliver while not docked at destinationSymbol
export const ERR_CONTRACT_NEGOTIATE_NO_FACTION = 4700; // negotiate with no owning faction

// ─── Construction error codes (48xx) — all HTTP 400 ───────────────────────────────────
export const ERR_CONSTRUCTION_MATERIAL_NOT_REQUIRED = 4800; // supply a material the site does not need
export const ERR_CONSTRUCTION_MATERIAL_FULFILLED = 4801;    // supply a material already fully supplied
export const ERR_CONSTRUCTION_INVALID_LOC = 4802;           // supply while not docked at the site

/** Thrown by the contract/construction world state machine (world/contracts.ts). The route layer
 *  catches it and maps (httpStatus, code, message, data) onto the SpaceTraders error envelope via
 *  sendContractError. Carries the real-API numeric `code` (e.g. 4511 one-active); defaults to HTTP
 *  400 (validation/state), overridable for a 404 (unknown contract/ship id). */
export class ContractError extends Error {
  readonly code: number;
  readonly httpStatus: number;
  readonly data?: Record<string, unknown>;
  constructor(code: number, message: string, opts: { httpStatus?: number; data?: Record<string, unknown> } = {}) {
    super(message);
    this.name = 'ContractError';
    this.code = code;
    this.httpStatus = opts.httpStatus ?? 400;
    if (opts.data !== undefined) this.data = opts.data;
  }
}

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

/** Map a ContractError from the world state machine onto the SpaceTraders error envelope. */
export function sendContractError(reply: FastifyReply, err: ContractError): FastifyReply {
  return sendError(reply, err.httpStatus, err.code, err.message, err.data);
}

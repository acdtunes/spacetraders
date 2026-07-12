import { describe, expect, it } from 'vitest';
import type { FastifyReply } from 'fastify';
import {
  apiError, sendError, notFound, badRequest, unauthorized,
  ERR_AGENT_HAS_CONTRACT, ERR_SHIP_MUST_BE_DOCKED, ERR_SHIP_NOT_DOCKED,
} from '../../src/errors';

function fakeReply() {
  const r = {
    statusCode: 200, payload: undefined as unknown,
    code(c: number) { r.statusCode = c; return r; },
    send(p: unknown) { r.payload = p; return r; },
  };
  return r as typeof r & FastifyReply;
}

describe('errors — SpaceTraders envelope + code constants', () => {
  it('code constants match the Go client', () => {
    expect(ERR_SHIP_MUST_BE_DOCKED).toBe(4214);
    expect(ERR_SHIP_NOT_DOCKED).toBe(4244);
    expect(ERR_AGENT_HAS_CONTRACT).toBe(4511);
  });
  it('apiError omits data when undefined', () => {
    expect(apiError(404, 'Ship X1 not found.')).toEqual({ error: { message: 'Ship X1 not found.', code: 404 } });
  });
  it('apiError includes data when provided', () => {
    expect(apiError(ERR_AGENT_HAS_CONTRACT, 'has contract', { contractId: 'c-1' })).toEqual({ error: { message: 'has contract', code: 4511, data: { contractId: 'c-1' } } });
  });
  it('sendError sets status AND envelope, returns the reply', () => {
    const reply = fakeReply();
    const ret = sendError(reply, 400, ERR_SHIP_MUST_BE_DOCKED, 'Ship must be docked.');
    expect(ret).toBe(reply); expect(reply.statusCode).toBe(400);
    expect(reply.payload).toEqual({ error: { message: 'Ship must be docked.', code: 4214 } });
  });
  it('notFound → 404/404', () => {
    const reply = fakeReply(); notFound(reply, 'Waypoint X1-PZ28-Z9 not found.');
    expect(reply.statusCode).toBe(404);
    expect(reply.payload).toEqual({ error: { message: 'Waypoint X1-PZ28-Z9 not found.', code: 404 } });
  });
  it('badRequest → 400/400', () => {
    const reply = fakeReply(); badRequest(reply, 'compression must be > 0');
    expect(reply.statusCode).toBe(400);
    expect(reply.payload).toEqual({ error: { message: 'compression must be > 0', code: 400 } });
  });
  it('unauthorized → 401 with the default message', () => {
    const reply = fakeReply(); unauthorized(reply);
    expect(reply.statusCode).toBe(401);
    expect(reply.payload).toEqual({ error: { message: 'Missing or invalid agent token.', code: 401 } });
  });
});

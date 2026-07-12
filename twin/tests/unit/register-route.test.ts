import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import { buildServer } from '../../src/server';
import { mintToken } from '../../src/world/loader';

describe('POST /v2/register (buildServer wiring)', () => {
  let app: FastifyInstance;
  beforeEach(async () => { app = buildServer(); await app.ready(); });
  afterEach(async () => { await app.close(); });

  it('mints the cold-start agent and returns { data: { token, agent, ships } }', async () => {
    const res = await app.inject({
      method: 'POST', url: '/v2/register',
      headers: { authorization: 'Bearer twin-test-account-token' },
      payload: { symbol: 'TWINAGENT', faction: 'COSMIC' },
    });
    expect(res.statusCode).toBe(201);
    const body = res.json() as { data: { token: string; agent: { symbol: string; credits: number; headquarters: string; startingFaction: string }; ships: Array<{ symbol: string; registration: { role: string } }> } };
    expect(body.data.token).toBe(mintToken('TWINAGENT'));
    expect(body.data.agent).toMatchObject({ symbol: 'TWINAGENT', credits: 175000, headquarters: 'X1-PZ28-A1', startingFaction: 'COSMIC' });
    expect(body.data.ships.map((s) => s.symbol).sort()).toEqual(['TWINAGENT-1', 'TWINAGENT-2']);
    expect(body.data.ships.map((s) => s.registration.role).sort()).toEqual(['COMMAND', 'SATELLITE']);
  });

  it('mutates the world (GET /_twin/state reports the cold-start agent + 2 ships)', async () => {
    await app.inject({ method: 'POST', url: '/v2/register', headers: { authorization: 'Bearer x' }, payload: { symbol: 'TWINAGENT', faction: 'COSMIC' } });
    const state = (await app.inject({ method: 'GET', url: '/_twin/state' })).json() as { agent: { symbol: string; credits: number } | null; ships: unknown[] };
    expect(state.agent?.symbol).toBe('TWINAGENT');
    expect(state.agent?.credits).toBe(175000);
    expect(state.ships).toHaveLength(2);
  });

  it('rejects a request with no Authorization header (401)', async () => {
    const res = await app.inject({ method: 'POST', url: '/v2/register', payload: { symbol: 'TWINAGENT', faction: 'COSMIC' } });
    expect(res.statusCode).toBe(401);
    expect((res.json() as { error: { code: number } }).error.code).toBe(401);
  });
});

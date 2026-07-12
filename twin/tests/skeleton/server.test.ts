import { afterEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';

function coldWorld(): World {
  return {
    serverStatus: { resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } },
    agent: null, agentToken: null, ships: new Map(), systems: new Map(),
    markets: new Map(), shipyards: new Map(), transits: new Map(), shipCounter: 0,
    mutationLog: [], coverage: 0, marketScouting: new Map(), scoutAssigned: false,
    haulers: [], frigateContractTagged: false, batchContractRunning: false, creditsPerHour: 0, hubs: [],
    construction: { site: '', percent: 0, started: false, adopted: false }, gateWorkers: [],
    executorRunning: false, autosizerRunning: false, standingCoordinators: { siting: false, workerRebalancer: false }, done: false,
    contracts: new Map(), activeContractId: null,
  };
}
let app: FastifyInstance;
afterEach(async () => { if (app) await app.close(); });

describe('buildServer — GET /v2/ server status (unwrapped)', () => {
  it('returns the UNWRAPPED server-status shape', async () => {
    app = buildServer({ world: coldWorld() });
    const res = await app.inject({ method: 'GET', url: '/v2/' });
    expect(res.statusCode).toBe(200);
    const body = res.json();
    expect(body).toEqual({ resetDate: '2026-07-05', serverResets: { next: '2026-08-05T00:00:00.000Z', frequency: 'monthly' } });
    expect(body).not.toHaveProperty('data');
    expect(body.resetDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });
  it('ignoreTrailingSlash: GET /v2 also resolves', async () => {
    app = buildServer({ world: coldWorld() });
    const res = await app.inject({ method: 'GET', url: '/v2' });
    expect(res.statusCode).toBe(200);
    expect(res.json().resetDate).toBe('2026-07-05');
  });
});

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// AGENT VIEW PARITY — GET /my/agent (spec Agent, incl. the required shipCount) must deep-equal the
// /_twin/state `agent` projection. The live tests/agent.test.ts asserts exactly this toEqual; it
// failed because /_twin/state served the reduced stored Agent (no shipCount) while /my/agent served
// the full serializeAgent. This replicates that assertion in-process so the parity is locked forever
// (and can be verified without the live stack). Hermetic Fastify-inject.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN }); // 2 starting hulls
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow('2026-07-11T00:00:00.000Z'); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

describe('GET /my/agent .data deep-equals /_twin/state .agent', () => {
  it('both carry the full spec Agent incl. shipCount', async () => {
    app = buildServer({ world: baseWorld() });

    const agentRes = await app.inject({ method: 'GET', url: '/v2/my/agent', headers: AUTH });
    expect(agentRes.statusCode).toBe(200);
    const agentData = (agentRes.json() as { data: Record<string, unknown> }).data;

    const stateRes = await app.inject({ method: 'GET', url: '/_twin/state' });
    expect(stateRes.statusCode).toBe(200);
    const stateAgent = (stateRes.json() as { agent: Record<string, unknown> }).agent;

    expect(stateAgent.shipCount, 'the /_twin/state agent must expose shipCount').toBe(2);
    expect(agentData, 'the two agent views must be field-for-field identical').toEqual(stateAgent);
  });
});

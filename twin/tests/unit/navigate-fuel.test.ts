import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// NAVIGATE FUEL BURN — POST /my/ships/:s/navigate must consume fuel AT DEPARTURE, faithfully to the
// real API, so a subsequent refuel has a drained tank to restore. This is the in-process teeth for
// the live-stack ship-actions "refuels TWINAGENT-1 after a voyage" scenario, whose refuel pre-check
// (fuelBefore < capacity) is dead unless navigate actually drains the tank. Hermetic Fastify-inject
// — no live stack, no daemon.
//
// Ground truth (fixtures/era2-X1-PZ28):
//   • TWINAGENT-1 = COMMAND frigate, tank 400/400, CRUISE, DOCKED at A1 (17,-21), engine speed 30.
//   • TWINAGENT-2 = SATELLITE probe, tank 0/0.
//   • A1 (17,-21) → F55 (53,-55): straight-line 49.518 → round 50 fuel in CRUISE; a flat 1 in DRIFT.
//   • A2 (17,-21) shares A1's coordinates: a 0-distance hop that must be a FREE move (0 fuel).
// ─────────────────────────────────────────────────────────────────────────────────────────────

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };

const FRIGATE = 'TWINAGENT-1';
const PROBE = 'TWINAGENT-2';
const A2 = 'X1-PZ28-A2';   // co-located with A1 (17,-21) → distance 0
const F55 = 'X1-PZ28-F55'; // (53,-55) → 49.518 from A1, rounds to 50
const FULL = 400;          // frigate tank capacity (fixture)
const CRUISE_BURN_TO_F55 = 50; // round(49.518)

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

function setFlightMode(symbol: string, flightMode: string) {
  return app.inject({ method: 'PATCH', url: `/v2/my/ships/${symbol}/nav`, headers: AUTH, payload: { flightMode } });
}
function navigate(symbol: string, waypointSymbol: string) {
  return app.inject({ method: 'POST', url: `/v2/my/ships/${symbol}/navigate`, headers: AUTH, payload: { waypointSymbol } });
}
/** The persisted tank a later GET /my/ships (the refuel pre-check's read-back path) would observe. */
async function persistedFuel(symbol: string): Promise<number> {
  const res = await app.inject({ method: 'GET', url: `/v2/my/ships/${symbol}`, headers: AUTH });
  expect(res.statusCode).toBe(200);
  return res.json().data.fuel.current as number;
}

describe('POST /v2/my/ships/:symbol/navigate — real-API fuel burn at departure', () => {
  it.each([
    { mode: 'CRUISE', dest: F55, expected: FULL - CRUISE_BURN_TO_F55, why: 'CRUISE burns round(distance)=50' },
    { mode: 'DRIFT',  dest: F55, expected: FULL - 1,                  why: 'DRIFT burns a flat 1 regardless of distance' },
    { mode: 'CRUISE', dest: A2,  expected: FULL,                      why: '0-distance co-located hop is a free move (0)' },
  ])('drains the frigate tank to $expected — $why', async ({ mode, dest, expected }) => {
    app = buildServer({ world: baseWorld() });
    expect(await persistedFuel(FRIGATE)).toBe(FULL);            // arrange: full tank
    if (mode !== 'CRUISE') expect((await setFlightMode(FRIGATE, mode)).statusCode).toBe(200);

    const res = await navigate(FRIGATE, dest);
    expect(res.statusCode).toBe(200);

    expect((res.json() as { data: { fuel: { current: number } } }).data.fuel.current)
      .toBe(expected);                                          // the navigate response reflects the drained tank
    expect(await persistedFuel(FRIGATE)).toBe(expected);        // and it persists — what refuel later restores
  });

  it('clamps the burn to the tank — a capacity-0 probe stays at 0 (never negative)', async () => {
    app = buildServer({ world: baseWorld() });
    expect(await persistedFuel(PROBE)).toBe(0);                 // arrange: empty tank, capacity 0

    expect((await navigate(PROBE, F55)).statusCode).toBe(200);

    expect(await persistedFuel(PROBE)).toBe(0);                 // burn clamped to on-board fuel — no negative tank
  });
});

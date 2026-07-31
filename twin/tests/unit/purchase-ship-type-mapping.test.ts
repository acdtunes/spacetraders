import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// PURCHASE SHIP TYPE-MAPPING — POST /my/ships must build the bought hull from the REQUESTED type's
// shipyard listing (frame/reactor/engine/fuel-capacity/crew), NOT from a one-size-fits-all frigate
// template. Regression for the live defect: buying a SHIP_PROBE debited the correct 24,680 but
// materialised a FRAME_FRIGATE hull (fuel 400/400, frigate everything) because buildPurchasedShip
// cloned the first stored hull. Both the 201 RESPONSE ship and the STORED world ship (re-GET) must
// carry the requested type's spec. Hermetic Fastify-inject — no live stack, no daemon.
//
// Ground truth (twin/fixtures/era2-X1-PZ28/shipyards.json, yard X1-PZ28-A2 — sells all three):
//   • SHIP_PROBE          → FRAME_PROBE,           fuelCapacity 0,   engine ENGINE_IMPULSE_DRIVE_I speed 3,  reactor REACTOR_SOLAR_I,   crew 0
//   • SHIP_LIGHT_HAULER   → FRAME_LIGHT_FREIGHTER, fuelCapacity 400, engine ENGINE_ION_DRIVE_I    speed 30, reactor REACTOR_FISSION_I
// The two rows share a yard but differ on frame/fuel/engine — asserting BOTH proves per-type
// mapping, not a probe special-case. The probe row also mirrors the seeded TWINAGENT-2 satellite.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const YARD = 'X1-PZ28-A2'; // shipyard that lists SHIP_PROBE + SHIP_LIGHT_HAULER + SHIP_COMMAND_FRIGATE

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  w.agent!.credits = 100_000_000; // ample funds — capital-gating is daemon-side, not the twin's
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

function buy(shipType: string) {
  return app.inject({ method: 'POST', url: '/v2/my/ships', headers: AUTH, payload: { shipType, waypointSymbol: YARD } });
}
function show(symbol: string) {
  return app.inject({ method: 'GET', url: `/v2/my/ships/${symbol}`, headers: AUTH });
}

interface WireShip {
  symbol: string;
  frame: { symbol: string };
  reactor: { symbol: string };
  engine: { speed: number };
  fuel: { current: number; capacity: number };
  crew: { required: number; capacity: number };
}

describe('POST /v2/my/ships — the bought hull matches the requested type\'s shipyard spec', () => {
  it.each([
    {
      shipType: 'SHIP_PROBE',
      frameSymbol: 'FRAME_PROBE', reactorSymbol: 'REACTOR_SOLAR_I',
      engineSpeed: 3, fuelCapacity: 0, crewCapacity: 0,
    },
    {
      shipType: 'SHIP_LIGHT_HAULER',
      frameSymbol: 'FRAME_LIGHT_FREIGHTER', reactorSymbol: 'REACTOR_FISSION_I',
      engineSpeed: 30, fuelCapacity: 400, crewCapacity: 0,
    },
  ])('$shipType materialises a $frameSymbol hull (fuel cap $fuelCapacity, engine $engineSpeed) in the response AND the store',
    async ({ shipType, frameSymbol, reactorSymbol, engineSpeed, fuelCapacity, crewCapacity }) => {
      app = buildServer({ world: baseWorld() });

      const buyRes = await buy(shipType);
      expect(buyRes.statusCode).toBe(201);
      const responseShip = (buyRes.json() as { data: { ship: WireShip } }).data.ship;

      // (1) the 201 response ship carries the requested type's spec — not a frigate clone
      expect(responseShip.frame.symbol).toBe(frameSymbol);
      expect(responseShip.reactor.symbol).toBe(reactorSymbol);
      expect(responseShip.engine.speed).toBe(engineSpeed);
      expect(responseShip.fuel.capacity).toBe(fuelCapacity);
      expect(responseShip.fuel.current).toBe(fuelCapacity); // freshly bought hull arrives with a full tank
      expect(responseShip.crew.capacity).toBe(crewCapacity);

      // (2) the STORED world ship re-GETs identically — the defect was the store keeping the frigate spec
      const showRes = await show(responseShip.symbol);
      expect(showRes.statusCode).toBe(200);
      const storedShip = (showRes.json() as { data: WireShip }).data;
      expect(storedShip.frame.symbol).toBe(frameSymbol);
      expect(storedShip.reactor.symbol).toBe(reactorSymbol);
      expect(storedShip.engine.speed).toBe(engineSpeed);
      expect(storedShip.fuel.capacity).toBe(fuelCapacity);
      expect(storedShip.fuel.current).toBe(fuelCapacity);
      expect(storedShip.crew.capacity).toBe(crewCapacity);
    });
});

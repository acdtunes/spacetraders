import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { getWorld, resetWorld, setWorld } from '../../src/world/store';
import { resetClock, setClockMode, setNow } from '../../src/clock';
import { validateResponse } from '../helpers/openapi';

// OpenAPI RESPONSE-shape conformance sweep — the twin analogue of the daemon's
// openapi_contract_test.go. Every bootstrapper endpoint below is driven through the REAL
// Fastify app via inject(); the ACTUAL captured response is validated against the vendored
// SpaceTraders 2.3.0 spec (gobot/api/openapi.json). Each case asserts BOTH the spec's status
// code AND that the response body conforms to the operation's response schema (data-wrapping,
// $ref component schemas, required fields, types, date-time formats).
//
// NOT THEATRE: we validate responses captured from inject(), never hand-authored objects, and
// the "HAS TEETH" block below proves a broken response FAILS validation — so a green sweep means
// "validated", not "the validator matches anything".

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const SHIP = 'TWINAGENT-1';
const SYS = 'X1-PZ28';
const HQ = 'X1-PZ28-A1'; // sells FUEL/ELECTRONICS/SILICON_CRYSTALS; contract delivery destination
const ORE_MARKET = 'X1-PZ28-C42'; // sells IRON_ORE; also a shipyard
const SHIPYARD_WP = 'X1-PZ28-A2';
const GATE = 'X1-PZ28-I67'; // the real JUMP_GATE waypoint — gate-entry construction site (construction.test.ts)
const GATE_MATERIAL = 'FAB_MATS'; // seeded by gate-entry: FAB_MATS 4000 + ADVANCED_CIRCUITRY 1200 (store.ts GATE_MANIFEST)

function baseWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  w.agent!.credits = 1_000_000;
  return w;
}

let app: FastifyInstance;
let world: World;
beforeEach(() => {
  resetClock();
  setNow(FROZEN_NOW);
  setClockMode('frozen');
  world = baseWorld();
  app = buildServer({ world });
});
afterEach(async () => {
  if (app) await app.close();
});

/** Assert (a) the response status == the spec's declared status, and (b) the response body
 *  conforms to the spec's response schema. Prints the ajv errors + body on failure. */
function expectConforms(method: string, template: string, status: number, res: { statusCode: number; json: () => unknown }): void {
  expect(res.statusCode, `HTTP status for ${method} ${template}`).toBe(status);
  const body = res.json();
  const result = validateResponse(method, template, String(status), body);
  if (!result.valid) {
    // eslint-disable-next-line no-console
    console.error(`SHAPE DRIFT ${method} ${template} ${status}:\n  ` + result.errors.join('\n  ') + '\n  body=' + JSON.stringify(body));
  }
  expect(result.valid, `shape ${method} ${template} ${status} :: ${result.errors.join(' | ')}`).toBe(true);
}

/** Dock a ship at a waypoint (clearing transit) and optionally load cargo. */
function placeShip(ship = SHIP, waypoint = HQ, status: 'DOCKED' | 'IN_ORBIT' = 'DOCKED', cargo: Array<{ symbol: string; units: number }> = []): void {
  const s = world.ships.get(ship)!;
  s.nav = { ...s.nav, waypointSymbol: waypoint, status, route: null };
  world.transits.delete(ship);
  s.cargo = { ...s.cargo, capacity: Math.max(s.cargo.capacity, 100), units: cargo.reduce((a, c) => a + c.units, 0), inventory: cargo.map((c) => ({ ...c })) };
}

/** Build a GATE-seeded world+app for the construction endpoints: registerAgent FIRST (so
 *  resetWorld's token-preserving reset keeps a valid bearer token for the registered agent),
 *  then resetWorld({mode:'gate-entry'}) to seed constructionMaterials (FAB_MATS 4000 +
 *  ADVANCED_CIRCUITRY 1200, store.ts GATE_MANIFEST) at the real JUMP_GATE site (mirrors
 *  construction.test.ts's beforeEach). The shared file-level `world`/`app` (from the outer
 *  beforeEach below) is cold — construction.site === '' — so construction cases need this
 *  separate LOCAL world+app instead, the same "fresh world when the shared one doesn't fit"
 *  idiom the register sweep below uses. Callers must close the returned app. */
function buildGateApp(): { world: World; app: FastifyInstance } {
  const registered = loadColdStartWorld();
  registerAgent(registered, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  setWorld(registered);
  resetWorld({ mode: 'gate-entry', gateSite: GATE });
  const gateWorld = getWorld();
  return { world: gateWorld, app: buildServer({ world: gateWorld }) };
}

// ──────────────────────────────────────────────────────────────────────────────────────
// HAS TEETH — negative controls. A real, valid response is mutated (a required field dropped)
// and MUST fail validation. If these ever pass, the validator is toothless and the sweep is a lie.
// ──────────────────────────────────────────────────────────────────────────────────────
describe('openapi helper — HAS TEETH (negative controls on REAL responses)', () => {
  it('a real Ship response with data.symbol deleted FAILS validation', async () => {
    const res = await app.inject({ method: 'GET', url: `/v2/my/ships/${SHIP}`, headers: AUTH });
    expect(res.statusCode).toBe(200);
    const body = res.json() as { data: Record<string, unknown> };
    // sanity: the pristine response is well-formed enough to have a data object
    expect(body.data).toBeTruthy();
    delete body.data.symbol; // drop a spec-REQUIRED Ship field
    const broken = validateResponse('get', '/my/ships/{shipSymbol}', '200', body);
    expect(broken.valid, 'dropping Ship.symbol must invalidate').toBe(false);
    expect(broken.errors.join(' ')).toMatch(/symbol/);
  });

  it('a real Contract response with data.expiration deleted FAILS validation', async () => {
    const id = (await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/negotiate/contract`, headers: AUTH })).json().data.contract.id;
    const res = await app.inject({ method: 'GET', url: `/v2/my/contracts/${id}`, headers: AUTH });
    expect(res.statusCode).toBe(200);
    const body = res.json() as { data: Record<string, unknown> };
    delete body.data.expiration; // drop the spec-REQUIRED (deprecated) Contract field
    const broken = validateResponse('get', '/my/contracts/{contractId}', '200', body);
    expect(broken.valid, 'dropping Contract.expiration must invalidate').toBe(false);
    expect(broken.errors.join(' ')).toMatch(/expiration/);
  });

  it('a real Construction response with data.isComplete deleted FAILS validation', async () => {
    const { app: gateApp } = buildGateApp();
    try {
      const res = await gateApp.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints/${GATE}/construction`, headers: AUTH });
      expect(res.statusCode).toBe(200);
      const body = res.json() as { data: Record<string, unknown> };
      // sanity: the pristine response is well-formed enough to have a data object
      expect(body.data).toBeTruthy();
      delete body.data.isComplete; // drop a spec-REQUIRED Construction field
      const broken = validateResponse('get', '/systems/{systemSymbol}/waypoints/{waypointSymbol}/construction', '200', body);
      expect(broken.valid, 'dropping Construction.isComplete must invalidate').toBe(false);
      expect(broken.errors.join(' ')).toMatch(/isComplete/);
    } finally {
      await gateApp.close();
    }
  });

  it('a valid hand-built minimal Agent response PASSES (positive control)', () => {
    const ok = validateResponse('get', '/my/agent', '200', {
      data: { symbol: 'TWINAGENT', headquarters: HQ, credits: 1, startingFaction: 'COSMIC', shipCount: 2 },
    });
    expect(ok.valid, ok.errors.join(' | ')).toBe(true);
  });
});

// ──────────────────────────────────────────────────────────────────────────────────────
// ENDPOINT SWEEP — real inject() responses validated against the spec.
// ──────────────────────────────────────────────────────────────────────────────────────
describe('openapi shape sweep — agent / ships (reads)', () => {
  it('GET /my/agent -> 200 {data:Agent}', async () => {
    expectConforms('get', '/my/agent', 200, await app.inject({ method: 'GET', url: '/v2/my/agent', headers: AUTH }));
  });

  it('GET /my/ships -> 200 {data:[Ship],meta}', async () => {
    expectConforms('get', '/my/ships', 200, await app.inject({ method: 'GET', url: '/v2/my/ships', headers: AUTH }));
  });

  it('GET /my/ships/{shipSymbol} -> 200 {data:Ship}', async () => {
    expectConforms('get', '/my/ships/{shipSymbol}', 200, await app.inject({ method: 'GET', url: `/v2/my/ships/${SHIP}`, headers: AUTH }));
  });
});

describe('openapi shape sweep — ship writes', () => {
  it('POST /my/ships (purchase ship) -> 201 {data:{agent,ship,transaction}}', async () => {
    const res = await app.inject({ method: 'POST', url: '/v2/my/ships', headers: AUTH, payload: { shipType: 'SHIP_PROBE', waypointSymbol: SHIPYARD_WP } });
    expectConforms('post', '/my/ships', 201, res);
  });

  it('POST /my/ships/{shipSymbol}/navigate -> 200 {data:{fuel,nav,events}}', async () => {
    placeShip(SHIP, HQ, 'IN_ORBIT');
    const res = await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/navigate`, headers: AUTH, payload: { waypointSymbol: ORE_MARKET } });
    expectConforms('post', '/my/ships/{shipSymbol}/navigate', 200, res);
  });

  it('POST /my/ships/{shipSymbol}/orbit -> 200 {data:{nav}}', async () => {
    placeShip(SHIP, HQ, 'DOCKED');
    expectConforms('post', '/my/ships/{shipSymbol}/orbit', 200, await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/orbit`, headers: AUTH }));
  });

  it('POST /my/ships/{shipSymbol}/dock -> 200 {data:{nav}}', async () => {
    placeShip(SHIP, HQ, 'IN_ORBIT');
    expectConforms('post', '/my/ships/{shipSymbol}/dock', 200, await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/dock`, headers: AUTH }));
  });

  it('POST /my/ships/{shipSymbol}/refuel -> 200 {data:{agent,fuel,transaction}}', async () => {
    placeShip(SHIP, HQ, 'DOCKED');
    world.ships.get(SHIP)!.fuel = { current: 10, capacity: 400 };
    expectConforms('post', '/my/ships/{shipSymbol}/refuel', 200, await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/refuel`, headers: AUTH }));
  });

  it('PATCH /my/ships/{shipSymbol}/nav -> 200 {data:{nav,fuel,events}}', async () => {
    placeShip(SHIP, HQ, 'DOCKED');
    const res = await app.inject({ method: 'PATCH', url: `/v2/my/ships/${SHIP}/nav`, headers: AUTH, payload: { flightMode: 'BURN' } });
    expectConforms('patch', '/my/ships/{shipSymbol}/nav', 200, res);
  });

  it('POST /my/ships/{shipSymbol}/purchase (cargo) -> 201 {data:{agent,cargo,transaction}}', async () => {
    placeShip(SHIP, ORE_MARKET, 'DOCKED');
    const res = await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/purchase`, headers: AUTH, payload: { symbol: 'IRON_ORE', units: 5 } });
    expectConforms('post', '/my/ships/{shipSymbol}/purchase', 201, res);
  });

  it('POST /my/ships/{shipSymbol}/sell (cargo) -> 201 {data:{agent,cargo,transaction}}', async () => {
    placeShip(SHIP, ORE_MARKET, 'DOCKED', [{ symbol: 'IRON_ORE', units: 5 }]);
    const res = await app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/sell`, headers: AUTH, payload: { symbol: 'IRON_ORE', units: 5 } });
    expectConforms('post', '/my/ships/{shipSymbol}/sell', 201, res);
  });
});

describe('openapi shape sweep — contracts', () => {
  const negotiate = () => app.inject({ method: 'POST', url: `/v2/my/ships/${SHIP}/negotiate/contract`, headers: AUTH });

  it('POST negotiate/contract -> 201 {data:{contract}}', async () => {
    expectConforms('post', '/my/ships/{shipSymbol}/negotiate/contract', 201, await negotiate());
  });

  it('GET /my/contracts/{contractId} -> 200 {data:Contract}', async () => {
    const id = (await negotiate()).json().data.contract.id;
    expectConforms('get', '/my/contracts/{contractId}', 200, await app.inject({ method: 'GET', url: `/v2/my/contracts/${id}`, headers: AUTH }));
  });

  it('POST /my/contracts/{contractId}/accept -> 200 {data:{agent,contract}}', async () => {
    const id = (await negotiate()).json().data.contract.id;
    expectConforms('post', '/my/contracts/{contractId}/accept', 200, await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/accept`, headers: AUTH }));
  });

  it('POST /my/contracts/{contractId}/deliver -> 200 {data:{contract,cargo}}', async () => {
    const id = (await negotiate()).json().data.contract.id;
    await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/accept`, headers: AUTH });
    placeShip(SHIP, HQ, 'DOCKED', [{ symbol: 'IRON_ORE', units: 60 }]);
    const res = await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/deliver`, headers: AUTH, payload: { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units: 60 } });
    expectConforms('post', '/my/contracts/{contractId}/deliver', 200, res);
  });

  it('POST /my/contracts/{contractId}/fulfill -> 200 {data:{agent,contract}}', async () => {
    const id = (await negotiate()).json().data.contract.id;
    await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/accept`, headers: AUTH });
    placeShip(SHIP, HQ, 'DOCKED', [{ symbol: 'IRON_ORE', units: 60 }]);
    await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/deliver`, headers: AUTH, payload: { shipSymbol: SHIP, tradeSymbol: 'IRON_ORE', units: 60 } });
    expectConforms('post', '/my/contracts/{contractId}/fulfill', 200, await app.inject({ method: 'POST', url: `/v2/my/contracts/${id}/fulfill`, headers: AUTH }));
  });
});

describe('openapi shape sweep — systems / market / shipyard', () => {
  it('GET /systems/{systemSymbol}/waypoints -> 200 {data:[Waypoint],meta}', async () => {
    expectConforms('get', '/systems/{systemSymbol}/waypoints', 200, await app.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints`, headers: AUTH }));
  });

  it('GET /systems/{systemSymbol}/waypoints/{waypointSymbol} -> 200 {data:Waypoint}', async () => {
    expectConforms('get', '/systems/{systemSymbol}/waypoints/{waypointSymbol}', 200, await app.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints/${HQ}`, headers: AUTH }));
  });

  it('GET market -> 200 {data:Market}', async () => {
    expectConforms('get', '/systems/{systemSymbol}/waypoints/{waypointSymbol}/market', 200, await app.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints/${HQ}/market`, headers: AUTH }));
  });

  it('GET shipyard -> 200 {data:Shipyard}', async () => {
    expectConforms('get', '/systems/{systemSymbol}/waypoints/{waypointSymbol}/shipyard', 200, await app.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints/${SHIPYARD_WP}/shipyard`, headers: AUTH }));
  });
});

describe('openapi shape sweep — construction (GATE)', () => {
  it('GET construction -> 200 {data:Construction}', async () => {
    const { app: gateApp } = buildGateApp();
    try {
      const res = await gateApp.inject({ method: 'GET', url: `/v2/systems/${SYS}/waypoints/${GATE}/construction`, headers: AUTH });
      expectConforms('get', '/systems/{systemSymbol}/waypoints/{waypointSymbol}/construction', 200, res);
    } finally {
      await gateApp.close();
    }
  });

  it('POST construction/supply -> 201 {data:{construction,cargo}}', async () => {
    const { world: gateWorld, app: gateApp } = buildGateApp();
    try {
      // Dock the hauler at the gate holding some of a required material (mirrors
      // construction.test.ts's dockAtGateHolding).
      const ship = gateWorld.ships.get(SHIP)!;
      ship.nav = { ...ship.nav, waypointSymbol: GATE, status: 'DOCKED', route: null };
      gateWorld.transits.delete(SHIP);
      ship.cargo = { ...ship.cargo, capacity: Math.max(ship.cargo.capacity, 10), units: 10, inventory: [{ symbol: GATE_MATERIAL, units: 10 }] };

      const res = await gateApp.inject({
        method: 'POST',
        url: `/v2/systems/${SYS}/waypoints/${GATE}/construction/supply`,
        headers: AUTH,
        payload: { shipSymbol: SHIP, tradeSymbol: GATE_MATERIAL, units: 10 },
      });
      expectConforms('post', '/systems/{systemSymbol}/waypoints/{waypointSymbol}/construction/supply', 201, res);
    } finally {
      await gateApp.close();
    }
  });
});

describe('openapi shape sweep — register', () => {
  it('POST /register -> 201 {data:{token,agent,faction,contract,ship}}', async () => {
    // register mutates the world; use a fresh cold world so we do not disturb the shared agent.
    const cold = loadColdStartWorld();
    const freshApp = buildServer({ world: cold });
    try {
      const res = await freshApp.inject({ method: 'POST', url: '/v2/register', headers: { authorization: 'Bearer account-token' }, payload: { symbol: 'NEWAGENT', faction: 'COSMIC' } });
      expectConforms('post', '/register', 201, res);
    } finally {
      await freshApp.close();
    }
  });
});

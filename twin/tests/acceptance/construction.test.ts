import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { getWorld, resetWorld, setWorld } from '../../src/world/store';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// ─────────────────────────────────────────────────────────────────────────────────────
// GATE / CONSTRUCTION acceptance — in-process (Fastify inject) proof of the SpaceTraders
// construction surface the daemon drives to BUILD THE JUMP GATE:
//   GET  /v2/systems/:s/waypoints/:w/construction          -> { data: Construction }
//   POST /v2/systems/:s/waypoints/:w/construction/supply    -> { data: { construction, cargo } }
// (shapes per gobot/api/openapi.json — Construction = { symbol, materials[{tradeSymbol,
//  required, fulfilled}], isComplete }).
//
// The user journey: a hauler DOCKED at the gate hands its cargo to the site; each delivery
// credits the site's materials and drains the ship's hold; when every material is met the
// gate flips to complete. These specs assert that OBSERVABLE EFFECT (before/after deltas on
// `fulfilled` and on the ship's cargo, and the isComplete transition) — read back through
// the endpoint itself — not response shape alone.
//
// STATUS: GREEN against the implemented construction surface. server.ts wires `constructionRoutes(v2)`
// and the gate-entry reset seeds a STATEFUL materials manifest that GET reads and supply mutates. Every
// scenario asserts OBSERVABLE EFFECTS through the /v2 driving port only — before/after `fulfilled`
// deltas, the ship's cargo drain, the isComplete transition — never internals. Business error codes are
// pinned to the twin's real contract (asserting the EXACT code, not merely "a numeric error"):
//   • 4800 material-not-required, 4801 material-already-fulfilled, 4802 not-docked (twin/src/errors.ts)
//   • 4218 insufficient-cargo — supplying more than the ship carries — shared with the cargo route's
//     over-sell guard (twin/src/routes/construction.ts:21 & cargo.ts:19).
// ─────────────────────────────────────────────────────────────────────────────────────

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';
const TOKEN = 'tok-1';
const AUTH = { authorization: `Bearer ${TOKEN}` };
const SHIP = 'TWINAGENT-1'; // the COMMAND frigate — our material hauler
const GATE = 'X1-PZ28-I67'; // the real JUMP_GATE waypoint = the construction site
const SYSTEM = 'X1-PZ28';
const OTHER_WAYPOINT = 'X1-PZ28-A1'; // a normal, NOT-under-construction waypoint

// Construction error codes the crafter implements (twin/src/errors.ts).
const ERR_MATERIAL_NOT_REQUIRED = 4800;
const ERR_MATERIAL_FULFILLED = 4801;
const ERR_INVALID_LOC = 4802;
// Insufficient-cargo: supplying more units than the ship is carrying. The construction supply route
// reuses the cargo route's over-sell code — twin/src/routes/construction.ts:21 & cargo.ts:19 both
// pin ERR_CARGO_INSUFFICIENT = 4218.
const ERR_CARGO_INSUFFICIENT = 4218;

type Material = { tradeSymbol: string; required: number; fulfilled: number };

let app: FastifyInstance;
let world: World;

beforeEach(() => {
  resetClock(); setNow(FROZEN_NOW); setClockMode('frozen');
  // Seed the GATE world via the store's gate-entry reset. Register the agent first so the reset
  // (which preserves symbol/faction/token) keeps a valid token for auth; the reset then puts the
  // world in the GATE view with construction.site = the JUMP_GATE.
  const registered = loadColdStartWorld();
  registerAgent(registered, { symbol: 'TWINAGENT', faction: 'COSMIC', token: TOKEN });
  setWorld(registered);
  resetWorld({ mode: 'gate-entry', gateSite: GATE, credits: 1_500_000 });
  world = getWorld();
  app = buildServer({ world });
});
afterEach(async () => { if (app) await app.close(); });

// ─── driving-port helpers (the only surface these specs touch) ───────────────────────
const getConstruction = (waypoint = GATE, system = SYSTEM) =>
  app.inject({ method: 'GET', url: `/v2/systems/${system}/waypoints/${waypoint}/construction`, headers: AUTH });
const supply = (payload: unknown, waypoint = GATE, system = SYSTEM) =>
  app.inject({ method: 'POST', url: `/v2/systems/${system}/waypoints/${waypoint}/construction/supply`, headers: AUTH, payload });
const getShip = (ship = SHIP) =>
  app.inject({ method: 'GET', url: `/v2/my/ships/${ship}`, headers: AUTH });

/** Read the construction site through the endpoint, asserting the GET contract up front. RED now:
 *  construction routes are unbuilt -> Fastify 404, so this fails cleanly at the first GET. */
async function siteNow(waypoint = GATE): Promise<{ symbol: string; materials: Material[]; isComplete: boolean }> {
  const res = await getConstruction(waypoint);
  expect(res.statusCode).toBe(200);
  return res.json().data;
}

/** Dock SHIP at the gate site holding exactly `units` of `tradeSymbol` — the supply precondition
 *  (world state setup, mirroring the exemplars' dockShipAt/placeShip; the ASSERTIONS all go
 *  through the /v2 surface). */
function dockAtGateHolding(tradeSymbol: string, units: number, waypoint = GATE): void {
  const ship = world.ships.get(SHIP)!;
  ship.nav = { ...ship.nav, waypointSymbol: waypoint, status: 'DOCKED', route: null };
  world.transits.delete(SHIP);
  ship.cargo = {
    ...ship.cargo,
    capacity: Math.max(ship.cargo.capacity, units),
    units,
    inventory: units > 0 ? [{ symbol: tradeSymbol, units }] : [],
  };
}

describe('GATE construction — reporting the site', () => {
  it('reports the gate site with its required materials, none of it built yet', async () => {
    // Given the gate is under construction  When the operator inspects the site
    const site = await siteNow();
    // Then it names the gate, lists the materials it still needs, and reads as not-yet-complete.
    expect(site.symbol).toBe(GATE);
    expect(site.isComplete).toBe(false);
    expect(Array.isArray(site.materials)).toBe(true);
    expect(site.materials.length).toBeGreaterThan(0);
    for (const m of site.materials) {
      expect(typeof m.tradeSymbol).toBe('string');
      expect(m.tradeSymbol.length).toBeGreaterThan(0);
      expect(m.required).toBeGreaterThan(0);
      expect(m.fulfilled).toBeGreaterThanOrEqual(0);
      expect(m.fulfilled).toBeLessThanOrEqual(m.required);
    }
  });
});

describe('GATE construction — supplying materials', () => {
  // @walking_skeleton — the thin end-to-end slice with observable user value: a hauler docked at
  // the gate hands over a required good, the site's progress rises by exactly that amount, and the
  // good leaves the ship. Demo-able to a stakeholder as "delivering materials advances the gate".
  it('a delivery of a required good raises that material’s progress and empties it from the ship', async () => {
    // Given the operator learns (from the site) a good the gate needs, and a hauler carrying some.
    const before = await siteNow();
    const target = before.materials[0];
    const units = Math.min(5, target.required - target.fulfilled);
    expect(units).toBeGreaterThan(0);
    dockAtGateHolding(target.tradeSymbol, units);

    // When the hauler supplies that good to the gate.
    const res = await supply({ shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units });
    expect([200, 201]).toContain(res.statusCode);
    const d = res.json().data;

    // Then the site credits EXACTLY `units` toward that material (teeth: fails if the supply no-op'd)…
    const credited = (d.construction.materials as Material[]).find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(credited.fulfilled).toBe(target.fulfilled + units);
    // …and the ship has handed the good over (its hold is drained by `units`).
    expect(d.cargo.units).toBe(0);

    // And the credit PERSISTED — a fresh read of the site still shows the higher progress.
    const reread = await siteNow();
    const persisted = reread.materials.find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(persisted.fulfilled).toBe(target.fulfilled + units);
  });

  // @walking_skeleton — the full user goal: keep delivering until the jump gate is BUILT.
  it('the gate stays incomplete until the final material is delivered, then flips to complete', async () => {
    const materials = (await siteNow()).materials;
    expect(materials.length).toBeGreaterThan(0);

    // Given every required material EXCEPT the last has been delivered in full.
    for (let i = 0; i < materials.length - 1; i++) {
      const m = materials[i];
      const remaining = m.required - m.fulfilled;
      if (remaining <= 0) continue;
      dockAtGateHolding(m.tradeSymbol, remaining);
      await supply({ shipSymbol: SHIP, tradeSymbol: m.tradeSymbol, units: remaining });
    }
    // Pre-transition: with one material still short, the gate is NOT yet complete.
    expect((await siteNow()).isComplete).toBe(false);

    // When the operator delivers the final outstanding material in full.
    const last = materials[materials.length - 1];
    const lastRemaining = last.required - last.fulfilled;
    expect(lastRemaining).toBeGreaterThan(0);
    dockAtGateHolding(last.tradeSymbol, lastRemaining);
    const done = (await supply({ shipSymbol: SHIP, tradeSymbol: last.tradeSymbol, units: lastRemaining })).json().data;

    // Then that completing delivery flips the gate to complete — and the flip PERSISTS.
    expect(done.construction.isComplete).toBe(true);
    expect((await siteNow()).isComplete).toBe(true);
  });
});

describe('GATE construction — rejected deliveries change nothing', () => {
  it('supplying a good the gate does not require is rejected (4800) and changes nothing', async () => {
    // Given the operator picks a good that is NOT on the gate's manifest.
    const before = await siteNow();
    const required = new Set(before.materials.map((m) => m.tradeSymbol));
    const bogus = ['PRECIOUS_STONES', 'PLATINUM', 'GOLD', 'DIAMONDS', 'NONEXISTENT_GOOD'].find((c) => !required.has(c))!;
    dockAtGateHolding(bogus, 10);

    // When a hauler tries to supply that unrequired good.
    const res = await supply({ shipSymbol: SHIP, tradeSymbol: bogus, units: 10 });

    // Then it is rejected with the "material not required" business error…
    expect(res.statusCode).toBeGreaterThanOrEqual(400);
    expect(res.json().error.code).toBe(ERR_MATERIAL_NOT_REQUIRED);
    // …the gate's manifest is untouched (teeth: nothing credited, still not complete)…
    const after = await siteNow();
    expect(after.materials).toEqual(before.materials);
    expect(after.isComplete).toBe(false);
    // …and the ship still holds all 10 units (nothing was consumed).
    const cargo = (await getShip()).json().data.cargo;
    const held = (cargo.inventory as Array<{ symbol: string; units: number }>).find((i) => i.symbol === bogus);
    expect(held?.units).toBe(10);
  });

  it('supplying while the ship is not at the gate is rejected (4802) and credits nothing', async () => {
    // Given a hauler carrying a required good but DOCKED at a different waypoint.
    const target = (await siteNow()).materials[0];
    dockAtGateHolding(target.tradeSymbol, 5, OTHER_WAYPOINT);

    // When it tries to supply the gate from afar.
    const res = await supply({ shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units: 5 });

    // Then it is rejected for the wrong location, and the gate's progress is unchanged.
    expect(res.statusCode).toBeGreaterThanOrEqual(400);
    expect(res.json().error.code).toBe(ERR_INVALID_LOC);
    const after = (await siteNow()).materials.find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(after.fulfilled).toBe(target.fulfilled);
  });

  // @property — invariant: a fully-supplied material never accepts more (fulfilled never exceeds
  // required, no double-credit). The crafter MAY realise this as a property-based test.
  it('supplying a material that is already fully met is rejected (4801) with no double-credit', async () => {
    // Given a material has been delivered in full.
    const target = (await siteNow()).materials[0];
    const remaining = target.required - target.fulfilled;
    expect(remaining).toBeGreaterThan(0);
    dockAtGateHolding(target.tradeSymbol, remaining);
    const met = (await supply({ shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units: remaining })).json().data;
    const metMaterial = (met.construction.materials as Material[]).find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(metMaterial.fulfilled).toBe(target.required);

    // When a hauler tries to supply one more unit of that already-met material.
    dockAtGateHolding(target.tradeSymbol, 1);
    const res = await supply({ shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units: 1 });

    // Then it is rejected, and the material stays at exactly `required` (no over-fulfilment).
    expect(res.statusCode).toBeGreaterThanOrEqual(400);
    expect(res.json().error.code).toBe(ERR_MATERIAL_FULFILLED);
    const after = (await siteNow()).materials.find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(after.fulfilled).toBe(target.required);
  });

  it('supplying more units than the ship is carrying is rejected as insufficient cargo (4218) and credits nothing', async () => {
    // Given a hauler holding only 3 units of a required good.
    const target = (await siteNow()).materials[0];
    dockAtGateHolding(target.tradeSymbol, 3);

    // When it claims to supply 10.
    const res = await supply({ shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units: 10 });

    // Then the delivery is rejected with the EXACT insufficient-cargo business code (not merely "some
    // numeric error" — that shape-only check passed even if the route returned an unrelated code), a
    // business error rather than a partial credit, and the site's progress is unchanged.
    expect(res.statusCode).toBeGreaterThanOrEqual(400);
    expect(res.json().error.code).toBe(ERR_CARGO_INSUFFICIENT);
    const after = (await siteNow()).materials.find((m) => m.tradeSymbol === target.tradeSymbol)!;
    expect(after.fulfilled).toBe(target.fulfilled);
  });
});

describe('GATE construction — access rules', () => {
  it('a waypoint that is not under construction has no construction site to report', async () => {
    // Given a normal waypoint  When the site is requested for it  Then there is nothing to build.
    const res = await getConstruction(OTHER_WAYPOINT);
    expect(res.statusCode).toBe(404);
    expect(typeof res.json().error.code).toBe('number'); // twin error envelope, not a missing route
  });

  it('supplying the gate without an agent token is refused', async () => {
    // Given a required good is ready to deliver  When supplied with no authentication.
    const target = (await siteNow()).materials[0];
    dockAtGateHolding(target.tradeSymbol, 1);
    const res = await app.inject({
      method: 'POST',
      url: `/v2/systems/${SYSTEM}/waypoints/${GATE}/construction/supply`,
      payload: { shipSymbol: SHIP, tradeSymbol: target.tradeSymbol, units: 1 },
    });
    // Then the delivery is refused as unauthenticated.
    expect(res.statusCode).toBe(401);
  });
});

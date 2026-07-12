// twin/src/routes/construction.ts — the GATE construction /v2 surface the daemon drives to BUILD THE
// JUMP GATE. A hauler DOCKED at the site hands its cargo over; each delivery credits the site's
// materials manifest and drains the ship's hold; the site flips complete once every material is met.
//   GET  /systems/:s/waypoints/:w/construction          -> { data: Construction }   (public read)
//   POST /systems/:s/waypoints/:w/construction/supply    -> { data: { construction, cargo } }
// Shapes per gobot/api/openapi.json (Construction = { symbol, materials[{tradeSymbol,required,fulfilled}],
// isComplete }). A waypoint is "under construction" iff it is the seeded gate site AND carries a manifest.
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import {
  badRequest, notFound, sendError,
  ERR_CONSTRUCTION_MATERIAL_NOT_REQUIRED,
  ERR_CONSTRUCTION_MATERIAL_FULFILLED,
  ERR_CONSTRUCTION_INVALID_LOC,
} from '../errors.js';
import { serializeCargo, serializeConstruction } from '../world/serialize.js';
import { authFailed } from './auth.js';
import { settleArrival } from './ships.js';
import type { ConstructionMaterial, Ship } from '../world/types.js';

const ERR_CARGO_INSUFFICIENT = 4218; // supply more units than the ship is carrying

/** The construction manifest for `waypointSymbol`, or null when that waypoint is not under
 *  construction (not the seeded gate site, or no manifest). The gate site is the discriminator. */
function siteMaterials(waypointSymbol: string): ConstructionMaterial[] | null {
  const world = getWorld();
  if (world.construction?.site !== waypointSymbol) return null;
  const materials = world.constructionMaterials;
  return materials && materials.length > 0 ? materials : null;
}

/** Drain `units` of `symbol` from the ship's hold (mirrors cargo.ts removeCargo). */
function removeCargo(ship: Ship, symbol: string, units: number): void {
  const item = ship.cargo.inventory.find((i) => i.symbol === symbol);
  if (!item) return;
  item.units -= units;
  ship.cargo.units = Math.max(0, ship.cargo.units - units);
  if (item.units <= 0) ship.cargo.inventory = ship.cargo.inventory.filter((i) => i.symbol !== symbol);
}

export async function constructionRoutes(app: FastifyInstance): Promise<void> {
  // GET …/construction — public read (spec security [{}, {AgentToken}], as the twin's waypoint reads).
  // A waypoint with no site reports a twin error-envelope 404 (numeric error.code), not a missing route.
  app.get('/systems/:systemSymbol/waypoints/:waypointSymbol/construction', async (request, reply) => {
    const { waypointSymbol } = request.params as { systemSymbol: string; waypointSymbol: string };
    const materials = siteMaterials(waypointSymbol);
    if (!materials) return notFound(reply, `Waypoint ${waypointSymbol} is not under construction.`);
    return reply.send({ data: serializeConstruction(materials, waypointSymbol) });
  });

  // POST …/construction/supply — hand ship cargo to the site (auth required). Credits the material
  // (capped at required), drains the ship's hold, and flips isComplete once every material is met.
  app.post('/systems/:systemSymbol/waypoints/:waypointSymbol/construction/supply', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { waypointSymbol } = request.params as { systemSymbol: string; waypointSymbol: string };

    const body = (request.body ?? {}) as { shipSymbol?: unknown; tradeSymbol?: unknown; units?: unknown };
    const shipSymbol = typeof body.shipSymbol === 'string' ? body.shipSymbol.trim() : '';
    const tradeSymbol = typeof body.tradeSymbol === 'string' ? body.tradeSymbol.trim() : '';
    const units = Math.trunc(Number(body.units));
    if (shipSymbol === '') return badRequest(reply, 'shipSymbol is required.');
    if (tradeSymbol === '') return badRequest(reply, 'tradeSymbol is required.');
    if (!Number.isFinite(units) || units <= 0) return badRequest(reply, 'units must be a positive integer.');

    const materials = siteMaterials(waypointSymbol);
    if (!materials) return notFound(reply, `Waypoint ${waypointSymbol} is not under construction.`);

    const ship = world.ships.get(shipSymbol);
    if (!ship) return notFound(reply, `Ship ${shipSymbol} not found.`);
    settleArrival(world, ship, shipSymbol);

    // Location gate (4802): the ship must be DOCKED at the site to hand over cargo.
    const atSite = ship.nav.status === 'DOCKED' && ship.nav.waypointSymbol === waypointSymbol;
    if (!atSite) return sendError(reply, 400, ERR_CONSTRUCTION_INVALID_LOC, `Ship ${shipSymbol} must be docked at ${waypointSymbol} to supply the construction site.`);

    // Manifest gate (4800): the good must be one the site still requires.
    const material = materials.find((m) => m.tradeSymbol === tradeSymbol);
    if (!material) return sendError(reply, 400, ERR_CONSTRUCTION_MATERIAL_NOT_REQUIRED, `${tradeSymbol} is not required by the construction at ${waypointSymbol}.`);

    // Already-met gate (4801): a fully-supplied material never accepts more (no double-credit).
    const remaining = material.required - material.fulfilled;
    if (remaining <= 0) return sendError(reply, 400, ERR_CONSTRUCTION_MATERIAL_FULFILLED, `${tradeSymbol} is already fully supplied to ${waypointSymbol}.`);

    // Cargo gate: cannot hand over more than the ship is carrying.
    const held = ship.cargo.inventory.find((i) => i.symbol === tradeSymbol)?.units ?? 0;
    if (units > held) return sendError(reply, 400, ERR_CARGO_INSUFFICIENT, `Ship ${shipSymbol} holds ${held} of ${tradeSymbol}, cannot supply ${units}.`);

    // Credit the site (capped at `required`) and drain the ship's hold by the accepted amount.
    const credited = Math.min(units, remaining);
    material.fulfilled += credited;
    removeCargo(ship, tradeSymbol, credited);

    // Spec: supplyConstruction returns 201 Created.
    return reply.code(201).send({
      data: {
        construction: serializeConstruction(materials, waypointSymbol),
        cargo: serializeCargo(ship),
      },
    });
  });
}

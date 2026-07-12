import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify';
import { getWorld } from '../world/store.js';
import { getNow, makeTransit, resolveNav } from '../clock.js';
import { appendMutation } from '../world/mutation-log.js';
import { badRequest, notFound, unauthorized, sendError, ERR_SHIP_MUST_BE_DOCKED, ERR_SHIP_NOT_DOCKED } from '../errors.js';
import type { Ship, Waypoint, World } from '../world/types.js';

const DEFAULT_LIMIT = 20;
const MAX_LIMIT = 20;

function authFailed(request: FastifyRequest, reply: FastifyReply): boolean {
  const world = getWorld();
  const header = request.headers.authorization;
  const token = typeof header === 'string' && header.startsWith('Bearer ') ? header.slice('Bearer '.length).trim() : '';
  if (!world.agentToken || token !== world.agentToken) { unauthorized(reply, 'Invalid or missing agent token.'); return true; }
  return false;
}
function intParam(raw: unknown, def: number, min: number, max: number): number {
  const n = Number.parseInt(typeof raw === 'string' ? raw : '', 10);
  if (!Number.isFinite(n) || n < min) return def;
  return n > max ? max : n;
}

// ─── purchase / navigate helpers (mutation-log-observable /v2 mutations) ─────────────
const ROLE_BY_SHIP_TYPE: Record<string, string> = {
  // Real-API ship-type symbols (what the coordinator sends — e.g. defaultHaulerShipType
  // "SHIP_LIGHT_HAULER"). The un-prefixed keys are kept for legacy/hermetic-test callers.
  SHIP_PROBE: 'SATELLITE',
  SHIP_COMMAND_FRIGATE: 'COMMAND',
  SHIP_LIGHT_HAULER: 'HAULER',
  SHIP_HEAVY_FREIGHTER: 'HAULER',
  SHIP_LIGHT_SHUTTLE: 'HAULER',
  SHIP_MINING_DRONE: 'EXCAVATOR',
  COMMAND_FRIGATE: 'COMMAND',
  LIGHT_HAULER: 'HAULER',
  HEAVY_FREIGHTER: 'HAULER',
  LIGHT_SHUTTLE: 'HAULER',
  MINING_DRONE: 'EXCAVATOR',
};
/** registration.role for a purchased hull (base-view `role`). Unknown types fall back to the
 *  raw shipType so the field is always a non-empty string. */
function roleForShipType(shipType: string): string {
  return ROLE_BY_SHIP_TYPE[shipType] ?? shipType;
}

/** System symbol embedded in a waypoint symbol: "X1-PZ28-A1" -> "X1-PZ28". */
function systemOf(waypointSymbol: string): string {
  const parts = waypointSymbol.split('-');
  return parts.length >= 2 ? `${parts[0]}-${parts[1]}` : waypointSymbol;
}

/** Look up a waypoint in the loaded topology; undefined for a symbol outside it (e.g. a logical
 *  hub name that is not a real captured waypoint). */
function findWaypoint(world: World, symbol: string): Waypoint | undefined {
  const direct = world.systems.get(systemOf(symbol))?.waypoints.get(symbol);
  if (direct) return direct;
  for (const sys of world.systems.values()) {
    const wp = sys.waypoints.get(symbol);
    if (wp) return wp;
  }
  return undefined;
}

/** engine.speed for a shipType from its shipyard listing (the listing's engine MUST carry a
 *  numeric speed); undefined when no listing is found. */
function speedForShipType(world: World, shipType: string): number | undefined {
  for (const sy of world.shipyards.values()) {
    for (const listing of sy.ships) {
      if (listing.type === shipType) {
        const speed = Number((listing.engine as { speed?: unknown }).speed);
        if (Number.isFinite(speed) && speed > 0) return speed;
      }
    }
  }
  return undefined;
}

/** purchasePrice for a shipType: the reset price lever (world.shipPrices) wins, else the shipyard
 *  listing price, else 0. */
function purchasePriceFor(world: World, shipType: string): number {
  const lever = world.shipPrices?.[shipType];
  if (typeof lever === 'number') return lever;
  for (const sy of world.shipyards.values()) {
    for (const listing of sy.ships) {
      if (listing.type === shipType && typeof listing.purchasePrice === 'number') return listing.purchasePrice;
    }
  }
  return 0;
}

/** A freshly bought hull, DOCKED at `waypoint`. Cloned off an existing hull so every required Ship
 *  field is structurally present (full-fidelity frame/reactor/etc. is Task 27's concern); a minimal
 *  hull is fabricated only in the degenerate no-template case. */
function buildPurchasedShip(world: World, symbol: string, shipType: string, waypoint: string): Ship {
  const systemSymbol = systemOf(waypoint);
  const role = roleForShipType(shipType);
  const speed = speedForShipType(world, shipType);
  const template = [...world.ships.values()][0];
  if (template) {
    const clone = structuredClone(template);
    clone.symbol = symbol;
    clone.registration = { ...clone.registration, role };
    clone.nav = { ...clone.nav, systemSymbol, waypointSymbol: waypoint, status: 'DOCKED', flightMode: 'CRUISE', route: null };
    if (speed !== undefined) clone.engine = { ...clone.engine, speed };
    clone.fuel = { ...clone.fuel, current: clone.fuel.capacity };
    clone.cargo = { ...clone.cargo, units: 0, inventory: [] };
    clone.cooldown = null;
    return clone;
  }
  return {
    symbol,
    registration: { role },
    nav: { systemSymbol, waypointSymbol: waypoint, status: 'DOCKED', flightMode: 'CRUISE', route: null },
    fuel: { current: 0, capacity: 0 },
    cargo: { capacity: 0, units: 0, inventory: [] },
    cooldown: null,
    engine: { speed: speed ?? 30 },
    frame: { symbol: `FRAME_${shipType}`, moduleSlots: 0, mountingPoints: 0 },
    reactor: { symbol: 'REACTOR', name: 'Reactor', powerOutput: 0, requirements: { power: 0, crew: 0, slots: 0 } },
    crew: { current: 0, required: 0, capacity: 0 },
    modules: [],
    mounts: [],
  };
}

export async function shipRoutes(app: FastifyInstance): Promise<void> {
  // GET /my/ships?page&limit — paginated; a page past the end returns { data: [], meta } HTTP 200.
  app.get('/my/ships', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = new Date(); // ship status/arrival is on the REAL clock (see clock.ts resolveNav)
    const q = request.query as Record<string, unknown>;
    const page = intParam(q.page, 1, 1, Number.MAX_SAFE_INTEGER);
    const limit = intParam(q.limit, DEFAULT_LIMIT, 1, MAX_LIMIT);
    const all: Ship[] = [...world.ships.values()]
      .sort((a, b) => a.symbol.localeCompare(b.symbol))
      .map((s) => resolveNav(s, world.transits.get(s.symbol), now));
    const start = (page - 1) * limit;
    const data = all.slice(start, start + limit);
    return reply.send({ data, meta: { total: all.length, page, limit } });
  });

  // GET /my/ships/:symbol — single ship with on-read arrival flip; 404 for unknown symbols.
  app.get('/my/ships/:symbol', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const now = new Date(); // ship status/arrival is on the REAL clock (see clock.ts resolveNav)
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    return reply.send({ data: resolveNav(ship, world.transits.get(symbol), now) });
  });

  // POST /my/ships — buy a hull. This is the TWIN-OBSERVABLE `PurchaseShip` mutation: the twin logs
  // it directly (detail:{shipType}, at:world-now) and files the new hull into the phase-correct
  // projection — GATE (construction sited) -> gateWorkers[] source:'bought'; INCOME (hubs ranked) ->
  // haulers[]; DATA (neither) -> ships[] only. So countCall(PurchaseShip) == bought gateWorkers ==
  // haulers appended. Credits are debited (clamped); full 4216 affordability is Task 27's concern.
  app.post('/my/ships', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    if (!world.agent) return unauthorized(reply, 'No agent registered.');
    const body = (request.body ?? {}) as { shipType?: unknown; waypointSymbol?: unknown };
    const shipType = typeof body.shipType === 'string' ? body.shipType.trim() : '';
    if (shipType === '') return badRequest(reply, 'shipType is required.');

    const waypoint = typeof body.waypointSymbol === 'string' && body.waypointSymbol.trim() !== ''
      ? body.waypointSymbol.trim()
      : world.agent.headquarters;

    const symbol = `${world.agent.symbol}-${world.shipCounter++}`;
    const ship = buildPurchasedShip(world, symbol, shipType, waypoint);
    world.ships.set(symbol, ship);
    world.agent.credits = Math.max(0, world.agent.credits - purchasePriceFor(world, shipType));

    if (world.construction.site !== '') {
      world.gateWorkers.push({ symbol, source: 'bought' });
    } else if (world.hubs.length > 0) {
      world.haulers.push({ symbol, role: roleForShipType(shipType), parkedHub: null });
    }

    appendMutation(world, 'PurchaseShip', { shipType });

    const transaction = {
      waypointSymbol: waypoint, shipSymbol: symbol, shipType,
      price: purchasePriceFor(world, shipType), agentSymbol: world.agent.symbol, timestamp: getNow().toISOString(),
    };
    return reply.code(201).send({ data: { agent: world.agent, ship, transaction } });
  });

  // POST /my/ships/:symbol/navigate — move a hull. TWIN-OBSERVABLE `navigate` mutation (no detail):
  // mints a real transit (arrival = departure + realTravelSeconds; resolved on-read by resolveNav)
  // and, when a hauler navigates onto one of the ranked hubs, parks it there (drives haulers[].parkedHub).
  app.post('/my/ships/:symbol/navigate', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    const body = (request.body ?? {}) as { waypointSymbol?: unknown };
    const dest = typeof body.waypointSymbol === 'string' ? body.waypointSymbol.trim() : '';
    if (dest === '') return badRequest(reply, 'waypointSymbol is required.');

    const now = new Date(); // ship status/arrival is on the REAL clock (see clock.ts resolveNav)
    world.transits.delete(symbol); // supersede any prior leg
    const originWp = findWaypoint(world, ship.nav.waypointSymbol);
    const destWp = findWaypoint(world, dest);
    if (originWp && destWp) {
      world.transits.set(symbol, makeTransit({
        shipSymbol: symbol, origin: originWp, destination: destWp,
        engineSpeed: ship.engine.speed, mode: ship.nav.flightMode, now,
      }));
    } else {
      // Destination outside the loaded topology (e.g. a logical hub symbol): best-effort move.
      ship.nav = { ...ship.nav, waypointSymbol: dest, status: 'IN_ORBIT', route: null };
    }

    const hauler = world.haulers.find((h) => h.symbol === symbol);
    if (hauler && world.hubs.includes(dest)) hauler.parkedHub = dest;

    appendMutation(world, 'navigate');

    const resolved = resolveNav(ship, world.transits.get(symbol), now);
    return reply.code(200).send({ data: { nav: resolved.nav, fuel: resolved.fuel } });
  });

  // PATCH /my/ships/:symbol/nav — set the flight mode (CRUISE/BURN/DRIFT/STEALTH). The route
  // executor sets the mode before navigating (it governs travel time + fuel per the clock formula).
  app.patch('/my/ships/:symbol/nav', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    const body = (request.body ?? {}) as { flightMode?: unknown };
    const mode = typeof body.flightMode === 'string' ? body.flightMode.trim() : '';
    const allowed = ['CRUISE', 'BURN', 'DRIFT', 'STEALTH'];
    if (!allowed.includes(mode)) return badRequest(reply, `flightMode must be one of ${allowed.join(', ')}.`);
    ship.nav = { ...ship.nav, flightMode: mode as Ship['nav']['flightMode'] };
    const resolved = resolveNav(ship, world.transits.get(symbol), new Date());
    return reply.code(200).send({ data: { nav: resolved.nav, fuel: resolved.fuel } });
  });

  // POST /my/ships/:symbol/orbit — DOCKED/arrived -> IN_ORBIT. Idempotent. The coordinator orbits
  // the purchaser before navigating it to the shipyard.
  app.post('/my/ships/:symbol/orbit', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    settleArrival(world, ship, symbol);
    if (world.transits.has(symbol)) return sendError(reply, 400, ERR_SHIP_MUST_BE_DOCKED, `Ship ${symbol} is in transit.`);
    ship.nav = { ...ship.nav, status: 'IN_ORBIT' };
    return reply.code(200).send({ data: { nav: ship.nav } });
  });

  // POST /my/ships/:symbol/dock — IN_ORBIT/arrived -> DOCKED. Idempotent. The coordinator docks at
  // the shipyard (after arrival) before POST /my/ships (purchase).
  app.post('/my/ships/:symbol/dock', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    settleArrival(world, ship, symbol);
    if (world.transits.has(symbol)) return sendError(reply, 400, ERR_SHIP_MUST_BE_DOCKED, `Ship ${symbol} is in transit.`);
    ship.nav = { ...ship.nav, status: 'DOCKED' };
    return reply.code(200).send({ data: { nav: ship.nav } });
  });

  // POST /my/ships/:symbol/refuel — fill to capacity, charge the agent. Must be docked (4244).
  // Probes (capacity 0) are a free no-op. 1 market unit = 100 ship-fuel (real-API convention).
  app.post('/my/ships/:symbol/refuel', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol } = request.params as { symbol: string };
    const ship = world.ships.get(symbol);
    if (!ship) return notFound(reply, `Ship ${symbol} not found.`);
    settleArrival(world, ship, symbol);
    if (ship.nav.status !== 'DOCKED') return sendError(reply, 400, ERR_SHIP_NOT_DOCKED, `Ship ${symbol} must be docked to refuel.`);
    const missing = Math.max(0, ship.fuel.capacity - ship.fuel.current);
    const marketUnits = Math.ceil(missing / 100);
    const totalPrice = marketUnits * fuelUnitPrice(world, ship.nav.waypointSymbol);
    ship.fuel = { ...ship.fuel, current: ship.fuel.capacity };
    if (world.agent) world.agent.credits = Math.max(0, world.agent.credits - totalPrice);
    return reply.code(200).send({
      data: { agent: { credits: world.agent?.credits ?? 0 }, fuel: ship.fuel, transaction: { units: missing, totalPrice } },
    });
  });
}

/** Commit a completed transit into the ship's stored nav (arrival is otherwise resolved only
 *  on-read by resolveNav, which never persists). After settling, orbit/dock/refuel act on the
 *  real arrived location. No-op while still in transit or when there is no transit. Exported so the
 *  contract/cargo routes can settle a ship's arrival before their docked-at-location checks. */
export function settleArrival(world: World, ship: Ship, symbol: string): void {
  const transit = world.transits.get(symbol);
  if (!transit) return;
  if (Date.now() >= Date.parse(transit.arrival)) {
    ship.nav = {
      ...ship.nav,
      status: 'IN_ORBIT',
      waypointSymbol: transit.destinationWaypoint,
      route: { departureTime: transit.departureTime, arrival: transit.arrival },
    };
    world.transits.delete(symbol);
  }
}

/** Per-ship-fuel-unit price = the waypoint market's FUEL purchasePrice / 100 (1 market unit =
 *  100 ship fuel), rounded up; falls back to a plausible default when the market lacks FUEL. */
function fuelUnitPrice(world: World, waypointSymbol: string): number {
  const market = world.markets.get(waypointSymbol);
  const fuel = market?.tradeGoods.find((g) => g.symbol === 'FUEL');
  return fuel ? Math.max(1, Math.ceil(fuel.purchasePrice / 100)) : 1;
}

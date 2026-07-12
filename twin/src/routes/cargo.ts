// twin/src/routes/cargo.ts — the cargo-trade /v2 surface: buy/sell trade goods at the ship's current
// market. Both require the ship DOCKED (4244) at a waypoint with a market that lists the good. Prices
// are served API-faithful (purchase costs the good's purchasePrice; sell yields its sellPrice — the
// gobot's internal column inversion is a daemon concern, never on the wire).
//
//   POST /my/ships/:s/purchase -> { data: { agent, cargo, transaction } }   (req { symbol, units })
//   POST /my/ships/:s/sell     -> { data: { agent, cargo, transaction } }   (req { symbol, units })
import type { FastifyInstance } from 'fastify';
import { getWorld } from '../world/store.js';
import { getNow } from '../clock.js';
import { serializeAgent, serializeCargo } from '../world/serialize.js';
import { badRequest, notFound, sendError, ERR_SHIP_NOT_DOCKED } from '../errors.js';
import { authFailed } from './auth.js';
import { settleArrival } from './ships.js';
import type { Ship } from '../world/types.js';

const ERR_MARKET_TRADE_NOT_SOLD = 4602; // good not sold at this market
const ERR_CARGO_FULL = 4217;            // purchase would exceed cargo capacity
const ERR_CARGO_INSUFFICIENT = 4218;    // sell more units than held

interface MarketGood { symbol: string; purchasePrice: number; sellPrice: number }

function marketGoodAt(waypointSymbol: string, symbol: string): MarketGood | undefined {
  const market = getWorld().markets.get(waypointSymbol);
  return market?.tradeGoods.find((g) => g.symbol === symbol) as MarketGood | undefined;
}

function heldUnits(ship: Ship, symbol: string): number {
  return ship.cargo.inventory.find((i) => i.symbol === symbol)?.units ?? 0;
}
function addCargo(ship: Ship, symbol: string, units: number): void {
  const item = ship.cargo.inventory.find((i) => i.symbol === symbol);
  if (item) item.units += units;
  else ship.cargo.inventory.push({ symbol, units });
  ship.cargo.units += units;
}
function removeCargo(ship: Ship, symbol: string, units: number): void {
  const item = ship.cargo.inventory.find((i) => i.symbol === symbol);
  if (!item) return;
  item.units -= units;
  ship.cargo.units = Math.max(0, ship.cargo.units - units);
  if (item.units <= 0) ship.cargo.inventory = ship.cargo.inventory.filter((i) => i.symbol !== symbol);
}

/** Parse + validate the shared { symbol, units } trade body. Returns the normalized pair or an error
 *  string to hand to badRequest. */
function parseTradeBody(body: unknown): { symbol: string; units: number } | string {
  const b = (body ?? {}) as { symbol?: unknown; units?: unknown };
  const symbol = typeof b.symbol === 'string' ? b.symbol.trim() : '';
  const units = Math.trunc(Number(b.units));
  if (symbol === '') return 'symbol is required.';
  if (!Number.isFinite(units) || units <= 0) return 'units must be a positive integer.';
  return { symbol, units };
}

export async function cargoRoutes(app: FastifyInstance): Promise<void> {
  // POST /my/ships/:symbol/purchase — buy `units` of `symbol` from the docked market.
  app.post('/my/ships/:symbol/purchase', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol: shipSymbol } = request.params as { symbol: string };
    const ship = world.ships.get(shipSymbol);
    if (!ship) return notFound(reply, `Ship ${shipSymbol} not found.`);
    const parsed = parseTradeBody(request.body);
    if (typeof parsed === 'string') return badRequest(reply, parsed);

    settleArrival(world, ship, shipSymbol);
    if (ship.nav.status !== 'DOCKED') return sendError(reply, 400, ERR_SHIP_NOT_DOCKED, `Ship ${shipSymbol} must be docked to purchase.`);
    const good = marketGoodAt(ship.nav.waypointSymbol, parsed.symbol);
    if (!good) return sendError(reply, 400, ERR_MARKET_TRADE_NOT_SOLD, `${parsed.symbol} is not sold at ${ship.nav.waypointSymbol}.`);
    const space = ship.cargo.capacity - ship.cargo.units;
    if (parsed.units > space) return sendError(reply, 400, ERR_CARGO_FULL, `Insufficient cargo space (${space}) for ${parsed.units} units.`);

    const pricePerUnit = good.purchasePrice;
    const totalPrice = pricePerUnit * parsed.units;
    addCargo(ship, parsed.symbol, parsed.units);
    if (world.agent) world.agent.credits = Math.max(0, world.agent.credits - totalPrice);

    // Spec: purchaseCargo returns 201 Created.
    return reply.code(201).send({
      data: {
        agent: serializeAgent(world),
        cargo: serializeCargo(ship),
        transaction: { waypointSymbol: ship.nav.waypointSymbol, shipSymbol, tradeSymbol: parsed.symbol, type: 'PURCHASE', units: parsed.units, pricePerUnit, totalPrice, timestamp: getNow().toISOString() },
      },
    });
  });

  // POST /my/ships/:symbol/sell — sell `units` of `symbol` into the docked market.
  app.post('/my/ships/:symbol/sell', async (request, reply) => {
    if (authFailed(request, reply)) return reply;
    const world = getWorld();
    const { symbol: shipSymbol } = request.params as { symbol: string };
    const ship = world.ships.get(shipSymbol);
    if (!ship) return notFound(reply, `Ship ${shipSymbol} not found.`);
    const parsed = parseTradeBody(request.body);
    if (typeof parsed === 'string') return badRequest(reply, parsed);

    settleArrival(world, ship, shipSymbol);
    if (ship.nav.status !== 'DOCKED') return sendError(reply, 400, ERR_SHIP_NOT_DOCKED, `Ship ${shipSymbol} must be docked to sell.`);
    const held = heldUnits(ship, parsed.symbol);
    if (parsed.units > held) return sendError(reply, 400, ERR_CARGO_INSUFFICIENT, `Ship ${shipSymbol} holds ${held} of ${parsed.symbol}, cannot sell ${parsed.units}.`);
    const good = marketGoodAt(ship.nav.waypointSymbol, parsed.symbol);
    if (!good) return sendError(reply, 400, ERR_MARKET_TRADE_NOT_SOLD, `${parsed.symbol} is not sold at ${ship.nav.waypointSymbol}.`);

    const pricePerUnit = good.sellPrice;
    const totalPrice = pricePerUnit * parsed.units;
    removeCargo(ship, parsed.symbol, parsed.units);
    if (world.agent) world.agent.credits += totalPrice;

    // Spec: sellCargo returns 201 Created.
    return reply.code(201).send({
      data: {
        agent: serializeAgent(world),
        cargo: serializeCargo(ship),
        transaction: { waypointSymbol: ship.nav.waypointSymbol, shipSymbol, tradeSymbol: parsed.symbol, type: 'SELL', units: parsed.units, pricePerUnit, totalPrice, timestamp: getNow().toISOString() },
      },
    });
  });
}

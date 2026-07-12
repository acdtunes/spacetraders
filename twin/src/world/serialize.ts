// twin/src/world/serialize.ts — OpenAPI RESPONSE-shape serializers.
//
// The twin stores REDUCED entities (only the fields the Go client historically read). The real
// SpaceTraders 2.3.0 spec (gobot/api/openapi.json) marks many more fields REQUIRED on Agent / Ship /
// Market / Shipyard / Contract — e.g. Ship.registration.name+factionSymbol, ShipNav.route (a full,
// non-null ShipNavRoute), Agent.shipCount, MarketTradeGood.type, ShipyardShip.supply/crew, and the
// deep condition/integrity/quality/description fields on frame/reactor/engine. These serializers map
// the reduced world objects onto the FULL spec shape on the way out of the /v2 routes, filling every
// spec-required field with a faithful value (never mutating stored world state, so /_twin/state and the
// internal clock/logic keep their reduced shapes). This is the twin analogue of the daemon's
// openapi_contract_test.go: it makes twin RESPONSES provably conform to the same vendored spec.
import type { Agent, Market, Ship, ShipNav, Shipyard, TradeGood, World } from './types.js';
import { getNow, resolveNav } from '../clock.js';

/** "FRAME_PROBE" -> "Frame Probe"; used to synthesize the spec-required human `name`/`description`
 *  fields the reduced fixtures omit. */
function humanize(symbol: string): string {
  return symbol
    .split('_')
    .map((w) => (w.length ? w[0].toUpperCase() + w.slice(1).toLowerCase() : w))
    .join(' ');
}

interface Requirements { power: number; crew: number; slots: number }
function reqs(r?: Partial<Requirements>): Requirements {
  return { power: r?.power ?? 0, crew: r?.crew ?? 0, slots: r?.slots ?? 0 };
}

// ─── Ship component enrichers (shared by serializeShip + serializeShipyard) ─────────────
// condition/integrity are 0..1 wear fractions, quality an integer — a freshly-served twin hull is
// pristine (1/1/1). name/description are synthesized from the component symbol when absent.
interface Named { symbol?: string; name?: string; description?: string }

function fullFrame(f: Record<string, unknown> & Named, fuelCapacity: number): Record<string, unknown> {
  const symbol = (f.symbol as string) ?? 'FRAME_UNKNOWN';
  return {
    symbol,
    name: f.name ?? humanize(symbol),
    description: f.description ?? humanize(symbol),
    condition: (f.condition as number) ?? 1,
    integrity: (f.integrity as number) ?? 1,
    moduleSlots: (f.moduleSlots as number) ?? 0,
    mountingPoints: (f.mountingPoints as number) ?? 0,
    fuelCapacity: (f.fuelCapacity as number) ?? fuelCapacity,
    requirements: reqs(f.requirements as Requirements | undefined),
    quality: (f.quality as number) ?? 1,
  };
}

function fullReactor(r: Record<string, unknown> & Named): Record<string, unknown> {
  const symbol = (r.symbol as string) ?? 'REACTOR_UNKNOWN';
  return {
    symbol,
    name: r.name ?? humanize(symbol),
    description: r.description ?? humanize(symbol),
    condition: (r.condition as number) ?? 1,
    integrity: (r.integrity as number) ?? 1,
    powerOutput: (r.powerOutput as number) ?? 1,
    requirements: reqs(r.requirements as Requirements | undefined),
    quality: (r.quality as number) ?? 1,
  };
}

function fullEngine(e: Record<string, unknown> & Named): Record<string, unknown> {
  const symbol = (e.symbol as string) ?? 'ENGINE_IMPULSE_DRIVE_I';
  return {
    symbol,
    name: e.name ?? humanize(symbol),
    description: e.description ?? humanize(symbol),
    condition: (e.condition as number) ?? 1,
    integrity: (e.integrity as number) ?? 1,
    speed: (e.speed as number) ?? 1,
    requirements: reqs(e.requirements as Requirements | undefined),
    quality: (e.quality as number) ?? 1,
  };
}

function fullModule(m: Record<string, unknown> & Named): Record<string, unknown> {
  const symbol = (m.symbol as string) ?? 'MODULE_UNKNOWN';
  const out: Record<string, unknown> = {
    ...m,
    symbol,
    name: m.name ?? humanize(symbol),
    description: m.description ?? humanize(symbol),
    requirements: reqs(m.requirements as Requirements | undefined),
  };
  return out;
}

function fullMount(m: Record<string, unknown> & Named): Record<string, unknown> {
  const symbol = (m.symbol as string) ?? 'MOUNT_UNKNOWN';
  return {
    ...m,
    symbol,
    name: m.name ?? humanize(symbol),
    requirements: reqs(m.requirements as Requirements | undefined),
  };
}

/** A spec ShipNavRouteWaypoint for `symbol`, looked up in the loaded topology for real x/y/type;
 *  falls back to a plausible planet at origin when the symbol is outside the capture. */
function routeWaypoint(world: World, symbol: string, systemSymbol: string): Record<string, unknown> {
  const wp = world.systems.get(systemSymbol)?.waypoints.get(symbol);
  if (wp) return { symbol: wp.symbol, type: wp.type, systemSymbol: wp.systemSymbol, x: wp.x, y: wp.y };
  for (const sys of world.systems.values()) {
    const hit = sys.waypoints.get(symbol);
    if (hit) return { symbol: hit.symbol, type: hit.type, systemSymbol: hit.systemSymbol, x: hit.x, y: hit.y };
  }
  return { symbol, type: 'PLANET', systemSymbol, x: 0, y: 0 };
}

/** Serialize a ship's nav to the FULL spec ShipNav: route is ALWAYS a non-null ShipNavRoute (the real
 *  API returns the last/current route even while docked). Origin/destination come from an active
 *  transit when present, else collapse to the current waypoint (a stationary route). */
export function serializeShipNav(world: World, nav: ShipNav, shipSymbol: string): Record<string, unknown> {
  const system = nav.systemSymbol;
  const transit = world.transits.get(shipSymbol);
  const originSym = transit ? transit.originWaypoint : nav.waypointSymbol;
  const destSym = transit ? transit.destinationWaypoint : nav.waypointSymbol;
  const departureTime = transit ? transit.departureTime : nav.route?.departureTime ?? getNow().toISOString();
  const arrival = transit ? transit.arrival : nav.route?.arrival ?? departureTime;
  const origin = routeWaypoint(world, originSym, system);
  const destination = routeWaypoint(world, destSym, system);
  return {
    systemSymbol: system,
    waypointSymbol: nav.waypointSymbol,
    status: nav.status,
    flightMode: nav.flightMode,
    route: { origin, destination, departure: origin, departureTime, arrival },
  };
}

/** Serialize the world agent to the FULL spec Agent (adds the required `shipCount`). */
export function serializeAgent(world: World): Record<string, unknown> {
  const a = world.agent as Agent;
  return {
    accountId: a.accountId,
    symbol: a.symbol,
    headquarters: a.headquarters,
    credits: a.credits,
    startingFaction: a.startingFaction,
    shipCount: world.ships.size,
  };
}

/** Serialize one ship to the FULL spec Ship. Applies resolveNav first so status/route reflect the
 *  current clock, then fills every spec-required nested field the reduced fixture omits. */
export function serializeShip(world: World, ship: Ship, now: Date = new Date()): Record<string, unknown> {
  const resolved = resolveNav(ship, world.transits.get(ship.symbol), now);
  const faction = world.agent?.startingFaction ?? 'COSMIC';
  const cd = resolved.cooldown;
  const cooldown = cd
    ? { shipSymbol: ship.symbol, totalSeconds: Math.max(0, Math.ceil((Date.parse(cd.expiration) - now.getTime()) / 1000)), remainingSeconds: Math.max(0, Math.ceil((Date.parse(cd.expiration) - now.getTime()) / 1000)), expiration: cd.expiration }
    : { shipSymbol: ship.symbol, totalSeconds: 0, remainingSeconds: 0 };
  const crew = resolved.crew as { current?: number; required?: number; capacity?: number } | undefined;
  return {
    symbol: ship.symbol,
    registration: {
      name: ship.symbol,
      factionSymbol: faction,
      role: ship.registration.role,
    },
    nav: serializeShipNav(world, resolved.nav, ship.symbol),
    crew: {
      current: crew?.current ?? 0,
      required: crew?.required ?? 0,
      capacity: crew?.capacity ?? 0,
      rotation: 'STRICT',
      morale: 100,
      wages: 0,
    },
    frame: fullFrame(resolved.frame as Record<string, unknown>, resolved.fuel.capacity),
    reactor: fullReactor(resolved.reactor as unknown as Record<string, unknown>),
    engine: fullEngine(resolved.engine as unknown as Record<string, unknown>),
    modules: (resolved.modules ?? []).map((m) => fullModule(m as unknown as Record<string, unknown>)),
    mounts: (resolved.mounts ?? []).map((m) => fullMount(m as unknown as Record<string, unknown>)),
    cargo: {
      capacity: resolved.cargo.capacity,
      units: resolved.cargo.units,
      inventory: resolved.cargo.inventory.map((i) => ({
        symbol: i.symbol,
        name: (i as { name?: string }).name ?? humanize(i.symbol),
        description: (i as { description?: string }).description ?? humanize(i.symbol),
        units: i.units,
      })),
    },
    fuel: { current: resolved.fuel.current, capacity: resolved.fuel.capacity },
    cooldown,
  };
}

/** Serialize the full ship cargo block (spec ShipCargo) — inventory items carry name/description. */
export function serializeCargo(ship: Ship): Record<string, unknown> {
  return {
    capacity: ship.cargo.capacity,
    units: ship.cargo.units,
    inventory: ship.cargo.inventory.map((i) => ({
      symbol: i.symbol,
      name: (i as { name?: string }).name ?? humanize(i.symbol),
      description: (i as { description?: string }).description ?? humanize(i.symbol),
      units: i.units,
    })),
  };
}

/** Serialize a market to the FULL spec Market: exports/imports/exchange become TradeGood objects
 *  (symbol+name+description) and each tradeGoods entry carries its derived `type`
 *  (EXPORT/IMPORT/EXCHANGE — the same which-array classification the Go client makes). */
export function serializeMarket(market: Market): Record<string, unknown> {
  const good = (g: { symbol: string }) => ({ symbol: g.symbol, name: humanize(g.symbol), description: `${humanize(g.symbol)} traded at ${market.symbol}.` });
  const exports = market.exports.map(good);
  const imports = market.imports.map(good);
  const exchange = market.exchange.map(good);
  const typeOf = (symbol: string): string => {
    if (market.exports.some((g) => g.symbol === symbol)) return 'EXPORT';
    if (market.imports.some((g) => g.symbol === symbol)) return 'IMPORT';
    return 'EXCHANGE';
  };
  return {
    symbol: market.symbol,
    exports,
    imports,
    exchange,
    tradeGoods: (market.tradeGoods ?? []).map((g: TradeGood) => ({
      symbol: g.symbol,
      type: typeOf(g.symbol),
      tradeVolume: g.tradeVolume,
      supply: g.supply,
      activity: g.activity,
      purchasePrice: g.purchasePrice,
      sellPrice: g.sellPrice,
    })),
  };
}

/** Serialize a shipyard to the FULL spec Shipyard: each listing becomes a spec ShipyardShip
 *  (symbol+supply+crew added; frame/reactor/engine/modules/mounts enriched to full fidelity). */
export function serializeShipyard(shipyard: Shipyard): Record<string, unknown> {
  return {
    symbol: shipyard.symbol,
    shipTypes: shipyard.shipTypes,
    modificationsFee: shipyard.modificationsFee,
    transactions: shipyard.transactions ?? [],
    ships: (shipyard.ships ?? []).map((listing) => {
      const l = listing as unknown as Record<string, unknown> & { type: string; frame?: Record<string, unknown>; reactor?: Record<string, unknown>; engine?: Record<string, unknown>; modules?: unknown[]; mounts?: unknown[]; crew?: { required?: number; capacity?: number } };
      return {
        type: l.type,
        symbol: (l.symbol as string) ?? l.type,
        name: l.name ?? humanize(l.type),
        description: l.description ?? humanize(l.type),
        supply: (l.supply as string) ?? 'MODERATE',
        activity: l.activity,
        purchasePrice: l.purchasePrice ?? 0,
        frame: fullFrame((l.frame ?? {}) as Record<string, unknown>, 0),
        reactor: fullReactor((l.reactor ?? {}) as Record<string, unknown>),
        engine: fullEngine((l.engine ?? {}) as Record<string, unknown>),
        modules: (l.modules ?? []).map((m) => fullModule(m as Record<string, unknown>)),
        mounts: (l.mounts ?? []).map((m) => fullMount(m as Record<string, unknown>)),
        crew: { required: l.crew?.required ?? 0, capacity: l.crew?.capacity ?? 0 },
      };
    }),
  };
}

/** A spec Faction object for the /register response (required: symbol,name,description,traits,isRecruiting). */
export function buildFaction(symbol: string): Record<string, unknown> {
  // `headquarters` is spec-optional with minLength:1 — omit it rather than emit an empty string.
  return {
    symbol,
    name: humanize(symbol),
    description: `${humanize(symbol)} faction.`,
    traits: [],
    isRecruiting: true,
  };
}

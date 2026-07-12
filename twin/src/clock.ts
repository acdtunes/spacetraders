// twin/src/clock.ts — compressed-time engine. Travel/fuel mirror routing_engine.py so
// the twin and the bot's planner never disagree about an ETA.
import type { FlightMode, Rfc3339, Ship, TransitState, Waypoint } from './world/types.js';

const DEFAULT_COMPRESSION = 100;
const TRAVEL_MULTIPLIER: Record<FlightMode, number> = { CRUISE: 31, DRIFT: 26, BURN: 15, STEALTH: 50 };
const FUEL_RATE: Record<FlightMode, number> = { CRUISE: 1.0, DRIFT: 0.003, BURN: 2.0, STEALTH: 1.0 };

export function parseCompression(raw: string | undefined): number {
  if (raw === undefined || raw === '') return DEFAULT_COMPRESSION;
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? n : DEFAULT_COMPRESSION;
}

let compression = parseCompression(process.env.TWIN_TIME_COMPRESSION);

export function getCompression(): number { return compression; }
export function setCompression(factor: number): void {
  if (!(factor > 0)) throw new RangeError(`compression must be > 0, got ${factor}`);
  compression = factor;
}

export function distance(a: { x: number; y: number }, b: { x: number; y: number }): number {
  const dx = a.x - b.x; const dy = a.y - b.y;
  return Math.sqrt(dx * dx + dy * dy);
}

export function realTravelSeconds(dist: number, engineSpeed: number, mode: FlightMode = 'CRUISE'): number {
  if (dist === 0) return 0;
  const speed = Math.max(1, engineSpeed);
  return Math.max(1, Math.floor((dist * TRAVEL_MULTIPLIER[mode]) / speed));
}

export function fuelCost(dist: number, mode: FlightMode = 'CRUISE'): number {
  if (dist === 0) return 0;
  return Math.max(1, Math.ceil(dist * FUEL_RATE[mode]));
}

export function makeTransit(args: {
  shipSymbol: string; origin: Waypoint; destination: Waypoint; engineSpeed: number; mode?: FlightMode; now?: Date;
}): TransitState {
  const mode = args.mode ?? 'CRUISE';
  const now = args.now ?? new Date();
  const dist = distance(args.origin, args.destination);
  const realSecs = realTravelSeconds(dist, args.engineSpeed, mode);
  const departureMs = now.getTime();
  const arrivalMs = Math.max(departureMs, departureMs + (realSecs / getCompression()) * 1000);
  return {
    shipSymbol: args.shipSymbol,
    originWaypoint: args.origin.symbol,
    destinationWaypoint: args.destination.symbol,
    departureTime: new Date(departureMs).toISOString(),
    arrival: new Date(arrivalMs).toISOString(),
  };
}

/** The ONLY place nav status/location is computed. Pure — returns a new Ship.
 *  no transit → unchanged; now < arrival → IN_TRANSIT at origin; now >= arrival →
 *  IN_ORBIT at destination (atomic flip). route stays populated in both in-flight cases. */
export function resolveNav(ship: Ship, transit: TransitState | undefined, now: Date = new Date()): Ship {
  if (transit === undefined) return ship;
  const arrived = now.getTime() >= Date.parse(transit.arrival);
  return {
    ...ship,
    nav: {
      ...ship.nav,
      status: arrived ? 'IN_ORBIT' : 'IN_TRANSIT',
      waypointSymbol: arrived ? transit.destinationWaypoint : transit.originWaypoint,
      route: { departureTime: transit.departureTime, arrival: transit.arrival },
    },
  };
}

export function makeCooldownExpiration(realSeconds: number, now: Date = new Date()): Rfc3339 {
  return new Date(now.getTime() + (realSeconds / getCompression()) * 1000).toISOString();
}

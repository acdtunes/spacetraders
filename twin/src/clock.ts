// twin/src/clock.ts — the twin's CONTROLLABLE world-clock + the REAL-API travel model.
//
// World clock: a single stored `now` instant the harness steps deterministically.
//   • FROZEN (the harness default): wall-clock is IGNORED. `now` moves ONLY via
//     advanceClock(ms) or setNow(iso).
//   • RUNNING: `now` tracks wall-clock elapsed since the last anchor.
// Every read (resolveNav, GET /_twin/state, mutation-log `at`) consults getNow(),
// so a ship's IN_TRANSIT->IN_ORBIT flip is driven by advanceClock(), not real time.
//
// Travel: the REAL SpaceTraders v2.3.0 formula (fidelity spec st-drm.2) — NOT the old
// routing_engine.py approximation. arrival = departure + realTravelSeconds, where
// realTravelSeconds = round(round(distance) * (multiplier / engineSpeed)) + 15.
import type { FlightMode, Rfc3339, Ship, TransitState, Waypoint } from './world/types.js';

// ─── World clock ────────────────────────────────────────────────────────────────
export type ClockMode = 'frozen' | 'running';

let clockMode: ClockMode = 'frozen';
let baseNowMs = Date.now();     // world-now (ms) captured at the last anchor
let wallAnchorMs = Date.now();  // Date.now() reading captured at the same anchor

/** Current world-now in ms. FROZEN: the stored base. RUNNING: base + wall elapsed. */
function nowMs(): number {
  return clockMode === 'running' ? baseNowMs + (Date.now() - wallAnchorMs) : baseNowMs;
}

/** The world clock every read consults — NOT wall time. */
export function getNow(): Date {
  return new Date(nowMs());
}

/** Step world-now forward by `ms` (the harness clock-step). Returns the new now (rfc3339). */
export function advanceClock(ms: number): Rfc3339 {
  baseNowMs = nowMs() + ms;
  wallAnchorMs = Date.now();
  return new Date(baseNowMs).toISOString();
}

/** Pin world-now to an explicit instant. Re-anchors wall time (RUNNING continues from here). */
export function setNow(iso: string): void {
  const parsed = Date.parse(iso);
  if (!Number.isFinite(parsed)) throw new RangeError(`setNow: invalid instant ${JSON.stringify(iso)}`);
  baseNowMs = parsed;
  wallAnchorMs = Date.now();
}

/** Switch frozen<->running WITHOUT jumping now (captures the current instant as the new base). */
export function setClockMode(mode: ClockMode): void {
  baseNowMs = nowMs();
  wallAnchorMs = Date.now();
  clockMode = mode;
}

/** {now, mode} — feeds GET /_twin/state `clock`. */
export function getClockState(): { now: Rfc3339; mode: ClockMode } {
  return { now: new Date(nowMs()).toISOString(), mode: clockMode };
}

/** POST /_twin/reset re-freezes the clock (at wall-now) so each scenario starts frozen. */
export function resetClock(): void {
  clockMode = 'frozen';
  baseNowMs = Date.now();
  wallAnchorMs = baseNowMs;
}

// ─── legacy time-compression knob (DECOUPLED from the world clock above) ──────────
// Retained only so the foundation /_twin admin route (compression reporting +
// POST /_twin/time-compression) stays green. The world clock does NOT consult it;
// travel is real-time now. New control-plane pieces should use the world clock.
const DEFAULT_COMPRESSION = 100;

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

// ─── Real-API travel + fuel model ─────────────────────────────────────────────────
const TRAVEL_MULTIPLIER: Record<FlightMode, number> = { CRUISE: 25, BURN: 12.5, DRIFT: 250, STEALTH: 30 };

/** Straight-line waypoint distance, RAW (un-rounded). realTravelSeconds / fuel round it
 *  per the spec, so callers pass the raw distance. */
export function distance(a: { x: number; y: number }, b: { x: number; y: number }): number {
  const dx = a.x - b.x; const dy = a.y - b.y;
  return Math.sqrt(dx * dx + dy * dy);
}

/** REAL v2.3.0 ETA: round(round(distance) * (multiplier / engineSpeed)) + 15.
 *  The +15 flat term and the round(distance) are exactly what naive twins drop.
 *  engineSpeed is clamped to >=1 so a degenerate speed-0 hull yields a finite ETA
 *  (probes navigate via the capacity-0 fuel rule, not by having speed 0). */
export function realTravelSeconds(dist: number, engineSpeed: number, mode: FlightMode = 'CRUISE'): number {
  const d = Math.round(dist);
  const speed = Math.max(1, engineSpeed);
  return Math.round(d * (TRAVEL_MULTIPLIER[mode] / speed)) + 15;
}

/** Fuel a move consumes (fidelity spec): CRUISE/STEALTH round(dist) (min 1),
 *  BURN 2*round(dist), DRIFT a flat 1. Ignores capacity — see fuelRequired. */
export function fuelCost(dist: number, mode: FlightMode = 'CRUISE'): number {
  const d = Math.round(dist);
  if (mode === 'DRIFT') return 1;
  if (mode === 'BURN') return 2 * d;
  return Math.max(1, d); // CRUISE / STEALTH
}

/** Fuel the navigate route must have on board. fuelCapacity 0 (probes/satellites) =>
 *  UNLIMITED range: consumes NO fuel, so navigate is ALWAYS allowed (never 4203). */
export function fuelRequired(dist: number, mode: FlightMode, fuelCapacity: number): number {
  if (fuelCapacity === 0) return 0;
  return fuelCost(dist, mode);
}

/** Mint a transit whose arrival is a REAL future world-instant: now + realTravelSeconds.
 *  Departure defaults to the world clock (getNow); pass `now` to compute against a fixed instant. */
export function makeTransit(args: {
  shipSymbol: string; origin: Waypoint; destination: Waypoint; engineSpeed: number; mode?: FlightMode; now?: Date;
}): TransitState {
  const mode = args.mode ?? 'CRUISE';
  const departure = args.now ?? getNow();
  const dist = distance(args.origin, args.destination);
  const realSecs = realTravelSeconds(dist, args.engineSpeed, mode);
  const departureMs = departure.getTime();
  const arrivalMs = departureMs + realSecs * 1000;
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
 *  IN_ORBIT at destination (single atomic flip). Reads the world clock by default so
 *  the flip is driven by advanceClock(), not wall time. route stays populated in flight. */
export function resolveNav(ship: Ship, transit: TransitState | undefined, now: Date = getNow()): Ship {
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

/** Cooldown expiry = now + realSeconds (real time; cooldowns aren't compressed). */
export function makeCooldownExpiration(realSeconds: number, now: Date = getNow()): Rfc3339 {
  return new Date(now.getTime() + realSeconds * 1000).toISOString();
}

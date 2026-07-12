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

// ─── The ONE time-compression knob (drives ship travel + cooldowns INVERSELY) ───────
// CRITICAL: the daemon detects ship arrival on the REAL wall clock (ship_state_scheduler
// arms time.AfterFunc(time.Until(arrival))). It does NOT consult the twin's frozen world
// clock. So arrivals + cooldowns MUST be real-future instants, or the daemon waits the full
// (uncompressed) real duration and the harness's clock-stepping window expires first.
//
// This single factor compresses the REAL v2.3.0 ETA INVERSELY: arrivalMs = realMs / factor.
//   • 1    → TRUE real-API timing (fidelity mode; a ~16-40s hop takes ~16-40s).
//   • 20   → the DEFAULT fast-run (a ~16-40s hop resolves in ~1-2s). The twin e2e specs and
//            the DATA harness pollUntil budgets are tuned to this — do NOT change the default.
//   • 100+ → very fast (bootstrap-harness fast runs).
// Seeded at boot from env TWIN_TIME_COMPRESSION (legacy alias: TWIN_ARRIVAL_COMPRESSION) and
// retunable LIVE via POST /_twin/time-compression (routes/admin.ts) — makeTransit /
// makeCooldownExpiration read getCompression() on EVERY call, so the lever takes effect with
// no restart. The frozen world clock (getNow/advanceClock) still governs mutation `at` stamps
// + the scalar levers — those stay decoupled from ship motion.
const DEFAULT_COMPRESSION = 20;

export function parseCompression(raw: string | undefined): number {
  if (raw === undefined || raw === '') return DEFAULT_COMPRESSION;
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? n : DEFAULT_COMPRESSION;
}

// TWIN_TIME_COMPRESSION is canonical (matches the admin route + the lever concept);
// TWIN_ARRIVAL_COMPRESSION is accepted as a legacy alias so an older harness that still
// exports the old name keeps working. Both default to 20 when unset.
let compression = parseCompression(process.env.TWIN_TIME_COMPRESSION ?? process.env.TWIN_ARRIVAL_COMPRESSION);

export function getCompression(): number { return compression; }
export function setCompression(factor: number): void {
  if (!(factor > 0)) throw new RangeError(`compression must be > 0, got ${factor}`);
  compression = factor;
}

// ─── The travel-time floor (configurable; default unchanged at 1000ms) ──────────────
// A compressed ETA never resolves below this many real milliseconds. env TWIN_MIN_TRAVEL_MS
// (default 1000) makes it tunable for very-fast stacks; unset preserves the historical 1s floor.
//
// INVARIANT (st-drm.8): in ANY twin+daemon stack, TWIN_MIN_TRAVEL_MS MUST be >= the daemon's
// ST_CLOCK_DRIFT_BUFFER_MS clamp (gobot ship_state_scheduler.go). The twin mints arrivals at
// least this floor in the (real) future; the daemon then waits arrival + its drift buffer. If
// the floor were SMALLER than the buffer, the daemon could still be inside its own tolerance
// when the harness's clock-step budget expires → a MISSED arrival. Default stack: floor 1000 >=
// clamp 1000 (OK). Twin test stack: floor 1000 >= clamp 50 (OK).
const DEFAULT_MIN_TRAVEL_MS = 1000;

export function parseMinTravelMs(raw: string | undefined): number {
  if (raw === undefined || raw === '') return DEFAULT_MIN_TRAVEL_MS;
  const n = Number(raw);
  return Number.isFinite(n) && n >= 1 ? Math.floor(n) : DEFAULT_MIN_TRAVEL_MS;
}

const MIN_TRAVEL_MS = parseMinTravelMs(process.env.TWIN_MIN_TRAVEL_MS);
export function getMinTravelMs(): number { return MIN_TRAVEL_MS; }

/** Compress a REAL ETA (seconds) into real milliseconds: realMs / compression, floored at
 *  MIN_TRAVEL_MS. Reads getCompression() live so the admin lever retunes travel without a
 *  restart. At compression 1 this is exact real-API timing (the floor only bites sub-floor
 *  durations, and the smallest real ETA — the flat +15s term — sits far above the 1s floor). */
function compressedMs(realSeconds: number): number {
  return Math.max(MIN_TRAVEL_MS, Math.round((realSeconds * 1000) / getCompression()));
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

/** Fuel a NAVIGATE actually consumes at departure — derived straight from the two waypoints'
 *  coordinates (reuses `distance`, so callers never re-derive the euclidean math). CRUISE/STEALTH
 *  round(dist), BURN 2*round(dist), DRIFT a flat 1. Unlike `fuelCost` there is NO min-of-1: a
 *  0-distance hop between co-located waypoints (an orbital and its planet, e.g. A2 from A1) is a
 *  FREE move (0). The caller clamps the result to the fuel on board, so a capacity-0 probe (tank 0)
 *  burns 0. */
export function fuelCostFor(args: {
  origin: { x: number; y: number };
  destination: { x: number; y: number };
  mode: FlightMode;
}): number {
  const roundedDistance = Math.round(distance(args.origin, args.destination));
  if (args.mode === 'DRIFT') return 1;
  if (args.mode === 'BURN') return 2 * roundedDistance;
  return roundedDistance; // CRUISE / STEALTH — no min-of-1; a 0-distance move is free
}

/** Mint a transit whose arrival is a REAL-wall-clock future instant: wall-now + the
 *  COMPRESSED real ETA (see compressedMs / getCompression). Real-clock — NOT the frozen world clock —
 *  because the daemon waits for arrival on real wall time. `now` (a wall Date) is accepted for
 *  test determinism; it defaults to actual wall time. departureTime/arrival are both real. */
export function makeTransit(args: {
  shipSymbol: string; origin: Waypoint; destination: Waypoint; engineSpeed: number; mode?: FlightMode; now?: Date;
}): TransitState {
  const mode = args.mode ?? 'CRUISE';
  const dist = distance(args.origin, args.destination);
  const realSecs = realTravelSeconds(dist, args.engineSpeed, mode);
  const departureMs = (args.now ?? new Date()).getTime();
  const arrivalMs = departureMs + compressedMs(realSecs);
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
 *  IN_ORBIT at destination (single atomic flip). Reads REAL wall time by default (arrivals
 *  live on the real clock so the twin's status agrees with the daemon's real-time detection).
 *  route stays populated in flight. */
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

/** Cooldown expiry = wall-now + the COMPRESSED real cooldown (same real-clock rationale as
 *  arrivals — the daemon clears cooldowns on real time). */
export function makeCooldownExpiration(realSeconds: number, now: Date = new Date()): Rfc3339 {
  return new Date(now.getTime() + compressedMs(realSeconds)).toISOString();
}

import type { ConstructionMaterial, GateWorkerState, HaulerState, Ship, World } from './types.js';
import { loadColdStartWorld, registerAgent } from './loader.js';

/** The single in-memory world every route reads/mutates. Lazily built on first access. */
let current: World | null = null;

export function getWorld(): World {
  if (current === null) current = loadColdStartWorld();
  return current;
}

/** Replace the live world outright (buildServer({ world }) and tests). */
export function setWorld(world: World): void { current = world; }

// ─── POST /_twin/reset options — one discriminated union over `mode` ─────────────────
// Absent `mode` = cold/DATA. Every field is optional; the seed functions supply the frozen
// contract defaults so a bare `{}` (or `{mode}`) reproduces the harness fixture. All three
// modes first re-materialize the agent + starting fleet (preserving symbol/faction/token so
// the seeded players row stays valid), THEN layer the mode-specific world state on top.

export interface ColdResetOptions {
  mode?: 'cold';
  credits?: number;              // default 175_000 (registerAgent's starting credits)
  probes?: number;               // default 1 — SATELLITE hull count
  frigates?: number;             // default 1 — COMMAND hull count
  probePrice?: number;           // shipyard SHIP_PROBE purchasePrice lever (DATA probe buys)
  preScoutedMarkets?: string[];  // waypoints to mark scouted+fresh up front
  coverage?: number;             // starting market-coverage fraction
}
export interface IncomeResetOptions {
  mode: 'income-entry';
  credits?: number;              // default 600_000
  haulerPrice?: number;          // default 300_000 — LIGHT_HAULER purchase lever
  hubs?: string[];               // default = every real MARKETPLACE waypoint in the loaded topology
                                 // (the candidate hub population the coordinator ranks + places haulers on)
  frigateContractTagged?: boolean; // default true — flips false on fleet-unassign report
  creditsPerHour?: number;       // default 0 — the ONE $/hr var
}
export interface GateResetOptions {
  mode: 'gate-entry';
  credits?: number;              // default 1_500_000
  haulers?: number;              // default 4 — idle income haulers available to repurpose
  incomePerHour?: number;        // default 50_000 — mapped onto the ONE $/hr var (creditsPerHour)
  gateSite?: string;             // default = the real JUMP_GATE waypoint from the fixture
  gateMaterialChains?: number;   // default 3 — worker-sizing input (stored for the autosizer)
  constructionPercent?: number;  // default 0 — construction never auto-advances
  workerPrice?: number;          // default 300_000 — LIGHT_HAULER top-up worker lever
  executorRunning?: boolean;     // default true — construction executor already up at entry
}
export type ResetOptions = ColdResetOptions | IncomeResetOptions | GateResetOptions;

const FALLBACK_JUMP_GATE = 'X1-PZ28-I67';

// The gate construction manifest — the two goods the real SpaceTraders JUMP_GATE requires.
// `required` counts are realistic (thousands); `fulfilled` is layered on at 0 by seedGateEntry.
const GATE_MANIFEST: ReadonlyArray<{ tradeSymbol: string; required: number }> = [
  { tradeSymbol: 'FAB_MATS', required: 4000 },
  { tradeSymbol: 'ADVANCED_CIRCUITRY', required: 1200 },
];

/** POST /_twin/reset: rebuild the world from fixtures, PRESERVING the registered agent's
 *  symbol/faction/token, then layer the mode-specific seed on top. Replaces the singleton —
 *  safe because no route captures a reference (all call getWorld()). A bare resetWorld() is
 *  the cold/DATA reset with contract defaults (backwards-compatible with every prior caller). */
export function resetWorld(opts: ResetOptions = {}): void {
  const prev = getWorld();
  const prevSymbol = prev.agent?.symbol ?? null;
  const prevFaction = prev.agent?.startingFaction ?? null;
  const prevToken = prev.agentToken;

  const fresh = loadColdStartWorld();
  if (prevSymbol !== null && prevToken !== null) {
    registerAgent(fresh, { symbol: prevSymbol, faction: prevFaction ?? 'COSMIC', token: prevToken });
  }

  switch (opts.mode) {
    case 'income-entry': seedIncomeEntry(fresh, opts); break;
    case 'gate-entry': seedGateEntry(fresh, opts); break;
    default: seedColdEntry(fresh, opts); break;
  }

  current = fresh;
}

// ─── mode seeders (self-contained: only mutate `world`, no new loader deps) ──────────

/** cold/DATA: the current re-materialize behavior + the optional levers. */
function seedColdEntry(world: World, opts: ColdResetOptions): void {
  if (world.agent && typeof opts.credits === 'number') world.agent.credits = opts.credits;
  reconcileRoleCount(world, 'COMMAND', opts.frigates);
  reconcileRoleCount(world, 'SATELLITE', opts.probes);
  if (typeof opts.coverage === 'number') world.coverage = opts.coverage;
  if (Array.isArray(opts.preScoutedMarkets)) {
    for (const wp of opts.preScoutedMarkets) world.marketScouting.set(wp, { scouted: true, fresh: true });
  }
  if (typeof opts.probePrice === 'number') {
    setShipyardPrice(world, 'SHIP_PROBE', opts.probePrice);
    world.shipPrices = { ...(world.shipPrices ?? {}), SHIP_PROBE: opts.probePrice };
  }
}

/** income-entry: seed the INCOME view. haulers[] starts EMPTY — the daemon BUYS them (each
 *  PurchaseShip appends a hauler); pre-seeding would defeat the capital-gate / buy-count asserts.
 *  hubs default to the REAL marketplace waypoints from the loaded topology: the coordinator ranks
 *  its own contract hubs off the (seeded) market data and navigates haulers to those REAL waypoints
 *  (there are no logical H1..H5 on the real API), so world.hubs must be that same real population for
 *  a placed hauler's destination to match and set parkedHub (navigate parks at a hub ∈ world.hubs). */
function seedIncomeEntry(world: World, opts: IncomeResetOptions): void {
  if (world.agent) world.agent.credits = opts.credits ?? 600_000;
  world.hubs = opts.hubs ?? marketplaceWaypoints(world);
  world.frigateContractTagged = opts.frigateContractTagged ?? true;
  world.creditsPerHour = opts.creditsPerHour ?? 0;
  world.batchContractRunning = false;
  world.haulers = [];
  world.shipPrices = { ...(world.shipPrices ?? {}), SHIP_LIGHT_HAULER: opts.haulerPrice ?? 300_000 };
}

/** gate-entry: seed the GATE view. gateWorkers[] starts EMPTY (repurpose/buy fill it during the
 *  run); construction.percent never auto-advances. incomePerHour maps onto the ONE $/hr var. */
function seedGateEntry(world: World, opts: GateResetOptions): void {
  if (world.agent) world.agent.credits = opts.credits ?? 1_500_000;
  world.creditsPerHour = opts.incomePerHour ?? 50_000; // the SAME $/hr var as income creditsPerHour
  world.construction = {
    site: opts.gateSite ?? findJumpGate(world),
    percent: opts.constructionPercent ?? 0,
    started: false,
    adopted: false,
  };
  // The gate's stateful materials manifest (the real JUMP_GATE requires FAB_MATS +
  // ADVANCED_CIRCUITRY). GET reads it; supply mutates `fulfilled`. fulfilled starts at 0.
  world.constructionMaterials = GATE_MANIFEST.map((m): ConstructionMaterial => ({ ...m, fulfilled: 0 }));
  world.gateWorkers = [] as GateWorkerState[];
  world.executorRunning = opts.executorRunning ?? true;
  world.autosizerRunning = false;
  world.standingCoordinators = { siting: false, workerRebalancer: false };
  world.done = false;
  world.gateMaterialChains = opts.gateMaterialChains ?? 3;
  world.shipPrices = { ...(world.shipPrices ?? {}), SHIP_LIGHT_HAULER: opts.workerPrice ?? 300_000 };
  // The idle income fleet carried into GATE — the pool the coordinator repurposes into workers.
  const n = opts.haulers ?? 4;
  const agentSym = world.agent?.symbol ?? 'TWIN';
  world.haulers = Array.from({ length: n }, (_, i): HaulerState => ({
    symbol: `${agentSym}-H${i + 1}`, role: 'HAULER', parkedHub: null,
  }));
}

/** Force the number of hulls with `role` to `target` by cloning/removing (no-op when `target`
 *  is undefined — "not specified, leave as-is"). Clones the existing hull of that role so the
 *  extra ship is a faithful copy; skips silently if there is no hull to clone from. */
function reconcileRoleCount(world: World, role: string, target: number | undefined): void {
  if (typeof target !== 'number') return;
  const of = [...world.ships.values()].filter((s) => s.registration?.role === role);
  if (of.length === target) return;
  if (of.length > target) {
    for (const s of of.slice(target)) world.ships.delete(s.symbol);
    return;
  }
  const template = of[0];
  if (!template) return; // cannot fabricate a hull with no template to copy
  const agentSym = world.agent?.symbol ?? 'TWIN';
  for (let i = of.length; i < target; i++) {
    const symbol = `${agentSym}-${world.shipCounter++}`;
    const clone = structuredClone(template) as Ship;
    clone.symbol = symbol;
    world.ships.set(symbol, clone);
  }
}

/** Set the purchasePrice of every shipyard listing whose type is `shipType`. */
function setShipyardPrice(world: World, shipType: string, price: number): void {
  for (const sy of world.shipyards.values()) {
    for (const listing of sy.ships) {
      if (listing.type === shipType) listing.purchasePrice = price;
    }
  }
}

/** Every real MARKETPLACE waypoint in the loaded topology, in topology order. This is the candidate
 *  contract-hub population: the daemon's selectContractHubs ranks these same real waypoints (off the
 *  seeded market data) and navigates haulers to the top ones, so seeding world.hubs from here keeps
 *  the twin's hub set aligned with wherever the coordinator actually places a hauler. */
function marketplaceWaypoints(world: World): string[] {
  const out: string[] = [];
  for (const sys of world.systems.values()) {
    for (const wp of sys.waypoints.values()) {
      if (wp.traits?.some((t) => t.symbol === 'MARKETPLACE')) out.push(wp.symbol);
    }
  }
  return out;
}

/** The real JUMP_GATE waypoint symbol from the loaded topology (fixtures have exactly one). */
function findJumpGate(world: World): string {
  for (const sys of world.systems.values()) {
    for (const wp of sys.waypoints.values()) {
      if (wp.type === 'JUMP_GATE') return wp.symbol;
    }
  }
  return FALLBACK_JUMP_GATE;
}

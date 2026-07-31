import type { FastifyInstance } from 'fastify';
import type {
  Agent, ConstructionState, GateWorkerState, HaulerState,
  MutationLogEntry, Ship, ShipNav, StandingCoordinators,
} from '../world/types.js';
import { getWorld, resetWorld, type ResetOptions } from '../world/store.js';
import type { ClockMode } from '../clock.js';
import {
  advanceClock, getClockState, getCompression, resetClock, resolveNav,
  setClockMode, setCompression, setNow,
} from '../clock.js';
import { applyReport } from '../world/mutation-log.js';
import { serializeAgent } from '../world/serialize.js';
import { armFault, resetFaults } from '../world/faults.js';
import { badRequest } from '../errors.js';

// ─── GET /_twin/state — the FROZEN superset (one object; three typed views) ──────────
// BASE is always emitted; the INCOME and GATE views layer on top so a single /state read
// serves every phase (the harness reads phase-specific subsets). Ships are the resolveNav'd
// FULL Ship (DATA acceptance reads registration.role + full nav/cargo/frame) AUGMENTED with
// the harness-facing base view: top-level `role`, `scoutAssignment`, and `nav.waypoint`.

/** A ship as /state serves it: the full resolveNav'd Ship + the base-view projections. */
export type TwinStateShip = Omit<Ship, 'nav'> & {
  role: string;                         // == registration.role (harness base view)
  scoutAssignment: string | null;       // no world field tracks this yet -> null
  nav: ShipNav & { waypoint: string };  // `waypoint` mirrors waypointSymbol (harness base view)
};

/** Per-waypoint market scouting flags as the base view serves them (array, not a map). */
export interface TwinMarketView { waypoint: string; scouted: boolean; fresh: boolean }

/** The spec Agent as /_twin/state serves it — the full stored Agent PLUS the required shipCount,
 *  identical to GET /my/agent (serializeAgent). tests/agent.test.ts asserts the two deep-equal. */
export type AgentView = Agent & { shipCount: number };

/** BASE view — always present. `agent` is the FULL spec Agent incl. shipCount (superset of the
 *  harness's {credits}; DATA acceptance asserts GET /my/agent .data toEqual(state.agent)). */
export interface TwinStateBase {
  agent: AgentView | null;
  ships: TwinStateShip[];
  coverage: number;
  markets: TwinMarketView[];
  clock: { now: string; mode: ClockMode };
  mutationLog: MutationLogEntry[];
}

/** INCOME view (+) — layered on BASE. */
export interface TwinStateIncomeView {
  haulers: HaulerState[];
  frigateContractTagged: boolean;
  batchContractRunning: boolean;
  creditsPerHour: number;
  hubs: string[];
}

/** GATE view (+) — layered on BASE+INCOME. */
export interface TwinStateGateView {
  construction: ConstructionState;
  gateWorkers: GateWorkerState[];
  executorRunning: boolean;
  autosizerRunning: boolean;
  standingCoordinators: StandingCoordinators;
  done: boolean;
}

/** The single object GET /_twin/state emits: BASE + INCOME + GATE. */
export type TwinState = TwinStateBase & TwinStateIncomeView & TwinStateGateView;

/** Project one stored Ship into the /state ship view: resolveNav (single IN_TRANSIT->IN_ORBIT
 *  flip at arrival, read against the world clock `now`) + the base-view augmentations. */
function toStateShip(ship: Ship, transit: Parameters<typeof resolveNav>[1], now: Date, scoutAssigned: boolean): TwinStateShip {
  const resolved = resolveNav(ship, transit, now);
  // scout-all-markets is a daemon-internal fleet assignment (no /v2 call). Once the coordinator
  // reports scout-assign, every probe (SATELLITE) reads as scouting until the next reset.
  const scoutAssignment = scoutAssigned && resolved.registration.role === 'SATELLITE' ? 'scout-all-markets' : null;
  return {
    ...resolved,
    role: resolved.registration.role,
    scoutAssignment,
    nav: { ...resolved.nav, waypoint: resolved.nav.waypointSymbol },
  };
}

export async function adminRoutes(app: FastifyInstance): Promise<void> {
  // POST /_twin/reset — dispatch on `mode` (absent = cold/DATA), seed the world, then leave the
  // world clock FROZEN so each scenario starts deterministic. Response body is ignored by the
  // harness (2xx is all it checks); the `{ ok, world }` shape is kept for the skeleton tests.
  app.post<{ Body?: ResetOptions }>('/reset', async (req) => {
    resetWorld(req.body ?? {});
    resetClock();
    resetFaults(); // a fresh scenario never inherits a stale arm from a previous one
    const w = getWorld();
    return { ok: true, world: { agent: w.agent, shipCount: w.ships.size } };
  });

  app.get('/state', async (): Promise<TwinState> => {
    const w = getWorld();
    const now = new Date(); // ship arrival is on the REAL clock; the frozen world clock feeds `clock` below
    const ships = [...w.ships.values()].map((ship) => toStateShip(ship, w.transits.get(ship.symbol), now, w.scoutAssigned));
    const markets: TwinMarketView[] = [...w.marketScouting.entries()].map(
      ([waypoint, s]) => ({ waypoint, scouted: s.scouted, fresh: s.fresh }),
    );
    return {
      // BASE — the full serializeAgent shape (incl. shipCount) so it deep-equals GET /my/agent .data
      agent: w.agent ? (serializeAgent(w) as unknown as AgentView) : null,
      ships,
      coverage: w.coverage,
      markets,
      clock: getClockState(),
      mutationLog: w.mutationLog,
      // INCOME (+)
      haulers: w.haulers,
      frigateContractTagged: w.frigateContractTagged,
      batchContractRunning: w.batchContractRunning,
      creditsPerHour: w.creditsPerHour,
      hubs: w.hubs,
      // GATE (+)
      construction: w.construction,
      gateWorkers: w.gateWorkers,
      executorRunning: w.executorRunning,
      autosizerRunning: w.autosizerRunning,
      standingCoordinators: w.standingCoordinators,
      done: w.done,
    };
  });

  // ─── POST /_twin/clock — drive the T1 world-clock; returns the resulting {now} ─────
  // Fields are all optional and applied in a stable order: setNow (pin) -> advanceMs (step)
  // -> mode (frozen<->running, captured without jumping). The harness only ever sends
  // {advanceMs:1000}. Supersedes the retired POST /_twin/time-compression.
  app.post<{ Body?: { mode?: unknown; advanceMs?: unknown; setNow?: unknown } }>(
    '/clock',
    async (req, reply) => {
      const body = req.body ?? {};

      if (body.setNow !== undefined) {
        if (typeof body.setNow !== 'string') {
          return badRequest(reply, `setNow must be an rfc3339 string, got ${JSON.stringify(body.setNow)}`);
        }
        try {
          setNow(body.setNow);
        } catch {
          return badRequest(reply, `setNow: invalid instant ${JSON.stringify(body.setNow)}`);
        }
      }

      if (body.advanceMs !== undefined) {
        const ms = typeof body.advanceMs === 'number' ? body.advanceMs : Number(body.advanceMs);
        if (!Number.isFinite(ms)) {
          return badRequest(reply, `advanceMs must be a finite number, got ${JSON.stringify(body.advanceMs)}`);
        }
        advanceClock(ms);
      }

      if (body.mode !== undefined) {
        if (body.mode !== 'frozen' && body.mode !== 'running') {
          return badRequest(reply, `mode must be 'frozen' | 'running', got ${JSON.stringify(body.mode)}`);
        }
        setClockMode(body.mode);
      }

      return { now: getClockState().now };
    },
  );

  // ─── POST /_twin/time-compression — the ONE live travel-compression lever ─────────
  // Retunes the single time-compression knob (clock.ts) at runtime: travel + cooldown ETAs
  // are compressed INVERSELY by this factor. 1 = true real-API timing (fidelity), 20 = the
  // default fast-run, 100+ = very fast. env TWIN_TIME_COMPRESSION seeds it at boot; this route
  // changes it LIVE (no twin restart) so a scenario can dial fidelity up/down mid-run. The new
  // value applies to the NEXT navigate/cooldown (makeTransit reads getCompression() per call).
  // Echoes the resulting factor. Supersedes the previously-retired route of the same path.
  app.post<{ Body?: { compression?: unknown } }>('/time-compression', async (req, reply) => {
    const body = req.body ?? {};
    const compression = typeof body.compression === 'number' ? body.compression : Number(body.compression);
    if (!Number.isFinite(compression) || !(compression > 0)) {
      return badRequest(reply, `time-compression requires a positive number 'compression', got ${JSON.stringify(body.compression)}`);
    }
    setCompression(compression);
    return { compression: getCompression() };
  });

  // ─── POST /_twin/agent — overwrite the treasury (credits) directly ────────────────
  app.post<{ Body?: { credits?: unknown } }>('/agent', async (req, reply) => {
    const body = req.body ?? {};
    const credits = typeof body.credits === 'number' ? body.credits : Number(body.credits);
    if (!Number.isFinite(credits)) {
      return badRequest(reply, `agent requires numeric credits, got ${JSON.stringify(body.credits)}`);
    }
    const world = getWorld();
    if (world.agent) world.agent.credits = credits;
    return reply.code(204).send();
  });

  // ─── POST /_twin/markets/coverage — set coverage / mark waypoints scouted+fresh ───
  // Either field is optional; when both are sent, coverage is set AND the waypoints are
  // marked. Always echoes the resulting coverage fraction (not void, per the contract).
  app.post<{ Body?: { fraction?: unknown; scoutWaypoints?: unknown } }>(
    '/markets/coverage',
    async (req, reply) => {
      const body = req.body ?? {};
      const world = getWorld();

      if (body.fraction !== undefined) {
        const fraction = typeof body.fraction === 'number' ? body.fraction : Number(body.fraction);
        if (!Number.isFinite(fraction)) {
          return badRequest(reply, `coverage fraction must be numeric, got ${JSON.stringify(body.fraction)}`);
        }
        world.coverage = fraction;
      }

      if (body.scoutWaypoints !== undefined) {
        if (!Array.isArray(body.scoutWaypoints)) {
          return badRequest(reply, `scoutWaypoints must be an array, got ${JSON.stringify(body.scoutWaypoints)}`);
        }
        for (const wp of body.scoutWaypoints) {
          if (typeof wp === 'string') world.marketScouting.set(wp, { scouted: true, fresh: true });
        }
      }

      return { coverage: world.coverage };
    },
  );

  // ─── POST /_twin/income — set the ONE $/hr var (== gate incomePerHour) ────────────
  app.post<{ Body?: { creditsPerHour?: unknown } }>('/income', async (req, reply) => {
    const body = req.body ?? {};
    const creditsPerHour = typeof body.creditsPerHour === 'number' ? body.creditsPerHour : Number(body.creditsPerHour);
    if (!Number.isFinite(creditsPerHour)) {
      return badRequest(reply, `income requires numeric creditsPerHour, got ${JSON.stringify(body.creditsPerHour)}`);
    }
    getWorld().creditsPerHour = creditsPerHour;
    return reply.code(204).send();
  });

  // ─── POST /_twin/construction — set construction.percent (NEVER auto-advances) ────
  // Sets the /_twin/state percent (the harness's progress view) AND, at completion, drives the
  // /v2-served construction manifest (world.constructionMaterials) to fully supplied so GET
  // …/construction reports isComplete — the ONLY completion signal the daemon observes (it never
  // reads world.construction.percent). Without that the lever was a no-op to the daemon and the gate
  // never derived COMPLETE (st-drm.19 BUG B). The frozen GET /_twin/state construction view stays
  // exactly {site, percent, started, adopted}; constructionMaterials is off that superset.
  app.post<{ Body?: { percent?: unknown } }>('/construction', async (req, reply) => {
    const body = req.body ?? {};
    const percent = typeof body.percent === 'number' ? body.percent : Number(body.percent);
    if (!Number.isFinite(percent)) {
      return badRequest(reply, `construction requires numeric percent, got ${JSON.stringify(body.percent)}`);
    }
    const world = getWorld();
    world.construction.percent = percent;
    world.constructionIsComplete = percent >= 100;
    // At completion, reach the SAME served state a real supply chain does: every material met, so
    // serializeConstruction (world/serialize.ts) derives isComplete=true on GET /v2 …/construction.
    if (percent >= 100 && world.constructionMaterials) {
      for (const material of world.constructionMaterials) {
        material.fulfilled = material.required;
      }
    }
    return reply.code(204).send();
  });

  // ─── POST /_twin/fault — arm the next `count` matching /v2 requests to return `code` ─
  // Checked by the preHandler registered on the /v2 plugin (server.ts); self-clears after
  // the Nth match (world/faults.ts:consumeFault). endpoint is "METHOD /path", path relative
  // to /v2 (e.g. "GET /my/ships" — matches bootstrap-harness/tests/data/fail-closed.e2e.test.ts).
  app.post<{ Body?: { endpoint?: unknown; code?: unknown; count?: unknown } }>('/fault', async (req, reply) => {
    const body = req.body ?? {};
    const raw = typeof body.endpoint === 'string' ? body.endpoint.trim() : '';
    const spaceAt = raw.indexOf(' ');
    const method = spaceAt === -1 ? raw : raw.slice(0, spaceAt);
    const path = spaceAt === -1 ? '' : raw.slice(spaceAt + 1).trim();
    if (method === '' || !path.startsWith('/')) {
      return badRequest(reply, `fault requires endpoint 'METHOD /path', got ${JSON.stringify(body.endpoint)}`);
    }
    const code = typeof body.code === 'number' ? body.code : Number(body.code);
    const count = typeof body.count === 'number' ? body.count : Number(body.count);
    if (!Number.isInteger(code)) {
      return badRequest(reply, `fault requires an integer code, got ${JSON.stringify(body.code)}`);
    }
    if (!Number.isInteger(count) || count < 1) {
      return badRequest(reply, `fault requires an integer count >= 1, got ${JSON.stringify(body.count)}`);
    }
    armFault(method, path, code, count);
    return reply.code(204).send();
  });

  // ─── POST /_twin/report — the daemon->twin seam for the DAEMON-INTERNAL ops ─────────
  // The bootstrap coordinator POSTs {call, detail?} when it fires one of the seven daemon-internal
  // ops (fleet-unassign / batch-contract / construction-start / executor-bounce / launch-autosizer
  // / launch-siting / launch-worker-rebalancer) — but ONLY when its API base is the twin (test-gated;
  // prod is unchanged). applyReport appends the mutation-log entry AND flips the paired /_twin/state
  // flag as one atomic, exactly-once unit: the flag is the guard, so a duplicate report (retry, or a
  // daemon kill+reboot mid-run) is a pure no-op. An unrecognized call is a harmless 2xx no-op
  // (applyReport ignores it) — repurpose in particular is STATE-ONLY and never reported here. Void.
  app.post<{ Body?: { call?: unknown; detail?: unknown } }>('/report', async (req, reply) => {
    const body = req.body ?? {};
    if (typeof body.call !== 'string' || body.call === '') {
      return badRequest(reply, `report requires a non-empty string 'call', got ${JSON.stringify(body.call)}`);
    }
    const detail = body.detail !== null && typeof body.detail === 'object'
      ? (body.detail as Record<string, unknown>)
      : undefined;
    applyReport(getWorld(), { call: body.call, detail });
    return reply.code(204).send();
  });
}

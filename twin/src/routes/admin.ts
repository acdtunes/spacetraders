import type { FastifyInstance } from 'fastify';
import type {
  Agent, ConstructionState, GateWorkerState, HaulerState,
  MutationLogEntry, Ship, ShipNav, StandingCoordinators,
} from '../world/types.js';
import { getWorld, resetWorld } from '../world/store.js';
import type { ClockMode } from '../clock.js';
import { advanceClock, getClockState, getNow, resolveNav, setClockMode, setNow } from '../clock.js';
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

/** BASE view — always present. `agent` is the FULL Agent (superset of the harness's {credits};
 *  DATA acceptance asserts GET /my/agent .data toEqual(state.agent)). */
export interface TwinStateBase {
  agent: Agent | null;
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
function toStateShip(ship: Ship, transit: Parameters<typeof resolveNav>[1], now: Date): TwinStateShip {
  const resolved = resolveNav(ship, transit, now);
  return {
    ...resolved,
    role: resolved.registration.role,
    scoutAssignment: null,
    nav: { ...resolved.nav, waypoint: resolved.nav.waypointSymbol },
  };
}

export async function adminRoutes(app: FastifyInstance): Promise<void> {
  app.post('/reset', async () => {
    resetWorld();
    const w = getWorld();
    return { ok: true, world: { agent: w.agent, shipCount: w.ships.size } };
  });

  app.get('/state', async (): Promise<TwinState> => {
    const w = getWorld();
    const now = getNow();
    const ships = [...w.ships.values()].map((ship) => toStateShip(ship, w.transits.get(ship.symbol), now));
    const markets: TwinMarketView[] = [...w.marketScouting.entries()].map(
      ([waypoint, s]) => ({ waypoint, scouted: s.scouted, fresh: s.fresh }),
    );
    return {
      // BASE
      agent: w.agent,
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
}

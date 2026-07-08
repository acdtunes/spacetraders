/**
 * Demo-mode Operational Pulse synthesizers (pure).
 *
 * Everything in this module is a pure function of the caller's clock (`nowMs`,
 * Unix epoch milliseconds) — no timers, no module state, no I/O — so the demo
 * /bot/* handlers in services/api/mockClient.ts stay deterministic and unit
 * testable. Three synthesizers:
 *
 *   demoEventTicker(nowMs)   — one synthetic FleetEvent every ~8s, cycling the
 *                              REAL gobot captain_events type strings with
 *                              monotonically increasing ids.
 *   demoGateProgress(nowMs)  — a jump-gate construction bill whose progress
 *                              ticks up monotonically (+0.1 per delivery-type
 *                              event) toward 100.
 *   isSignalLossWindow(nowMs)— a deliberate ~20s "backend dark" drill every
 *                              ~3min so the connection-health heartbeat can be
 *                              seen flipping to 'lost', backing off, and
 *                              auto-recovering — gated behind DEMO_SIGNAL_LOSS.
 *
 * The event `type` strings MUST stay the real gobot ones
 * (gobot/internal/domain/captain/events.go) — domain/eventFx keys its scene FX
 * off them, and inventing demo-only strings would silently demote every effect
 * to a feed-only generic ping.
 */

import type { FleetEvent, GateProgress } from '../types/spacetraders';
import { mockState } from './mockScenario';

/** One synthetic captain event appears every ~8s. */
export const DEMO_EVENT_INTERVAL_MS = 8_000;

/** Mirror of the live server's LIMIT LEAST($2, 200) hard cap. */
export const DEMO_EVENT_MAX_LIMIT = 200;

/**
 * The demo event script: REAL gobot captain_events type strings, arranged so
 * scene-FX kinds (arrival-ripple / gate-flash / income-ping — see
 * domain/eventFx) dominate the cadence and something visibly fires roughly
 * every ~8s, with a few feed-only types mixed in for feed variety.
 */
export const DEMO_EVENT_CYCLE: readonly string[] = [
  'workflow.finished', //  0 — arrival-ripple at the named ship
  'contract.completed', // 1 — gate-flash + one gate delivery tick
  'credits.threshold', //  2 — income-ping at the named ship
  'workflow.finished', //  3
  'ship.idle', //          4 — feed-only, ship-scoped
  'credits.threshold', //  5
  'workflow.finished', //  6
  'contract.completed', // 7
  'income.stalled', //     8 — feed-only, fleet-wide
  'credits.threshold', //  9
  'workflow.finished', // 10
  'deploy.completed', //  11 — feed-only, fleet-wide
];

/**
 * Event types that name a triggering ship (`ship` column non-null). Everything
 * else in the cycle is fleet-wide and carries ship: null — exactly how the
 * real captain emits them (contract.* / income.* / deploy.* have no single
 * triggering hull).
 */
export const DEMO_SHIP_SCOPED_TYPES: ReadonlySet<string> = new Set([
  'workflow.finished',
  'credits.threshold',
  'ship.idle',
]);

/**
 * Highest event id that exists at `nowMs`: a new id is minted every
 * DEMO_EVENT_INTERVAL_MS, so ids increase monotonically with the clock.
 */
export function demoLatestEventId(nowMs: number): number {
  return Math.max(0, Math.floor(nowMs / DEMO_EVENT_INTERVAL_MS));
}

/**
 * The deterministic event for one id. Type cycles through DEMO_EVENT_CYCLE;
 * ship-scoped types draw a hull from the mock fleet (round-robin by id) so
 * scene FX land on ships that are actually on the demo map.
 */
export function demoEventForId(id: number): FleetEvent {
  const type = DEMO_EVENT_CYCLE[id % DEMO_EVENT_CYCLE.length];
  const ships = mockState.ships;
  const ship =
    DEMO_SHIP_SCOPED_TYPES.has(type) && ships.length > 0
      ? ships[id % ships.length].symbol
      : null;
  return {
    id,
    type,
    ship,
    createdAt: new Date(id * DEMO_EVENT_INTERVAL_MS).toISOString(),
    processed: false,
  };
}

export interface DemoEventQuery {
  /** Cursor: only events with a strictly higher id are returned. */
  afterId?: number | null;
  /** Page size; defaults to 50 and is hard-capped at DEMO_EVENT_MAX_LIMIT. */
  limit?: number;
}

/**
 * The synthetic /bot/events feed: newest-first, one new event per ~8s bucket,
 * honoring the same `after` cursor + `limit` contract as the live endpoint so
 * the polling service's delta-fetch logic behaves identically in demo mode.
 */
export function demoEventTicker(nowMs: number, query: DemoEventQuery = {}): FleetEvent[] {
  const { afterId = null, limit = 50 } = query;
  const latest = demoLatestEventId(nowMs);
  const cap = Math.min(Math.max(Math.floor(limit) || 50, 1), DEMO_EVENT_MAX_LIMIT);
  const floor = Math.max(0, afterId ?? 0);

  const events: FleetEvent[] = [];
  for (let id = latest; id > floor && events.length < cap; id--) {
    events.push(demoEventForId(id));
  }
  return events;
}

// ==================== Jump-gate construction ====================

/** The event type that advances the gate bill (contracts fund the gate). */
export const DEMO_GATE_DELIVERY_TYPE = 'contract.completed';

/**
 * Demo construction era origin: deliveries are counted from here, so the bar
 * opens at DEMO_GATE_BASE_PROGRESS and creeps upward. A monotone bounded bar
 * necessarily saturates: at +0.1 per delivery (one delivery per ~48s of wall
 * clock) the bill completes ~5h past this epoch and then honestly reads 100
 * ("gate complete"). Bump this constant to re-arm the construction narrative
 * for a live demo day.
 */
export const DEMO_GATE_EPOCH_MS = Date.UTC(2026, 6, 1); // 2026-07-01T00:00:00Z

/** Where the bill stands at the demo epoch (percent). */
export const DEMO_GATE_BASE_PROGRESS = 62.5;

/** Progress percent gained per delivery-type event. */
export const DEMO_GATE_TICK_PER_DELIVERY = 0.1;

/**
 * A plausible jump-gate bill (real SpaceTraders gate materials). Total 5000
 * units so 1% of progress is exactly 50 units.
 */
const DEMO_GATE_BILL: ReadonlyArray<{ tradeSymbol: string; required: number }> = [
  { tradeSymbol: 'FAB_MATS', required: 3500 },
  { tradeSymbol: 'ADVANCED_CIRCUITRY', required: 1400 },
  { tradeSymbol: 'QUANTUM_STABILIZERS', required: 100 },
];

const DELIVERIES_PER_CYCLE = DEMO_EVENT_CYCLE.filter(
  (type) => type === DEMO_GATE_DELIVERY_TYPE
).length;

/** How many delivery-type events exist among ids 1..id (O(cycle length)). */
function deliveriesUpToId(id: number): number {
  if (id <= 0) return 0;
  const n = DEMO_EVENT_CYCLE.length;
  const fullCycles = Math.floor(id / n);
  let count = fullCycles * DELIVERIES_PER_CYCLE;
  for (let i = fullCycles * n + 1; i <= id; i++) {
    if (DEMO_EVENT_CYCLE[i % n] === DEMO_GATE_DELIVERY_TYPE) count++;
  }
  return count;
}

/** Delivery-type events emitted since the demo gate epoch (0 before it). */
export function demoDeliveryCount(nowMs: number): number {
  const upToNow = deliveriesUpToId(demoLatestEventId(nowMs));
  const upToEpoch = deliveriesUpToId(demoLatestEventId(DEMO_GATE_EPOCH_MS));
  return Math.max(0, upToNow - upToEpoch);
}

/**
 * Synthetic /bot/construction/:wp payload. `progress` ticks up monotonically —
 * +0.1 per delivery-type event in the ticker — toward 100 (clamped), and the
 * per-material fulfillment is kept unit-consistent with it (materials fill in
 * bill order, exactly round(total * progress / 100) units delivered overall).
 */
export function demoGateProgress(nowMs: number): GateProgress {
  const raw =
    DEMO_GATE_BASE_PROGRESS + DEMO_GATE_TICK_PER_DELIVERY * demoDeliveryCount(nowMs);
  // One-decimal ticks read cleanly in the HUD; Math.round is monotone, so the
  // "never decreases" guarantee survives the tidy-up.
  const progress = Math.min(100, Math.round(raw * 10) / 10);

  const totalRequired = DEMO_GATE_BILL.reduce((sum, m) => sum + m.required, 0);
  let unitsLeft = Math.round((totalRequired * progress) / 100);
  const materials = DEMO_GATE_BILL.map((m) => {
    const fulfilled = Math.min(m.required, unitsLeft);
    unitsLeft -= fulfilled;
    return { tradeSymbol: m.tradeSymbol, required: m.required, fulfilled };
  });

  return { progress, materials };
}

// ==================== Signal-loss drill ====================

/**
 * Master switch for the simulated outage. With it on, every /bot/* mock
 * request throws during the window below, so the polling heartbeat flips
 * connection-health to 'lost', engages exponential backoff, and auto-recovers
 * once the window passes. Flip to false for demos that must never blink.
 */
export const DEMO_SIGNAL_LOSS = true;

/** A loss window recurs every ~3 minutes... */
export const DEMO_SIGNAL_LOSS_PERIOD_MS = 180_000;

/** ...and lasts ~20 seconds. */
export const DEMO_SIGNAL_LOSS_DURATION_MS = 20_000;

/**
 * True while the demo backend is deliberately dark. The window sits at the END
 * of each period (phase [160s, 180s) of every 3-minute cycle), so a demo
 * starting near a period boundary gets a healthy stretch before the first
 * drill. `enabled` defaults to the DEMO_SIGNAL_LOSS gate and exists as a
 * parameter purely so tests can exercise the off position.
 */
export function isSignalLossWindow(nowMs: number, enabled: boolean = DEMO_SIGNAL_LOSS): boolean {
  if (!enabled) return false;
  const period = DEMO_SIGNAL_LOSS_PERIOD_MS;
  // Euclidean modulo: well-defined for pre-epoch (negative) clocks too.
  const phase = ((nowMs % period) + period) % period;
  return phase >= period - DEMO_SIGNAL_LOSS_DURATION_MS;
}

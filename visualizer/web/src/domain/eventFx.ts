/**
 * Event drama FX (pure).
 *
 * Maps raw gobot `captain_events` rows (see FleetEvent) to short-lived scene
 * effects the Konva EventFxLayer renders on the map:
 *
 *   'arrival-ripple' — workflow.finished: an expanding ring at the ship that
 *                      just completed its workflow (it "arrived").
 *   'gate-flash'     — contract.completed: a flash at the jump-gate
 *                      construction site (contracts fund the gate).
 *   'income-ping'    — credits.threshold: a small credit blip, anchored to the
 *                      triggering ship when the event names one.
 *   'generic-ping'   — every other event type (workflow.failed,
 *                      container.crashed / crashloop / heartbeat_lost,
 *                      ship.idle, income.stalled, stream.down, contract.failed,
 *                      deploy.completed, …). These are feed-only: they have no
 *                      scene anchor, so fxForEvent yields null and they surface
 *                      only in the event feed HUD.
 *
 * Everything here is pure and frame-clock agnostic: `now` is whatever
 * monotonic-ish millisecond clock the caller prunes with (the layer uses the
 * shared frameTimestamp), so spawn and prune can never drift apart.
 */

import type { FleetEvent } from '../types/spacetraders';

export type FxKind = 'arrival-ripple' | 'gate-flash' | 'income-ping' | 'generic-ping';

/** Where a given FX kind anchors in the scene (what its position means). */
export type FxAnchor = 'ship' | 'gate' | 'none';

export interface FxPoint {
  x: number;
  y: number;
}

/** One live effect instance, spawned once per new event id. */
export interface FxInstance {
  /** Stable render key — always String(event.id). */
  key: string;
  kind: FxKind;
  x: number;
  y: number;
  /** Clock value at spawn (same timebase the caller prunes with). */
  bornAt: number;
  ttlMs: number;
}

/** Effect lifetimes. 'generic-ping' never reaches the scene (anchor 'none'). */
export const FX_TTL_MS: Record<FxKind, number> = {
  'arrival-ripple': 2400,
  'gate-flash': 3000,
  'income-ping': 1500,
  'generic-ping': 1500,
};

/** Raw gobot captain_events type string -> FX kind. Unknown types are pings. */
export function fxKindForEventType(type: string): FxKind {
  switch (type) {
    case 'workflow.finished':
      return 'arrival-ripple';
    case 'contract.completed':
      return 'gate-flash';
    case 'credits.threshold':
      return 'income-ping';
    default:
      return 'generic-ping';
  }
}

/**
 * Scene anchor per kind. 'none' is the feed-only contract: the layer's
 * resolver returns null for such events, so no FX instance is ever spawned.
 */
export function fxAnchor(kind: FxKind): FxAnchor {
  switch (kind) {
    case 'arrival-ripple':
    case 'income-ping':
      return 'ship';
    case 'gate-flash':
      return 'gate';
    case 'generic-ping':
      return 'none';
  }
}

/**
 * Resolve the jump-gate waypoint a gate-flash anchors to. Prefers the
 * current-era construction site (preferredSymbol / GATE_WAYPOINT) when it is in
 * the map, but falls back to the FIRST waypoint of type 'JUMP_GATE'. Without
 * this fallback the anchor is invisible whenever preferredSymbol is absent —
 * notably demo mode, whose waypoints are in the X1-DEMO / X1-GRID / X1-TWIN
 * families and never include the live X1-PZ28 gate, so every contract.completed
 * silently produced no FX. Also hardens live mode against GATE_WAYPOINT drift.
 */
export function findGateWaypoint<W extends { type: string }>(
  waypoints: Map<string, W>,
  preferredSymbol: string
): W | undefined {
  return (
    waypoints.get(preferredSymbol) ??
    [...waypoints.values()].find((w) => w.type === 'JUMP_GATE')
  );
}

/**
 * Position resolver contract the layer supplies: given the event and its
 * mapped kind, return the world-space anchor — or null when the event has no
 * scene anchor (feed-only kind, fleet-wide event with ship=null, ship not on
 * the current map, gate waypoint not loaded).
 */
export type FxPositionResolver = (event: FleetEvent, kind: FxKind) => FxPoint | null;

/**
 * Build the FX instance for one event, or null when the event is feed-only
 * (a null position from the resolver is always tolerated — no scene FX).
 */
export function fxForEvent(
  event: FleetEvent,
  resolvePos: FxPositionResolver,
  now: number
): FxInstance | null {
  const kind = fxKindForEventType(event.type);
  const pos = resolvePos(event, kind);
  if (!pos) return null;
  return {
    key: String(event.id),
    kind,
    x: pos.x,
    y: pos.y,
    bornAt: now,
    ttlMs: FX_TTL_MS[kind],
  };
}

/** Normalized age of an effect, clamped to [0, 1] (1 = fully expired). */
export function fxProgress(fx: FxInstance, now: number): number {
  if (fx.ttlMs <= 0) return 1;
  return Math.min(1, Math.max(0, (now - fx.bornAt) / fx.ttlMs));
}

/**
 * Drop expired instances (age >= ttl). Called every frame, so it preserves the
 * input array's identity when nothing expired to avoid needless churn.
 */
export function pruneFx(list: FxInstance[], now: number): FxInstance[] {
  if (list.length === 0) return list;
  const alive = list.filter((fx) => now - fx.bornAt < fx.ttlMs);
  return alive.length === list.length ? list : alive;
}

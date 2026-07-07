import type { ShipTrailPoint } from '../types/spacetraders';

export const TRAIL_MAX_POINTS = 120;
export const TRAIL_FADE_MS = 60_000;

// Single source of truth for the bounded, time-faded trail buffer: drop points
// older than the fade window, then cap to `maxPoints` (newest kept). useStore's
// addTrailPosition wraps this with a min-distance gate, so the runtime append
// path and these unit tests exercise the same buffer logic.
export function appendTrailPoint(
  points: ShipTrailPoint[],
  next: ShipTrailPoint,
  maxPoints = TRAIL_MAX_POINTS,
): ShipTrailPoint[] {
  const fresh = points.filter((p) => next.timestamp - p.timestamp < TRAIL_FADE_MS);
  fresh.push(next);
  return fresh.length > maxPoints ? fresh.slice(fresh.length - maxPoints) : fresh;
}

export function trailOpacity(point: { timestamp: number }, nowMs: number): number {
  return Math.max(0, Math.min(1, 1 - (nowMs - point.timestamp) / TRAIL_FADE_MS));
}

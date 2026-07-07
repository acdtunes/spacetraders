/**
 * Sprite glow helpers for the Noir observatory restyle.
 *
 * Pure math so the flicker and the "which waypoints glow" rule can be tested
 * without a Konva stage; the components below only wire these into nodes.
 */

/**
 * Steady engine-glow opacity used when the viewer prefers reduced motion.
 * Sits mid-range in the animated band so the glow still reads without flicker.
 */
export const ENGINE_GLOW_STEADY = 0.7;

/**
 * Stern engine glow opacity — a fast flicker driven by the frame clock.
 * Bounded to [0.55, 0.9] so the glow always reads without ever going opaque.
 * When `reducedMotion` is set the flicker is pinned to a constant, honoring the
 * same prefers-reduced-motion contract the CSS layer can't reach on a canvas.
 */
export function enginePulse(frameTimestamp: number, reducedMotion = false): number {
  if (reducedMotion) {
    return ENGINE_GLOW_STEADY;
  }
  return 0.55 + 0.35 * Math.abs(Math.sin(frameTimestamp / 90));
}

/**
 * Waypoint types that receive a Noir atmosphere rim glow. Mirrors the
 * planet / gas-giant substring checks used by `Waypoint.getRadius`.
 */
export function hasRimGlow(type: string): boolean {
  return type.includes('PLANET') || type.includes('GAS_GIANT');
}

// Pure camera math for the cinematic idle drift + ease-to-target motion.
// Time is always an explicit parameter (never Date.now) so the motion is
// deterministic and testable frame-by-frame.

export interface CameraPose { x: number; y: number; scale: number }

// Idle-drift envelope: the camera never wanders more than ±DRIFT_X / ±DRIFT_Y
// px from the base pose, and the breathing zoom stays within
// [base·(1-BREATH_AMP), base·(1+BREATH_AMP)] — a zero-mean swing centred on base.
export const DRIFT_X = 105;
export const DRIFT_Y = 70;
export const DRIFT_PERIOD_MS = 30_000;
export const BREATH_PERIOD_MS = 20_000;
// Maximum fractional zoom deviation on *either* side of base — a symmetric,
// zero-mean swing rather than the old one-sided [base, base·(1+amp)]: one-sided
// breathing let every re-anchor that captured a drifted pose bake the peak zoom
// in, ratcheting base scale upward across idle/select/manual cycles.
export const BREATH_AMP = 0.045;

// A slow wander around `base` on two axes with different periods, plus an
// out-of-phase breathing zoom. Every component is individually bounded and
// continuous; the differing periods keep the composed path from visibly looping.
// All three terms are zero at t=0, so idleDrift(0, base) === base exactly: every
// (re)anchor (mount, ease-settle, grace expiry) resets the epoch to now and thus
// resumes drift *from rest*, never lurching by a fixed offset toward the first
// drift target.
export function idleDrift(tMs: number, base: CameraPose): CameraPose {
  const a = (tMs / DRIFT_PERIOD_MS) * Math.PI * 2;
  const b = (tMs / BREATH_PERIOD_MS) * Math.PI * 2;
  return {
    x: base.x + Math.sin(a) * DRIFT_X,
    y: base.y + Math.sin(a * 0.7) * DRIFT_Y,
    scale: base.scale * (1 + Math.sin(b) * BREATH_AMP),
  };
}

// Frame-rate-independent exponential approach. `k` is the fraction of the
// remaining gap closed this frame; because k ∈ [0, 1) for any dt ≥ 0 the result
// is a convex blend of current→target and can never overshoot. Negative dt is
// clamped, so a stray backwards timestamp simply holds the pose.
export function easeToward(current: CameraPose, target: CameraPose, dtMs: number, tauMs = 450): CameraPose {
  const k = 1 - Math.exp(-Math.max(0, dtMs) / tauMs);
  return {
    x: current.x + (target.x - current.x) * k,
    y: current.y + (target.y - current.y) * k,
    scale: current.scale + (target.scale - current.scale) * k,
  };
}

// True once the two poses are within a sub-pixel / sub-percent step of each
// other — the signal for an ease to settle and hand back to idle.
export function poseSettled(a: CameraPose, b: CameraPose): boolean {
  return Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5 && Math.abs(a.scale - b.scale) < 0.005;
}

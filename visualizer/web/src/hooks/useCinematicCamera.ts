import { useCallback, useRef, useState } from 'react';
import type Konva from 'konva';
import { idleDrift, easeToward, poseSettled } from '../domain/camera';
import type { CameraPose } from '../domain/camera';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

export type CinematicCameraMode = 'idle' | 'easing' | 'follow' | 'manual';

export interface CinematicCamera {
  mode: CinematicCameraMode;
  // Called once per animation tick with a monotonic clock (performance.now).
  // Drives the map's content Layer — the same transform every manual pan/zoom
  // already reads and writes — so a manual gesture simply resumes from wherever
  // the camera left the layer (rather than compounding over a separate node).
  // Returns whether it wrote the node this frame (false while a manual gesture
  // owns the camera), so callers can throttle a viewport resync off real motion.
  applyFrame(nowMs: number, node: Konva.Layer): boolean;
  // Glide the node to a pose (e.g. centre a selected ship), then hand back to idle.
  easeTo(pose: CameraPose): void;
  follow(getTarget: () => CameraPose | null): void;
  stopFollow(): void;
  // Wheel / drag / keyboard: manual wins instantly and holds the camera off for
  // `manualGraceMs` before idle drift resumes from the user's left pose.
  notifyManual(): void;
  // Press-and-hold gate for drags: while held, manual mode is pinned open past the
  // grace window (Konva emits no dragmove while the pointer is held still, so a
  // long motionless hold must not let idle drift resume under the user's cursor).
  // Call with true on drag start and false on drag end.
  setManualHeld(held: boolean): void;
}

const DEFAULT_MANUAL_GRACE_MS = 20_000;
const FIRST_FRAME_DT_MS = 16;

export function useCinematicCamera(opts?: { manualGraceMs?: number }): CinematicCamera {
  const grace = opts?.manualGraceMs ?? DEFAULT_MANUAL_GRACE_MS;

  // Reduced-motion holds the base pose (no drift); easing/selection still glide.
  // matchMedia is absent under jsdom, so this reads false in tests.
  const reducedMotion = usePrefersReducedMotion();
  const reducedMotionRef = useRef(reducedMotion);
  reducedMotionRef.current = reducedMotion;

  const [mode, setModeState] = useState<CinematicCameraMode>('idle');
  const modeRef = useRef<CinematicCameraMode>('idle');
  const baseRef = useRef<CameraPose>({ x: 0, y: 0, scale: 1 });
  const driftEpochRef = useRef<number | null>(null);
  const targetRef = useRef<CameraPose | null>(null);
  const followRef = useRef<(() => CameraPose | null) | null>(null);
  // manualAt is stamped from applyFrame's own clock (not performance.now at the
  // event) so the grace window is measured in one consistent time base.
  const manualAtRef = useRef<number | null>(null);
  const manualPendingRef = useRef<boolean>(false);
  // True while a drag is physically held (set on drag start, cleared on drag end).
  const manualHeldRef = useRef<boolean>(false);
  const reanchorPendingRef = useRef<boolean>(false);
  const lastMsRef = useRef<number | null>(null);
  const initializedRef = useRef<boolean>(false);

  const setMode = useCallback((next: CinematicCameraMode) => {
    modeRef.current = next;
    setModeState(next);
  }, []);

  const readPose = (node: Konva.Layer): CameraPose => {
    const p = node.position();
    const s = node.scale();
    return { x: p.x, y: p.y, scale: s.x };
  };
  const writePose = (node: Konva.Layer, pose: CameraPose): void => {
    node.position({ x: pose.x, y: pose.y });
    node.scale({ x: pose.scale, y: pose.scale });
    node.batchDraw();
  };

  const applyFrame = useCallback(
    (nowMs: number, node: Konva.Layer): boolean => {
      // Hidden tabs: honour the plan-wide "pause on document.hidden" constraint.
      // The rAF that drives us is already throttled while hidden; this also stops
      // any stray call from writing. lastMs is advanced so dt stays sane on return.
      if (typeof document !== 'undefined' && document.hidden) {
        lastMsRef.current = nowMs;
        return false;
      }

      const dt =
        lastMsRef.current === null ? FIRST_FRAME_DT_MS : Math.max(0, nowMs - lastMsRef.current);
      lastMsRef.current = nowMs;

      // Anchor the drift to the node's real pose on the first frame, so idle drift
      // orbits wherever the scene actually starts (centred cluster) — not (0,0,1).
      if (!initializedRef.current) {
        baseRef.current = readPose(node);
        driftEpochRef.current = nowMs;
        initializedRef.current = true;
      }

      if (manualPendingRef.current) {
        manualAtRef.current = nowMs;
        manualPendingRef.current = false;
      }

      if (modeRef.current === 'manual') {
        // A physically held drag keeps the camera off indefinitely: Konva emits no
        // dragmove while the pointer is held still, so without this a motionless
        // hold longer than the grace window would let idle drift resume under the
        // user's cursor. Keep the grace stamp pinned to now until release, so the
        // normal post-gesture grace runs from when the user lets go.
        if (manualHeldRef.current) {
          manualAtRef.current = nowMs;
          return false;
        }
        if (manualAtRef.current !== null && nowMs - manualAtRef.current < grace) {
          return false; // user owns the camera — zero writes this frame
        }
        // Grace expired: resume idle drift from exactly where the user left it.
        baseRef.current = readPose(node);
        driftEpochRef.current = nowMs;
        setMode('idle');
      }

      if (modeRef.current === 'follow' && followRef.current) {
        const target = followRef.current();
        if (target) {
          writePose(node, easeToward(readPose(node), target, dt));
          return true;
        }
        return false;
      }

      if (modeRef.current === 'easing' && targetRef.current) {
        const next = easeToward(readPose(node), targetRef.current, dt);
        writePose(node, next);
        if (poseSettled(next, targetRef.current)) {
          baseRef.current = targetRef.current;
          targetRef.current = null;
          driftEpochRef.current = nowMs;
          setMode('idle');
        }
        return true;
      }

      // Idle. stopFollow leaves the node off its old base, so re-anchor on entry.
      if (reanchorPendingRef.current) {
        baseRef.current = readPose(node);
        driftEpochRef.current = nowMs;
        reanchorPendingRef.current = false;
      }
      const base = baseRef.current;
      if (reducedMotionRef.current) {
        writePose(node, easeToward(readPose(node), base, dt));
        return true;
      }
      const elapsed = nowMs - (driftEpochRef.current ?? nowMs);
      // Ease toward the drift target rather than snapping onto it, so entering
      // idle (mount, or grace expiry) glides in from the current pose seamlessly.
      writePose(node, easeToward(readPose(node), idleDrift(elapsed, base), dt));
      return true;
    },
    [grace, setMode]
  );

  const easeTo = useCallback(
    (pose: CameraPose): void => {
      targetRef.current = pose;
      setMode('easing');
    },
    [setMode]
  );

  const follow = useCallback(
    (getTarget: () => CameraPose | null): void => {
      followRef.current = getTarget;
      setMode('follow');
    },
    [setMode]
  );

  const stopFollow = useCallback((): void => {
    // No-op unless a follow is actually active, so callers (e.g. deselect) can fire
    // it unconditionally without interrupting an in-flight ease or the idle drift.
    if (modeRef.current !== 'follow') return;
    followRef.current = null;
    reanchorPendingRef.current = true;
    setMode('idle');
  }, [setMode]);

  const notifyManual = useCallback((): void => {
    // Defer the grace stamp to the next applyFrame so it shares that clock.
    manualPendingRef.current = true;
    manualAtRef.current = null;
    setMode('manual');
  }, [setMode]);

  const setManualHeld = useCallback((held: boolean): void => {
    manualHeldRef.current = held;
    if (held) {
      // Entering a press-and-hold behaves like any manual input (mode flips to
      // manual now); the held flag then pins the grace window open until release.
      manualPendingRef.current = true;
      manualAtRef.current = null;
      setMode('manual');
    }
    // On release we leave mode/grace as applyFrame last stamped them, so the
    // standard grace window counts down from the final held frame (~now).
  }, [setMode]);

  return { mode, applyFrame, easeTo, follow, stopFollow, notifyManual, setManualHeld };
}

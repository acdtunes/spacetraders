import { describe, it, expect } from 'vitest';
import {
  idleDrift,
  easeToward,
  poseSettled,
  DRIFT_X,
  DRIFT_Y,
  DRIFT_PERIOD_MS,
  BREATH_PERIOD_MS,
  BREATH_AMP,
  type CameraPose,
} from '../camera';

const base: CameraPose = { x: 0, y: 0, scale: 1 };

// L1 distance to a target, used to assert monotonic convergence.
const distTo = (p: CameraPose, t: CameraPose) =>
  Math.abs(p.x - t.x) + Math.abs(p.y - t.y) + Math.abs(p.scale - t.scale);

describe('camera math', () => {
  describe('idleDrift', () => {
    it('orbits the base pose within the documented envelope for any t', () => {
      // includes a full day and beyond — sin is always bounded so drift can never run away
      for (const t of [0, 10_000, 60_000, 3_600_000, 8.64e7, 1e9]) {
        const p = idleDrift(t, base);
        expect(Math.abs(p.x)).toBeLessThanOrEqual(40);
        expect(Math.abs(p.y)).toBeLessThanOrEqual(40);
        expect(p.scale).toBeGreaterThanOrEqual(0.97);
        expect(p.scale).toBeLessThanOrEqual(1.08);
        expect(Number.isFinite(p.x)).toBe(true);
        expect(Number.isFinite(p.y)).toBe(true);
        expect(Number.isFinite(p.scale)).toBe(true);
      }
    });

    it('stays inside the exact amplitude bounds of its own constants', () => {
      for (let t = 0; t <= DRIFT_PERIOD_MS; t += 250) {
        const p = idleDrift(t, base);
        expect(Math.abs(p.x - base.x)).toBeLessThanOrEqual(DRIFT_X + 1e-9);
        expect(Math.abs(p.y - base.y)).toBeLessThanOrEqual(DRIFT_Y + 1e-9);
        expect(p.scale).toBeGreaterThanOrEqual(base.scale * (1 - BREATH_AMP) - 1e-9);
        expect(p.scale).toBeLessThanOrEqual(base.scale * (1 + BREATH_AMP) + 1e-9);
      }
    });

    it('genuinely oscillates — reaches near both extremes across a period', () => {
      let maxX = -Infinity;
      let minX = Infinity;
      let maxY = -Infinity;
      let minY = Infinity;
      for (let t = 0; t <= DRIFT_PERIOD_MS * 2; t += 200) {
        const p = idleDrift(t, base);
        maxX = Math.max(maxX, p.x);
        minX = Math.min(minX, p.x);
        maxY = Math.max(maxY, p.y);
        minY = Math.min(minY, p.y);
      }
      expect(maxX).toBeGreaterThan(DRIFT_X * 0.9);
      expect(minX).toBeLessThan(-DRIFT_X * 0.9);
      expect(maxY).toBeGreaterThan(DRIFT_Y * 0.9);
      expect(minY).toBeLessThan(-DRIFT_Y * 0.9);
    });

    it('is continuous — no jumps between adjacent frames', () => {
      let prev = idleDrift(0, base);
      for (let t = 50; t <= 300_000; t += 50) {
        const p = idleDrift(t, base);
        expect(Math.abs(p.x - prev.x)).toBeLessThan(0.5);
        expect(Math.abs(p.y - prev.y)).toBeLessThan(0.5);
        expect(Math.abs(p.scale - prev.scale)).toBeLessThan(0.01);
        prev = p;
      }
    });

    it('loops smoothly — pan repeats each drift period, zoom each breath period', () => {
      for (const t of [0, 5_000, 23_500, 61_000]) {
        expect(idleDrift(t, base).x).toBeCloseTo(idleDrift(t + DRIFT_PERIOD_MS, base).x, 6);
        expect(idleDrift(t, base).scale).toBeCloseTo(idleDrift(t + BREATH_PERIOD_MS, base).scale, 6);
      }
    });

    it('is deterministic and does not mutate the base pose', () => {
      const b: CameraPose = { x: 7, y: -3, scale: 1.5 };
      const snapshot = { ...b };
      const first = idleDrift(12_345, b);
      const second = idleDrift(12_345, b);
      expect(first).toEqual(second);
      expect(b).toEqual(snapshot);
    });

    it('drifts relative to the base — additive pan, multiplicative zoom', () => {
      const b: CameraPose = { x: 100, y: -40, scale: 3 };
      const t = 33_000;
      const atZero = idleDrift(t, base);
      const atBase = idleDrift(t, b);
      expect(atBase.x - b.x).toBeCloseTo(atZero.x - base.x, 9);
      expect(atBase.y - b.y).toBeCloseTo(atZero.y - base.y, 9);
      expect(atBase.scale / b.scale).toBeCloseTo(atZero.scale / base.scale, 9);
    });

    it('resumes from rest — idleDrift(0, base) equals base exactly on every axis', () => {
      // Every (re)anchor evaluates drift at elapsed=0; if any term were non-zero
      // the camera would lurch by a fixed offset the instant idle drift restarts
      // (right after a selection centres, or after the hands-off grace expires).
      expect(idleDrift(0, base)).toEqual(base);
      const b: CameraPose = { x: 128, y: -64, scale: 1.5 };
      expect(idleDrift(0, b)).toEqual(b);
    });

    it('breathes zero-mean around base — dips below and rises above, so re-anchor cannot ratchet zoom', () => {
      let min = Infinity;
      let max = -Infinity;
      for (let t = 0; t <= BREATH_PERIOD_MS; t += 200) {
        const s = idleDrift(t, base).scale;
        min = Math.min(min, s);
        max = Math.max(max, s);
      }
      expect(min).toBeLessThan(base.scale); // genuinely zooms out below base
      expect(max).toBeGreaterThan(base.scale); // and in above it
      expect((min + max) / 2).toBeCloseTo(base.scale, 3); // symmetric about base
    });
  });

  describe('easeToward', () => {
    it('converges exponentially and never overshoots', () => {
      let cur: CameraPose = { x: 0, y: 0, scale: 1 };
      const target: CameraPose = { x: 100, y: -60, scale: 2 };
      for (let i = 0; i < 300; i++) cur = easeToward(cur, target, 16);
      expect(cur.x).toBeCloseTo(100, 0);
      expect(cur.y).toBeCloseTo(-60, 0);
      expect(cur.scale).toBeCloseTo(2, 1);
    });

    it('never overshoots and monotonically closes the gap for any dt', () => {
      const target: CameraPose = { x: 250, y: -120, scale: 0.4 };
      const dts = [0.5, 8, 16, 33, 100, 1000, 5000, 100_000];
      let cur: CameraPose = { x: -30, y: 90, scale: 2.5 };
      let prevDist = distTo(cur, target);
      for (let i = 0; i < 400; i++) {
        const dt = dts[i % dts.length];
        const next = easeToward(cur, target, dt);
        // convex blend of current→target: each component stays between the two, never past target
        expect(next.x).toBeGreaterThanOrEqual(Math.min(cur.x, target.x) - 1e-9);
        expect(next.x).toBeLessThanOrEqual(Math.max(cur.x, target.x) + 1e-9);
        expect(next.y).toBeGreaterThanOrEqual(Math.min(cur.y, target.y) - 1e-9);
        expect(next.y).toBeLessThanOrEqual(Math.max(cur.y, target.y) + 1e-9);
        expect(next.scale).toBeGreaterThanOrEqual(Math.min(cur.scale, target.scale) - 1e-9);
        expect(next.scale).toBeLessThanOrEqual(Math.max(cur.scale, target.scale) + 1e-9);
        const d = distTo(next, target);
        expect(d).toBeLessThanOrEqual(prevDist + 1e-9);
        prevDist = d;
        cur = next;
      }
      expect(poseSettled(cur, target)).toBe(true);
    });

    it('clamps to the target for a single huge dt step', () => {
      const cur: CameraPose = { x: 3, y: 4, scale: 1 };
      const target: CameraPose = { x: 900, y: -700, scale: 6 };
      const next = easeToward(cur, target, 1e12);
      expect(next.x).toBeCloseTo(target.x, 6);
      expect(next.y).toBeCloseTo(target.y, 6);
      expect(next.scale).toBeCloseTo(target.scale, 6);
    });

    it('holds still for zero or negative dt', () => {
      const cur: CameraPose = { x: 12, y: -8, scale: 1.7 };
      const target: CameraPose = { x: 500, y: 500, scale: 4 };
      expect(easeToward(cur, target, 0)).toEqual(cur);
      expect(easeToward(cur, target, -250)).toEqual(cur);
    });

    it('respects tauMs — a shorter time-constant converges faster', () => {
      const cur: CameraPose = { x: 0, y: 0, scale: 1 };
      const target: CameraPose = { x: 100, y: 0, scale: 1 };
      const fast = easeToward(cur, target, 16, 100);
      const slow = easeToward(cur, target, 16, 1000);
      expect(distTo(fast, target)).toBeLessThan(distTo(slow, target));
    });

    it('is pure — leaves current and target untouched', () => {
      const cur: CameraPose = { x: 1, y: 2, scale: 1 };
      const target: CameraPose = { x: 9, y: 9, scale: 3 };
      const curCopy = { ...cur };
      const targetCopy = { ...target };
      easeToward(cur, target, 16);
      expect(cur).toEqual(curCopy);
      expect(target).toEqual(targetCopy);
    });
  });

  describe('poseSettled', () => {
    it('detects arrival within tolerance and rejects a real gap', () => {
      expect(poseSettled({ x: 100, y: 50, scale: 2 }, { x: 100.05, y: 49.95, scale: 2.0005 })).toBe(true);
      expect(poseSettled(base, { x: 5, y: 0, scale: 1 })).toBe(false);
    });

    it('treats identical poses as settled', () => {
      const p: CameraPose = { x: 42, y: -17, scale: 1.3 };
      expect(poseSettled(p, { ...p })).toBe(true);
    });

    it('applies the position threshold strictly on each axis', () => {
      expect(poseSettled(base, { x: 0.49, y: 0, scale: 1 })).toBe(true);
      expect(poseSettled(base, { x: 0.5, y: 0, scale: 1 })).toBe(false);
      expect(poseSettled(base, { x: 0, y: 0.5, scale: 1 })).toBe(false);
    });

    it('applies the scale threshold', () => {
      expect(poseSettled(base, { x: 0, y: 0, scale: 1.004 })).toBe(true);
      expect(poseSettled(base, { x: 0, y: 0, scale: 1.006 })).toBe(false);
    });

    it('is symmetric in its arguments', () => {
      const a: CameraPose = { x: 10, y: 10, scale: 1 };
      const b: CameraPose = { x: 10.3, y: 9.8, scale: 1.002 };
      expect(poseSettled(a, b)).toBe(poseSettled(b, a));
    });
  });
});

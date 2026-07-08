import { describe, it, expect, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import type Konva from 'konva';
import { useCinematicCamera } from '../useCinematicCamera';
import { DRIFT_X, DRIFT_Y, BREATH_AMP } from '../../domain/camera';

// A minimal Konva-node stand-in: position()/scale() act as getter+setter and
// record every setter call so a test can prove the camera wrote (or did not
// write) the node on a given frame. The hook only ever touches these three.
function stubStage() {
  const state = { x: 0, y: 0, scale: 1 };
  const node = {
    state,
    position: vi.fn((p?: { x: number; y: number }) => {
      if (p) {
        state.x = p.x;
        state.y = p.y;
      }
      return { x: state.x, y: state.y };
    }),
    scale: vi.fn((s?: { x: number; y: number }) => {
      if (s) state.scale = s.x;
      return { x: state.scale, y: state.scale };
    }),
    batchDraw: vi.fn(),
  };
  return node;
}

type StubStage = ReturnType<typeof stubStage>;
const asNode = (s: StubStage): Konva.Layer => s as unknown as Konva.Layer;

describe('useCinematicCamera', () => {
  it('starts idle and drifts the node within the committed bounds', () => {
    const { result } = renderHook(() => useCinematicCamera());
    const stage = stubStage();

    const xs: number[] = [];
    const ys: number[] = [];
    const scales: number[] = [];
    act(() => {
      for (let t = 0; t <= 120_000; t += 250) {
        result.current.applyFrame(t, asNode(stage));
        xs.push(stage.state.x);
        ys.push(stage.state.y);
        scales.push(stage.state.scale);
      }
    });

    expect(result.current.mode).toBe('idle');
    // Envelope: the eased pose tracks idleDrift, which is bounded ±DRIFT on each
    // axis with a gentle breathing zoom. Allow 1px of easing lag slack.
    for (const x of xs) expect(Math.abs(x)).toBeLessThanOrEqual(DRIFT_X + 1);
    for (const y of ys) expect(Math.abs(y)).toBeLessThanOrEqual(DRIFT_Y + 1);
    for (const s of scales) {
      expect(s).toBeGreaterThanOrEqual(1 - BREATH_AMP);
      expect(s).toBeLessThanOrEqual(1 + BREATH_AMP);
    }
    // And it genuinely moves — not pinned at the base pose.
    expect(Math.max(...xs.map(Math.abs))).toBeGreaterThan(5);
    expect(stage.position).toHaveBeenCalled();
  });

  it('eases to a selection target without overshoot, then hands back to idle', () => {
    const { result } = renderHook(() => useCinematicCamera());
    const stage = stubStage();
    const target = { x: 500, y: 300, scale: 2 };

    act(() => {
      result.current.applyFrame(0, asNode(stage));
    });
    act(() => {
      result.current.easeTo(target);
    });

    let overshoot = false;
    let arrived = false;
    act(() => {
      for (let t = 16; t <= 12_000 && !arrived; t += 16) {
        result.current.applyFrame(t, asNode(stage));
        if (
          stage.state.x > target.x + 0.001 ||
          stage.state.y > target.y + 0.001 ||
          stage.state.scale > target.scale + 0.0001
        ) {
          overshoot = true;
        }
        // Stop the moment the ease has settled onto the target, before idle
        // drift is allowed to move the pose off it again.
        if (
          Math.abs(stage.state.x - target.x) < 0.5 &&
          Math.abs(stage.state.y - target.y) < 0.5 &&
          Math.abs(stage.state.scale - target.scale) < 0.005
        ) {
          arrived = true;
        }
      }
    });

    expect(overshoot).toBe(false);
    expect(arrived).toBe(true);
    expect(stage.state.x).toBeCloseTo(500, 0);
    expect(stage.state.y).toBeCloseTo(300, 0);
    expect(result.current.mode).toBe('idle');
  });

  it('manual wins instantly, writes nothing through the grace window, then resumes from the left pose', () => {
    const { result } = renderHook(() => useCinematicCamera({ manualGraceMs: 1000 }));
    const stage = stubStage();

    // Land a selection glide far from the origin so "resumes from the left pose"
    // is distinguishable from any snap back toward (0,0).
    act(() => {
      result.current.applyFrame(0, asNode(stage));
    });
    act(() => {
      result.current.easeTo({ x: 800, y: -400, scale: 3 });
    });
    act(() => {
      for (let t = 16; t <= 5_000; t += 16) result.current.applyFrame(t, asNode(stage));
    });
    expect(result.current.mode).toBe('idle');
    expect(Math.abs(stage.state.x)).toBeGreaterThan(700); // parked far from origin

    // The user grabs the camera.
    act(() => {
      result.current.notifyManual();
    });
    // First frame after notifyManual stamps the grace clock and writes nothing.
    act(() => {
      result.current.applyFrame(5_016, asNode(stage));
    });
    expect(result.current.mode).toBe('manual');
    const frozen = { ...stage.state };
    const writesBefore = stage.position.mock.calls.filter((c) => c[0] !== undefined).length;

    // Deep inside the grace window: still zero writes, pose untouched.
    act(() => {
      result.current.applyFrame(5_300, asNode(stage));
    });
    const writesDuring = stage.position.mock.calls.filter((c) => c[0] !== undefined).length;
    expect(writesDuring).toBe(writesBefore);
    expect(stage.state).toEqual(frozen);
    expect(result.current.mode).toBe('manual');

    // Grace expired (5_300 - 5_016 = 284 < 1000; 6_100 - 5_016 = 1084 >= 1000).
    act(() => {
      result.current.applyFrame(6_100, asNode(stage));
    });
    expect(result.current.mode).toBe('idle');
    // Drift resumes AROUND the pose the user left — never a snap back to origin.
    expect(Math.abs(stage.state.x - frozen.x)).toBeLessThanOrEqual(DRIFT_X + 1);
    expect(Math.abs(stage.state.y - frozen.y)).toBeLessThanOrEqual(DRIFT_Y + 1);
    expect(Math.abs(frozen.x)).toBeGreaterThan(100); // left pose really is far from origin
  });

  it('follow eases the node toward the live target each frame', () => {
    const { result } = renderHook(() => useCinematicCamera());
    const stage = stubStage();
    act(() => {
      result.current.applyFrame(0, asNode(stage));
    });
    act(() => {
      result.current.follow(() => ({ x: 200, y: 120, scale: 1.5 }));
    });
    act(() => {
      for (let t = 16; t <= 6_000; t += 16) result.current.applyFrame(t, asNode(stage));
    });
    expect(result.current.mode).toBe('follow');
    expect(stage.state.x).toBeCloseTo(200, 0);
    expect(stage.state.y).toBeCloseTo(120, 0);

    act(() => {
      result.current.stopFollow();
    });
    expect(result.current.mode).toBe('idle');
  });

  it('keeps manual open through a motionless held drag, then times out the grace after release', () => {
    const { result } = renderHook(() => useCinematicCamera({ manualGraceMs: 1000 }));
    const stage = stubStage();
    act(() => {
      result.current.applyFrame(0, asNode(stage));
    });

    // Drag starts and the pointer is then held perfectly still: Konva fires no
    // further dragmove, so only the held flag keeps the camera off.
    act(() => {
      result.current.setManualHeld(true);
    });
    act(() => {
      result.current.applyFrame(16, asNode(stage));
    });
    const frozen = { ...stage.state };
    const writesBefore = stage.position.mock.calls.filter((c) => c[0] !== undefined).length;

    // Hold well past the 1000ms grace window with no further input events.
    act(() => {
      for (let t = 32; t <= 10_000; t += 16) result.current.applyFrame(t, asNode(stage));
    });
    const writesDuring = stage.position.mock.calls.filter((c) => c[0] !== undefined).length;
    expect(result.current.mode).toBe('manual'); // never flipped to idle while held
    expect(writesDuring).toBe(writesBefore); // zero camera writes while held
    expect(stage.state).toEqual(frozen);

    // Release: the grace now counts down from the final held frame (~10_000).
    act(() => {
      result.current.setManualHeld(false);
    });
    act(() => {
      result.current.applyFrame(10_100, asNode(stage));
    });
    expect(result.current.mode).toBe('manual'); // still inside the fresh grace
    act(() => {
      result.current.applyFrame(11_200, asNode(stage));
    });
    expect(result.current.mode).toBe('idle'); // grace expired after release
  });
});

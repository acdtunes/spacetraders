// Task 12 — interactions: hover/select on scene content, anchored wheel zoom,
// drag pan with a 3px tap-preserving threshold, F/Escape keys, and focusTour
// camera follow with break-on-input. Harness: the mount test's Application
// stub (jsdom has no WebGL) over the REAL pixi scene graph (the build tests'
// trick), so the real builders attach real interactive children we can poke.
// systemBand is mocked to observe the deferred (fade-tail) teardown, and
// useSystemDetail is mocked to yield a synchronous detail (no fetches).
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import type { MutableRefObject } from 'react';
import { Container, type FederatedPointerEvent } from 'pixi.js';
import { NebulaScene, type NebulaApi } from '../NebulaScene';
import { buildSystemBand, clearSystemBand } from '../layers/systemBand';
import { anchoredZoom } from '../camera';
import type { SceneData } from '../sceneData';

// ---- pixi.js: real module, stubbed Application (init needs WebGL) ----------
const pixiState = vi.hoisted(() => ({
  apps: [] as any[],
  /** When set, StubApplication.init awaits this before resolving — lets a test
   * unmount mid-init to pin the cancelled-guard (StrictMode race) behavior. */
  initHold: null as Promise<void> | null,
}));

vi.mock('pixi.js', async (importOriginal) => {
  const actual = await importOriginal<typeof import('pixi.js')>();
  class StubApplication {
    stage = new actual.Container();
    canvas = document.createElement('canvas');
    renderer: { resize: ReturnType<typeof vi.fn> } | null = null;
    ticker = { add: vi.fn(), remove: vi.fn() };
    destroy = vi.fn();
    init = vi.fn(async (_opts: unknown) => {
      if (pixiState.initHold != null) await pixiState.initHold;
      this.renderer = { resize: vi.fn() };
    });
    constructor() {
      pixiState.apps.push(this);
    }
  }
  return { ...actual, Application: StubApplication };
});

// ---- SYSTEM band: mocked so the wheel-out fade test can pin WHEN teardown
// happens (the Task 11 pop was clearSystemBand firing on the first exit tick).
vi.mock('../layers/systemBand', () => ({
  buildSystemBand: vi.fn(() => ({ labels: { alpha: 0, visible: false } })),
  clearSystemBand: vi.fn(),
}));

// ---- detail hook: synchronous per-symbol detail, no fetch/store traffic.
vi.mock('../useSystemDetail', () => {
  const cache = new Map<string, unknown>();
  return {
    useSystemDetail: (symbol: string | null) => {
      if (symbol == null) return { detail: null, loading: false };
      let d = cache.get(symbol);
      if (d == null) {
        d = { symbol, waypoints: [], freshness: null, gate: null };
        cache.set(symbol, d);
      }
      return { detail: d, loading: false };
    },
  };
});

class StubResizeObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
}
vi.stubGlobal('ResizeObserver', StubResizeObserver);

// ---- fixtures ---------------------------------------------------------------
// Two systems in two clusters joined by one profitable lane, one live ship.
// Viewport in jsdom is 1×1 (clientWidth 0 → max(1,·)), so the landing fit is
// tiny but exact: bounds (0,0)-(200,120) + 60 pad → fitScale = 1/320.
const ship = { id: 's1', flowId: 'flow-1', x: 100, y: 60, headingRad: 0, system: null };
const sceneData: SceneData = {
  systems: [
    { symbol: 'X1-A', x: 0, y: 0, activity: 5, isHome: true, underConstruction: false },
    { symbol: 'X1-B', x: 200, y: 120, activity: 3, isHome: false, underConstruction: false },
  ],
  lanes: [{ from: 'X1-A', to: 'X1-B', profitPerHr: 100, volume: 10, realized: 5, projected: 5 }],
  ships: [ship],
  edges: [{ from: 'X1-A', to: 'X1-B', underConstruction: false }],
  clusters: [
    { id: 'c1', members: ['X1-A'], cx: 0, cy: 0, isHome: true },
    { id: 'c2', members: ['X1-B'], cx: 200, cy: 120, isHome: false },
  ],
  homeSystem: 'X1-A',
  fitPoints: [
    { x: 0, y: 0 },
    { x: 200, y: 120 },
  ],
};
const FIT_SCALE = 1 / 320;

let nowMs = 100_000;

/** Minimal pointer payload for emitting on pixi's typed event emitter. */
const fed = (p: { clientX?: number; clientY?: number } = {}) => p as unknown as FederatedPointerEvent;

function findByLabel(root: Container, label: string): Container | undefined {
  if (root.label === label) return root;
  for (const c of root.children) {
    const hit = findByLabel(c as Container, label);
    if (hit != null) return hit;
  }
  return undefined;
}

async function setup(data: SceneData | null = sceneData) {
  const apiRef: MutableRefObject<NebulaApi | null> = { current: null };
  const onSelectSystem = vi.fn();
  const onHover = vi.fn();
  const utils = render(
    <NebulaScene data={data} onSelectSystem={onSelectSystem} onHover={onHover} apiRef={apiRef} />,
  );
  const host = screen.getByTestId('nebula-host');
  await waitFor(() => expect(host.querySelector('canvas')).not.toBeNull());
  const app = pixiState.apps[0];
  const tickFn = app.ticker.add.mock.calls[0][0] as () => void;
  const world = app.stage.children[0] as Container;
  const layer = (name: string) => world.children.find((c) => c.label === name) as Container;
  /** Advance the mocked clock, then run one ticker frame (inside act — the
   * ticker can setState on focus changes). */
  const tick = (advanceMs = 0) =>
    act(() => {
      nowMs += advanceMs;
      tickFn();
    });
  const canvas = app.canvas as HTMLCanvasElement;
  const rerender = (d: SceneData) =>
    utils.rerender(
      <NebulaScene data={d} onSelectSystem={onSelectSystem} onHover={onHover} apiRef={apiRef} />,
    );
  return { apiRef, onSelectSystem, onHover, app, world, layer, tick, canvas, rerender, unmount: utils.unmount };
}

const pointerDown = (canvas: HTMLElement, x: number, y: number) =>
  act(() => {
    canvas.dispatchEvent(new MouseEvent('pointerdown', { clientX: x, clientY: y, bubbles: true }));
  });
const pointerMove = (x: number, y: number) =>
  act(() => {
    window.dispatchEvent(new MouseEvent('pointermove', { clientX: x, clientY: y }));
  });
const pointerUp = (x: number, y: number) =>
  act(() => {
    window.dispatchEvent(new MouseEvent('pointerup', { clientX: x, clientY: y }));
  });
function wheel(canvas: HTMLElement, deltaY: number, x = 0, y = 0, deltaMode = 0): WheelEvent {
  const ev = new WheelEvent('wheel', { deltaY, deltaMode, clientX: x, clientY: y, bubbles: true, cancelable: true });
  act(() => {
    canvas.dispatchEvent(ev);
  });
  return ev;
}
const key = (k: string, target: EventTarget = window, init: KeyboardEventInit = {}) =>
  act(() => {
    target.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true, ...init }));
  });

beforeEach(() => {
  pixiState.apps.length = 0;
  pixiState.initHold = null;
  nowMs = 100_000;
  vi.spyOn(performance, 'now').mockImplementation(() => nowMs);
  vi.mocked(buildSystemBand).mockClear();
  vi.mocked(clearSystemBand).mockClear();
});

describe('nebula interactions', () => {
  it('wheel zooms about the pointer, one 100px notch = ×1.1 in / ÷1.1 out (exact round-trip), camera-clamped', async () => {
    const { world, tick, canvas } = await setup();
    tick(700); // landing fit flight completes
    const s0 = world.scale.x;
    expect(s0 / FIT_SCALE).toBeCloseTo(1, 6);
    const cam0 = { x: world.position.x, y: world.position.y, scale: s0 };

    const ev = wheel(canvas, +100, 0, 0); // deltaY > 0 → out one notch
    tick(16);
    expect(ev.defaultPrevented).toBe(true);
    expect(world.scale.x / (s0 / 1.1)).toBeCloseTo(1, 6);
    const expected = anchoredZoom(cam0, 0, 0, 1 / 1.1, FIT_SCALE);
    expect(world.position.x).toBeCloseTo(expected.x, 8);
    expect(world.position.y).toBeCloseTo(expected.y, 8);

    wheel(canvas, -100, 0, 0); // deltaY < 0 → in one notch: exact round-trip
    tick(16);
    expect(world.scale.x / s0).toBeCloseTo(1, 6);

    // A horizontal trackpad swipe (deltaY 0) must not zoom.
    const sBefore = world.scale.x;
    wheel(canvas, 0, 0, 0);
    tick(16);
    expect(world.scale.x).toBeCloseTo(sBefore, 10);
  });

  it('init requests autoDensity: the canvas CSS size must equal the logical size (Retina 2×-clip / anchor-skew fix)', async () => {
    const { app } = await setup();
    // pixi's default (autoDensity: false) lays the canvas out at resolution×
    // the renderer's logical size on DPR>1 displays: the scene renders
    // 2×-clipped and wheel anchors land at 2× the cursor offset (user report).
    expect(app.init.mock.calls[0][0]).toMatchObject({ autoDensity: true });
  });

  it('wheel zoom holds the world point under the CURSOR when the canvas has a CSS offset and CSS size ≠ logical size', async () => {
    const { world, tick, canvas } = await setup();
    tick(700); // landing fit flight completes
    // Nav-bar offset (left 10 / top 64) PLUS the DPR-2 layout geometry: the
    // 1×1 logical viewport displayed across 2×2 CSS px (what autoDensity:false
    // produced on Retina). The anchor must survive both.
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      x: 10, y: 64, left: 10, top: 64, width: 2, height: 2, right: 12, bottom: 66,
      toJSON: () => ({}),
    } as DOMRect);
    const cam0 = { x: world.position.x, y: world.position.y, scale: world.scale.x };
    // Cursor at client (11.5, 65.5) → canvas-local CSS (1.5, 1.5). The render
    // pipeline displays world w at CSS (w·scale + cam)·(rectW/viewportW), so
    // the world point actually under the cursor is at logical (0.75, 0.75):
    const worldX0 = (0.75 - cam0.x) / cam0.scale;
    const worldY0 = (0.75 - cam0.y) / cam0.scale;

    wheel(canvas, -100, 11.5, 65.5); // one notch in
    tick(16);
    expect(world.scale.x / (cam0.scale * 1.1)).toBeCloseTo(1, 6);
    expect((0.75 - world.position.x) / world.scale.x).toBeCloseTo(worldX0, 8);
    expect((0.75 - world.position.y) / world.scale.x).toBeCloseTo(worldY0, 8);
  });

  it('trackpad flicks integrate: 25 small pixel-deltas total ONE notch (not the old 1.1^25 ≈ 10.8× runaway); per-mode notch equivalences pinned', async () => {
    const { world, tick, canvas } = await setup();
    tick(700);
    const s0 = world.scale.x;

    // 25 × deltaY −4 pixel-mode = −100px total → exp(100·ln(1.1)/100) = ×1.1,
    // exactly one classic notch. The old fixed ×1.1 PER EVENT gave 10.83×.
    for (let i = 0; i < 25; i++) wheel(canvas, -4, 0, 0);
    tick(16);
    expect(world.scale.x / (s0 * 1.1)).toBeCloseTo(1, 6);
    expect(world.scale.x / s0).toBeLessThan(1.2);

    // Firefox classic wheel: deltaMode 1 (lines), one notch = 3 lines → ×1.1.
    const s1 = world.scale.x;
    wheel(canvas, -3, 0, 0, 1);
    tick(16);
    expect(world.scale.x / (s1 * 1.1)).toBeCloseTo(1, 6);

    // Page mode (deltaMode 2) normalizes by the viewport height (1px here).
    const s2 = world.scale.x;
    wheel(canvas, -1, 0, 0, 2);
    tick(16);
    expect(world.scale.x / (s2 * Math.exp(Math.log(1.1) / 100))).toBeCloseTo(1, 8);
  });

  it('modifier chords (Cmd/Ctrl+F) and OS key repeats do not start the fit tween', async () => {
    const { world, tick, canvas } = await setup();
    tick(700); // landing fit flight completes
    wheel(canvas, +100);
    tick(16);
    const before = world.scale.x;
    expect(before / (FIT_SCALE / 1.1)).toBeCloseTo(1, 6);

    key('f', window, { metaKey: true }); // Cmd+F — find-in-page, not ours
    tick(700); // a fit tween would have fully landed by now — camera must be unchanged
    expect(world.scale.x).toBeCloseTo(before, 10);

    key('F', window, { ctrlKey: true }); // Ctrl+F likewise
    tick(700);
    expect(world.scale.x).toBeCloseTo(before, 10);

    key('f', window, { repeat: true }); // held-F OS auto-repeat must not (re)start the tween
    tick(700);
    expect(world.scale.x).toBeCloseTo(before, 10);
  });

  it('F begins the fit tween (eased mid-flight, exact fit at the end); typing targets are ignored', async () => {
    const { world, tick, canvas } = await setup();
    tick(700);
    wheel(canvas, +100);
    wheel(canvas, +100);
    wheel(canvas, +100);
    tick(16);
    const zoomed = world.scale.x;
    expect(zoomed / (FIT_SCALE / 1.1 ** 3)).toBeCloseTo(1, 6);

    key('f');
    tick(0); // t=0 — tween has not moved yet
    expect(world.scale.x).toBeCloseTo(zoomed, 10);
    tick(300); // mid-tween: strictly between zoomed and fit
    const mid = world.scale.x;
    expect(mid).toBeGreaterThan(zoomed * 1.001);
    expect(mid).toBeLessThan(FIT_SCALE * 0.999);
    tick(400); // flight over → exact fit
    expect(world.scale.x / FIT_SCALE).toBeCloseTo(1, 6);

    // Keydown while typing in an input must not steal the camera.
    wheel(canvas, +100);
    tick(16);
    const before = world.scale.x;
    const input = document.createElement('input');
    document.body.appendChild(input);
    key('f', input);
    tick(700);
    expect(world.scale.x).toBeCloseTo(before, 10);
    input.remove();
  });

  it('drag past 3px pans the camera and suppresses the tap-select; a clean tap selects', async () => {
    const { world, tick, canvas, layer, onSelectSystem } = await setup();
    tick(700);
    const orbHit = findByLabel(layer('orbs'), 'orb-hit:X1-A');
    expect(orbHit).toBeDefined();
    expect(orbHit!.eventMode).toBe('static');

    // Drag: down at (10,10), move to (20,18) — 12.8px > 3px threshold → pan.
    const x0 = world.position.x;
    const y0 = world.position.y;
    pointerDown(canvas, 10, 10);
    pointerMove(20, 18);
    tick(16);
    expect(world.position.x).toBeCloseTo(x0 + 10, 8);
    expect(world.position.y).toBeCloseTo(y0 + 8, 8);
    pointerUp(20, 18);
    act(() => {
      orbHit!.emit('pointertap', fed({ clientX: 20, clientY: 18 }));
    });
    expect(onSelectSystem).not.toHaveBeenCalled();

    // Clean tap (movement under the threshold) still selects.
    pointerDown(canvas, 40, 40);
    pointerMove(41, 41);
    pointerUp(41, 41);
    act(() => {
      orbHit!.emit('pointertap', fed({ clientX: 41, clientY: 41 }));
    });
    expect(onSelectSystem).toHaveBeenCalledWith('X1-A');
  });

  it('hover over orbs, lane threads, and galaxy currents emits typed HoverTargets; out clears', async () => {
    const { tick, layer, onHover } = await setup();
    tick(700);

    const orbHit = findByLabel(layer('orbs'), 'orb-hit:X1-A');
    expect(orbHit).toBeDefined();
    act(() => {
      orbHit!.emit('pointerover', fed({ clientX: 7, clientY: 9 }));
    });
    expect(onHover).toHaveBeenLastCalledWith({ kind: 'system', key: 'X1-A', clientX: 7, clientY: 9 });
    act(() => {
      orbHit!.emit('pointerout', fed());
    });
    expect(onHover).toHaveBeenLastCalledWith(null);

    // Threads draw from the RAW topology edges (Task 13) — directed key, the
    // FlowTooltip lane-key shape.
    const thread = findByLabel(layer('orbs'), 'thread:X1-A→X1-B');
    expect(thread).toBeDefined();
    expect(thread!.eventMode).toBe('static');
    act(() => {
      thread!.emit('pointerover', fed({ clientX: 1, clientY: 2 }));
    });
    expect(onHover).toHaveBeenLastCalledWith({ kind: 'lane', key: 'X1-A→X1-B', clientX: 1, clientY: 2 });

    const current = findByLabel(layer('currents'), 'current:c1|c2');
    expect(current).toBeDefined();
    expect(current!.eventMode).toBe('static');
    act(() => {
      current!.emit('pointerover', fed({ clientX: 3, clientY: 4 }));
    });
    expect(onHover).toHaveBeenLastCalledWith({ kind: 'current', key: 'c1|c2', clientX: 3, clientY: 4 });
  });

  it('focusTour follows the flow ship each tick, tracks data updates, and a wheel breaks the follow', async () => {
    const { apiRef, world, tick, canvas, rerender } = await setup();
    tick(700);
    const s0 = world.scale.x;

    act(() => apiRef.current!.focusTour('flow-1'));
    tick(16);
    expect(world.position.x).toBeCloseTo(0.5 - ship.x * s0, 8);
    expect(world.position.y).toBeCloseTo(0.5 - ship.y * s0, 8);

    // A new snapshot moves the ship — the follow re-centers on the next tick.
    act(() => rerender({ ...sceneData, ships: [{ ...ship, x: 120, y: 80 }] }));
    tick(16);
    expect(world.position.x).toBeCloseTo(0.5 - 120 * s0, 8);

    // Wheel input breaks the follow: the camera takes the anchored zoom and
    // STAYS there instead of re-centering on the ship next tick.
    const cam = { x: world.position.x, y: world.position.y, scale: world.scale.x };
    wheel(canvas, +100, 0, 0);
    tick(16);
    const expected = anchoredZoom(cam, 0, 0, 1 / 1.1, FIT_SCALE);
    expect(world.position.x).toBeCloseTo(expected.x, 8);
    tick(16);
    expect(world.position.x).toBeCloseTo(expected.x, 8); // still not re-centered
  });

  it('focusTour stops following when the flow disappears from the data', async () => {
    const { apiRef, world, tick, rerender } = await setup();
    tick(700);
    act(() => apiRef.current!.focusTour('flow-1'));
    tick(16);
    const followedX = world.position.x;
    act(() => rerender({ ...sceneData, ships: [] }));
    tick(16);
    expect(world.position.x).toBeCloseTo(followedX, 10); // camera holds, no snap
    // ...and a later ship reappearance must NOT resume the dead follow.
    act(() => rerender({ ...sceneData, ships: [{ ...ship, x: 150, y: 90 }] }));
    tick(16);
    expect(world.position.x).toBeCloseTo(followedX, 10);
  });

  it('Escape with a focused system clears the focus (dimmer recovers) and then fits the galaxy', async () => {
    const { apiRef, tick, layer } = await setup();
    tick(700);
    act(() => apiRef.current!.focusSystem('X1-A'));
    tick(700); // focus flight lands in the SYSTEM band
    expect(apiRef.current!.band()).toBe('SYSTEM');
    expect(buildSystemBand).toHaveBeenCalled();
    tick(100);
    tick(100);
    tick(100);
    tick(100);
    expect(layer('backdrop').alpha).toBeCloseTo(0.25, 5); // dimmer settled

    key('Escape');
    tick(100);
    expect(layer('backdrop').alpha).toBeGreaterThan(0.25); // focus cleared → dimmer recovering
    tick(700); // the fit flight completes
    expect(apiRef.current!.band()).toBe('GALAXY');
    tick(100);
    tick(100);
    tick(100);
    expect(layer('backdrop').alpha).toBeCloseTo(1, 5);
  });

  it('wheel-out past SYSTEM exit fades the fx band over the systemFade tail instead of popping', async () => {
    const { apiRef, tick, canvas, layer } = await setup();
    tick(700);
    act(() => apiRef.current!.focusSystem('X1-A'));
    tick(700);
    tick(100);
    tick(100);
    tick(100);
    expect(apiRef.current!.band()).toBe('SYSTEM');
    expect(layer('fx').alpha).toBeCloseTo(1, 5);
    expect(clearSystemBand).not.toHaveBeenCalled();

    // Five wheel-outs: z 12 → 12/1.1⁵ ≈ 7.45 < SYSTEM_EXIT (7.5) → focus clears.
    for (let i = 0; i < 5; i++) wheel(canvas, +100, 0, 0);
    tick(16);
    expect(apiRef.current!.band()).not.toBe('SYSTEM');
    // THE Task 11 finding: the first exit tick must NOT destroy the fx band.
    expect(clearSystemBand).not.toHaveBeenCalled();
    expect(layer('fx').alpha).toBeGreaterThan(0.9);
    expect(layer('fx').visible).toBe(true);

    // ...but once the 250ms fade tail runs out, the band is reaped exactly once.
    tick(100);
    tick(100);
    tick(100);
    expect(clearSystemBand).toHaveBeenCalledTimes(1);
    expect(layer('fx').visible).toBe(false);
  });

  it('unmount removes the window-level listeners', async () => {
    const removeSpy = vi.spyOn(window, 'removeEventListener');
    const { unmount } = await setup();
    unmount();
    const removed = removeSpy.mock.calls.map((c) => c[0]);
    expect(removed).toContain('keydown');
    expect(removed).toContain('pointermove');
    expect(removed).toContain('pointerup');
  });

  it('unmount before init resolves attaches no listeners and appends no canvas (init race)', async () => {
    // Pins the leak class: the post-init continuation must bail on `cancelled`
    // before appending the canvas or attaching any listeners (StrictMode's
    // first mount cleans up while init is still in flight).
    let release!: () => void;
    pixiState.initHold = new Promise<void>((r) => {
      release = r;
    });
    const addSpy = vi.spyOn(window, 'addEventListener');
    const apiRef: MutableRefObject<NebulaApi | null> = { current: null };
    const { unmount } = render(
      <NebulaScene data={sceneData} onSelectSystem={vi.fn()} onHover={vi.fn()} apiRef={apiRef} />,
    );
    const host = screen.getByTestId('nebula-host');
    const app = pixiState.apps[0];
    expect(app.init).toHaveBeenCalled();

    unmount(); // cleanup runs while init is still pending → cancelled = true
    release(); // now let init resolve; the continuation must self-destruct
    await waitFor(() => expect(app.destroy).toHaveBeenCalledWith(true));

    expect(host.querySelector('canvas')).toBeNull();
    const added = addSpy.mock.calls.map((c) => c[0]);
    expect(added).not.toContain('keydown');
    expect(added).not.toContain('pointermove');
    expect(added).not.toContain('pointerup');
    expect(added).not.toContain('pointercancel');
    expect(app.ticker.add).not.toHaveBeenCalled();
  });
});

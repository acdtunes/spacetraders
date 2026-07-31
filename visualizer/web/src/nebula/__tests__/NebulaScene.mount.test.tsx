import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import type { MutableRefObject } from 'react';
import { NebulaScene, type NebulaApi } from '../NebulaScene';
import type { SceneData } from '../sceneData';

// ---- pixi.js stub -----------------------------------------------------------
// The real Application needs WebGL; jsdom has none. Stub the two classes the
// scene consumes, capturing init/destroy/ticker calls and using a real <canvas>
// element so DOM assertions stay honest.
const pixiState = vi.hoisted(() => ({
  apps: [] as any[],
  rejectInit: false,
}));

vi.mock('pixi.js', () => {
  class StubContainer {
    label = '';
    children: any[] = [];
    position = { set: vi.fn() };
    scale = { set: vi.fn() };
    addChild(...kids: any[]) {
      this.children.push(...kids);
      return kids[0];
    }
    removeChildren() {
      const removed = this.children;
      this.children = [];
      return removed;
    }
    destroy() {}
  }
  class StubApplication {
    stage = new StubContainer();
    canvas = document.createElement('canvas');
    renderer: { resize: ReturnType<typeof vi.fn> } | null = null;
    ticker = { add: vi.fn(), remove: vi.fn() };
    destroy = vi.fn();
    init = vi.fn(async (_opts: unknown) => {
      if (pixiState.rejectInit) throw new Error('WebGL unavailable');
      this.renderer = { resize: vi.fn() };
    });
    constructor() {
      pixiState.apps.push(this);
    }
  }
  return { Application: StubApplication, Container: StubContainer };
});

// jsdom has no ResizeObserver; the scene observes the host div for viewport dims.
const roInstances: Array<{ observe: ReturnType<typeof vi.fn>; disconnect: ReturnType<typeof vi.fn> }> = [];
class StubResizeObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
  constructor(_cb: unknown) {
    roInstances.push(this);
  }
}
vi.stubGlobal('ResizeObserver', StubResizeObserver);

// ---- fixtures ---------------------------------------------------------------
// Contract pin: later layer tasks attach to these EXACT names in this z-order.
const LAYER_NAMES = ['backdrop', 'auras', 'currents', 'lanes', 'orbs', 'ships', 'labels', 'fx'];

const sceneData: SceneData = {
  systems: [],
  lanes: [],
  ships: [],
  edges: [],
  clusters: [],
  homeSystem: null,
  clusterFreshness: new Map(),
  rotationBoundMinutes: 0,
  rotationBoundBasis: 'unknown',
  marketsKnown: 0,
  fitPoints: [
    { x: 0, y: 0 },
    { x: 200, y: 120 },
  ],
};

function renderScene(data: SceneData | null = null) {
  const apiRef: MutableRefObject<NebulaApi | null> = { current: null };
  const utils = render(
    <NebulaScene data={data} onSelectSystem={vi.fn()} onHover={vi.fn()} apiRef={apiRef} />,
  );
  return { ...utils, apiRef };
}

async function waitForCanvas(host: HTMLElement) {
  await waitFor(() => expect(host.querySelector('canvas')).not.toBeNull());
}

beforeEach(() => {
  pixiState.apps.length = 0;
  pixiState.rejectInit = false;
  roInstances.length = 0;
});

describe('NebulaScene mount', () => {
  it('renders a host div and appends the pixi canvas only after init resolves', async () => {
    renderScene();
    const host = screen.getByTestId('nebula-host');
    // init is async: the canvas must not be attached synchronously at render.
    expect(host.querySelector('canvas')).toBeNull();
    await waitForCanvas(host);
    expect(pixiState.apps).toHaveLength(1);
    expect(host.querySelector('canvas')).toBe(pixiState.apps[0].canvas);
  });

  it('calls init exactly once with the nebula background, antialias, and a resolution capped at 2', async () => {
    renderScene();
    await waitForCanvas(screen.getByTestId('nebula-host'));
    const app = pixiState.apps[0];
    expect(app.init).toHaveBeenCalledTimes(1);
    const opts = app.init.mock.calls[0][0];
    expect(opts).toMatchObject({ background: 0x070312, antialias: true });
    expect(opts.resolution).toBeGreaterThan(0);
    expect(opts.resolution).toBeLessThanOrEqual(2);
  });

  it('registers all 8 layers in z-order under a single world container on the stage', async () => {
    renderScene();
    await waitForCanvas(screen.getByTestId('nebula-host'));
    const app = pixiState.apps[0];
    expect(app.stage.children).toHaveLength(1);
    const world = app.stage.children[0];
    expect(world.label).toBe('world');
    expect(world.children.map((c: any) => c.label)).toEqual(LAYER_NAMES);
  });

  it('exposes exactly the four NebulaApi methods via apiRef, with band() GALAXY before any fit', async () => {
    const { apiRef } = renderScene(null);
    await waitFor(() => expect(apiRef.current).not.toBeNull());
    const api = apiRef.current!;
    expect(Object.keys(api).sort()).toEqual(['band', 'fitGalaxy', 'focusSystem', 'focusTour']);
    expect(typeof api.fitGalaxy).toBe('function');
    expect(typeof api.focusSystem).toBe('function');
    expect(typeof api.focusTour).toBe('function');
    expect(typeof api.band).toBe('function');
    expect(api.band()).toBe('GALAXY');
    // Stubs this task: must be callable without throwing.
    expect(() => {
      api.focusSystem('X1-A');
      api.focusTour('flow-1');
      api.fitGalaxy();
    }).not.toThrow();
  });

  it('drives the world transform from the ticker callback (camera loop wired)', async () => {
    renderScene(sceneData); // data present → initial fitGalaxy schedules a flight
    await waitForCanvas(screen.getByTestId('nebula-host'));
    const app = pixiState.apps[0];
    expect(app.ticker.add).toHaveBeenCalledTimes(1);
    const tick = app.ticker.add.mock.calls[0][0];
    tick();
    const world = app.stage.children[0];
    expect(world.position.set).toHaveBeenCalled();
    const [x, y] = world.position.set.mock.calls.at(-1)!;
    expect(Number.isFinite(x)).toBe(true);
    expect(Number.isFinite(y)).toBe(true);
    expect(world.scale.set).toHaveBeenCalled();
    const [s] = world.scale.set.mock.calls.at(-1)!;
    expect(Number.isFinite(s)).toBe(true);
    expect(s).toBeGreaterThan(0);
  });

  it('unmount destroys the app with true, cancels the ticker, and disconnects observers', async () => {
    const { unmount } = renderScene();
    await waitForCanvas(screen.getByTestId('nebula-host'));
    const app = pixiState.apps[0];
    const tick = app.ticker.add.mock.calls[0][0];
    expect(roInstances).toHaveLength(1);
    unmount();
    expect(app.destroy).toHaveBeenCalledTimes(1);
    expect(app.destroy).toHaveBeenCalledWith(true);
    expect(app.ticker.remove).toHaveBeenCalledWith(tick);
    expect(roInstances[0].disconnect).toHaveBeenCalled();
  });

  it('renders the WebGL fallback (no throw) when init rejects', async () => {
    pixiState.rejectInit = true;
    const { unmount } = renderScene();
    const fallback = await screen.findByText(/WebGL unavailable/);
    expect(fallback.className).toContain('nebula-fallback');
    expect(document.querySelector('canvas')).toBeNull();
    // Unmount after a failed init must also be clean: nothing to destroy.
    expect(() => unmount()).not.toThrow();
    expect(pixiState.apps[0].destroy).not.toHaveBeenCalled();
  });
});

// ---- Task 14: dev `?fps=1` overlay ------------------------------------------
// Rolling-average FPS from the pixi ticker, drawn into a small non-interactive
// DOM overlay. EMA over ~30 frames; the DOM text re-renders at ~2 Hz, never
// per frame. The ticker uses the RAW frame delta (not the MAX_DT_MS-clamped
// one the sim uses), so a stall reads as the honest low number.
describe('NebulaScene ?fps=1 overlay', () => {
  let nowMs = 100_000;
  let perfSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    nowMs = 100_000;
    perfSpy = vi.spyOn(performance, 'now').mockImplementation(() => nowMs);
  });

  afterEach(() => {
    perfSpy.mockRestore();
    window.history.replaceState({}, '', '/');
  });

  it('renders no overlay without the param', async () => {
    renderScene();
    await waitForCanvas(screen.getByTestId('nebula-host'));
    expect(screen.queryByTestId('nebula-fps')).toBeNull();
  });

  it('with ?fps=1: a non-interactive monospace overlay shows a ticker-driven EMA at ~2 Hz', async () => {
    window.history.replaceState({}, '', '/trade-flows?fps=1');
    renderScene(sceneData);
    const host = screen.getByTestId('nebula-host');
    await waitForCanvas(host);
    const overlay = screen.getByTestId('nebula-fps');
    expect(host.contains(overlay)).toBe(true);
    // Dev affordance, never an input surface.
    expect(overlay.style.pointerEvents).toBe('none');
    expect(overlay.style.fontFamily.toLowerCase()).toContain('mono');

    const app = pixiState.apps[0];
    const tick = app.ticker.add.mock.calls[0][0] as () => void;
    // First 10ms frame: the EMA seeds at the instantaneous 100 fps and the
    // first text write is unthrottled.
    nowMs += 10;
    tick();
    expect(overlay.textContent).toBe('100 fps');
    // A 33ms frame moves the EMA (100 → ~97.7) but the ~2 Hz throttle holds
    // the text.
    nowMs += 33;
    tick();
    expect(overlay.textContent).toBe('100 fps');
    // 600ms later the throttle window has passed: raw dt 600ms → inst ~1.7 fps
    // pulls the EMA to ~94.5, and the text re-renders.
    nowMs += 600;
    tick();
    expect(overlay.textContent).toBe('94 fps');
  });
});

// sp-fw6a2 — the band gate over the far thread tier. buildOrbs splits the
// lattice (see latticeTiers.test.ts); this pins WHEN each tier is on screen:
//   GALAXY — neither tier (layers.orbs is hidden outright, as it always was)
//   REGION — near tier only. This is the fix: the band you must enter to read
//            trade lanes no longer arrives with all ~5.2k gate edges on it.
//   SYSTEM — both tiers; the viewport covers one system, so the full web is
//            context rather than a mat.
//   ...and the Lattice toggle reveals the far tier at REGION on demand.
// Harness: the interactions-test trick — REAL pixi scene graph with a stubbed
// Application (jsdom has no WebGL), so the real builders attach real children.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import type { MutableRefObject } from 'react';
import { Container } from 'pixi.js';
import { NebulaScene, type NebulaApi, type NebulaLayerToggles } from '../NebulaScene';
import { THREADS_NEAR, THREADS_FAR } from '../layers/orbs';
import type { SceneData, SceneEdge } from '../sceneData';
import { DARK } from '../freshness';

const pixiState = vi.hoisted(() => ({ apps: [] as any[] }));

vi.mock('pixi.js', async (importOriginal) => {
  const actual = await importOriginal<typeof import('pixi.js')>();
  class StubApplication {
    stage = new actual.Container();
    canvas = document.createElement('canvas');
    renderer: { resize: ReturnType<typeof vi.fn> } | null = null;
    ticker = { add: vi.fn(), remove: vi.fn() };
    destroy = vi.fn();
    init = vi.fn(async () => { this.renderer = { resize: vi.fn() }; });
    constructor() { pixiState.apps.push(this); }
  }
  return { ...actual, Application: StubApplication };
});

// The SYSTEM band's own drilldown needs waypoint fetches; neither matters here.
vi.mock('../layers/systemBand', () => ({
  buildSystemBand: vi.fn(() => ({ labels: { alpha: 0, visible: false } })),
  clearSystemBand: vi.fn(),
}));
vi.mock('../useSystemDetail', () => ({
  useSystemDetail: (symbol: string | null) => ({
    detail: symbol == null ? null : { symbol, waypoints: [], freshness: null, gate: null },
    loading: false,
  }),
}));

class StubResizeObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
}
vi.stubGlobal('ResizeObserver', StubResizeObserver);

// ---- fixture: one traded pair (A→B) plus an untraded tail (B→C, C→D) --------
const edge = (from: string, to: string, relevant: boolean): SceneEdge =>
  ({ from, to, underConstruction: false, relevant });

const sceneData: SceneData = {
  systems: [
    { symbol: 'X1-A', x: 0, y: 0, activity: 5, isHome: true, underConstruction: false, freshness: DARK },
    { symbol: 'X1-B', x: 200, y: 120, activity: 3, isHome: false, underConstruction: false, freshness: DARK },
    { symbol: 'X1-C', x: 60, y: 90, activity: 0, isHome: false, underConstruction: false, freshness: DARK },
    { symbol: 'X1-D', x: 140, y: 30, activity: 0, isHome: false, underConstruction: false, freshness: DARK },
  ],
  lanes: [{ from: 'X1-A', to: 'X1-B', profitPerHr: 100, volume: 10, realized: 5, projected: 0 }],
  ships: [],
  edges: [edge('X1-A', 'X1-B', true), edge('X1-B', 'X1-C', false), edge('X1-C', 'X1-D', false)],
  clusters: [{ id: 'c1', members: ['X1-A', 'X1-B'], cx: 100, cy: 60, isHome: true }],
  homeSystem: 'X1-A',
  clusterFreshness: new Map(),
  rotationBoundMinutes: 0,
  rotationBoundBasis: 'unknown',
  marketsKnown: 0,
  fitPoints: [{ x: 0, y: 0 }, { x: 200, y: 120 }],
};

// The ticker reads performance.now() for flight progress and fade steps; jsdom's
// clock never moves on its own, so drive it explicitly (interactions-test pattern).
let nowMs = 100_000;

async function setup(layerToggles?: NebulaLayerToggles) {
  const apiRef: MutableRefObject<NebulaApi | null> = { current: null };
  render(
    <NebulaScene
      data={sceneData}
      onSelectSystem={vi.fn()}
      onHover={vi.fn()}
      apiRef={apiRef}
      layerToggles={layerToggles}
    />,
  );
  const host = screen.getByTestId('nebula-host');
  await waitFor(() => expect(host.querySelector('canvas')).not.toBeNull());
  const app = pixiState.apps[0];
  const tickFn = app.ticker.add.mock.calls[0][0] as () => void;
  const world = app.stage.children[0] as Container;
  const orbs = world.children.find((c) => c.label === 'orbs') as Container;
  const canvas = app.canvas as HTMLCanvasElement;
  /** Advance the mocked clock, then run one frame (act — the ticker can setState). */
  const tick = (advanceMs = 0) =>
    act(() => {
      nowMs += advanceMs;
      tickFn();
    });
  /** Finish any camera flight, then run the 250ms band cross-fade to completion
   * (dt is clamped to 100ms per frame, so the fade needs several frames). */
  const settle = () => {
    tick(FLIGHT_MS);
    for (let i = 0; i < 5; i++) tick(100);
  };
  const wheel = (notches: number, deltaY: number) =>
    act(() => {
      for (let i = 0; i < notches; i++) {
        canvas.dispatchEvent(
          new WheelEvent('wheel', { deltaY, deltaMode: 0, clientX: 0, clientY: 0, bubbles: true, cancelable: true }),
        );
      }
    });
  const tierOf = (label: string) => orbs.children.find((c) => c.label === label) as Container;
  /** Effective on-screen state: a tier only shows if it AND layers.orbs are visible. */
  const shows = (label: string) => orbs.visible && tierOf(label).visible;
  return {
    apiRef, tick, settle, tierOf, shows,
    zoomIn: (n: number) => wheel(n, -100),
    zoomOut: (n: number) => wheel(n, +100),
  };
}

const FLIGHT_MS = 600;

beforeEach(() => {
  pixiState.apps.length = 0;
  nowMs = 100_000;
  vi.spyOn(performance, 'now').mockImplementation(() => nowMs);
});

describe('lattice band gate', () => {
  it('builds both tiers with the relevant/irrelevant split the scene asked for', async () => {
    const { tierOf } = await setup();
    expect(tierOf(THREADS_NEAR).children.map((c) => c.label)).toEqual(['thread:X1-A→X1-B']);
    expect(tierOf(THREADS_FAR).children).toHaveLength(1); // B→C and C→D, batched
  });

  it('shows NEITHER tier at the GALAXY band', async () => {
    const { settle, shows, apiRef } = await setup();
    settle(); // land the initial fit → z = 1 × fit
    expect(apiRef.current!.band()).toBe('GALAXY');
    expect(shows(THREADS_NEAR)).toBe(false);
    expect(shows(THREADS_FAR)).toBe(false);
  });

  it('shows the near tier but HOLDS BACK the far tier at REGION — the legibility fix', async () => {
    const { settle, zoomIn, shows, apiRef } = await setup();
    settle();
    zoomIn(9); // z = 1.1⁹ ≈ 2.36 × fit ≥ REGION_ENTER (2.2)
    settle();
    expect(apiRef.current!.band()).toBe('REGION');
    expect(shows(THREADS_NEAR)).toBe(true);
    expect(shows(THREADS_FAR)).toBe(false);
  });

  it('reveals the far tier once the camera reaches SYSTEM', async () => {
    const { settle, shows, apiRef } = await setup();
    settle();
    act(() => apiRef.current!.focusSystem('X1-A')); // z = 12 × fit ≥ SYSTEM_ENTER (9)
    settle();
    expect(apiRef.current!.band()).toBe('SYSTEM');
    expect(shows(THREADS_NEAR)).toBe(true);
    expect(shows(THREADS_FAR)).toBe(true);
  });

  it('re-hides the far tier when the camera pulls back out of SYSTEM', async () => {
    const { settle, zoomOut, shows, apiRef } = await setup();
    settle();
    act(() => apiRef.current!.focusSystem('X1-A'));
    settle();
    expect(shows(THREADS_FAR)).toBe(true);
    // z = 12 → 12/1.1¹⁷ ≈ 2.36, back inside REGION (above REGION_EXIT 1.8).
    zoomOut(17);
    settle();
    expect(apiRef.current!.band()).toBe('REGION');
    expect(shows(THREADS_FAR)).toBe(false);
  });

  it('the Lattice toggle reveals the far tier at REGION, and clearing it hides it again', async () => {
    const on: NebulaLayerToggles = { lanes: true, paths: true, ships: true, freshness: true, lattice: true };
    const { settle, tick, zoomIn, shows, apiRef } = await setup(on);
    settle();
    zoomIn(9);
    settle();
    expect(apiRef.current!.band()).toBe('REGION');
    expect(shows(THREADS_FAR)).toBe(true);

    // The toggle is read from a ref every tick, so flipping the object is enough.
    on.lattice = false;
    tick(16);
    expect(shows(THREADS_FAR)).toBe(false);
  });

  it('the Lattice toggle does NOT force the lattice on at GALAXY (orbs stay hidden there)', async () => {
    const { settle, shows } = await setup({ lanes: true, paths: true, ships: true, freshness: true, lattice: true });
    settle();
    expect(shows(THREADS_NEAR)).toBe(false);
    expect(shows(THREADS_FAR)).toBe(false);
  });

  it('defaults the far tier off when the page passes no toggles at all', async () => {
    const { settle, zoomIn, shows } = await setup(undefined);
    settle();
    zoomIn(9);
    settle();
    expect(shows(THREADS_NEAR)).toBe(true);
    expect(shows(THREADS_FAR)).toBe(false);
  });
});

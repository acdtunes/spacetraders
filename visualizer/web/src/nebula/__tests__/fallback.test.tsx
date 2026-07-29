// Task 14 — WebGL fallback proof at PAGE level: pixi init rejects → the scene
// degrades to the static `.nebula-fallback` notice while the DOM chrome around
// it (window switch, layer toggles, tour roster) still mounts and stays live.
// The REAL NebulaScene renders inside the REAL TradeFlowsView; only pixi's
// Application is stubbed (jsdom has no WebGL), with init hard-rejecting — the
// scene-in-isolation half of this proof lives in NebulaScene.mount.test.tsx.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { TradeFlowsView } from '../../pages/TradeFlowsView';
import { useFlowStore } from '../../store/flowStore';
import { mockTopology, mockLanes, mockLiveFlows } from '../../mocks/mockFlows';

// ---- pixi.js: real module, Application stubbed with an init that REJECTS ----
const pixiState = vi.hoisted(() => ({ apps: [] as any[] }));

vi.mock('pixi.js', async (importOriginal) => {
  const actual = await importOriginal<typeof import('pixi.js')>();
  class StubApplication {
    stage = new actual.Container();
    canvas = document.createElement('canvas');
    renderer: unknown = null;
    ticker = { add: vi.fn(), remove: vi.fn() };
    destroy = vi.fn();
    init = vi.fn(async (_opts: unknown) => {
      throw new Error('WebGL unavailable');
    });
    constructor() {
      pixiState.apps.push(this);
    }
  }
  return { ...actual, Application: StubApplication };
});

class StubResizeObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
}
vi.stubGlobal('ResizeObserver', StubResizeObserver);

beforeEach(() => {
  pixiState.apps.length = 0;
  useFlowStore.setState(useFlowStore.getInitialState());
});

describe('WebGL fallback (page level)', () => {
  it('init rejection renders the fallback card; the surrounding chrome still mounts; no crash propagates', async () => {
    const { unmount } = render(
      <MemoryRouter>
        <TradeFlowsView />
      </MemoryRouter>,
    );
    act(() => {
      const s = useFlowStore.getState();
      s.setTopology(mockTopology);
      s.setLanes(mockLanes('6h'));
      s.setLive(mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')));
    });

    // Scene half: the init failure lands as the static fallback notice...
    const card = await screen.findByText(/WebGL unavailable/);
    expect(card.className).toContain('nebula-fallback');
    // ...with no canvas ever attached (init never produced a renderer).
    expect(document.querySelector('canvas')).toBeNull();

    // Chrome half: the panels around the dead scene are alive.
    for (const w of ['1h', '6h', '24h']) {
      expect(screen.getByRole('button', { name: w })).toBeInTheDocument();
    }
    for (const k of ['lanes', 'paths', 'ships', 'freshness']) {
      expect(screen.getByRole('button', { name: k })).toBeInTheDocument();
    }
    // Store→panel wiring runs despite the dead scene: the roster lists hulls.
    expect(screen.getByText('TORWIND-3')).toBeInTheDocument();

    // Teardown after a failed init is clean: nothing was created to destroy.
    expect(() => unmount()).not.toThrow();
    expect(pixiState.apps[0].destroy).not.toHaveBeenCalled();
  });
});

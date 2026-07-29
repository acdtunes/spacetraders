import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { TradeFlowsView } from '../TradeFlowsView';
import { useFlowStore } from '../../store/flowStore';
import { mockTopology, mockLanes, mockLiveFlows } from '../../mocks/mockFlows';

// The pixi NebulaScene needs WebGL, which jsdom lacks. This test covers only
// the HTML overlay layer (window switch, layer toggles, detail panel, roster
// auto-filter, FEED LOST chip, detail-error chip) — the on-canvas render is
// verified by the mandatory screenshot step. Stub the scene but CAPTURE its
// props so the scene→page channels (onSelectSystem, onDetailError) can be
// driven from the tests.
const nebulaProps = vi.hoisted(() => ({ current: null as any }));
vi.mock('../../nebula/NebulaScene', () => ({
  NebulaScene: (props: unknown) => {
    nebulaProps.current = props;
    return null;
  },
}));

// Seed the store directly (bypass the network/poll) so layout is deterministic.
beforeEach(() => {
  useFlowStore.setState(useFlowStore.getInitialState());
  nebulaProps.current = null;
});

function seed() {
  const s = useFlowStore.getState();
  s.setTopology(mockTopology);
  s.setLanes(mockLanes('6h'));
  s.setLive(mockLiveFlows(Date.parse('2026-07-11T00:00:00Z')));
}

describe('TradeFlowsView layout (demo, fleet-stopped)', () => {
  it('renders the window switch with the three windows', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => seed());
    for (const w of ['1h', '6h', '24h']) {
      expect(screen.getByRole('button', { name: w })).toBeInTheDocument();
    }
  });

  it('renders the four layer-toggle buttons wired through to the scene', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => seed());
    const lanesBtn = screen.getByRole('button', { name: 'lanes' });
    expect(screen.getByRole('button', { name: 'paths' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'ships' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'freshness' })).toBeInTheDocument();
    act(() => lanesBtn.click());
    expect(useFlowStore.getState().layerToggles.lanes).toBe(false);
    // ...and the scene receives the store's toggles as a prop.
    expect(nebulaProps.current.layerToggles.lanes).toBe(false);
  });

  it('shows the detail panel when a flow is selected', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      seed();
      useFlowStore.getState().selectFlow('tour-run-TORWIND-3-galaxyA');
    });
    // The roster also lists TORWIND-3, so the ship symbol is no longer unique
    // to the panel; the first match still proves it rendered, and the
    // detail-panel-only tranche good below confirms the panel specifically.
    await waitFor(() => expect(screen.getAllByText('TORWIND-3')[0]).toBeInTheDocument());
    expect(screen.getByText(/ADVANCED_CIRCUITRY/)).toBeInTheDocument();
  });

  it('shows the FEED LOST chip when the live feed reports feedLost', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      useFlowStore.getState().setTopology(mockTopology);
      useFlowStore.getState().setLive({ flows: [], generatedAt: new Date().toISOString(), feedLost: true, lastPlanAt: '2026-07-11T00:00:00Z' });
    });
    await waitFor(() => expect(screen.getByText(/FEED LOST/)).toBeInTheDocument());
  });

  it('auto-filters the roster to the scene-focused system and restores on unfocus', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => seed());
    // Demo flows roster up unfiltered.
    expect(screen.getByText('TORWIND-3')).toBeInTheDocument(); // in X1-NK36
    expect(screen.getByText('TORWIND-7')).toBeInTheDocument(); // dwelling in X1-KA42

    // The scene reports a focused system (orb tap) → only its resident hulls.
    act(() => nebulaProps.current.onSelectSystem('X1-KA42'));
    expect(screen.getByText('TORWIND-7')).toBeInTheDocument();
    expect(screen.queryByText('TORWIND-3')).not.toBeInTheDocument();

    // Unfocus (wheel-out / Escape reports null) → the full roster returns.
    act(() => nebulaProps.current.onSelectSystem(null));
    expect(screen.getByText('TORWIND-3')).toBeInTheDocument();
  });

  it('surfaces the focused system detail-fetch error as a one-line chip, cleared on recovery', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      seed();
      nebulaProps.current.onSelectSystem('X1-KA42');
      nebulaProps.current.onDetailError('failed to load waypoints');
    });
    expect(screen.getByText('X1-KA42: failed to load waypoints')).toBeInTheDocument();
    act(() => nebulaProps.current.onDetailError(null));
    expect(screen.queryByText(/failed to load waypoints/)).not.toBeInTheDocument();
  });
});

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { TradeFlowsView } from '../TradeFlowsView';
import { useFlowStore } from '../../store/flowStore';
import { mockTopology, mockLanes, mockLiveFlows } from '../../mocks/mockFlows';

// The Konva galaxy scene draws to a real <canvas>, which jsdom has no 2D context
// for (node-canvas is not installed). This test covers only the HTML overlay
// layer (window switch, detail panel, drilldown, FEED LOST chip) — the on-canvas
// render is verified by the mandatory screenshot step (Task 10). Stub the scene.
vi.mock('../../components/flows/FlowGalaxyScene', () => ({
  default: () => null,
}));

// Seed the store directly (bypass the network/poll) so layout is deterministic.
beforeEach(() => {
  useFlowStore.setState(useFlowStore.getInitialState());
  vi.spyOn(useFlowStore.getState(), 'setError');
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

  it('shows the detail panel when a flow is selected', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      seed();
      useFlowStore.getState().selectFlow('tour-run-TORWIND-19-086680f9');
    });
    await waitFor(() => expect(screen.getByText('TORWIND-19')).toBeInTheDocument());
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
});

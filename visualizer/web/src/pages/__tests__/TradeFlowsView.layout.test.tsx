import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { TradeFlowsView } from '../TradeFlowsView';
import { useFlowStore } from '../../store/flowStore';
import { mockTopology, mockLanes, mockLiveFlows, mockSystemWaypoints } from '../../mocks/mockFlows';

// The Konva galaxy scene draws to a real <canvas>, which jsdom has no 2D context
// for (node-canvas is not installed). This test covers only the HTML overlay
// layer (window switch, detail panel, drilldown header/badge, FEED LOST chip) —
// the on-canvas render (galaxy home marker, drilldown waypoint scene) is verified
// by the mandatory screenshot step (Task 10). Stub the scene.
vi.mock('../../components/flows/FlowGalaxyScene', () => ({
  default: () => null,
}));

// The drilldown fetches this system's waypoints; serve the demo fixture so the
// header count + HOME badge render without a live server.
const { getWaypointsMock } = vi.hoisted(() => ({ getWaypointsMock: vi.fn() }));
vi.mock('../../services/api/systems', () => ({ getWaypoints: getWaypointsMock }));

// Seed the store directly (bypass the network/poll) so layout is deterministic.
beforeEach(() => {
  useFlowStore.setState(useFlowStore.getInitialState());
  vi.spyOn(useFlowStore.getState(), 'setError');
  getWaypointsMock.mockImplementation(async (sym: string) => mockSystemWaypoints(sym) ?? []);
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
      useFlowStore.getState().selectFlow('tour-run-TORWIND-3-galaxyA');
    });
    await waitFor(() => expect(screen.getByText('TORWIND-3')).toBeInTheDocument());
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

  it('opens the drilldown with the HOME badge + waypoint count for the home system', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      seed();
      useFlowStore.getState().openDrilldown('X1-NK36'); // the demo home system
    });
    await waitFor(() => expect(screen.getByLabelText('home system')).toBeInTheDocument());
    expect(screen.getByText('X1-NK36')).toBeInTheDocument();
    expect(screen.getByText(/5 waypoints/)).toBeInTheDocument(); // fixture ships 5 for X1-NK36
  });

  it('drilldown for a non-home system shows no HOME badge', async () => {
    render(<MemoryRouter><TradeFlowsView /></MemoryRouter>);
    act(() => {
      seed();
      useFlowStore.getState().openDrilldown('X1-KA42');
    });
    await waitFor(() => expect(screen.getByText('X1-KA42')).toBeInTheDocument());
    expect(screen.queryByLabelText('home system')).not.toBeInTheDocument();
  });
});

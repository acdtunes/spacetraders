import { create } from 'zustand';
import type { LiveFlowsResponse, LanesResponse, TopologyResponse, FlowWindow } from '../types/flows';

export interface FlowState {
  topology: TopologyResponse | null;
  lanes: LanesResponse | null;
  live: LiveFlowsResponse | null;
  window: FlowWindow;
  lastPlanAt: string | null;   // sticky across feed loss
  selectedFlowId: string | null;
  drilldownSystem: string | null;
  error: string | null;

  setTopology: (t: TopologyResponse) => void;
  setLanes: (l: LanesResponse) => void;
  setLive: (l: LiveFlowsResponse) => void;
  setWindow: (w: FlowWindow) => void;
  selectFlow: (containerId: string | null) => void;
  openDrilldown: (systemSymbol: string) => void;
  closeDrilldown: () => void;
  setError: (message: string | null) => void;
}

export const useFlowStore = create<FlowState>((set) => ({
  topology: null,
  lanes: null,
  live: null,
  window: '6h',
  lastPlanAt: null,
  selectedFlowId: null,
  drilldownSystem: null,
  error: null,

  setTopology: (topology) => set({ topology, error: null }),
  setLanes: (lanes) => set({ lanes, error: null }),
  // lastPlanAt is sticky: only advance it when the server reports a real plan.
  setLive: (live) =>
    set((state) => ({
      live,
      error: null,
      lastPlanAt: live.lastPlanAt ?? state.lastPlanAt,
    })),
  setWindow: (window) => set({ window }),
  selectFlow: (selectedFlowId) => set({ selectedFlowId }),
  openDrilldown: (drilldownSystem) => set({ drilldownSystem }),
  closeDrilldown: () => set({ drilldownSystem: null }),
  setError: (error) => set({ error }),
}));

import { create } from 'zustand';
import type { LiveFlowsResponse, LiveFlow, LanesResponse, TopologyResponse, FlowWindow, FreshnessResponse, FillsResponse, TooltipState } from '../types/flows';

export interface FlowState {
  topology: TopologyResponse | null;
  lanes: LanesResponse | null;
  live: LiveFlowsResponse | null;
  window: FlowWindow;
  lastPlanAt: string | null;   // sticky across feed loss
  selectedFlowId: string | null;
  drilldownSystem: string | null;
  error: string | null;
  hoveredFlowId: string | null;
  focusFlowId: string | null;    // one-shot camera request; scene clears it
  layerToggles: { lanes: boolean; paths: boolean; ships: boolean; freshness: boolean };
  staleFlows: LiveFlow[] | null; // last live flows, frozen while feedLost
  freezeAtMs: number | null;     // clock value the frozen render pins to
  freshness: FreshnessResponse | null;
  freshnessMissedPolls: number;  // consecutive freshness-poll failures; >=5 dims the layer
  fills: FillsResponse | null;   // ambient ticker feed (15s poll; failures skip silently)
  tooltip: TooltipState;         // shared hover card for a system node or lane

  setTopology: (t: TopologyResponse) => void;
  setLanes: (l: LanesResponse) => void;
  setLive: (l: LiveFlowsResponse) => void;
  setWindow: (w: FlowWindow) => void;
  selectFlow: (containerId: string | null) => void;
  openDrilldown: (systemSymbol: string) => void;
  closeDrilldown: () => void;
  setError: (message: string | null) => void;
  hoverFlow: (containerId: string | null) => void;
  requestFocus: (containerId: string) => void;
  clearFocus: () => void;
  toggleLayer: (key: 'lanes' | 'paths' | 'ships' | 'freshness') => void;
  // Freshness poll: success resets the missed counter; failure increments it
  // (freshness failures dim the layer, they never surface through setError).
  setFreshness: (freshness: FreshnessResponse) => void;
  freshnessPollFailed: () => void;
  setFills: (fills: FillsResponse) => void;
  setTooltip: (tooltip: TooltipState) => void;
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
  hoveredFlowId: null,
  focusFlowId: null,
  layerToggles: { lanes: true, paths: true, ships: true, freshness: true },
  staleFlows: null,
  freezeAtMs: null,
  freshness: null,
  freshnessMissedPolls: 0,
  fills: null,
  tooltip: null,

  setTopology: (topology) => set({ topology, error: null }),
  setLanes: (lanes) => set({ lanes, error: null }),
  // lastPlanAt is sticky; staleFlows freezes the last real snapshot the moment
  // the feed drops (never fabricate motion on stale intent — spec §8).
  setLive: (live) =>
    set((state) => ({
      live,
      error: null,
      lastPlanAt: live.lastPlanAt ?? state.lastPlanAt,
      ...(live.feedLost
        ? state.staleFlows
          ? {}
          : {
              staleFlows: state.live && !state.live.feedLost && state.live.flows.length > 0 ? state.live.flows : null,
              freezeAtMs: Date.now(),
            }
        : { staleFlows: null, freezeAtMs: null }),
    })),
  setWindow: (window) => set({ window }),
  selectFlow: (selectedFlowId) => set({ selectedFlowId }),
  openDrilldown: (drilldownSystem) => set({ drilldownSystem }),
  closeDrilldown: () => set({ drilldownSystem: null }),
  setError: (error) => set({ error }),

  hoverFlow: (hoveredFlowId) => set({ hoveredFlowId }),
  requestFocus: (focusFlowId) => set({ focusFlowId }),
  clearFocus: () => set({ focusFlowId: null }),
  // Toggling Lanes OFF removes the hovered artery from under the pointer, so
  // its tooltip must not linger (no mouseleave will ever fire for it).
  toggleLayer: (key) =>
    set((state) => ({
      layerToggles: { ...state.layerToggles, [key]: !state.layerToggles[key] },
      ...(key === 'lanes' && state.layerToggles.lanes && state.tooltip?.kind === 'lane' ? { tooltip: null } : {}),
    })),
  setFreshness: (freshness) => set({ freshness, freshnessMissedPolls: 0 }),
  freshnessPollFailed: () => set((s) => ({ freshnessMissedPolls: s.freshnessMissedPolls + 1 })),
  setFills: (fills) => set({ fills }),
  setTooltip: (tooltip) => set({ tooltip }),
}));

// Dev-only debugging affordance: expose the store so the flows tab can be driven
// from the console / e2e (e.g. window.__flowStore.getState().openDrilldown('X1-UQ16')).
// Guarded by import.meta.env.DEV, so it is stripped from production builds.
if (typeof window !== 'undefined' && import.meta.env.DEV) {
  (window as unknown as { __flowStore?: typeof useFlowStore }).__flowStore = useFlowStore;
}

import { describe, it, expect, beforeEach } from 'vitest';
import { useFlowStore } from '../flowStore';
import { mockLiveFlows, mockFeedLostResponse } from '../../mocks/mockFlows';

describe('flowStore', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
  });

  it('setLive stores flows and updates sticky lastPlanAt on a live feed', () => {
    const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
    useFlowStore.getState().setLive(res);
    expect(useFlowStore.getState().live?.flows).toHaveLength(4);
    expect(useFlowStore.getState().lastPlanAt).toBe(res.lastPlanAt);
  });

  it('keeps the previous lastPlanAt sticky when a feed-loss response arrives', () => {
    const live = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
    useFlowStore.getState().setLive(live);
    const before = useFlowStore.getState().lastPlanAt;
    useFlowStore.getState().setLive(mockFeedLostResponse(Date.parse('2026-07-11T00:10:00Z')));
    expect(useFlowStore.getState().live?.feedLost).toBe(true);
    expect(useFlowStore.getState().lastPlanAt).toBe(before); // unchanged, not null
  });

  it('setWindow switches the active lane window', () => {
    useFlowStore.getState().setWindow('24h');
    expect(useFlowStore.getState().window).toBe('24h');
  });

  it('selectFlow and drilldown toggles round-trip', () => {
    const s = useFlowStore.getState();
    s.selectFlow('tour-run-1');
    expect(useFlowStore.getState().selectedFlowId).toBe('tour-run-1');
    s.openDrilldown('X1-KA42');
    expect(useFlowStore.getState().drilldownSystem).toBe('X1-KA42');
    s.closeDrilldown();
    expect(useFlowStore.getState().drilldownSystem).toBeNull();
  });
});

describe('galaxy view state', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
  });

  it('hover, focus, and layer toggles round-trip', () => {
    const s = useFlowStore.getState();
    s.hoverFlow('tour-1');
    expect(useFlowStore.getState().hoveredFlowId).toBe('tour-1');
    s.hoverFlow(null);
    expect(useFlowStore.getState().hoveredFlowId).toBeNull();

    s.requestFocus('tour-2');
    expect(useFlowStore.getState().focusFlowId).toBe('tour-2');
    s.clearFocus();
    expect(useFlowStore.getState().focusFlowId).toBeNull();

    expect(useFlowStore.getState().layerToggles).toEqual({ lanes: true, paths: true, ships: true, freshness: true });
    s.toggleLayer('lanes');
    expect(useFlowStore.getState().layerToggles.lanes).toBe(false);
    s.toggleLayer('lanes');
    expect(useFlowStore.getState().layerToggles.lanes).toBe(true);
    s.toggleLayer('freshness');
    expect(useFlowStore.getState().layerToggles.freshness).toBe(false);
  });

  it('freezes the last live flows across feed loss and clears on recovery', () => {
    const s = useFlowStore.getState();
    const mk = (feedLost: boolean, flows: any[]) => ({ flows, generatedAt: new Date().toISOString(), feedLost, lastPlanAt: null });
    s.setLive(mk(false, [{ containerId: 'a' }]) as any);
    s.setLive(mk(true, []) as any);
    expect(useFlowStore.getState().staleFlows?.[0]?.containerId).toBe('a');
    expect(useFlowStore.getState().freezeAtMs).not.toBeNull();
    s.setLive(mk(true, []) as any); // repeated loss keeps the ORIGINAL snapshot
    expect(useFlowStore.getState().staleFlows?.[0]?.containerId).toBe('a');
    s.setLive(mk(false, []) as any);
    expect(useFlowStore.getState().staleFlows).toBeNull();
    expect(useFlowStore.getState().freezeAtMs).toBeNull();
  });
});

describe('freshness state', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
  });

  it('stores responses and resets the missed-poll counter', () => {
    const s = useFlowStore.getState();
    s.freshnessPollFailed();
    s.freshnessPollFailed();
    expect(useFlowStore.getState().freshnessMissedPolls).toBe(2);
    s.setFreshness({ systems: [], staleAfterMinutes: 75, generatedAt: 'x' });
    expect(useFlowStore.getState().freshnessMissedPolls).toBe(0);
    expect(useFlowStore.getState().freshness?.staleAfterMinutes).toBe(75);
  });
});

describe('fills + tooltip state', () => {
  beforeEach(() => {
    useFlowStore.setState(useFlowStore.getInitialState());
  });

  it('stores fills and tooltip round-trips', () => {
    const s = useFlowStore.getState();
    s.setFills({ fills: [{ id: 't-1', at: 'x', ship: 'S', good: 'IRON', isBuy: false, units: 1, credits: 5, waypoint: 'W' }], generatedAt: 'x' });
    expect(useFlowStore.getState().fills?.fills[0].id).toBe('t-1');
    s.setTooltip({ kind: 'system', key: 'X1-AA', x: 10, y: 20 });
    expect(useFlowStore.getState().tooltip?.key).toBe('X1-AA');
    s.setTooltip(null);
    expect(useFlowStore.getState().tooltip).toBeNull();
  });
});

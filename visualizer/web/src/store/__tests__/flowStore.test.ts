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
    expect(useFlowStore.getState().live?.flows).toHaveLength(3);
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

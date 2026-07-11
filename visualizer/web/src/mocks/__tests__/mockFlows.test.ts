import { describe, it, expect } from 'vitest';
import {
  mockTopology,
  mockLanes,
  mockLiveFlows,
  mockFeedLostResponse,
} from '../mockFlows';

describe('mockFlows fixtures', () => {
  it('topology has systems and only real (non-backoff) edges', () => {
    expect(mockTopology.systems.length).toBeGreaterThan(1);
    expect(mockTopology.edges.length).toBeGreaterThan(0);
    // Every edge endpoint resolves to a system node.
    const symbols = new Set(mockTopology.systems.map((s) => s.symbol));
    for (const e of mockTopology.edges) {
      expect(symbols.has(e.from)).toBe(true);
      expect(symbols.has(e.to)).toBe(true);
      expect(e.to).not.toBe(''); // no backoff markers
    }
  });

  it('live fixture covers all three programs, each with a current leg', () => {
    const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
    expect(res.feedLost).toBe(false);
    const programs = res.flows.map((f) => f.program).sort();
    expect(programs).toEqual(['arb', 'tour', 'trade-route']);
    for (const f of res.flows) {
      expect(f.currentLeg).not.toBeNull();
      expect(typeof f.plannedAt).toBe('string');
    }
  });

  it('the tour flow carries remaining hops with priced tranches', () => {
    const res = mockLiveFlows(Date.parse('2026-07-11T00:00:00Z'));
    const tour = res.flows.find((f) => f.program === 'tour')!;
    expect(tour.remainingHops.length).toBeGreaterThan(0);
    const tranche = tour.remainingHops[0].tranches[0];
    expect(tranche.expectedUnitPrice).toBeGreaterThan(0);
    expect(typeof tranche.isBuy).toBe('boolean');
  });

  it('feed-loss fixture is empty flows + feedLost true + null lastPlanAt', () => {
    const res = mockFeedLostResponse(Date.parse('2026-07-11T00:00:00Z'));
    expect(res.feedLost).toBe(true);
    expect(res.flows).toEqual([]);
    expect(res.lastPlanAt).toBeNull();
  });

  it('lanes fixture is sorted by realized profit descending', () => {
    const lanes = mockLanes('6h').lanes;
    expect(lanes.length).toBeGreaterThan(0);
    for (let i = 1; i < lanes.length; i++) {
      expect(lanes[i - 1].realizedProfit).toBeGreaterThanOrEqual(lanes[i].realizedProfit);
    }
  });
});

import { describe, it, expect } from 'vitest';
import { mockRequest } from '../mockClient';
import type { LiveFlowsResponse, TopologyResponse, LanesResponse } from '../../../types/flows';

describe('mockClient /flows dispatch', () => {
  it('serves topology with systems + edges', async () => {
    const res = await mockRequest<TopologyResponse>('/flows/topology');
    expect(res.systems.length).toBeGreaterThan(0);
    expect(res.edges.length).toBeGreaterThan(0);
  });

  it('serves lanes for the requested window', async () => {
    const res = await mockRequest<LanesResponse>('/flows/lanes?window=24h');
    expect(res.window).toBe('24h');
    expect(res.lanes.length).toBeGreaterThan(0);
  });

  it('serves live flows OR a feed-loss envelope (never throws)', async () => {
    const res = await mockRequest<LiveFlowsResponse>('/flows/live');
    expect(typeof res.feedLost).toBe('boolean');
    if (res.feedLost) expect(res.flows).toEqual([]);
    else expect(res.flows.length).toBe(3);
  });
});

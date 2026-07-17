import { fetchApi } from './client';
import type { LiveFlowsResponse, LanesResponse, TopologyResponse, FlowWindow, FreshnessResponse, FillsResponse } from '../../types/flows';

export async function getFlowsLive(): Promise<LiveFlowsResponse> {
  return fetchApi<LiveFlowsResponse>('/flows/live');
}

export async function getFlowFills(): Promise<FillsResponse> {
  return fetchApi<FillsResponse>('/flows/fills');
}

export async function getFlowLanes(window: FlowWindow): Promise<LanesResponse> {
  return fetchApi<LanesResponse>(`/flows/lanes?window=${window}`);
}

export async function getFlowTopology(): Promise<TopologyResponse> {
  return fetchApi<TopologyResponse>('/flows/topology');
}

export async function getFlowFreshness(): Promise<FreshnessResponse> {
  return fetchApi<FreshnessResponse>('/flows/freshness');
}

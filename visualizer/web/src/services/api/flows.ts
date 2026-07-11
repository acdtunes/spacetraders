import { fetchApi } from './client';
import type { LiveFlowsResponse, LanesResponse, TopologyResponse, FlowWindow } from '../../types/flows';

export async function getFlowsLive(): Promise<LiveFlowsResponse> {
  return fetchApi<LiveFlowsResponse>('/flows/live');
}

export async function getFlowLanes(window: FlowWindow): Promise<LanesResponse> {
  return fetchApi<LanesResponse>(`/flows/lanes?window=${window}`);
}

export async function getFlowTopology(): Promise<TopologyResponse> {
  return fetchApi<TopologyResponse>('/flows/topology');
}

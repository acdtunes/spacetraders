import type { System, Waypoint, Market, ApiResponse } from '../../types/spacetraders';
import { fetchApi } from './client';
import { fetchAllPaginated } from '../../utils/pagination';

export async function getSystem(systemSymbol: string): Promise<System> {
  const data = await fetchApi<ApiResponse<System>>(`/systems/${systemSymbol}`);
  return data.data;
}

export async function getWaypoints(systemSymbol: string): Promise<Waypoint[]> {
  return fetchAllPaginated<Waypoint>(
    (page, limit) => fetchApi<ApiResponse<Waypoint[]>>(`/systems/${systemSymbol}/waypoints?limit=${limit}&page=${page}`)
  );
}

export async function getWaypoint(systemSymbol: string, waypointSymbol: string): Promise<Waypoint> {
  const data = await fetchApi<ApiResponse<Waypoint>>(
    `/systems/${systemSymbol}/waypoints/${waypointSymbol}`
  );
  return data.data;
}

export async function getMarket(
  systemSymbol: string,
  waypointSymbol: string,
  agentId: string
): Promise<Market> {
  const data = await fetchApi<ApiResponse<Market>>(
    `/systems/${systemSymbol}/waypoints/${waypointSymbol}/market?agentId=${agentId}`
  );
  return data.data;
}

export async function getAllSystems(): Promise<System[]> {
  return fetchAllPaginated<System>(
    (page, limit) => fetchApi<ApiResponse<System[]>>(`/systems?limit=${limit}&page=${page}`)
  );
}

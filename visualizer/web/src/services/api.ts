import type { Agent, Ship, Waypoint, System, Market, ApiResponse } from '../types/spacetraders';

const API_BASE = '/api';

async function fetchApi<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const url = `${API_BASE}${endpoint}`;

  try {
    const response = await fetch(url, {
      headers: {
        'Content-Type': 'application/json',
        ...options?.headers,
      },
      ...options,
    });

    if (!response.ok) {
      const contentType = response.headers.get('content-type');

      // Check if we got HTML instead of JSON (backend not running)
      if (contentType?.includes('text/html')) {
        throw new Error(
          'Backend server not responding. Make sure the server is running on port 4000. ' +
          'Run: cd server && npm start'
        );
      }

      const error = await response.json().catch(() => ({ error: 'Request failed' }));
      throw new Error(error.error || `HTTP ${response.status}`);
    }

    return response.json();
  } catch (err: any) {
    // Network error (backend not running)
    if (err.message.includes('Failed to fetch') || err.name === 'TypeError') {
      throw new Error(
        'Cannot connect to backend server. Make sure the server is running on port 4000. ' +
        'Run: cd server && npm start'
      );
    }
    throw err;
  }
}

// Agent APIs
export async function getAgents(): Promise<Agent[]> {
  const data = await fetchApi<{ agents: Agent[] }>('/agents');
  return data.agents;
}

export async function addAgent(token: string): Promise<Agent> {
  const data = await fetchApi<{ agent: Agent }>('/agents', {
    method: 'POST',
    body: JSON.stringify({ token }),
  });
  return data.agent;
}

export async function updateAgent(id: string, updates: Partial<Agent>): Promise<Agent> {
  const data = await fetchApi<{ agent: Agent }>(`/agents/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(updates),
  });
  return data.agent;
}

export async function deleteAgent(id: string): Promise<void> {
  await fetchApi(`/agents/${id}`, { method: 'DELETE' });
}

export async function getAgentShips(agentId: string): Promise<Ship[]> {
  const data = await fetchApi<ApiResponse<Ship[]>>(`/agents/${agentId}/ships`);
  return data.data;
}

// System APIs
export async function getSystem(systemSymbol: string): Promise<System> {
  const data = await fetchApi<ApiResponse<System>>(`/systems/${systemSymbol}`);
  return data.data;
}

export async function getWaypoints(systemSymbol: string): Promise<Waypoint[]> {
  // Fetch all waypoints by using pagination
  let allWaypoints: Waypoint[] = [];
  let page = 1;
  let hasMore = true;

  while (hasMore) {
    const data = await fetchApi<ApiResponse<Waypoint[]>>(`/systems/${systemSymbol}/waypoints?limit=20&page=${page}`);
    allWaypoints = [...allWaypoints, ...data.data];

    // Check if there are more pages (SpaceTraders API returns meta info)
    const meta = (data as any).meta;
    if (meta && meta.page * meta.limit >= meta.total) {
      hasMore = false;
    } else if (data.data.length < 20) {
      hasMore = false;
    } else {
      page++;
    }
  }

  return allWaypoints;
}

export async function getWaypoint(systemSymbol: string, waypointSymbol: string): Promise<Waypoint> {
  const data = await fetchApi<ApiResponse<Waypoint>>(
    `/systems/${systemSymbol}/waypoints/${waypointSymbol}`
  );
  return data.data;
}

// Market APIs
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

// Galaxy APIs
export async function getAllSystems(): Promise<System[]> {
  // Fetch all systems with pagination
  let allSystems: System[] = [];
  let page = 1;
  let hasMore = true;

  while (hasMore) {
    const data = await fetchApi<ApiResponse<System[]>>(`/systems?limit=20&page=${page}`);
    allSystems = [...allSystems, ...data.data];

    // Check if there are more pages
    const meta = (data as any).meta;
    if (meta && meta.page * meta.limit >= meta.total) {
      hasMore = false;
    } else if (data.data.length < 20) {
      hasMore = false;
    } else {
      page++;
    }
  }

  return allSystems;
}

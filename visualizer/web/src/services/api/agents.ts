import type { Agent, Ship, ApiResponse } from '../../types/spacetraders';
import { fetchApi } from './client';

export async function getAgents(): Promise<Agent[]> {
  const data = await fetchApi<{ agents: Agent[] }>('/agents');
  return data.agents;
}

export async function addAgent(token: string): Promise<Agent> {
  const data = await fetchApi<{ agent: Agent }>('/agents', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ token }),
  });
  return data.agent;
}

export async function updateAgent(id: string, updates: Partial<Agent>): Promise<Agent> {
  const data = await fetchApi<{ agent: Agent }>(`/agents/${id}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
    },
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

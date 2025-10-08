import type { ApiRequestOptions } from './client';
import type { Agent, ApiResponse } from '../../types/spacetraders';
import { mockState, startMockScenarioIfNeeded, advanceShipScenario } from '../../mocks/mockScenario';

const DEFAULT_LIMIT = 20;

const parseJsonBody = (body: BodyInit | null | undefined): any => {
  if (!body) return undefined;
  if (typeof body === 'string') {
    try {
      return JSON.parse(body);
    } catch (error) {
      console.warn('Failed to parse mock request body', error);
      return undefined;
    }
  }
  return undefined;
};

const paginate = <T>(items: T[], page: number, limit: number): ApiResponse<T[]> => {
  const start = (page - 1) * limit;
  const data = items.slice(start, start + limit);
  return {
    data,
    meta: {
      page,
      limit,
      total: items.length,
    },
  };
};

const getPageAndLimit = (searchParams: URLSearchParams): { page: number; limit: number } => {
  const page = Number.parseInt(searchParams.get('page') ?? '1', 10) || 1;
  const limit = Number.parseInt(searchParams.get('limit') ?? `${DEFAULT_LIMIT}`, 10) || DEFAULT_LIMIT;
  return { page, limit };
};

const getAgentById = (agentId: string): Agent | undefined =>
  mockState.agents.find((agent) => agent.id === agentId);

const sanitizeId = (value: string): string => value.trim().toUpperCase();

export async function mockRequest<T>(endpoint: string, options: ApiRequestOptions = {}): Promise<T> {
  startMockScenarioIfNeeded();

  const url = new URL(endpoint, 'https://mock.local');
  const path = url.pathname;
  const method = (options.method ?? 'GET').toUpperCase();

  // Agents endpoints
  if (path === '/agents' && method === 'GET') {
    const response = { agents: mockState.agents } as T;
    return response;
  }

  if (path === '/agents' && method === 'POST') {
    const payload = parseJsonBody(options.body);
    const token: string = payload?.token ?? 'demo';
    const id = `MOCK-${mockState.agents.length + 1}`;
    const newAgent: Agent = {
      id,
      symbol: token.toUpperCase(),
      color: '#22d3ee',
      visible: true,
      createdAt: new Date().toISOString(),
      credits: 0,
    };
    mockState.agents.push(newAgent);
    const response = { agent: newAgent } as T;
    return response;
  }

  if (path.startsWith('/agents/') && method === 'PATCH') {
    const agentId = sanitizeId(path.split('/')[2]);
    const payload = parseJsonBody(options.body) ?? {};
    const agent = getAgentById(agentId);
    if (!agent) {
      throw new Error(`Mock agent ${agentId} not found`);
    }
    Object.assign(agent, payload);
    return { agent } as T;
  }

  if (path.startsWith('/agents/') && method === 'DELETE') {
    const agentId = sanitizeId(path.split('/')[2]);
    mockState.agents = mockState.agents.filter((agent) => agent.id !== agentId);
    mockState.ships = mockState.ships.filter((ship) => ship.agentId !== agentId);
    return undefined as T;
  }

  if (path.startsWith('/agents/') && path.endsWith('/ships') && method === 'GET') {
    const segments = path.split('/');
    const agentId = sanitizeId(segments[2]);
    const { page, limit } = getPageAndLimit(url.searchParams);
    const ships = mockState.ships.filter((ship) => ship.agentId === agentId);
    return paginate(ships, page, limit) as T;
  }

  // Systems
  if (path === '/systems' && method === 'GET') {
    const { page, limit } = getPageAndLimit(url.searchParams);
    return paginate(mockState.systems, page, limit) as T;
  }

  if (path.startsWith('/systems/') && !path.includes('/waypoints') && method === 'GET') {
    const systemSymbol = sanitizeId(path.split('/')[2]);
    const system = mockState.systems.find((s) => s.symbol === systemSymbol);
    if (!system) {
      throw new Error(`Mock system ${systemSymbol} not found`);
    }
    return { data: system } as T;
  }

  if (path.includes('/waypoints') && path.match(/^\/systems\/[^/]+\/waypoints$/) && method === 'GET') {
    const systemSymbol = sanitizeId(path.split('/')[2]);
    const { page, limit } = getPageAndLimit(url.searchParams);
    const waypoints = mockState.waypoints.filter((wp) => wp.systemSymbol === systemSymbol);
    return paginate(waypoints, page, limit) as T;
  }

  if (path.match(/^\/systems\/[^/]+\/waypoints\/[^/]+$/) && method === 'GET') {
    const [, , systemSymbol, , waypointSymbol] = path.split('/');
    const waypoint = mockState.waypoints.find((wp) => wp.systemSymbol === sanitizeId(systemSymbol) && wp.symbol === sanitizeId(waypointSymbol));
    if (!waypoint) {
      throw new Error(`Mock waypoint ${waypointSymbol} not found`);
    }
    return { data: waypoint } as T;
  }

  if (path.endsWith('/market') && method === 'GET') {
    const [, , systemSymbol, , waypointSymbol] = path.split('/');
    const key = `${sanitizeId(systemSymbol)}:${sanitizeId(waypointSymbol)}`;
    const market = mockState.markets.get(key);
    return { data: market ?? { symbol: sanitizeId(waypointSymbol), exports: [], imports: [], exchange: [] } } as T;
  }

  // Scenario control endpoints (optional helpers)
  if (path === '/mock/advance' && method === 'POST') {
    advanceShipScenario();
    return undefined as T;
  }

  throw new Error(`Mock API does not handle ${method} ${path}`);
}

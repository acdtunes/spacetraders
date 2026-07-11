import type { ApiRequestOptions } from './client';
import type { Agent, ApiResponse } from '../../types/spacetraders';
import { mockState, startMockScenarioIfNeeded, advanceShipScenario } from '../../mocks/mockScenario';
import {
  DEMO_SIGNAL_LOSS_DURATION_MS,
  demoEventTicker,
  demoGateProgress,
  isSignalLossWindow,
} from '../../mocks/demoEvents';
import { mockTopology, mockLanes, mockLiveFlows, mockFeedLostResponse, mockSystemWaypoints } from '../../mocks/mockFlows';
import type { FlowWindow } from '../../types/flows';

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

/**
 * Demo-mode /bot/* namespace (Operational Pulse).
 *
 * The real /bot/* endpoints only exist against the live server, and mockRequest
 * throws for anything unhandled — which the polling heartbeat (/bot/events in
 * botPolling.pollCycle) would read as a permanently dead backend and pin the
 * HUD at SIGNAL LOST. In demo mode the namespace is synthesized instead
 * (mocks/demoEvents.ts): a deterministic captain-event ticker, a monotonic
 * jump-gate bill, and benign empty-but-valid envelopes for every other bot
 * dataset so their panels render an honest "nothing yet" rather than erroring.
 *
 * The one deliberate exception: during the recurring ~20s signal-loss drill
 * (isSignalLossWindow, every ~3min) EVERY /bot/* request throws, so
 * connection-health flips to 'lost', exponential backoff engages, and the HUD
 * auto-recovers once the window passes. Non-bot mock endpoints stay healthy
 * throughout — only the bot backend "goes dark".
 *
 * The whole namespace is read-only GETs in practice, so requests are handled
 * uniformly regardless of method.
 */
function demoBotRequest<T>(path: string, searchParams: URLSearchParams, nowMs: number): T {
  if (isSignalLossWindow(nowMs)) {
    throw new Error(
      `Mock API signal-loss drill: /bot/* is dark for ~${DEMO_SIGNAL_LOSS_DURATION_MS / 1000}s (${path})`
    );
  }

  // GET /bot/events?after=&limit= — the connection heartbeat + event feed.
  if (path === '/bot/events') {
    const afterRaw = searchParams.get('after');
    const after = afterRaw !== null && Number.isFinite(Number(afterRaw)) ? Number(afterRaw) : null;
    const limitRaw = searchParams.get('limit');
    const limit = limitRaw !== null && Number.isFinite(Number(limitRaw)) ? Number(limitRaw) : 50;
    return { events: demoEventTicker(nowMs, { afterId: after, limit }) } as T;
  }

  // GET /bot/construction/:waypointSymbol — jump-gate construction progress.
  if (path.startsWith('/bot/construction/')) {
    return demoGateProgress(nowMs) as T;
  }

  const emptyPeriod = { start: new Date(nowMs).toISOString(), end: new Date(nowMs).toISOString() };

  // Benign empty-but-valid envelopes for the remaining bot datasets, matching
  // the response shapes the fetchers in services/api/bot.ts unwrap.
  if (path === '/bot/assignments') return { assignments: [] } as T;
  if (path === '/bot/daemons') return { daemons: [] } as T;
  if (path === '/bot/players') return { players: [] } as T;
  if (path === '/bot/operations/summary') return { summary: [] } as T;
  if (path.startsWith('/bot/markets/') && path.endsWith('/freshness')) return { freshness: [] } as T;
  if (path.startsWith('/bot/markets/')) return { markets: [] } as T;
  if (path.startsWith('/bot/tours/')) return { tours: [] } as T;
  if (path.startsWith('/bot/trade-opportunities/')) return { opportunities: [] } as T;
  if (path.startsWith('/bot/transactions/')) return { transactions: [] } as T;
  if (path.startsWith('/bot/graph/')) {
    const systemSymbol = sanitizeId(path.split('/')[3] ?? '');
    return {
      graph: {
        system_symbol: systemSymbol,
        graph_data: { nodes: {}, edges: {} },
        created_at: new Date(nowMs).toISOString(),
        updated_at: new Date(nowMs).toISOString(),
      },
    } as T;
  }
  if (path === '/bot/ledger/transactions') {
    const limitRaw = searchParams.get('limit');
    const limit = limitRaw !== null && Number.isFinite(Number(limitRaw)) ? Number(limitRaw) : 50;
    return { transactions: [], total: 0, page: 1, limit } as T;
  }
  if (path === '/bot/ledger/cash-flow') {
    return {
      period: emptyPeriod,
      summary: { total_inflow: 0, total_outflow: 0, net_cash_flow: 0 },
      categories: [],
    } as T;
  }
  if (path === '/bot/ledger/profit-loss') {
    return {
      period: emptyPeriod,
      revenue: { total: 0, breakdown: {} },
      expenses: { total: 0, breakdown: {} },
      net_profit: 0,
      profit_margin: 0,
    } as T;
  }
  if (path === '/bot/ledger/profit-loss-by-operation') {
    return {
      period: emptyPeriod,
      summary: { total_revenue: 0, total_expenses: 0, net_profit: 0 },
      operations: [],
    } as T;
  }
  if (path === '/bot/ledger/balance-history') {
    return { dataPoints: [], current_balance: 0, starting_balance: 0, net_change: 0 } as T;
  }

  // Any future /bot/* endpoint: succeed benignly rather than fake-killing the
  // backend — only the drill above may make demo mode look disconnected.
  return {} as T;
}

// Demo-mode /flows/* namespace (Trade Flows tab). Deterministic from the caller
// clock so a fleet-stopped demo still interpolates. During the recurring
// signal-loss drill the LIVE feed goes dark (feedLost envelope) while topology
// and lanes — which are PG-backed in production and survive daemon loss — keep
// serving, so the tab degrades exactly as designed.
function demoFlowsRequest<T>(path: string, searchParams: URLSearchParams, nowMs: number): T {
  if (path === '/flows/topology') return mockTopology as T;
  if (path === '/flows/lanes') {
    const w = (searchParams.get('window') as FlowWindow) || '6h';
    const window: FlowWindow = w === '1h' || w === '6h' || w === '24h' ? w : '6h';
    return mockLanes(window) as T;
  }
  if (path === '/flows/live') {
    return (isSignalLossWindow(nowMs) ? mockFeedLostResponse(nowMs) : mockLiveFlows(nowMs)) as T;
  }
  return {} as T;
}

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
    // Trade Flows demo systems carry their own intra-system waypoint fixture so the
    // drilldown renders to scale; other systems fall back to the scenario waypoints.
    const flowsWaypoints = mockSystemWaypoints(systemSymbol);
    const waypoints = flowsWaypoints ?? mockState.waypoints.filter((wp) => wp.systemSymbol === systemSymbol);
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

  // Trade Flows namespace (demo — see demoFlowsRequest).
  if (path.startsWith('/flows/')) {
    return demoFlowsRequest<T>(path, url.searchParams, Date.now());
  }

  // Bot operations namespace (demo Operational Pulse — see demoBotRequest).
  if (path.startsWith('/bot/')) {
    return demoBotRequest<T>(path, url.searchParams, Date.now());
  }

  throw new Error(`Mock API does not handle ${method} ${path}`);
}

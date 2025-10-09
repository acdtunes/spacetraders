import { fetchApi } from './client';
import type {
  ShipAssignment,
  Daemon,
  MarketData,
  MarketFreshness,
  ScoutTour,
  TradeOpportunityData,
  MarketTransaction,
  SystemGraph,
  OperationSummary,
} from '../../types/spacetraders';

/**
 * Get all active ship assignments
 */
export async function getAssignments(): Promise<ShipAssignment[]> {
  const response = await fetchApi<{ assignments: ShipAssignment[] }>('/bot/assignments');
  return response.assignments;
}

/**
 * Get assignment for specific ship
 */
export async function getShipAssignment(shipSymbol: string): Promise<ShipAssignment> {
  const response = await fetchApi<{ assignment: ShipAssignment }>(`/bot/assignments/${shipSymbol}`);
  return response.assignment;
}

/**
 * Get all active daemons
 */
export async function getDaemons(): Promise<Daemon[]> {
  const response = await fetchApi<{ daemons: Daemon[] }>('/bot/daemons');
  return response.daemons;
}

/**
 * Get market data for system
 */
export async function getMarketData(systemSymbol: string): Promise<MarketData[]> {
  const response = await fetchApi<{ markets: MarketData[] }>(`/bot/markets/${systemSymbol}`);
  return response.markets;
}

/**
 * Get market freshness (last updated times)
 */
export async function getMarketFreshness(systemSymbol: string): Promise<MarketFreshness[]> {
  const response = await fetchApi<{ freshness: MarketFreshness[] }>(`/bot/markets/${systemSymbol}/freshness`);
  return response.freshness;
}

/**
 * Get scout tours for system
 */
export async function getScoutTours(systemSymbol: string): Promise<ScoutTour[]> {
  const response = await fetchApi<{ tours: ScoutTour[] }>(`/bot/tours/${systemSymbol}`);
  return response.tours;
}

/**
 * Get trade opportunities
 */
export async function getTradeOpportunities(
  systemSymbol: string,
  minProfit: number = 100
): Promise<TradeOpportunityData[]> {
  const response = await fetchApi<{ opportunities: TradeOpportunityData[] }>(
    `/bot/trade-opportunities/${systemSymbol}?minProfit=${minProfit}`
  );
  return response.opportunities;
}

/**
 * Get recent market transactions
 */
export async function getMarketTransactions(systemSymbol: string, limit: number = 100): Promise<MarketTransaction[]> {
  const response = await fetchApi<{ transactions: MarketTransaction[] }>(
    `/bot/transactions/${systemSymbol}?limit=${limit}`
  );
  return response.transactions;
}

/**
 * Get system navigation graph
 */
export async function getSystemGraph(systemSymbol: string): Promise<SystemGraph> {
  const response = await fetchApi<{ graph: SystemGraph }>(`/bot/graph/${systemSymbol}`);
  return response.graph;
}

/**
 * Get operations summary
 */
export async function getOperationsSummary(): Promise<OperationSummary[]> {
  const response = await fetchApi<{ summary: OperationSummary[] }>('/bot/operations/summary');
  return response.summary;
}

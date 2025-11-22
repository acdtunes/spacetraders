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
  FinancialTransaction,
  TransactionCategory,
  TransactionType,
  CashFlowData,
  ProfitLossData,
  BalanceHistoryData,
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
type RawMarketGood = {
  good_symbol: string;
  supply: string;
  activity: string | null;
  purchase_price: number;
  sell_price: number;
  trade_volume: number;
};

type RawMarketData = {
  waypoint_symbol: string;
  last_updated: string;
  goods: RawMarketGood[];
};

export async function getMarketData(systemSymbol: string): Promise<MarketData[]> {
  const response = await fetchApi<{ markets: RawMarketData[] }>(`/bot/markets/${systemSymbol}`);
  return response.markets.map((market) => ({
    waypointSymbol: market.waypoint_symbol,
    lastUpdated: market.last_updated,
    goods: market.goods.map((good) => ({
      symbol: good.good_symbol,
      supply: good.supply as MarketData['goods'][number]['supply'],
      activity: good.activity,
      purchasePrice: good.purchase_price,
      sellPrice: good.sell_price,
      tradeVolume: good.trade_volume,
    })),
  }));
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
export async function getScoutTours(systemSymbol: string, playerId?: number): Promise<ScoutTour[]> {
  const url = playerId
    ? `/bot/tours/${systemSymbol}?player_id=${playerId}`
    : `/bot/tours/${systemSymbol}`;

  console.log('[getScoutTours] Fetching tours', {
    systemSymbol,
    playerId,
    url,
    filteringEnabled: playerId !== undefined,
  });

  const response = await fetchApi<{ tours: ScoutTour[] }>(url);

  console.log('[getScoutTours] Response received', {
    toursCount: response.tours.length,
    tours: response.tours.map(t => ({
      ship: t.ship_symbol,
      player_id: t.player_id,
      daemon_id: t.daemon_id,
      markets: t.markets.length
    })),
    requestedPlayerId: playerId,
  });

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

/**
 * Get agent to player_id mappings
 */
export async function getPlayerMappings(): Promise<Map<string, number>> {
  const response = await fetchApi<{ players: Array<{ agent_symbol: string; player_id: number }> }>('/bot/players');
  return new Map(response.players.map(p => [p.agent_symbol, p.player_id]));
}

// ==================== Financial Ledger API Functions ====================

interface GetTransactionsParams {
  playerId: number;
  limit?: number;
  offset?: number;
  category?: TransactionCategory;
  type?: TransactionType;
  startDate?: string;
  endDate?: string;
  search?: string;
}

/**
 * Get financial transactions with filtering and pagination
 */
export async function getFinancialTransactions(
  params: GetTransactionsParams
): Promise<{ transactions: FinancialTransaction[]; total: number }> {
  const queryParams = new URLSearchParams();
  queryParams.append('player_id', params.playerId.toString());
  if (params.limit) queryParams.append('limit', params.limit.toString());
  if (params.offset) queryParams.append('offset', params.offset.toString());
  if (params.category) queryParams.append('category', params.category);
  if (params.type) queryParams.append('type', params.type);
  if (params.startDate) queryParams.append('start_date', params.startDate);
  if (params.endDate) queryParams.append('end_date', params.endDate);
  if (params.search) queryParams.append('search', params.search);

  const response = await fetchApi<{
    transactions: FinancialTransaction[];
    total: number;
    page: number;
    limit: number;
  }>(`/bot/ledger/transactions?${queryParams.toString()}`);

  return {
    transactions: response.transactions,
    total: response.total,
  };
}

/**
 * Get cash flow analysis for a player
 */
export async function getCashFlow(
  playerId: number,
  startDate?: string,
  endDate?: string
): Promise<CashFlowData> {
  const queryParams = new URLSearchParams();
  queryParams.append('player_id', playerId.toString());
  if (startDate) queryParams.append('start_date', startDate);
  if (endDate) queryParams.append('end_date', endDate);

  const response = await fetchApi<CashFlowData>(
    `/bot/ledger/cash-flow?${queryParams.toString()}`
  );
  return response;
}

/**
 * Get profit & loss statement for a player
 */
export async function getProfitLoss(
  playerId: number,
  startDate?: string,
  endDate?: string
): Promise<ProfitLossData> {
  const queryParams = new URLSearchParams();
  queryParams.append('player_id', playerId.toString());
  if (startDate) queryParams.append('start_date', startDate);
  if (endDate) queryParams.append('end_date', endDate);

  const response = await fetchApi<ProfitLossData>(
    `/bot/ledger/profit-loss?${queryParams.toString()}`
  );
  return response;
}

/**
 * Get balance history for a player
 */
export async function getBalanceHistory(
  playerId: number,
  startDate?: string,
  endDate?: string,
  interval?: 'hourly' | 'daily' | 'auto'
): Promise<BalanceHistoryData> {
  const queryParams = new URLSearchParams();
  queryParams.append('player_id', playerId.toString());
  if (startDate) queryParams.append('start_date', startDate);
  if (endDate) queryParams.append('end_date', endDate);
  if (interval) queryParams.append('interval', interval);

  const response = await fetchApi<BalanceHistoryData>(
    `/bot/ledger/balance-history?${queryParams.toString()}`
  );
  return response;
}

import { useEffect, useRef, useState, useCallback } from 'react';
import { useStore } from '../store/useStore';
import {
  getFinancialTransactions,
  getCashFlow,
  getOperationPL,
  getBalanceHistory,
} from '../services/api/bot';

const POLLING_INTERVAL = 10000; // 10 seconds

// Calculate date range from preset (matching DateRangePicker logic)
const calculateDateRange = (preset: string): { start: Date; end: Date } => {
  const end = new Date();
  let start: Date;

  switch (preset) {
    case '5m':
      start = new Date(Date.now() - 5 * 60 * 1000);
      break;
    case '15m':
      start = new Date(Date.now() - 15 * 60 * 1000);
      break;
    case '30m':
      start = new Date(Date.now() - 30 * 60 * 1000);
      break;
    case '1h':
      start = new Date(Date.now() - 60 * 60 * 1000);
      break;
    case '3h':
      start = new Date(Date.now() - 3 * 60 * 60 * 1000);
      break;
    case '6h':
      start = new Date(Date.now() - 6 * 60 * 60 * 1000);
      break;
    case '12h':
      start = new Date(Date.now() - 12 * 60 * 60 * 1000);
      break;
    case '24h':
      start = new Date(Date.now() - 24 * 60 * 60 * 1000);
      break;
    case '2d':
      start = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000);
      break;
    case '7d':
      start = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
      break;
    case '30d':
      start = new Date(Date.now() - 30 * 24 * 60 * 60 * 1000);
      break;
    case 'all':
      start = new Date(0);
      break;
    default:
      // For 'custom' or unknown presets, return current values
      start = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
      break;
  }

  return { start, end };
};

export function useFinancialPolling() {
  const {
    selectedPlayerId,
    financialDateRange,
    transactionFilters,
    transactionPagination,
    setFinancialTransactions,
    setCashFlowData,
    setOperationPLData,
    setBalanceHistory,
  } = useStore();

  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const [isRefreshing, setIsRefreshing] = useState(false);

  const fetchFinancialData = useCallback(async () => {
    if (!selectedPlayerId) {
      return;
    }

    setIsRefreshing(true);
    console.log('[useFinancialPolling] Starting refresh for player:', selectedPlayerId);
    try {
      // Recalculate date range for presets to get current time window
      // Only use stored dates for 'custom' preset
      const dateRange = financialDateRange.preset === 'custom'
        ? { start: financialDateRange.start, end: financialDateRange.end }
        : calculateDateRange(financialDateRange.preset);

      const startDate = financialDateRange.preset === 'all'
        ? undefined
        : dateRange.start.toISOString();
      const endDate = dateRange.end.toISOString();

      console.log('[useFinancialPolling] Date range:', { startDate, endDate, preset: financialDateRange.preset });

      // Fetch all financial data in parallel
      const [transactions, cashFlow, operationPL, balanceHistory] = await Promise.all([
        getFinancialTransactions({
          playerId: selectedPlayerId,
          limit: transactionPagination.limit,
          offset: (transactionPagination.page - 1) * transactionPagination.limit,
          category: transactionFilters.category || undefined,
          type: transactionFilters.type || undefined,
          search: transactionFilters.search || undefined,
          startDate,
          endDate,
        }),
        getCashFlow(selectedPlayerId, startDate, endDate),
        getOperationPL(selectedPlayerId, startDate, endDate),
        getBalanceHistory(selectedPlayerId, startDate, endDate),
      ]);

      console.log('[useFinancialPolling] Data fetched:', {
        transactions: transactions.transactions.length,
        total: transactions.total,
        cashFlowCategories: cashFlow.categories.length,
        operationPLOperations: operationPL.operations.length,
        balanceDataPoints: balanceHistory.dataPoints.length,
      });

      // Update store
      setFinancialTransactions(transactions.transactions, transactions.total);
      setCashFlowData(cashFlow);
      setOperationPLData(operationPL);
      setBalanceHistory(balanceHistory);

      console.log('[useFinancialPolling] Store updated successfully');
    } catch (error) {
      console.error('Failed to fetch financial data:', error);
    } finally {
      setIsRefreshing(false);
    }
  }, [
    selectedPlayerId,
    financialDateRange,
    transactionFilters,
    transactionPagination,
    setFinancialTransactions,
    setCashFlowData,
    setOperationPLData,
    setBalanceHistory,
  ]);

  useEffect(() => {
    if (!selectedPlayerId) {
      return;
    }

    // Initial fetch
    fetchFinancialData();

    // Set up polling interval
    intervalRef.current = setInterval(fetchFinancialData, POLLING_INTERVAL);

    // Cleanup on unmount or when dependencies change
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
      }
    };
  }, [selectedPlayerId, fetchFinancialData]);

  return {
    refresh: fetchFinancialData,
    isRefreshing,
  };
}

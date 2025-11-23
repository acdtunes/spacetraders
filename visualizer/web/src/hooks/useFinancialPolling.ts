import { useEffect, useRef } from 'react';
import { useStore } from '../store/useStore';
import {
  getFinancialTransactions,
  getCashFlow,
  getOperationPL,
  getBalanceHistory,
} from '../services/api/bot';

const POLLING_INTERVAL = 10000; // 10 seconds

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

  useEffect(() => {
    if (!selectedPlayerId) {
      return;
    }

    const fetchFinancialData = async () => {
      try {
        const startDate = financialDateRange.preset === 'all'
          ? undefined
          : financialDateRange.start.toISOString();
        const endDate = financialDateRange.end.toISOString();

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

        // Update store
        setFinancialTransactions(transactions.transactions, transactions.total);
        setCashFlowData(cashFlow);
        setOperationPLData(operationPL);
        setBalanceHistory(balanceHistory);
      } catch (error) {
        console.error('Failed to fetch financial data:', error);
      }
    };

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
}

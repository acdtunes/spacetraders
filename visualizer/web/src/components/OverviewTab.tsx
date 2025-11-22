import { SummaryCard } from './SummaryCard';
import { BalanceChart } from './BalanceChart';
import { useStore } from '../store/useStore';
import { format } from 'date-fns';

export function OverviewTab() {
  const { balanceHistory, cashFlowData, profitLossData, financialTransactions } = useStore();

  const formatCurrency = (value: number) => {
    if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
    if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
    return value.toFixed(0);
  };

  const currentBalance = balanceHistory?.current_balance || 0;
  const totalRevenue = cashFlowData?.summary.total_inflow || 0;
  const totalExpenses = cashFlowData?.summary.total_outflow || 0;
  const netProfit = profitLossData?.net_profit || 0;

  // Get top categories by net flow
  const topCategories = (cashFlowData?.categories || [])
    .slice(0, 5)
    .sort((a, b) => b.net_flow - a.net_flow);

  // Get recent transactions
  const recentTransactions = financialTransactions.slice(0, 10);

  return (
    <div className="space-y-6">
      {/* Summary Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <SummaryCard
          title="Current Balance"
          value={`${formatCurrency(currentBalance)} ¢`}
          icon="💰"
          valueColor="text-blue-400"
        />
        <SummaryCard
          title="Total Revenue"
          value={`${formatCurrency(totalRevenue)} ¢`}
          icon="📈"
          valueColor="text-green-400"
        />
        <SummaryCard
          title="Total Expenses"
          value={`${formatCurrency(Math.abs(totalExpenses))} ¢`}
          icon="📉"
          valueColor="text-red-400"
        />
        <SummaryCard
          title="Net Profit"
          value={`${formatCurrency(netProfit)} ¢`}
          icon="💵"
          valueColor={netProfit >= 0 ? 'text-green-400' : 'text-red-400'}
        />
      </div>

      {/* Balance Timeline Chart */}
      <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
        <h2 className="text-lg font-semibold mb-4">Balance Over Time</h2>
        <BalanceChart data={balanceHistory?.dataPoints || []} />
      </div>

      {/* Two-column layout */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Top Categories */}
        <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
          <h2 className="text-lg font-semibold mb-4">Top Categories by Net Flow</h2>
          <div className="space-y-3">
            {topCategories.length === 0 ? (
              <p className="text-gray-500 text-sm">No category data available</p>
            ) : (
              topCategories.map((category) => (
                <div key={category.category} className="flex items-center justify-between">
                  <div>
                    <p className="text-sm font-medium">{category.category}</p>
                    <p className="text-xs text-gray-400">{category.transaction_count} transactions</p>
                  </div>
                  <span
                    className={`text-sm font-semibold ${
                      category.net_flow >= 0 ? 'text-green-400' : 'text-red-400'
                    }`}
                  >
                    {category.net_flow >= 0 ? '+' : ''}{formatCurrency(category.net_flow)} ¢
                  </span>
                </div>
              ))
            )}
          </div>
        </div>

        {/* Recent Transactions */}
        <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
          <h2 className="text-lg font-semibold mb-4">Recent Transactions</h2>
          <div className="space-y-2">
            {recentTransactions.length === 0 ? (
              <p className="text-gray-500 text-sm">No transactions available</p>
            ) : (
              recentTransactions.map((tx) => (
                <div key={tx.id} className="flex items-center justify-between py-2 border-b border-gray-700 last:border-0">
                  <div className="flex-1">
                    <p className="text-sm">{tx.description}</p>
                    <p className="text-xs text-gray-400">{format(new Date(tx.timestamp), 'MMM dd, HH:mm:ss')}</p>
                  </div>
                  <span
                    className={`text-sm font-semibold ml-4 ${
                      tx.amount >= 0 ? 'text-green-400' : 'text-red-400'
                    }`}
                  >
                    {tx.amount >= 0 ? '+' : ''}{formatCurrency(tx.amount)} ¢
                  </span>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

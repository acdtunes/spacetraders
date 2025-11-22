import { useStore } from '../store/useStore';
import { PieChart, Pie, Cell, ResponsiveContainer, Tooltip } from 'recharts';

const COLORS = {
  TRADING_REVENUE: '#10B981',
  CONTRACT_REVENUE: '#8B5CF6',
  FUEL_COSTS: '#F59E0B',
  TRADING_COSTS: '#EF4444',
  SHIP_INVESTMENTS: '#EC4899',
};

export function ProfitLossPanel() {
  const { profitLossData } = useStore();

  if (!profitLossData) {
    return (
      <div className="p-8 text-center text-gray-500">
        No profit & loss data available
      </div>
    );
  }

  const formatCurrency = (value: number) => {
    if (Math.abs(value) >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
    if (Math.abs(value) >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
    return value.toFixed(0);
  };

  const revenueData = Object.entries(profitLossData.revenue.breakdown).map(([name, value]) => ({
    name,
    value,
  }));

  const expenseData = Object.entries(profitLossData.expenses.breakdown).map(([name, value]) => ({
    name,
    value: Math.abs(value),
  }));

  return (
    <div className="space-y-6">
      {/* Summary */}
      <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
        <h2 className="text-lg font-semibold mb-4">Profit & Loss Statement</h2>
        <div className="space-y-4">
          <div className="flex justify-between items-center pb-3 border-b border-gray-700">
            <span className="text-gray-400">Total Revenue</span>
            <span className="text-xl font-bold text-green-400">
              +{formatCurrency(profitLossData.revenue.total)} ¢
            </span>
          </div>
          <div className="flex justify-between items-center pb-3 border-b border-gray-700">
            <span className="text-gray-400">Total Expenses</span>
            <span className="text-xl font-bold text-red-400">
              {formatCurrency(profitLossData.expenses.total)} ¢
            </span>
          </div>
          <div className="flex justify-between items-center pt-2">
            <span className="text-lg font-semibold">Net Profit</span>
            <span
              className={`text-2xl font-bold ${
                profitLossData.net_profit >= 0 ? 'text-green-400' : 'text-red-400'
              }`}
            >
              {profitLossData.net_profit >= 0 ? '+' : ''}
              {formatCurrency(profitLossData.net_profit)} ¢
            </span>
          </div>
          <div className="flex justify-between items-center text-sm">
            <span className="text-gray-400">Profit Margin</span>
            <span className="text-gray-300">
              {(profitLossData.profit_margin * 100).toFixed(2)}%
            </span>
          </div>
        </div>
      </div>

      {/* Charts */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Revenue Breakdown */}
        <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
          <h3 className="text-lg font-semibold mb-4">Revenue Breakdown</h3>
          {revenueData.length === 0 ? (
            <p className="text-gray-500 text-center py-8">No revenue data</p>
          ) : (
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={revenueData}
                    dataKey="value"
                    nameKey="name"
                    cx="50%"
                    cy="50%"
                    outerRadius={80}
                    label={(entry) => `${entry.name}: ${formatCurrency(entry.value)}`}
                  >
                    {revenueData.map((entry, index) => (
                      <Cell
                        key={`cell-${index}`}
                        fill={COLORS[entry.name as keyof typeof COLORS] || '#6B7280'}
                      />
                    ))}
                  </Pie>
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#1F2937',
                      border: '1px solid #374151',
                      borderRadius: '0.5rem',
                    }}
                    formatter={(value: number) => `${value.toLocaleString()} ¢`}
                  />
                </PieChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>

        {/* Expense Breakdown */}
        <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
          <h3 className="text-lg font-semibold mb-4">Expense Breakdown</h3>
          {expenseData.length === 0 ? (
            <p className="text-gray-500 text-center py-8">No expense data</p>
          ) : (
            <div className="h-64">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={expenseData}
                    dataKey="value"
                    nameKey="name"
                    cx="50%"
                    cy="50%"
                    outerRadius={80}
                    label={(entry) => `${entry.name}: ${formatCurrency(entry.value)}`}
                  >
                    {expenseData.map((entry, index) => (
                      <Cell
                        key={`cell-${index}`}
                        fill={COLORS[entry.name as keyof typeof COLORS] || '#6B7280'}
                      />
                    ))}
                  </Pie>
                  <Tooltip
                    contentStyle={{
                      backgroundColor: '#1F2937',
                      border: '1px solid #374151',
                      borderRadius: '0.5rem',
                    }}
                    formatter={(value: number) => `${value.toLocaleString()} ¢`}
                  />
                </PieChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

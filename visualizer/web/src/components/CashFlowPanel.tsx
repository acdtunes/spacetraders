import { useStore } from '../store/useStore';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';

export function CashFlowPanel() {
  const { cashFlowData } = useStore();

  if (!cashFlowData) {
    return (
      <div className="p-8 text-center text-gray-500">
        No cash flow data available
      </div>
    );
  }

  const formatCurrency = (value: number | string) => {
    const num = typeof value === 'string' ? parseFloat(value) : value;
    if (isNaN(num)) return '0';
    if (Math.abs(num) >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`;
    if (Math.abs(num) >= 1_000) return `${(num / 1_000).toFixed(1)}K`;
    return num.toFixed(0);
  };

  const chartData = cashFlowData.categories.map(cat => ({
    category: cat.category,
    inflow: cat.total_inflow,
    outflow: Math.abs(cat.total_outflow),
  }));

  return (
    <div className="space-y-6">
      {/* Summary Cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 className="text-sm font-medium text-gray-400 mb-1">Total Inflow</h3>
          <p className="text-2xl font-bold text-green-400">
            +{formatCurrency(cashFlowData.summary.total_inflow)} ¢
          </p>
        </div>
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 className="text-sm font-medium text-gray-400 mb-1">Total Outflow</h3>
          <p className="text-2xl font-bold text-red-400">
            {formatCurrency(cashFlowData.summary.total_outflow)} ¢
          </p>
        </div>
        <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
          <h3 className="text-sm font-medium text-gray-400 mb-1">Net Cash Flow</h3>
          <p
            className={`text-2xl font-bold ${
              cashFlowData.summary.net_cash_flow >= 0 ? 'text-green-400' : 'text-red-400'
            }`}
          >
            {cashFlowData.summary.net_cash_flow >= 0 ? '+' : ''}
            {formatCurrency(cashFlowData.summary.net_cash_flow)} ¢
          </p>
        </div>
      </div>

      {/* Chart */}
      <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
        <h2 className="text-lg font-semibold mb-4">Cash Flow by Category</h2>
        <div className="h-96">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#374151" />
              <XAxis
                dataKey="category"
                stroke="#9CA3AF"
                tick={{ fill: '#9CA3AF', fontSize: 12 }}
                angle={-45}
                textAnchor="end"
                height={100}
              />
              <YAxis
                stroke="#9CA3AF"
                tick={{ fill: '#9CA3AF', fontSize: 12 }}
                tickFormatter={formatCurrency}
              />
              <Tooltip
                contentStyle={{
                  backgroundColor: '#1F2937',
                  border: '1px solid #374151',
                  borderRadius: '0.5rem',
                  color: '#F3F4F6',
                }}
                formatter={(value: number) => `${value.toLocaleString()} ¢`}
              />
              <Legend />
              <Bar dataKey="inflow" fill="#10B981" name="Inflow" />
              <Bar dataKey="outflow" fill="#EF4444" name="Outflow" />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Category Breakdown Table */}
      <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        <h2 className="text-lg font-semibold p-6 border-b border-gray-700">Category Breakdown</h2>
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="bg-gray-700 text-left">
                <th className="px-4 py-3 text-sm font-medium text-gray-300">Category</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Inflow</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Outflow</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Net Flow</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Transactions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-700">
              {cashFlowData.categories.map((cat) => (
                <tr key={cat.category} className="hover:bg-gray-750">
                  <td className="px-4 py-3 text-sm">{cat.category}</td>
                  <td className="px-4 py-3 text-sm text-green-400 text-right">
                    +{formatCurrency(cat.total_inflow)} ¢
                  </td>
                  <td className="px-4 py-3 text-sm text-red-400 text-right">
                    {formatCurrency(cat.total_outflow)} ¢
                  </td>
                  <td
                    className={`px-4 py-3 text-sm font-semibold text-right ${
                      cat.net_flow >= 0 ? 'text-green-400' : 'text-red-400'
                    }`}
                  >
                    {cat.net_flow >= 0 ? '+' : ''}
                    {formatCurrency(cat.net_flow)} ¢
                  </td>
                  <td className="px-4 py-3 text-sm text-gray-400 text-right">
                    {cat.transaction_count}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

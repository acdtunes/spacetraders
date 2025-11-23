import { useStore } from '../store/useStore';
import { useState } from 'react';

const OPERATION_COLORS: Record<string, string> = {
  CONTRACTS: '#8B5CF6',
  TRADE: '#10B981',
  OPERATIONAL: '#F59E0B',
  MINING: '#3B82F6',
  TRANSPORT: '#EC4899',
};

export function ProfitLossPanel() {
  const { operationPLData } = useStore();
  const [expandedOperations, setExpandedOperations] = useState<Set<string>>(new Set());

  if (!operationPLData) {
    return (
      <div className="p-8 text-center text-gray-500">
        No profit & loss data available
      </div>
    );
  }

  const formatCurrency = (value: number | string) => {
    const num = typeof value === 'string' ? parseFloat(value) : value;
    if (isNaN(num)) return '0';
    if (Math.abs(num) >= 1_000_000) return `${(num / 1_000_000).toFixed(2)}M`;
    if (Math.abs(num) >= 1_000) return `${(num / 1_000).toFixed(1)}K`;
    return num.toFixed(0);
  };

  const toggleOperation = (operation: string) => {
    setExpandedOperations((prev) => {
      const next = new Set(prev);
      if (next.has(operation)) {
        next.delete(operation);
      } else {
        next.add(operation);
      }
      return next;
    });
  };

  const formatOperationName = (operation: string) => {
    // Capitalize first letter of each word
    return operation
      .split('_')
      .map(word => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
      .join(' ');
  };

  return (
    <div className="space-y-6">
      {/* Summary by Operation */}
      <div className="bg-gray-800 rounded-lg p-6 border border-gray-700">
        <h2 className="text-lg font-semibold mb-4">P&L Statement by Operation</h2>
        <div className="space-y-3">
          {operationPLData.operations.map((op) => (
            <div key={op.operation} className="pb-3 border-b border-gray-700 last:border-0">
              <div className="flex justify-between items-center">
                <span className="font-medium">{formatOperationName(op.operation)}</span>
                <span
                  className={`font-bold ${
                    op.net_profit >= 0 ? 'text-green-400' : 'text-red-400'
                  }`}
                >
                  {op.net_profit >= 0 ? '+' : ''}
                  {formatCurrency(op.net_profit)} ¢
                </span>
              </div>
            </div>
          ))}
          <div className="flex justify-between items-center pt-3 border-t-2 border-gray-600">
            <span className="text-lg font-bold">Total</span>
            <span
              className={`text-xl font-bold ${
                operationPLData.summary.net_profit >= 0 ? 'text-green-400' : 'text-red-400'
              }`}
            >
              {operationPLData.summary.net_profit >= 0 ? '+' : ''}
              {formatCurrency(operationPLData.summary.net_profit)} ¢
            </span>
          </div>
        </div>
      </div>

      {/* Detailed Breakdown by Operation */}
      <div className="space-y-4">
        <h2 className="text-lg font-semibold">Detailed Breakdown</h2>
        {operationPLData.operations.map((op) => (
          <div key={op.operation} className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
            <button
              onClick={() => toggleOperation(op.operation)}
              className="w-full px-6 py-4 flex justify-between items-center hover:bg-gray-750 transition-colors"
            >
              <div className="flex items-center gap-3">
                <div
                  className="w-3 h-3 rounded-full"
                  style={{ backgroundColor: OPERATION_COLORS[op.operation.toUpperCase()] || '#6B7280' }}
                />
                <span className="font-semibold text-lg">{formatOperationName(op.operation)}</span>
              </div>
              <div className="flex items-center gap-6">
                <div className="text-right">
                  <div className="text-sm text-gray-400">Revenue</div>
                  <div className="text-green-400 font-semibold">
                    +{formatCurrency(op.revenue)} ¢
                  </div>
                </div>
                <div className="text-right">
                  <div className="text-sm text-gray-400">Expenses</div>
                  <div className="text-red-400 font-semibold">
                    {formatCurrency(op.expenses)} ¢
                  </div>
                </div>
                <div className="text-right min-w-[120px]">
                  <div className="text-sm text-gray-400">Net</div>
                  <div
                    className={`font-bold ${
                      op.net_profit >= 0 ? 'text-green-400' : 'text-red-400'
                    }`}
                  >
                    {op.net_profit >= 0 ? '+' : ''}
                    {formatCurrency(op.net_profit)} ¢
                  </div>
                </div>
                <svg
                  className={`w-5 h-5 text-gray-400 transition-transform ${
                    expandedOperations.has(op.operation) ? 'rotate-180' : ''
                  }`}
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
                </svg>
              </div>
            </button>

            {expandedOperations.has(op.operation) && (
              <div className="px-6 py-4 bg-gray-750 border-t border-gray-700">
                <div className="space-y-2">
                  {Object.entries(op.breakdown)
                    .sort(([, a], [, b]) => b - a)
                    .map(([category, amount]) => (
                      <div key={category} className="flex justify-between items-center py-2">
                        <span className="text-sm text-gray-300 ml-6">{category}</span>
                        <span
                          className={`text-sm font-medium ${
                            amount >= 0 ? 'text-green-400' : 'text-red-400'
                          }`}
                        >
                          {amount >= 0 ? '+' : ''}
                          {formatCurrency(amount)} ¢
                        </span>
                      </div>
                    ))}
                  <div className="flex justify-between items-center pt-2 border-t border-gray-700">
                    <span className="text-sm font-semibold ml-6">Total</span>
                    <span
                      className={`text-sm font-bold ${
                        op.net_profit >= 0 ? 'text-green-400' : 'text-red-400'
                      }`}
                    >
                      {op.net_profit >= 0 ? '+' : ''}
                      {formatCurrency(op.net_profit)} ¢
                    </span>
                  </div>
                </div>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

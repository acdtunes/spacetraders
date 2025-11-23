import { useStore } from '../store/useStore';
import { format } from 'date-fns';

export function TransactionList() {
  const {
    financialTransactions,
    transactionTotal,
    transactionPagination,
    transactionFilters,
    setTransactionFilters,
    setTransactionPagination,
  } = useStore();

  const formatCurrency = (value: number | null | undefined) => {
    const num = Number(value) || 0;
    if (Math.abs(num) >= 1_000_000) return `${(num / 1_000_000).toFixed(2)}M`;
    if (Math.abs(num) >= 1_000) return `${(num / 1_000).toFixed(1)}K`;
    return num.toFixed(0);
  };

  const totalPages = Math.ceil(transactionTotal / transactionPagination.limit);

  return (
    <div className="space-y-4">
      {/* Filters */}
      <div className="bg-gray-800 rounded-lg p-4 border border-gray-700">
        <div className="flex flex-wrap gap-4">
          <div className="flex-1 min-w-[200px]">
            <label className="block text-sm text-gray-400 mb-1">Search</label>
            <input
              type="text"
              value={transactionFilters.search}
              onChange={(e) => setTransactionFilters({ search: e.target.value })}
              placeholder="Search descriptions..."
              className="w-full px-3 py-2 bg-gray-700 border border-gray-600 rounded text-white text-sm focus:outline-none focus:border-blue-500"
            />
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">Category</label>
            <select
              value={transactionFilters.category || ''}
              onChange={(e) =>
                setTransactionFilters({ category: e.target.value as any || null })
              }
              className="px-3 py-2 bg-gray-700 border border-gray-600 rounded text-white text-sm focus:outline-none focus:border-blue-500"
            >
              <option value="">All Categories</option>
              <option value="FUEL_COSTS">Fuel Costs</option>
              <option value="TRADING_REVENUE">Trading Revenue</option>
              <option value="TRADING_COSTS">Trading Costs</option>
              <option value="SHIP_INVESTMENTS">Ship Investments</option>
              <option value="CONTRACT_REVENUE">Contract Revenue</option>
            </select>
          </div>
          <div>
            <label className="block text-sm text-gray-400 mb-1">Type</label>
            <select
              value={transactionFilters.type || ''}
              onChange={(e) =>
                setTransactionFilters({ type: e.target.value as any || null })
              }
              className="px-3 py-2 bg-gray-700 border border-gray-600 rounded text-white text-sm focus:outline-none focus:border-blue-500"
            >
              <option value="">All Types</option>
              <option value="REFUEL">Refuel</option>
              <option value="PURCHASE_CARGO">Purchase Cargo</option>
              <option value="SELL_CARGO">Sell Cargo</option>
              <option value="PURCHASE_SHIP">Purchase Ship</option>
              <option value="CONTRACT_ACCEPTED">Contract Accepted</option>
              <option value="CONTRACT_FULFILLED">Contract Fulfilled</option>
            </select>
          </div>
        </div>
      </div>

      {/* Transaction Table */}
      <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="bg-gray-700 text-left">
                <th className="px-4 py-3 text-sm font-medium text-gray-300">Timestamp</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300">Type</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300">Category</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300">Description</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Amount</th>
                <th className="px-4 py-3 text-sm font-medium text-gray-300 text-right">Balance</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-700">
              {financialTransactions.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-4 py-8 text-center text-gray-500">
                    No transactions found
                  </td>
                </tr>
              ) : (
                financialTransactions.map((tx) => (
                  <tr key={tx.id} className="hover:bg-gray-750">
                    <td className="px-4 py-3 text-sm text-gray-300">
                      {format(new Date(tx.timestamp), 'MMM dd, HH:mm:ss')}
                    </td>
                    <td className="px-4 py-3 text-sm text-gray-400">{tx.transaction_type}</td>
                    <td className="px-4 py-3 text-sm text-gray-400">{tx.category}</td>
                    <td className="px-4 py-3 text-sm">{tx.description}</td>
                    <td
                      className={`px-4 py-3 text-sm font-semibold text-right ${
                        tx.amount >= 0 ? 'text-green-400' : 'text-red-400'
                      }`}
                    >
                      {tx.amount >= 0 ? '+' : ''}{formatCurrency(tx.amount)} ¢
                    </td>
                    <td className="px-4 py-3 text-sm text-gray-300 text-right">
                      {formatCurrency(tx.balance_after)} ¢
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination */}
        {totalPages > 1 && (
          <div className="bg-gray-700 px-4 py-3 flex items-center justify-between border-t border-gray-600">
            <div className="text-sm text-gray-400">
              Showing {financialTransactions.length} of {transactionTotal} transactions
            </div>
            <div className="flex gap-2">
              <button
                onClick={() =>
                  setTransactionPagination({ page: Math.max(1, transactionPagination.page - 1) })
                }
                disabled={transactionPagination.page === 1}
                className="px-3 py-1 bg-gray-600 text-white text-sm rounded disabled:opacity-50 disabled:cursor-not-allowed hover:bg-gray-500"
              >
                Previous
              </button>
              <span className="px-3 py-1 text-sm text-gray-300">
                Page {transactionPagination.page} of {totalPages}
              </span>
              <button
                onClick={() =>
                  setTransactionPagination({
                    page: Math.min(totalPages, transactionPagination.page + 1),
                  })
                }
                disabled={transactionPagination.page === totalPages}
                className="px-3 py-1 bg-gray-600 text-white text-sm rounded disabled:opacity-50 disabled:cursor-not-allowed hover:bg-gray-500"
              >
                Next
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

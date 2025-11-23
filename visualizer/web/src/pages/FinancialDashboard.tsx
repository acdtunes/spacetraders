import { useStore } from '../store/useStore';
import { DateRangePicker } from '../components/DateRangePicker';
import { OverviewTab } from '../components/OverviewTab';
import { TransactionList } from '../components/TransactionList';
import { CashFlowPanel } from '../components/CashFlowPanel';
import { ProfitLossPanel } from '../components/ProfitLossPanel';
import { useFinancialPolling } from '../hooks/useFinancialPolling';

export function FinancialDashboard() {
  const { selectedPlayerId, financialTab, setFinancialTab } = useStore();

  // Start polling for financial data
  const { refresh, isRefreshing } = useFinancialPolling();

  if (!selectedPlayerId) {
    return (
      <div className="h-full w-full bg-gray-900 text-white flex items-center justify-center">
        <div className="text-center">
          <h2 className="text-2xl font-bold mb-2">No Player Selected</h2>
          <p className="text-gray-400">
            Please select a player from the navigation bar to view financial data.
          </p>
        </div>
      </div>
    );
  }

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'transactions', label: 'Transactions' },
    { id: 'cashflow', label: 'Cash Flow' },
    { id: 'profitloss', label: 'P&L' },
  ] as const;

  return (
    <div className="h-full w-full bg-gray-900 text-white overflow-auto">
      <div className="max-w-7xl mx-auto p-6">
        {/* Header */}
        <header className="mb-6">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h1 className="text-3xl font-bold">Financial Dashboard</h1>
              <p className="text-gray-400 mt-1">
                Analytics for Player {selectedPlayerId}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <DateRangePicker />
              <button
                onClick={refresh}
                disabled={isRefreshing}
                className="px-4 py-2 bg-gray-800 border border-gray-600 rounded hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed transition-all"
                title="Refresh data"
              >
                <svg
                  className={`w-4 h-4 ${isRefreshing ? 'animate-spin' : ''}`}
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                  />
                </svg>
              </button>
            </div>
          </div>

          {/* Tabs */}
          <div className="flex gap-2 border-b border-gray-700">
            {tabs.map((tab) => (
              <button
                key={tab.id}
                onClick={() => setFinancialTab(tab.id)}
                className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
                  financialTab === tab.id
                    ? 'border-blue-500 text-blue-400'
                    : 'border-transparent text-gray-400 hover:text-gray-300'
                }`}
              >
                {tab.label}
              </button>
            ))}
          </div>
        </header>

        {/* Tab Content */}
        <div className="mt-6">
          {financialTab === 'overview' && <OverviewTab />}
          {financialTab === 'transactions' && <TransactionList />}
          {financialTab === 'cashflow' && <CashFlowPanel />}
          {financialTab === 'profitloss' && <ProfitLossPanel />}
        </div>
      </div>
    </div>
  );
}

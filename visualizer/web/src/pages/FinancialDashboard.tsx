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
  useFinancialPolling();

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
            <DateRangePicker />
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

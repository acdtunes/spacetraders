# Financial Dashboard Implementation Plan

## Executive Summary

Build a comprehensive financial analytics dashboard as a separate full-page application within the SpaceTraders visualizer. The dashboard will provide complete visibility into financial transactions, cash flow analysis, profit & loss statements, and balance history through interactive charts and detailed transaction listings.

## Background & Context

### Current State
- **Database**: PostgreSQL `transactions` table with complete financial ledger (player_id, timestamp, type, category, amount, balance tracking, metadata)
- **Backend**: Go application layer with query handlers (GetTransactions, GetCashFlow, GetProfitLoss) NOT yet exposed via API
- **Frontend**: React SPA with Zustand state management, no routing, no charting library
- **Gap**: Financial data exists but has zero visibility in the visualizer

### Business Value
- **Operational Insight**: Understand which operations are profitable (contracts vs trading vs mining)
- **Resource Allocation**: Identify where credits are being spent (fuel, cargo, ships)
- **Performance Tracking**: Monitor net profit trends over time
- **Decision Support**: Data-driven decisions on fleet expansion, route optimization, market selection

### Technical Architecture
- **Backend**: Express.js + PostgreSQL (existing)
- **Frontend**: React 18 + TypeScript + Zustand + Tailwind CSS
- **New Dependencies**: React Router, Recharts
- **Data Flow**: PostgreSQL → Express API → Zustand Store → React Components

---

## Phase 1: Backend API Layer

### Objectives
- Expose financial ledger data through RESTful endpoints
- Support filtering, pagination, aggregation
- Maintain consistency with existing API patterns

### 1.1 New API Endpoints

**File**: `server/routes/bot.ts`

#### Endpoint 1: Get Transactions
```typescript
GET /api/bot/ledger/transactions
```

**Query Parameters**:
- `player_id` (required): Filter by player
- `limit` (optional, default: 50): Page size
- `offset` (optional, default: 0): Pagination offset
- `category` (optional): Filter by category (FUEL_COSTS, TRADING_REVENUE, etc.)
- `type` (optional): Filter by transaction type (REFUEL, PURCHASE_CARGO, etc.)
- `start_date` (optional): ISO timestamp for date range start
- `end_date` (optional): ISO timestamp for date range end
- `search` (optional): Text search in description

**Response**:
```json
{
  "transactions": [
    {
      "id": "uuid",
      "player_id": 1,
      "timestamp": "2025-11-22T10:30:00Z",
      "transaction_type": "SELL_CARGO",
      "category": "TRADING_REVENUE",
      "amount": 12500,
      "balance_before": 100000,
      "balance_after": 112500,
      "description": "Sold IRON_ORE at X1-GZ7-B6",
      "metadata": {
        "ship_symbol": "ENDURANCE-1",
        "good_symbol": "IRON_ORE",
        "waypoint": "X1-GZ7-B6",
        "units": 50,
        "price_per_unit": 250
      },
      "related_entity_type": "container",
      "related_entity_id": "mining-worker-abc123"
    }
  ],
  "total": 1247,
  "page": 1,
  "limit": 50
}
```

**PostgreSQL Query**:
```sql
SELECT *
FROM transactions
WHERE player_id = $1
  AND ($2::varchar IS NULL OR category = $2)
  AND ($3::varchar IS NULL OR transaction_type = $3)
  AND ($4::timestamp IS NULL OR timestamp >= $4)
  AND ($5::timestamp IS NULL OR timestamp <= $5)
  AND ($6::text IS NULL OR description ILIKE '%' || $6 || '%')
ORDER BY timestamp DESC
LIMIT $7 OFFSET $8;
```

#### Endpoint 2: Get Cash Flow Analysis
```typescript
GET /api/bot/ledger/cash-flow
```

**Query Parameters**:
- `player_id` (required)
- `start_date` (optional)
- `end_date` (optional)

**Response**:
```json
{
  "period": {
    "start": "2025-11-15T00:00:00Z",
    "end": "2025-11-22T23:59:59Z"
  },
  "summary": {
    "total_inflow": 450000,
    "total_outflow": -320000,
    "net_cash_flow": 130000
  },
  "categories": [
    {
      "category": "TRADING_REVENUE",
      "total_inflow": 350000,
      "total_outflow": 0,
      "net_flow": 350000,
      "transaction_count": 142
    },
    {
      "category": "FUEL_COSTS",
      "total_inflow": 0,
      "total_outflow": -45000,
      "net_flow": -45000,
      "transaction_count": 89
    }
  ]
}
```

**PostgreSQL Query**:
```sql
SELECT
  category,
  SUM(CASE WHEN amount > 0 THEN amount ELSE 0 END) as total_inflow,
  SUM(CASE WHEN amount < 0 THEN amount ELSE 0 END) as total_outflow,
  SUM(amount) as net_flow,
  COUNT(*) as transaction_count
FROM transactions
WHERE player_id = $1
  AND ($2::timestamp IS NULL OR timestamp >= $2)
  AND ($3::timestamp IS NULL OR timestamp <= $3)
GROUP BY category
ORDER BY net_flow DESC;
```

#### Endpoint 3: Get Profit & Loss Statement
```typescript
GET /api/bot/ledger/profit-loss
```

**Query Parameters**:
- `player_id` (required)
- `start_date` (optional)
- `end_date` (optional)

**Response**:
```json
{
  "period": {
    "start": "2025-11-15T00:00:00Z",
    "end": "2025-11-22T23:59:59Z"
  },
  "revenue": {
    "total": 450000,
    "breakdown": {
      "TRADING_REVENUE": 350000,
      "CONTRACT_REVENUE": 100000
    }
  },
  "expenses": {
    "total": -320000,
    "breakdown": {
      "FUEL_COSTS": -45000,
      "TRADING_COSTS": -250000,
      "SHIP_INVESTMENTS": -25000
    }
  },
  "net_profit": 130000,
  "profit_margin": 0.2889
}
```

**PostgreSQL Query**:
```sql
-- Revenue
SELECT
  SUM(amount) as total_revenue,
  jsonb_object_agg(category, category_total) as breakdown
FROM (
  SELECT category, SUM(amount) as category_total
  FROM transactions
  WHERE player_id = $1
    AND amount > 0
    AND ($2::timestamp IS NULL OR timestamp >= $2)
    AND ($3::timestamp IS NULL OR timestamp <= $3)
  GROUP BY category
) revenue_categories;

-- Expenses (similar query for amount < 0)
```

#### Endpoint 4: Get Balance History
```typescript
GET /api/bot/ledger/balance-history
```

**Query Parameters**:
- `player_id` (required)
- `start_date` (optional)
- `end_date` (optional)
- `interval` (optional, default: 'auto'): 'hourly', 'daily', 'auto'

**Response**:
```json
{
  "dataPoints": [
    {
      "timestamp": "2025-11-22T00:00:00Z",
      "balance": 100000,
      "transaction_id": "uuid",
      "transaction_type": "PURCHASE_CARGO",
      "amount": -15000
    },
    {
      "timestamp": "2025-11-22T02:15:00Z",
      "balance": 125000,
      "transaction_id": "uuid2",
      "transaction_type": "SELL_CARGO",
      "amount": 25000
    }
  ],
  "current_balance": 232500,
  "starting_balance": 100000,
  "net_change": 132500
}
```

**PostgreSQL Query**:
```sql
SELECT
  timestamp,
  balance_after as balance,
  id as transaction_id,
  transaction_type,
  amount
FROM transactions
WHERE player_id = $1
  AND ($2::timestamp IS NULL OR timestamp >= $2)
  AND ($3::timestamp IS NULL OR timestamp <= $3)
ORDER BY timestamp ASC;
```

### 1.2 Implementation Details

**Error Handling**:
- Return 400 for invalid player_id
- Return 400 for invalid date ranges (start > end)
- Return 500 for database errors with sanitized messages
- Log all errors to console/logging service

**Performance Considerations**:
- Use existing indexes (idx_player_timestamp, idx_category, idx_type)
- Limit max page size to 1000 transactions
- Cache aggregated queries for 30 seconds (optional optimization)
- Use connection pooling (already configured)

**Testing**:
- Manual testing with curl/Postman
- Verify date filtering works correctly
- Test pagination edge cases (offset > total)
- Test with multiple players

---

## Phase 2: Frontend Foundation

### Objectives
- Install required dependencies
- Set up routing infrastructure
- Define TypeScript interfaces
- Create API client functions
- Extend Zustand store

### 2.1 Install Dependencies

**Commands**:
```bash
cd web
npm install recharts
npm install react-router-dom
npm install date-fns  # If not already installed
npm install @types/react-router-dom --save-dev
```

**Package Versions** (use latest compatible):
- `recharts`: ^2.x
- `react-router-dom`: ^6.x
- `date-fns`: ^3.x

### 2.2 TypeScript Interfaces

**File**: `web/src/types/spacetraders.ts`

**Add Transaction Types**:
```typescript
export type TransactionType =
  | 'REFUEL'
  | 'PURCHASE_CARGO'
  | 'SELL_CARGO'
  | 'PURCHASE_SHIP'
  | 'CONTRACT_ACCEPTED'
  | 'CONTRACT_FULFILLED';

export type TransactionCategory =
  | 'FUEL_COSTS'
  | 'TRADING_REVENUE'
  | 'TRADING_COSTS'
  | 'SHIP_INVESTMENTS'
  | 'CONTRACT_REVENUE';

export interface FinancialTransaction {
  id: string;
  player_id: number;
  timestamp: string;
  transaction_type: TransactionType;
  category: TransactionCategory;
  amount: number;
  balance_before: number;
  balance_after: number;
  description: string;
  metadata: {
    ship_symbol?: string;
    good_symbol?: string;
    waypoint?: string;
    units?: number;
    price_per_unit?: number;
    [key: string]: any;
  } | null;
  related_entity_type: string | null;
  related_entity_id: string | null;
}

export interface CategoryCashFlow {
  category: TransactionCategory;
  total_inflow: number;
  total_outflow: number;
  net_flow: number;
  transaction_count: number;
}

export interface CashFlowData {
  period: {
    start: string;
    end: string;
  };
  summary: {
    total_inflow: number;
    total_outflow: number;
    net_cash_flow: number;
  };
  categories: CategoryCashFlow[];
}

export interface ProfitLossData {
  period: {
    start: string;
    end: string;
  };
  revenue: {
    total: number;
    breakdown: Record<string, number>;
  };
  expenses: {
    total: number;
    breakdown: Record<string, number>;
  };
  net_profit: number;
  profit_margin: number;
}

export interface BalanceDataPoint {
  timestamp: string;
  balance: number;
  transaction_id: string;
  transaction_type: TransactionType;
  amount: number;
}

export interface BalanceHistoryData {
  dataPoints: BalanceDataPoint[];
  current_balance: number;
  starting_balance: number;
  net_change: number;
}
```

### 2.3 API Client Functions

**File**: `web/src/services/api/bot.ts`

**Add Functions**:
```typescript
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
```

### 2.4 Zustand Store Extension

**File**: `web/src/store/useStore.ts`

**Add to Store State**:
```typescript
interface StoreState {
  // ... existing state

  // Financial data
  financialTransactions: FinancialTransaction[];
  transactionTotal: number;
  cashFlowData: CashFlowData | null;
  profitLossData: ProfitLossData | null;
  balanceHistory: BalanceHistoryData | null;

  // Financial UI state
  showFinancialDashboard: boolean;
  financialTab: 'overview' | 'transactions' | 'cashflow' | 'profitloss';
  financialDateRange: {
    start: Date;
    end: Date;
    preset: '24h' | '7d' | '30d' | 'all' | 'custom';
  };
  transactionFilters: {
    category: TransactionCategory | null;
    type: TransactionType | null;
    search: string;
  };
  transactionPagination: {
    page: number;
    limit: number;
  };

  // Actions
  setFinancialTransactions: (transactions: FinancialTransaction[], total: number) => void;
  setCashFlowData: (data: CashFlowData) => void;
  setProfitLossData: (data: ProfitLossData) => void;
  setBalanceHistory: (data: BalanceHistoryData) => void;
  setFinancialTab: (tab: 'overview' | 'transactions' | 'cashflow' | 'profitloss') => void;
  setFinancialDateRange: (range: { start: Date; end: Date; preset: string }) => void;
  setTransactionFilters: (filters: Partial<typeof transactionFilters>) => void;
  setTransactionPagination: (pagination: Partial<typeof transactionPagination>) => void;
  toggleFinancialDashboard: () => void;
}
```

**Implementation**:
```typescript
const useStore = create<StoreState>((set) => ({
  // ... existing state

  // Financial data
  financialTransactions: [],
  transactionTotal: 0,
  cashFlowData: null,
  profitLossData: null,
  balanceHistory: null,

  // Financial UI state
  showFinancialDashboard: false,
  financialTab: 'overview',
  financialDateRange: {
    start: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000), // 7 days ago
    end: new Date(),
    preset: '7d',
  },
  transactionFilters: {
    category: null,
    type: null,
    search: '',
  },
  transactionPagination: {
    page: 1,
    limit: 50,
  },

  // Actions
  setFinancialTransactions: (transactions, total) =>
    set({ financialTransactions: transactions, transactionTotal: total }),
  setCashFlowData: (data) => set({ cashFlowData: data }),
  setProfitLossData: (data) => set({ profitLossData: data }),
  setBalanceHistory: (data) => set({ balanceHistory: data }),
  setFinancialTab: (tab) => set({ financialTab: tab }),
  setFinancialDateRange: (range) => set({ financialDateRange: range }),
  setTransactionFilters: (filters) =>
    set((state) => ({
      transactionFilters: { ...state.transactionFilters, ...filters },
    })),
  setTransactionPagination: (pagination) =>
    set((state) => ({
      transactionPagination: { ...state.transactionPagination, ...pagination },
    })),
  toggleFinancialDashboard: () =>
    set((state) => ({ showFinancialDashboard: !state.showFinancialDashboard })),
}));
```

---

## Phase 3: Routing Setup

### Objectives
- Add React Router to enable multiple pages
- Create navigation between Map and Financial views
- Preserve state when switching pages

### 3.1 Install React Router

**Already covered in Phase 2.1**

### 3.2 Update App Component

**File**: `web/src/App.tsx`

**Wrap with Router**:
```typescript
import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { FinancialDashboard } from './pages/FinancialDashboard';
import { MapView } from './pages/MapView'; // Extract existing content

function App() {
  return (
    <BrowserRouter>
      <div className="app">
        <Navigation />
        <Routes>
          <Route path="/" element={<MapView />} />
          <Route path="/financial" element={<FinancialDashboard />} />
        </Routes>
      </div>
    </BrowserRouter>
  );
}
```

### 3.3 Create Navigation Component

**File**: `web/src/components/Navigation.tsx`

**Purpose**: Top navigation bar with links

**Design**:
```typescript
import { Link, useLocation } from 'react-router-dom';

export const Navigation = () => {
  const location = useLocation();

  return (
    <nav className="bg-gray-800 border-b border-gray-700 px-4 py-3">
      <div className="flex items-center gap-6">
        <div className="text-white font-bold text-lg">
          SpaceTraders Fleet Visualizer
        </div>
        <div className="flex gap-4">
          <Link
            to="/"
            className={`px-4 py-2 rounded ${
              location.pathname === '/'
                ? 'bg-blue-600 text-white'
                : 'text-gray-300 hover:bg-gray-700'
            }`}
          >
            🗺️ Map
          </Link>
          <Link
            to="/financial"
            className={`px-4 py-2 rounded ${
              location.pathname === '/financial'
                ? 'bg-blue-600 text-white'
                : 'text-gray-300 hover:bg-gray-700'
            }`}
          >
            💰 Financial
          </Link>
        </div>
      </div>
    </nav>
  );
};
```

### 3.4 Extract MapView Component

**File**: `web/src/pages/MapView.tsx`

**Purpose**: Move existing App.tsx content here

**Content**: All existing map-related components (SpaceMap, GalaxyView, sidebars, etc.)

---

## Phase 4: Dashboard Page & Components

### 4.1 Main Dashboard Page

**File**: `web/src/pages/FinancialDashboard.tsx`

**Layout Structure**:
```
┌─────────────────────────────────────────────────┐
│ Header (Player Selector, Date Range)           │
├─────────────────────────────────────────────────┤
│ Tab Navigation [Overview | Transactions | ... ]│
├─────────────────────────────────────────────────┤
│                                                 │
│           Tab Content Area                      │
│                                                 │
└─────────────────────────────────────────────────┘
```

**Component Code**: See full implementation in code examples

### 4.2 Overview Tab Component

**File**: `web/src/components/OverviewTab.tsx`

**Layout**:
```
┌──────────┬──────────┬──────────┬──────────┐
│ Balance  │ Revenue  │ Expenses │ Profit   │ ← Summary Cards
└──────────┴──────────┴──────────┴──────────┘
┌─────────────────────────────────────────────┐
│                                             │
│         Balance Timeline Chart              │
│                                             │
└─────────────────────────────────────────────┘
┌──────────────────┬──────────────────────────┐
│ Top Categories   │ Recent Transactions      │
│ (by net flow)    │ (last 10)                │
└──────────────────┴──────────────────────────┘
```

### 4.3 Transactions Tab Component

**File**: `web/src/components/TransactionList.tsx`

**Features**:
- Paginated table of transactions
- Column sorting
- Filters (category, type, search)
- Expandable rows for metadata
- Color-coded amounts

**Component Structure**:
```
┌─────────────────────────────────────────────┐
│ Filters: [Category ▼] [Type ▼] [Search...] │
├─────────────────────────────────────────────┤
│ Timestamp │ Type │ Category │ Amount │ Bal │
├───────────┼──────┼──────────┼────────┼─────┤
│ 2:30 PM   │ SELL │ TRADING  │ +12.5K │ 113K│
│ 1:15 PM   │ BUY  │ TRADING  │ -8.2K  │ 100K│
│ ...                                         │
├─────────────────────────────────────────────┤
│ Showing 1-50 of 1,247    [<] Page 1/25 [>] │
└─────────────────────────────────────────────┘
```

### 4.4 Cash Flow Tab Component

**File**: `web/src/components/CashFlowPanel.tsx`

**Visualizations**:
- Summary metrics (total inflow, outflow, net)
- Category breakdown table
- Stacked bar chart (inflow vs outflow by category)
- Period-over-period comparison

### 4.5 P&L Tab Component

**File**: `web/src/components/ProfitLossPanel.tsx`

**Visualizations**:
- P&L statement layout (revenue - expenses = profit)
- Revenue breakdown pie chart
- Expense breakdown pie chart
- Profitability metrics (margin %, ROI)

---

## Phase 5: Charts & Visualizations

### 5.1 Balance Timeline Chart

**File**: `web/src/components/BalanceChart.tsx`

**Technology**: Recharts LineChart

**Features**:
- Line chart with gradient fill
- X-axis: Time, Y-axis: Balance
- Tooltip showing transaction details on hover
- Reference lines for major events (ship purchases)
- Responsive container

### 5.2 Cash Flow Charts

**Stacked Bar Chart**:
- X-axis: Categories
- Y-axis: Amount
- Two bars per category: Inflow (green), Outflow (red)

### 5.3 P&L Charts

**Pie Charts**:
- Revenue breakdown by category
- Expense breakdown by category
- Custom colors per category
- Legend with percentages

---

## Phase 6: Data Polling & Integration

### 6.1 Financial Polling Hook

**File**: `web/src/hooks/useFinancialPolling.ts`

**Purpose**: Fetch financial data periodically

**Features**:
- Poll every 15 seconds (slower than ship polling)
- Fetch all financial endpoints
- Update Zustand store
- Error handling and retry logic

---

## Phase 7: Polish & UX

### 7.1 Loading States

**Components to Add**:
- Skeleton loaders for charts (gray animated rectangles)
- Spinner overlays during data fetch
- Empty state messages ("No transactions in this period")

### 7.2 Error Handling

**Strategies**:
- Error boundaries around charts
- Toast notifications for API errors
- Retry buttons on failed fetches
- Graceful degradation (show partial data if some endpoints fail)

### 7.3 Responsive Design

**Breakpoints**:
- Mobile (<768px): Stack all elements vertically, simplify tables
- Tablet (768-1024px): 2-column grid for summary cards
- Desktop (>1024px): Full 4-column grid

### 7.4 Performance Optimizations

**Techniques**:
- Memoize expensive computations (React.useMemo)
- Virtualize long transaction lists (react-window)
- Debounce search input
- Lazy load chart library (React.lazy)

---

## Testing Plan

### Manual Testing Checklist

**Backend**:
- [ ] Test each endpoint with curl/Postman
- [ ] Verify correct data returned for different players
- [ ] Test date range filtering edge cases
- [ ] Test pagination (first page, last page, offset > total)
- [ ] Test with empty results (new player with no transactions)
- [ ] Test with large result sets (1000+ transactions)

**Frontend**:
- [ ] Verify routing works (navigate between Map and Financial)
- [ ] Test player selector updates all data
- [ ] Test date range picker updates all tabs
- [ ] Test transaction filters (category, type, search)
- [ ] Test transaction pagination (next/prev, page size)
- [ ] Verify all charts render correctly
- [ ] Test responsive layout on mobile/tablet/desktop
- [ ] Verify real-time updates via polling
- [ ] Test error states (disconnect backend, return errors)
- [ ] Test empty states (no transactions, no cash flow data)

### Browser Compatibility

**Targets**:
- Chrome (latest)
- Firefox (latest)
- Safari (latest)
- Edge (latest)

### Performance Benchmarks

**Goals**:
- Dashboard loads in <2 seconds
- Charts render in <500ms
- Polling doesn't cause UI lag
- Handles 10,000+ transactions without performance degradation

---

## File Structure
```
web/src/
├── pages/
│   ├── FinancialDashboard.tsx (new)
│   └── MapView.tsx (new - extract from App.tsx)
├── components/
│   ├── Navigation.tsx (new)
│   ├── OverviewTab.tsx (new)
│   ├── TransactionList.tsx (new)
│   ├── CashFlowPanel.tsx (new)
│   ├── ProfitLossPanel.tsx (new)
│   ├── BalanceChart.tsx (new)
│   ├── SummaryCard.tsx (new)
│   └── DateRangePicker.tsx (new)
├── hooks/
│   └── useFinancialPolling.ts (new)
├── services/api/
│   └── bot.ts (update)
├── types/
│   └── spacetraders.ts (update)
├── store/
│   └── useStore.ts (update)
└── App.tsx (update for routing)

server/routes/
└── bot.ts (update with new endpoints)
```

## Implementation Order
1. Backend API routes (Phase 1)
2. Frontend types and API client (Phase 2.2-2.3)
3. Zustand store updates (Phase 2.4)
4. Install dependencies and setup routing (Phase 2.1, 3)
5. Dashboard page skeleton (Phase 4.1)
6. Overview tab with balance chart (Phase 4.2, 5.1)
7. Transactions tab (Phase 4.3)
8. Cash Flow tab with charts (Phase 4.4, 5.2)
9. P&L tab with charts (Phase 4.5, 5.3)
10. Polling integration (Phase 6)
11. Polish and UX improvements (Phase 7)

## Success Criteria
- ✅ All 4 API endpoints working and returning correct data
- ✅ Separate page accessible via routing
- ✅ All 4 tabs functional (Overview, Transactions, Cash Flow, P&L)
- ✅ Charts render correctly with Recharts
- ✅ Real-time updates via polling
- ✅ Player filter integration working
- ✅ Date range filtering functional
- ✅ Responsive design on different screen sizes

---

## Risk Assessment

### Technical Risks

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| PostgreSQL query performance with large datasets | Medium | High | Add indexes, implement pagination, cache aggregates |
| Recharts bundle size bloat | Low | Medium | Code-split chart components, lazy load |
| React Router conflicts with existing code | Low | Low | Thorough testing, gradual migration |
| API rate limiting from frequent polling | Low | Medium | Adjust polling interval, implement exponential backoff |

### Timeline Risks

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Chart complexity takes longer than expected | Medium | Medium | Start with simple charts, iterate |
| TypeScript type issues slow development | Low | Low | Use `unknown` and type guards |
| Responsive design issues on mobile | Medium | Low | Test early and often on real devices |

---

## Appendices

### A. Color Scheme

**Transaction Categories**:
- TRADING_REVENUE: Green (#10B981)
- CONTRACT_REVENUE: Purple (#8B5CF6)
- FUEL_COSTS: Orange (#F59E0B)
- TRADING_COSTS: Red (#EF4444)
- SHIP_INVESTMENTS: Pink (#EC4899)

**UI Elements**:
- Background: Gray-900 (#111827)
- Cards: Gray-800 (#1F2937)
- Borders: Gray-700 (#374151)
- Text Primary: White (#FFFFFF)
- Text Secondary: Gray-400 (#9CA3AF)

### B. Database Schema

See database migration file:
`gobot/migrations/011_add_transactions_table.up.sql`

### C. Glossary

- **Cash Flow**: Movement of money in and out (inflow vs outflow)
- **P&L**: Profit & Loss statement (revenue - expenses = profit)
- **Transaction Ledger**: Complete record of all financial transactions
- **Balance**: Total credits available at a point in time
- **Category**: High-level grouping of transactions (fuel, trading, contracts)
- **Transaction Type**: Specific action that caused a transaction (refuel, buy, sell)

---

## Conclusion

This implementation plan provides a comprehensive roadmap for building a full-featured financial dashboard. The phased approach ensures steady progress while maintaining code quality. The dashboard will provide crucial financial visibility to SpaceTraders players, enabling data-driven decision-making and operational optimization.

**Estimated Timeline**: 4-6 days (full-time equivalent)
**Estimated Effort**: ~40-50 hours
**Lines of Code**: ~3,000-4,000 (including tests)

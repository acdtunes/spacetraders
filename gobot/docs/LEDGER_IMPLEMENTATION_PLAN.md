# Ledger System Implementation Plan

## Overview

Implement a comprehensive financial tracking and reporting system that records all credit-affecting operations as individual transactions in an immutable ledger. The system will provide profit/loss analysis, cash flow monitoring by category, and complete audit trail capabilities for all financial operations in the SpaceTraders bot.

## Design Goals

1. **Complete Transaction History**: Record every operation that affects player credits
2. **Categorized Cash Flow**: Group transactions by type (fuel, trading, ships, contracts) for reporting
3. **Immutable Audit Trail**: Append-only ledger with no updates or deletions
4. **Balance Tracking**: Record before/after balances for reconciliation
5. **Comprehensive Reporting**: Support P&L statements, cash flow analysis, transaction queries
6. **Non-Intrusive**: Ledger failures don't block operations (graceful degradation)

## User Requirements

Based on clarifying questions:

- **Purpose**: Comprehensive financial reporting (profit/loss, cash flow, audit trail)
- **Scope**: All operations (refueling costs, cargo trading, ship purchases, contract payments)
- **Granularity**: Individual transactions (not aggregated)
- **Accounting Model**: Categorized cash flow with transaction log

## Current State Analysis

### What Exists Today

#### Player Credits

**Domain Entity:** `internal/domain/player/player.go`
```go
type Player struct {
    ID              shared.PlayerID
    AgentSymbol     string
    Token           string
    Credits         int  // Current balance tracked here
    StartingFaction string
    Metadata        map[string]interface{}
}
```

**Database Model:** `internal/adapters/persistence/models.go`
```go
type PlayerModel struct {
    ID          int
    AgentSymbol string
    Token       string
    CreatedAt   time.Time
    LastActive  *time.Time
    Metadata    string
}
// NOTE: Credits are NOT persisted in database
// Always fetched fresh from SpaceTraders API
```

**Architectural Decision:** Credits are considered volatile state that should always be fetched from the API.

#### Operations That Affect Credits

1. **Refueling** (`internal/application/ship/commands/refuel_ship.go`)
   - API returns `RefuelResult` with `CreditsCost` field
   - Cost is returned but NOT persisted anywhere

2. **Purchase Cargo** (`internal/application/ship/commands/purchase_cargo.go`)
   - Returns `PurchaseCargoResponse` with `TotalCost` field
   - May involve multiple API calls (batched purchases)
   - Cost is transient, not stored

3. **Sell Cargo** (`internal/application/ship/commands/sell_cargo.go`)
   - Returns `SellCargoResponse` with `TotalRevenue` field
   - Revenue is transient, not stored

4. **Ship Purchase** (`internal/application/shipyard/commands/purchase_ship.go`)
   - Returns `PurchaseShipResponse` with `PurchasePrice` and `AgentCredits`
   - **ONLY operation that persists credits to database**
   - API response includes full transaction details with timestamp

5. **Contract Acceptance** (`internal/application/contract/commands/accept_contract.go`)
   - Payment amount stored in `ContractModel.PaymentOnAccepted`
   - Actual receipt of payment NOT tracked as transaction

6. **Contract Fulfillment** (`internal/application/contract/commands/deliver_cargo.go` / fulfill logic)
   - Payment amount stored in `ContractModel.PaymentOnFulfilled`
   - Actual receipt of payment NOT tracked as transaction

#### Existing Aggregate Tracking

**Goods Factory:** `internal/domain/goods/goods_factory.go`
```go
type GoodsFactory struct {
    quantityAcquired int
    totalCost        int     // Aggregate cost of purchases
    shipsUsed        int
    // ...
}
```

Tracks total cost but not individual purchase transactions.

### What's Missing

- ❌ **Transaction history table** - no persistent record of individual financial events
- ❌ **Transaction timestamps** - only ship purchases have timestamps
- ❌ **Running balance history** - only current balance available
- ❌ **Transaction categorization** - no concept of transaction types/categories
- ❌ **Audit trail** - no immutable financial record
- ❌ **Profit/loss tracking** - can't calculate profitability of operations
- ❌ **Cash flow analysis** - can't analyze income vs expenses by category

## Architecture

### Domain Layer (`internal/domain/ledger/`)

#### Entities

**`Transaction`** (Aggregate Root)

```go
type Transaction struct {
    id                 TransactionID
    playerID           PlayerID
    timestamp          time.Time
    transactionType    TransactionType
    category           Category
    amount             int  // Positive for income, negative for expenses
    balanceBefore      int
    balanceAfter       int
    description        string
    metadata           map[string]interface{}
    relatedEntityType  string  // "contract", "factory", "ship_purchase", etc.
    relatedEntityID    string  // ID of related entity
}

type TransactionID struct {
    value string  // UUID
}
```

**Methods:**
- `NewTransaction(...)` - Constructor with validation
- `Validate() error` - Ensure amount != 0, balances are consistent
- `IsIncome() bool` - amount > 0
- `IsExpense() bool` - amount < 0
- `GetCategory() Category` - Return transaction category

**Invariants:**
- `balanceAfter = balanceBefore + amount`
- `amount` cannot be zero
- `timestamp` cannot be in the future
- Once created, transaction is immutable

#### Value Objects

**`TransactionType`** (Enum)

```go
type TransactionType string

const (
    TransactionTypeRefuel            TransactionType = "REFUEL"
    TransactionTypePurchaseCargo     TransactionType = "PURCHASE_CARGO"
    TransactionTypeSellCargo         TransactionType = "SELL_CARGO"
    TransactionTypePurchaseShip      TransactionType = "PURCHASE_SHIP"
    TransactionTypeContractAccepted  TransactionType = "CONTRACT_ACCEPTED"
    TransactionTypeContractFulfilled TransactionType = "CONTRACT_FULFILLED"
)
```

**Methods:**
- `String() string` - Convert to string
- `IsValid() bool` - Check if valid type
- `ToCategory() Category` - Map to category

**`Category`** (Enum for Cash Flow Reporting)

```go
type Category string

const (
    CategoryFuelCosts       Category = "FUEL_COSTS"
    CategoryTradingRevenue  Category = "TRADING_REVENUE"
    CategoryTradingCosts    Category = "TRADING_COSTS"
    CategoryShipInvestments Category = "SHIP_INVESTMENTS"
    CategoryContractRevenue Category = "CONTRACT_REVENUE"
)
```

**Mapping:**
```go
var TypeToCategoryMap = map[TransactionType]Category{
    TransactionTypeRefuel:            CategoryFuelCosts,
    TransactionTypePurchaseCargo:     CategoryTradingCosts,
    TransactionTypeSellCargo:         CategoryTradingRevenue,
    TransactionTypePurchaseShip:      CategoryShipInvestments,
    TransactionTypeContractAccepted:  CategoryContractRevenue,
    TransactionTypeContractFulfilled: CategoryContractRevenue,
}
```

**Methods:**
- `String() string`
- `IsIncome() bool` - Revenue categories return true
- `IsExpense() bool` - Cost/investment categories return true

#### Repository Ports (`ports.go`)

```go
type TransactionRepository interface {
    Create(ctx context.Context, transaction *Transaction) error
    FindByID(ctx context.Context, id TransactionID, playerID PlayerID) (*Transaction, error)
    FindByPlayer(ctx context.Context, playerID PlayerID, opts QueryOptions) ([]*Transaction, error)
    CountByPlayer(ctx context.Context, playerID PlayerID, opts QueryOptions) (int, error)
}

type QueryOptions struct {
    StartDate       *time.Time
    EndDate         *time.Time
    Category        *Category
    TransactionType *TransactionType
    Limit           int
    Offset          int
    OrderBy         string  // "timestamp ASC" or "timestamp DESC"
}
```

### Application Layer (`internal/application/ledger/`)

#### Commands

**`commands/record_transaction.go`**

```go
type RecordTransactionCommand struct {
    PlayerID          int
    TransactionType   string
    Amount            int  // Positive for income, negative for expenses
    BalanceBefore     int
    BalanceAfter      int
    Description       string
    Metadata          map[string]interface{}
    RelatedEntityType string
    RelatedEntityID   string
}

type RecordTransactionResponse struct {
    TransactionID string
    Timestamp     time.Time
}

type RecordTransactionHandler struct {
    transactionRepo ports.TransactionRepository
    clock           shared.Clock
}

func (h *RecordTransactionHandler) Handle(
    ctx context.Context,
    cmd RecordTransactionCommand,
) (*RecordTransactionResponse, error) {
    // 1. Parse and validate transaction type
    // 2. Determine category from type
    // 3. Create transaction entity
    // 4. Validate invariants (balance_after = balance_before + amount)
    // 5. Persist to repository
    // 6. Return transaction ID and timestamp
}
```

**Error Handling:**
- If recording fails, log error but don't fail the original operation
- Return error for monitoring/alerting
- Operation success takes precedence over ledger recording

#### Queries

**`queries/get_transactions.go`**

```go
type GetTransactionsQuery struct {
    PlayerID        int
    StartDate       *time.Time
    EndDate         *time.Time
    Category        *string
    TransactionType *string
    Limit           int
    Offset          int
    OrderBy         string
}

type GetTransactionsResponse struct {
    Transactions []*TransactionDTO
    Total        int
}

type TransactionDTO struct {
    ID                string
    Timestamp         time.Time
    Type              string
    Category          string
    Amount            int
    BalanceBefore     int
    BalanceAfter      int
    Description       string
    Metadata          map[string]interface{}
    RelatedEntityType string
    RelatedEntityID   string
}

type GetTransactionsHandler struct {
    transactionRepo ports.TransactionRepository
}
```

**`queries/get_profit_loss.go`**

```go
type GetProfitLossQuery struct {
    PlayerID  int
    StartDate time.Time
    EndDate   time.Time
}

type GetProfitLossResponse struct {
    Period        string  // e.g., "2024-01-15 to 2024-01-22"
    TotalRevenue  int
    TotalExpenses int
    NetProfit     int     // TotalRevenue - TotalExpenses

    RevenueBreakdown  map[string]int  // category -> amount
    ExpenseBreakdown  map[string]int  // category -> amount
}

type GetProfitLossHandler struct {
    transactionRepo ports.TransactionRepository
}

func (h *GetProfitLossHandler) Handle(
    ctx context.Context,
    query GetProfitLossQuery,
) (*GetProfitLossResponse, error) {
    // 1. Query all transactions in date range
    // 2. Group by category
    // 3. Sum income categories (revenue)
    // 4. Sum expense categories (costs)
    // 5. Calculate net profit
    // 6. Return breakdown
}
```

**`queries/get_cash_flow.go`**

```go
type GetCashFlowQuery struct {
    PlayerID  int
    StartDate time.Time
    EndDate   time.Time
    GroupBy   string  // "category", "day", "week", "month"
}

type GetCashFlowResponse struct {
    Period     string
    Categories []CategoryCashFlow
}

type CategoryCashFlow struct {
    Category      string
    TotalInflow   int
    TotalOutflow  int
    NetFlow       int
    Transactions  int  // count
}

type GetCashFlowHandler struct {
    transactionRepo ports.TransactionRepository
}
```

### Adapters Layer

#### Persistence (`internal/adapters/persistence/`)

**`transaction_repository.go`**

```go
type TransactionRepositoryGORM struct {
    db *gorm.DB
}

func NewTransactionRepository(db *gorm.DB) *TransactionRepositoryGORM

func (r *TransactionRepositoryGORM) Create(ctx, transaction) error {
    // Convert domain entity to database model
    // Insert into transactions table
    // Handle unique constraint violations
}

func (r *TransactionRepositoryGORM) FindByPlayer(ctx, playerID, opts) ([]*Transaction, error) {
    // Build query with filters
    // Apply pagination
    // Convert models to domain entities
}
```

**Database Model (`models.go`):**

```go
type TransactionModel struct {
    ID                string    `gorm:"primaryKey"`
    PlayerID          int       `gorm:"index:idx_player_timestamp"`
    Timestamp         time.Time `gorm:"index:idx_player_timestamp"`
    TransactionType   string    `gorm:"index:idx_type"`
    Category          string    `gorm:"index:idx_category"`
    Amount            int       // Positive for income, negative for expenses
    BalanceBefore     int
    BalanceAfter      int
    Description       string
    Metadata          string    `gorm:"type:jsonb"`  // JSON field
    RelatedEntityType string    `gorm:"index:idx_related"`
    RelatedEntityID   string    `gorm:"index:idx_related"`
    CreatedAt         time.Time
}

func (TransactionModel) TableName() string {
    return "transactions"
}
```

**Indexes:**
```sql
CREATE INDEX idx_player_timestamp ON transactions(player_id, timestamp DESC);
CREATE INDEX idx_type ON transactions(transaction_type);
CREATE INDEX idx_category ON transactions(category);
CREATE INDEX idx_related ON transactions(related_entity_type, related_entity_id);
```

#### CLI (`internal/adapters/cli/`)

**`ledger.go`**

```go
var ledgerCmd = &cobra.Command{
    Use:   "ledger",
    Short: "Financial ledger operations",
}

var ledgerListCmd = &cobra.Command{
    Use:   "list",
    Short: "List transactions",
    Run: func(cmd *cobra.Command, args []string) {
        // Resolve player
        // Build query with flags (start-date, end-date, category, type)
        // Send GetTransactions query via mediator
        // Display results in table format
    },
}

var ledgerReportCmd = &cobra.Command{
    Use:   "report",
    Short: "Generate financial reports",
}

var ledgerProfitLossCmd = &cobra.Command{
    Use:   "profit-loss",
    Short: "Generate profit & loss statement",
    Run: func(cmd *cobra.Command, args []string) {
        // Resolve player
        // Parse date range from flags
        // Send GetProfitLoss query
        // Display formatted P&L statement
    },
}

var ledgerCashFlowCmd = &cobra.Command{
    Use:   "cash-flow",
    Short: "Generate cash flow statement by category",
    Run: func(cmd *cobra.Command, args []string) {
        // Resolve player
        // Parse date range and grouping from flags
        // Send GetCashFlow query
        // Display formatted cash flow report
    },
}
```

**Flags:**

ledger list:
- `--start-date` (YYYY-MM-DD)
- `--end-date` (YYYY-MM-DD)
- `--category` (fuel_costs, trading_revenue, etc.)
- `--type` (refuel, purchase_cargo, etc.)
- `--limit` (default 50)
- `--offset` (default 0)

ledger report profit-loss:
- `--start-date` (required)
- `--end-date` (required)

ledger report cash-flow:
- `--start-date` (required)
- `--end-date` (required)
- `--group-by` (category, day, week, month)

**Usage Examples:**

```bash
# List all transactions for the last 7 days
./bin/spacetraders ledger list --start-date 2024-01-15

# List only fuel costs
./bin/spacetraders ledger list --category fuel_costs

# Profit & loss for January
./bin/spacetraders ledger report profit-loss \
  --start-date 2024-01-01 \
  --end-date 2024-01-31

# Cash flow by category for the last week
./bin/spacetraders ledger report cash-flow \
  --start-date 2024-01-15 \
  --end-date 2024-01-22 \
  --group-by category
```

**Output Examples:**

```
$ spacetraders ledger list --limit 10

TRANSACTIONS (Last 10)
─────────────────────────────────────────────────────────────────────
Timestamp            Type             Category          Amount  Balance
─────────────────────────────────────────────────────────────────────
2024-01-22 14:32:15  SELL_CARGO       TRADING_REVENUE   +15000  485000
2024-01-22 14:30:10  PURCHASE_CARGO   TRADING_COSTS     -8000   470000
2024-01-22 14:25:05  REFUEL           FUEL_COSTS        -2500   478000
2024-01-22 13:15:20  CONTRACT_FULFILLED CONTRACT_REVENUE +50000  480500
2024-01-22 12:00:00  PURCHASE_SHIP    SHIP_INVESTMENTS  -25000  430500
─────────────────────────────────────────────────────────────────────
Total: 127 transactions
```

```
$ spacetraders ledger report profit-loss --start-date 2024-01-15 --end-date 2024-01-22

PROFIT & LOSS STATEMENT
Period: 2024-01-15 to 2024-01-22
─────────────────────────────────────────────────────────────────────

REVENUE
  Contract Revenue:        +150,000
  Trading Revenue:         +85,000
                          ─────────
  Total Revenue:           235,000

EXPENSES
  Fuel Costs:              -12,500
  Trading Costs:           -45,000
  Ship Investments:        -25,000
                          ─────────
  Total Expenses:          -82,500

─────────────────────────────────────────────────────────────────────
NET PROFIT:                +152,500
─────────────────────────────────────────────────────────────────────
```

```
$ spacetraders ledger report cash-flow --start-date 2024-01-15 --end-date 2024-01-22

CASH FLOW STATEMENT (By Category)
Period: 2024-01-15 to 2024-01-22
─────────────────────────────────────────────────────────────────────
Category             Inflow    Outflow   Net Flow  Transactions
─────────────────────────────────────────────────────────────────────
Contract Revenue     +150,000  0         +150,000  3
Trading Revenue      +85,000   0         +85,000   42
Trading Costs        0         -45,000   -45,000   38
Fuel Costs           0         -12,500   -12,500   25
Ship Investments     0         -25,000   -25,000   1
─────────────────────────────────────────────────────────────────────
TOTAL                +235,000  -82,500   +152,500  109
─────────────────────────────────────────────────────────────────────
```

## Integration Points

### Instrumenting Existing Handlers

Each command handler that affects credits must be updated to record a transaction.

#### 1. RefuelShipHandler

**Location:** `internal/application/ship/commands/refuel_ship.go`

**Current Code:**
```go
func (h *RefuelShipHandler) Handle(ctx, cmd) (*RefuelShipResponse, error) {
    // ... existing logic ...

    refuelResult, err := h.apiClient.RefuelShip(ctx, cmd.ShipSymbol)
    if err != nil {
        return nil, err
    }

    return &RefuelShipResponse{
        FuelAdded:   refuelResult.FuelAdded,
        CreditsCost: refuelResult.CreditsCost,
    }, nil
}
```

**Updated Code:**
```go
func (h *RefuelShipHandler) Handle(ctx, cmd) (*RefuelShipResponse, error) {
    // Fetch current credits (balance before)
    player, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch player: %w", err)
    }
    balanceBefore := player.Credits

    // Execute refuel operation
    refuelResult, err := h.apiClient.RefuelShip(ctx, cmd.ShipSymbol)
    if err != nil {
        return nil, err
    }

    balanceAfter := balanceBefore - refuelResult.CreditsCost

    // Record transaction (async, don't fail operation if recording fails)
    go h.recordTransaction(context.Background(), cmd.PlayerID, cmd.ShipSymbol,
        refuelResult.CreditsCost, balanceBefore, balanceAfter)

    return &RefuelShipResponse{
        FuelAdded:   refuelResult.FuelAdded,
        CreditsCost: refuelResult.CreditsCost,
    }, nil
}

func (h *RefuelShipHandler) recordTransaction(
    ctx context.Context,
    playerID int,
    shipSymbol string,
    cost int,
    balanceBefore int,
    balanceAfter int,
) {
    recordCmd := RecordTransactionCommand{
        PlayerID:        playerID,
        TransactionType: "REFUEL",
        Amount:          -cost,  // Negative for expense
        BalanceBefore:   balanceBefore,
        BalanceAfter:    balanceAfter,
        Description:     fmt.Sprintf("Refueled ship %s", shipSymbol),
        Metadata: map[string]interface{}{
            "ship_symbol": shipSymbol,
            "fuel_added":  refuelResult.FuelAdded,
        },
    }

    _, err := h.mediator.Send(ctx, recordCmd)
    if err != nil {
        // Log error but don't fail the operation
        h.logger.Log("ERROR", fmt.Sprintf("Failed to record refuel transaction: %v", err), nil)
    }
}
```

**Pattern:** Same approach for all handlers (fetch balance before, execute operation, record transaction after).

#### 2. PurchaseCargoHandler

**Transaction Details:**
- Type: `PURCHASE_CARGO`
- Category: `TRADING_COSTS`
- Amount: `-TotalCost` (negative)
- Metadata: `{"ship_symbol": "...", "good_symbol": "...", "units": N, "waypoint": "..."}`

#### 3. SellCargoHandler

**Transaction Details:**
- Type: `SELL_CARGO`
- Category: `TRADING_REVENUE`
- Amount: `+TotalRevenue` (positive)
- Metadata: `{"ship_symbol": "...", "good_symbol": "...", "units": N, "waypoint": "..."}`

#### 4. PurchaseShipHandler

**Transaction Details:**
- Type: `PURCHASE_SHIP`
- Category: `SHIP_INVESTMENTS`
- Amount: `-PurchasePrice` (negative)
- Metadata: `{"ship_symbol": "...", "ship_type": "...", "waypoint": "..."}`

**Note:** This handler already updates player credits in the database. Add transaction recording alongside.

#### 5. AcceptContractHandler

**Transaction Details:**
- Type: `CONTRACT_ACCEPTED`
- Category: `CONTRACT_REVENUE`
- Amount: `+PaymentOnAccepted` (positive)
- Metadata: `{"contract_id": "...", "contract_type": "...", "faction": "..."}`
- RelatedEntityType: `"contract"`
- RelatedEntityID: Contract ID

#### 6. Contract Fulfillment (DeliverCargoHandler / FulfillContractHandler)

**Transaction Details:**
- Type: `CONTRACT_FULFILLED`
- Category: `CONTRACT_REVENUE`
- Amount: `+PaymentOnFulfilled` (positive)
- Metadata: `{"contract_id": "...", "contract_type": "...", "final_delivery": true}`
- RelatedEntityType: `"contract"`
- RelatedEntityID: Contract ID

**Challenge:** Payment is received when ALL deliveries complete. Need to detect final delivery.

### Balance Fetching Strategy

**Problem:** Most API responses don't include updated agent credits.

**Solutions:**

**Option 1: Fetch player after each operation**
```go
balanceBefore := player.Credits
// Execute operation
balanceAfter, err := h.fetchCurrentCredits(ctx, playerID)
```

**Option 2: Calculate balance from transaction amount**
```go
balanceBefore := player.Credits
balanceAfter := balanceBefore + amount  // amount is negative for expenses
```

**Recommendation:** Use Option 2 (calculation) for efficiency. Option 1 only when API response includes credits (e.g., ship purchase).

**Validation:** Periodically reconcile ledger balance with API balance (separate reconciliation job).

## Testing Strategy

### BDD Tests (`test/bdd/`)

#### Domain Tests

**`features/domain/ledger/transaction_entity.feature`**

```gherkin
Feature: Transaction Entity

  Scenario: Create valid transaction
    Given a player with ID 1 and current balance 100000
    When I create a transaction with:
      | Type            | Amount  | BalanceBefore | BalanceAfter |
      | REFUEL          | -2500   | 100000        | 97500        |
    Then the transaction should be valid
    And the category should be "FUEL_COSTS"
    And the transaction should be an expense

  Scenario: Validate balance invariant
    Given a player with ID 1
    When I create a transaction with:
      | Amount  | BalanceBefore | BalanceAfter |
      | -5000   | 100000        | 94000        |
    Then I should get a validation error
    Because balance_after should equal balance_before + amount (95000)

  Scenario: Transaction immutability
    Given a created transaction
    When I attempt to modify the amount
    Then I should get an error
    Because transactions are immutable

  Scenario: Map transaction type to category
    Given the following transaction types:
      | Type                  | Expected Category    |
      | REFUEL                | FUEL_COSTS           |
      | PURCHASE_CARGO        | TRADING_COSTS        |
      | SELL_CARGO            | TRADING_REVENUE      |
      | PURCHASE_SHIP         | SHIP_INVESTMENTS     |
      | CONTRACT_ACCEPTED     | CONTRACT_REVENUE     |
      | CONTRACT_FULFILLED    | CONTRACT_REVENUE     |
    When I map each type to its category
    Then all mappings should match expected categories
```

**`features/domain/ledger/category.feature`**

```gherkin
Feature: Transaction Categories

  Scenario: Identify income categories
    Given the following categories:
      | Category            | Expected Type |
      | TRADING_REVENUE     | income        |
      | CONTRACT_REVENUE    | income        |
      | FUEL_COSTS          | expense       |
      | TRADING_COSTS       | expense       |
      | SHIP_INVESTMENTS    | expense       |
    When I check if each category is income or expense
    Then all classifications should be correct
```

#### Application Tests

**`features/application/ledger/record_transaction.feature`**

```gherkin
Feature: Record Transaction Command

  Scenario: Record refuel transaction
    Given a player with ID 1 and balance 100000
    When I record a transaction:
      | Type    | Amount  | BalanceBefore | BalanceAfter | Description      |
      | REFUEL  | -2500   | 100000        | 97500        | Refueled SHIP-1  |
    Then the transaction should be persisted
    And the transaction should have a unique ID
    And the timestamp should be set

  Scenario: Graceful failure on invalid data
    When I record a transaction with amount = 0
    Then I should get a validation error
    And the transaction should NOT be persisted

  Scenario: Record with metadata
    Given a player with ID 1
    When I record a purchase cargo transaction with metadata:
      | ship_symbol | good_symbol  | units | waypoint  |
      | SHIP-1      | IRON_ORE     | 50    | X1-A2     |
    Then the metadata should be stored as JSON
    And I should be able to query by ship_symbol
```

**`features/application/ledger/profit_loss_report.feature`**

```gherkin
Feature: Profit & Loss Report

  Scenario: Calculate P&L for date range with transactions
    Given a player with the following transactions in January 2024:
      | Date       | Type               | Category          | Amount   |
      | 2024-01-10 | CONTRACT_ACCEPTED  | CONTRACT_REVENUE  | +50000   |
      | 2024-01-11 | PURCHASE_CARGO     | TRADING_COSTS     | -8000    |
      | 2024-01-12 | SELL_CARGO         | TRADING_REVENUE   | +15000   |
      | 2024-01-13 | REFUEL             | FUEL_COSTS        | -2500    |
      | 2024-01-14 | PURCHASE_SHIP      | SHIP_INVESTMENTS  | -25000   |
    When I generate a P&L report for January 2024
    Then the total revenue should be 65000 (50000 + 15000)
    And the total expenses should be -35500 (-8000 - 2500 - 25000)
    And the net profit should be 29500

  Scenario: P&L with no transactions
    Given a player with no transactions
    When I generate a P&L report for January 2024
    Then the total revenue should be 0
    And the total expenses should be 0
    And the net profit should be 0
```

**`features/application/ledger/cash_flow_report.feature`**

```gherkin
Feature: Cash Flow Report

  Scenario: Cash flow by category
    Given a player with multiple transactions across categories
    When I generate a cash flow report grouped by category
    Then I should see:
      | Category          | Inflow  | Outflow | Net Flow | Transactions |
      | CONTRACT_REVENUE  | +150000 | 0       | +150000  | 3            |
      | TRADING_REVENUE   | +85000  | 0       | +85000   | 42           |
      | TRADING_COSTS     | 0       | -45000  | -45000   | 38           |
      | FUEL_COSTS        | 0       | -12500  | -12500   | 25           |
      | SHIP_INVESTMENTS  | 0       | -25000  | -25000   | 1            |

  Scenario: Filter by date range
    Given transactions spanning multiple months
    When I generate a cash flow report for January 2024 only
    Then only January transactions should be included
    And transactions from other months should be excluded
```

**`features/application/ledger/get_transactions.feature`**

```gherkin
Feature: Get Transactions Query

  Scenario: List all transactions for a player
    Given a player with 100 transactions
    When I query transactions with limit 50
    Then I should receive 50 transactions
    And they should be ordered by timestamp DESC (newest first)

  Scenario: Filter by category
    Given a player with transactions in multiple categories
    When I query transactions for category "FUEL_COSTS"
    Then only FUEL_COSTS transactions should be returned

  Scenario: Filter by date range
    Given transactions from 2024-01-01 to 2024-12-31
    When I query transactions from 2024-01-15 to 2024-01-22
    Then only transactions in that date range should be returned

  Scenario: Pagination
    Given a player with 200 transactions
    When I query with limit=50 and offset=100
    Then I should receive transactions 101-150
```

#### Integration Tests

**`features/integration/ledger/refuel_integration.feature`**

```gherkin
Feature: Refuel Operation with Ledger Recording

  Scenario: Refuel creates ledger entry
    Given a ship docked at a waypoint with fuel market
    And the player has 100000 credits
    When I refuel the ship for 2500 credits
    Then the refuel operation should succeed
    And a REFUEL transaction should be recorded
    And the transaction amount should be -2500
    And the balance_before should be 100000
    And the balance_after should be 97500

  Scenario: Ledger failure doesn't block refuel
    Given a ship docked at a waypoint
    And the ledger repository is unavailable
    When I refuel the ship
    Then the refuel operation should still succeed
    And an error should be logged about ledger failure
    But the ship should be refueled normally
```

#### Step Definitions

**`test/bdd/steps/transaction_steps.go`**

```go
type TransactionContext struct {
    transaction        *ledger.Transaction
    transactions       []*ledger.Transaction
    transactionRepo    *helpers.MockTransactionRepository
    recordHandler      *commands.RecordTransactionHandler
    getHandler         *queries.GetTransactionsHandler
    profitLossHandler  *queries.GetProfitLossHandler
    cashFlowHandler    *queries.GetCashFlowHandler
    error              error
    profitLossReport   *queries.GetProfitLossResponse
    cashFlowReport     *queries.GetCashFlowResponse
}

func (ctx *TransactionContext) aPlayerWithBalance(playerID int, balance int) error
func (ctx *TransactionContext) iCreateATransactionWith(data *godog.Table) error
func (ctx *TransactionContext) theTransactionShouldBeValid() error
func (ctx *TransactionContext) theCategoryShouldBe(category string) error
// ... more step definitions
```

### Test Helpers

**`test/helpers/mock_transaction_repository.go`**

```go
type MockTransactionRepository struct {
    transactions map[string]*ledger.Transaction
    mu           sync.Mutex
}

func (m *MockTransactionRepository) Create(ctx, transaction) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.transactions[transaction.ID.String()] = transaction
    return nil
}

func (m *MockTransactionRepository) FindByPlayer(ctx, playerID, opts) ([]*ledger.Transaction, error) {
    // Filter and return transactions
}
```

## Implementation Phases

### Phase 1: Domain Foundation (Day 1-2)

**Goal:** Create domain entities, value objects, and ports

**Files to Create:**
- `internal/domain/ledger/transaction.go`
- `internal/domain/ledger/transaction_id.go`
- `internal/domain/ledger/transaction_type.go`
- `internal/domain/ledger/category.go`
- `internal/domain/ledger/ports.go`
- `internal/domain/ledger/errors.go`

**BDD Tests:**
- `test/bdd/features/domain/ledger/transaction_entity.feature`
- `test/bdd/features/domain/ledger/category.feature`
- `test/bdd/steps/transaction_steps.go`

**Validation:**
- Run `make test-bdd` - all domain tests pass
- Transaction entity validates balance invariant
- Type-to-category mapping works correctly
- Immutability enforced

### Phase 2: Application Commands (Day 3)

**Goal:** Create RecordTransaction command handler

**Files to Create:**
- `internal/application/ledger/commands/record_transaction.go`

**Dependencies:**
- Transaction repository port (already defined in Phase 1)

**Testing:**
- `test/bdd/features/application/ledger/record_transaction.feature`

**Validation:**
- Handler creates valid transactions
- Validation errors handled gracefully
- Metadata stored correctly

### Phase 3: Application Queries (Day 4-5)

**Goal:** Create query handlers for reports

**Files to Create:**
- `internal/application/ledger/queries/get_transactions.go`
- `internal/application/ledger/queries/get_profit_loss.go`
- `internal/application/ledger/queries/get_cash_flow.go`

**BDD Tests:**
- `test/bdd/features/application/ledger/profit_loss_report.feature`
- `test/bdd/features/application/ledger/cash_flow_report.feature`
- `test/bdd/features/application/ledger/get_transactions.feature`

**Validation:**
- P&L calculations correct
- Cash flow grouping works
- Filtering and pagination functional

### Phase 4: Persistence (Day 6)

**Goal:** Implement repository with database

**Files to Create:**
- `internal/adapters/persistence/transaction_repository.go`
- Update `internal/adapters/persistence/models.go`

**Database Migration:**
- Create `transactions` table
- Add indexes for performance

**SQL Migration:**
```sql
CREATE TABLE transactions (
    id VARCHAR(36) PRIMARY KEY,
    player_id INTEGER NOT NULL,
    timestamp TIMESTAMP NOT NULL,
    transaction_type VARCHAR(50) NOT NULL,
    category VARCHAR(50) NOT NULL,
    amount INTEGER NOT NULL,
    balance_before INTEGER NOT NULL,
    balance_after INTEGER NOT NULL,
    description TEXT,
    metadata JSONB,
    related_entity_type VARCHAR(50),
    related_entity_id VARCHAR(100),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_player_timestamp ON transactions(player_id, timestamp DESC);
CREATE INDEX idx_type ON transactions(transaction_type);
CREATE INDEX idx_category ON transactions(category);
CREATE INDEX idx_related ON transactions(related_entity_type, related_entity_id);
```

**Testing:**
- Test repository CRUD operations
- Verify JSON serialization/deserialization
- Test query performance with indexes

**Validation:**
- Repository passes all interface tests
- Database constraints enforced
- Queries return correct results

### Phase 5: Handler Integration (Day 7-8)

**Goal:** Instrument existing command handlers to record transactions

**Files to Modify:**
- `internal/application/ship/commands/refuel_ship.go`
- `internal/application/ship/commands/purchase_cargo.go`
- `internal/application/ship/commands/sell_cargo.go`
- `internal/application/shipyard/commands/purchase_ship.go`
- `internal/application/contract/commands/accept_contract.go`
- Contract fulfillment handler (identify correct file)

**Pattern for Each Handler:**
1. Fetch current player credits (balance before)
2. Execute operation via API
3. Calculate balance after
4. Record transaction (async, non-blocking)
5. Log error if recording fails (don't fail operation)

**Testing:**
- `test/bdd/features/integration/ledger/refuel_integration.feature`
- `test/bdd/features/integration/ledger/purchase_cargo_integration.feature`
- `test/bdd/features/integration/ledger/sell_cargo_integration.feature`
- etc.

**Validation:**
- All operations create ledger entries
- Ledger failure doesn't block operations
- Metadata captured correctly

### Phase 6: CLI Commands (Day 9)

**Goal:** Create CLI commands for ledger queries

**Files to Create:**
- `internal/adapters/cli/ledger.go`

**Commands to Implement:**
- `spacetraders ledger list`
- `spacetraders ledger report profit-loss`
- `spacetraders ledger report cash-flow`

**Testing:**
- Manual CLI testing
- Verify output formatting
- Test all flag combinations

**Validation:**
- CLI displays transactions correctly
- Reports calculate correctly
- Filtering works as expected

### Phase 7: Mediator Registration (Day 9)

**Goal:** Register all handlers with mediator

**Files to Modify:**
- Application setup/initialization (wherever mediator handlers are registered)

**Registrations:**
```go
mediator.RegisterHandler(recordTransactionHandler)
mediator.RegisterHandler(getTransactionsHandler)
mediator.RegisterHandler(getProfitLossHandler)
mediator.RegisterHandler(getCashFlowHandler)
```

**Validation:**
- Handlers can be invoked via mediator
- Type-safe routing works

### Phase 8: End-to-End Testing (Day 10)

**Goal:** Validate complete flow with real operations

**Testing Scenarios:**
1. Refuel ship → verify transaction recorded
2. Trade goods (buy + sell) → verify both transactions
3. Accept contract → verify payment recorded
4. Fulfill contract → verify payment recorded
5. Purchase ship → verify transaction
6. Generate P&L report → verify calculations
7. Generate cash flow report → verify grouping

**Performance Testing:**
- Load 10,000+ transactions
- Test query performance
- Verify index usage

**Validation:**
- All operations create correct ledger entries
- Reports generate in <1 second
- No operation failures due to ledger

## Key Design Decisions

### 1. Credits Storage Strategy

**Decision:** Do NOT persist credits in `players` table. Keep current architecture (API as source of truth).

**Rationale:**
- Maintains existing architectural pattern
- Avoids sync issues between database and API
- Ledger tracks changes, not absolute balances
- Simpler implementation

**Trade-off:** Requires fetching player credits before each operation to get `balance_before`.

### 2. Transaction Immutability

**Decision:** Transactions are append-only. No updates or deletes.

**Rationale:**
- Audit compliance (immutable record)
- Simplifies concurrency (no race conditions on updates)
- Historical accuracy preserved
- Follows accounting best practices

**Correction Mechanism:** If a transaction is recorded incorrectly, create a reversal transaction with opposite amount.

### 3. Graceful Degradation

**Decision:** Ledger recording failures do NOT block operations.

**Rationale:**
- Operations are primary, ledger is secondary
- Better user experience (operations always succeed)
- Prevents cascading failures

**Implementation:**
- Record transaction in goroutine (async)
- Log errors for monitoring
- Alert on repeated failures

**Trade-off:** Possible missing transactions if recording consistently fails.

**Mitigation:** Reconciliation job compares ledger with API data periodically.

### 4. Balance Calculation vs Fetching

**Decision:** Calculate `balance_after` from `balance_before + amount` instead of fetching from API.

**Rationale:**
- More efficient (avoids extra API call)
- Most API responses don't include updated credits
- Calculation is deterministic

**Validation:** Periodic reconciliation job verifies ledger balance matches API balance.

### 5. Metadata Schema

**Decision:** Store operation-specific details as JSON in `metadata` field.

**Rationale:**
- Flexible schema (different operations have different data)
- Easy to extend (add new fields without migration)
- Standard library JSON encoding

**Structure:**
```json
{
  "ship_symbol": "SHIP-1",
  "good_symbol": "IRON_ORE",
  "units": 50,
  "waypoint": "X1-A2",
  "fuel_added": 100
}
```

### 6. Transaction Categories

**Decision:** Use 5 categories for cash flow reporting: FUEL_COSTS, TRADING_REVENUE, TRADING_COSTS, SHIP_INVESTMENTS, CONTRACT_REVENUE.

**Rationale:**
- Balance detail vs simplicity
- Aligns with business operations
- Sufficient for P&L and cash flow analysis

**Future Extension:** Add sub-categories if needed (e.g., TRADING_COSTS_ORES, TRADING_COSTS_GOODS).

### 7. Related Entity Linking

**Decision:** Store `related_entity_type` and `related_entity_id` for linking transactions to contracts, factories, etc.

**Rationale:**
- Enables correlation analysis (e.g., "all transactions for Contract-123")
- Supports future features (contract profitability report)
- Simple foreign key pattern

**Example:**
- Contract acceptance: `related_entity_type="contract"`, `related_entity_id="contract-123"`
- Goods factory: `related_entity_type="factory"`, `related_entity_id="factory-456"`

### 8. Timestamp Source

**Decision:** Use API timestamp when available (ship purchases), otherwise use current time.

**Rationale:**
- Most accurate when API provides timestamp
- Fallback ensures all transactions have timestamps
- Acceptable accuracy trade-off for operations without API timestamps

**Enhancement:** Store both `api_timestamp` and `recorded_at` for audit purposes.

## Error Scenarios and Handling

### Repository Unavailable

**Scenario:** Database connection fails during transaction recording

**Handling:**
- Log error with details
- Don't fail the operation
- Alert monitoring system
- Retry with exponential backoff (in background)

### Invalid Transaction Data

**Scenario:** Balance invariant violated (balance_after ≠ balance_before + amount)

**Handling:**
- Log error with all transaction details
- Don't record transaction
- Alert for investigation
- Continue operation normally

### Concurrent Transaction Recording

**Scenario:** Multiple goroutines try to record transactions for same player simultaneously

**Handling:**
- Database handles concurrency (isolated transactions)
- No locking needed (append-only)
- Unique ID prevents duplicates

### Ledger Reconciliation Mismatch

**Scenario:** Periodic reconciliation finds ledger balance doesn't match API balance

**Handling:**
- Log discrepancy with details
- Alert for investigation
- Generate reconciliation report
- Manual review of transactions
- Possible correction transaction

### Missing Transactions

**Scenario:** Operation succeeded but transaction never recorded (recording failure)

**Handling:**
- Detect via reconciliation job
- Analyze API logs vs ledger
- Create corrective transaction manually
- Investigate root cause

### Metadata Serialization Failure

**Scenario:** Metadata contains non-serializable data

**Handling:**
- Catch JSON serialization error
- Log error with metadata dump
- Store partial metadata or empty JSON
- Record transaction anyway (don't fail)

## Performance Considerations

### Database Optimization

**Indexes:**
- `(player_id, timestamp DESC)` - Most common query pattern
- `(category)` - For cash flow reports
- `(transaction_type)` - For type filtering
- `(related_entity_type, related_entity_id)` - For entity correlation

**Query Optimization:**
- Use prepared statements
- Batch inserts for multiple transactions
- Limit result sets with pagination
- Use covering indexes where possible

### Application Optimization

**Async Recording:**
- Record transactions in goroutines (non-blocking)
- Use buffered channel if needed
- Rate limit recording to avoid overwhelming database

**Caching:**
- Cache recent transactions (last 100) per player in memory
- TTL: 5 minutes
- Invalidate on new transaction
- Only for `ledger list` command

**Batch Operations:**
- For goods factory with many purchases, consider batching recordings
- Trade-off: Detail vs performance

### Estimated Load

**Assumptions:**
- 10 active players
- 100 operations/day per player
- 1,000 transactions/day total

**Database:**
- ~1KB per row
- ~30MB per month
- ~360MB per year

**Queries:**
- List transactions: <100ms (indexed)
- P&L report: <200ms (aggregation)
- Cash flow report: <200ms (grouping)

## Monitoring and Observability

### Logging

**Events to Log:**
- Transaction recorded (INFO)
- Recording failed (ERROR)
- Validation failed (WARN)
- Reconciliation mismatch (ERROR)
- Report generated (DEBUG)

**Log Format:**
```json
{
  "level": "INFO",
  "event": "transaction_recorded",
  "transaction_id": "abc-123",
  "player_id": 1,
  "type": "REFUEL",
  "amount": -2500,
  "timestamp": "2024-01-22T14:32:15Z"
}
```

### Metrics

**Counters:**
- `transactions_recorded_total` (by type, category)
- `transactions_failed_total` (by type, error reason)
- `reports_generated_total` (by type)

**Histograms:**
- `transaction_recording_duration_seconds`
- `report_generation_duration_seconds`
- `query_duration_seconds`

**Gauges:**
- `total_balance` (current balance from latest transaction)
- `daily_profit` (today's net profit)

### Alerts

**Critical:**
- Transaction recording failure rate > 5%
- Database connection failures
- Reconciliation mismatch detected

**Warning:**
- Query duration > 1 second
- Transaction recording latency > 500ms

## Future Enhancements

### Phase 2 Features

**Budgeting:**
- Set budget limits per category
- Alert when budget exceeded
- Track budget vs actual spending

**Forecasting:**
- Predict future cash flow based on historical data
- Estimate time to profitability
- Recommend cost optimizations

**Export:**
- Export transactions to CSV/Excel
- Generate PDF reports
- Integration with accounting software

### Phase 3 Features

**Advanced Reports:**
- Profitability by ship
- Profitability by contract
- Trading route analysis
- Cost per unit of good produced

**Dashboard:**
- Real-time P&L chart
- Cash flow trends
- Top expense categories
- Revenue breakdown

**Automation:**
- Auto-generate weekly/monthly reports
- Email reports to user
- Slack/Discord notifications for milestones

### Phase 4 Features

**Multi-Currency:**
- Support for multiple players
- Consolidated reports across players
- Fleet-level profitability

**Tax Calculation:**
- Track taxable income
- Generate tax reports
- Support different tax jurisdictions

## Success Criteria

### MVP (Minimum Viable Product)

- ✅ Record transactions for all 6 operation types
- ✅ Store transactions in database with indexes
- ✅ Generate P&L report for date range
- ✅ Generate cash flow report by category
- ✅ List transactions with filtering
- ✅ CLI commands functional
- ✅ Graceful failure handling
- ✅ BDD tests passing (>80% coverage)

### Production Ready

- ✅ All operation types instrumented
- ✅ Transaction recording never blocks operations
- ✅ Reports generate in <1 second
- ✅ Handle 10,000+ transactions efficiently
- ✅ Comprehensive error handling and logging
- ✅ Monitoring and alerting configured
- ✅ BDD tests passing (>90% coverage)
- ✅ Reconciliation job implemented
- ✅ Documentation complete

### Stretch Goals

- ✅ Budgeting system
- ✅ Forecasting capabilities
- ✅ Advanced profitability reports
- ✅ CSV/PDF export
- ✅ Web dashboard
- ✅ Automated report scheduling

## References

### Existing Patterns to Follow

- **Domain Entity**: `internal/domain/player/player.go`
- **Value Objects**: `internal/domain/shared/*.go`
- **Command Handler**: `internal/application/ship/commands/refuel_ship.go`
- **Query Handler**: `internal/application/player/queries/get_player.go`
- **Repository**: `internal/adapters/persistence/player_repository.go`
- **CLI Command**: `internal/adapters/cli/navigate.go`

### Documentation

- **ARCHITECTURE.md**: Hexagonal architecture principles
- **CLAUDE.md**: Testing strategy, patterns, commands
- **GOODS_FACTORY_IMPLEMENTATION_PLAN.md**: Similar domain implementation

### External Resources

- Accounting Principles: Double-entry bookkeeping (for future enhancement)
- CQRS Pattern: Command/Query separation
- Event Sourcing: Immutable event log pattern (similar to our append-only transactions)

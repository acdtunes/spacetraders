# Manufacturing Arbitrage Implementation Plan

## Overview

Implement a demand-driven manufacturing system that identifies high-demand goods (markets with high import prices), manufactures them using the existing production infrastructure, and sells them for profit. This runs in parallel with existing arbitrage operations, using the shared idle hauler pool.

## Core Concept

**Traditional Arbitrage:** Buy low from export market → Sell high to import market

**Manufacturing Arbitrage:** Manufacture from raw materials → Sell high to import market

The key insight: instead of finding goods to buy cheaply, we **manufacture** goods that are in high demand (high purchase prices at import markets).

## Design Goals

1. **Demand-Driven Discovery**: Find goods with high import prices that can be manufactured
2. **Reuse Existing Infrastructure**: Leverage `ProductionExecutor`, `SupplyChainResolver`, idle hauler pool
3. **Parallel Operation**: Run alongside existing arbitrage without interference
4. **No Upfront Cost Estimation**: Profit calculated from actual ledger transactions
5. **Shared Fleet**: Use the same idle hauler pool as arbitrage (no dedicated ships)

## Why Manufacturing for Arbitrage?

1. **Bypass Scarcity**: Some high-demand goods aren't available in export markets
2. **Higher Margins**: Manufacturing cost can be significantly lower than direct purchase
3. **Market Gaps**: When no export market exists, manufacturing is the only option
4. **Parallel Revenue Stream**: Additional profit source alongside traditional arbitrage

## Architecture

### High-Level Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                Manufacturing Arbitrage Coordinator               │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. Discovery Loop (every 2 min)                                │
│     ├── Scan all import markets in system                       │
│     ├── Find goods with high purchase prices                    │
│     ├── Filter: goods that CAN be manufactured                  │
│     └── Rank by purchase price (highest first)                  │
│                                                                 │
│  2. Assignment Loop (every 30 sec)                              │
│     ├── Get idle haulers from shared pool                       │
│     ├── Assign to highest-value opportunities                   │
│     └── Spawn workers (max 5 parallel)                          │
│                                                                 │
│  3. Worker Execution                                            │
│     ├── Use ProductionExecutor to manufacture good              │
│     ├── Navigate to import market                               │
│     ├── Sell manufactured goods                                 │
│     └── Record profit (via ledger transactions)                 │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Component Interaction

```
ManufacturingArbitrageCoordinator
    │
    ├── ManufacturingDemandFinder (new)
    │       ├── MarketRepository.GetAllMarketsInSystem()
    │       ├── Filter by purchase price threshold
    │       └── Filter by ExportToImportMap (manufacturable)
    │
    ├── ShipPoolManager (existing)
    │       └── FindIdleLightHaulers()
    │
    └── ManufacturingArbitrageWorker (new)
            ├── ProductionExecutor.ProduceGood() (existing)
            ├── NavigateRouteCommand (existing)
            ├── SellCargoCommand (existing)
            └── Ledger.RecordTransaction() (existing)
```

## Domain Layer

### New Value Object: ManufacturingOpportunity

```go
// internal/domain/trading/manufacturing_opportunity.go

type ManufacturingOpportunity struct {
    good           string                    // Target good to manufacture
    sellMarket     *shared.Waypoint          // Import market with high purchase price
    purchasePrice  int                       // What the market pays per unit
    dependencyTree *goods.SupplyChainNode    // From SupplyChainResolver
    discoveredAt   time.Time
}

func (o *ManufacturingOpportunity) Good() string
func (o *ManufacturingOpportunity) SellMarket() *shared.Waypoint
func (o *ManufacturingOpportunity) PurchasePrice() int
func (o *ManufacturingOpportunity) DependencyTree() *goods.SupplyChainNode
func (o *ManufacturingOpportunity) TreeDepth() int
```

**Note:** No estimated cost or profit - actual values come from ledger after execution.

## Application Layer

### Service: ManufacturingDemandFinder

```go
// internal/application/trading/services/manufacturing_demand_finder.go

type ManufacturingDemandFinder struct {
    marketRepo      market.MarketRepository
    supplyChainMap  map[string][]string  // ExportToImportMap
    resolver        *goods.SupplyChainResolver
}

type DemandFinderConfig struct {
    MinPurchasePrice  int     // Minimum price to consider (default: 1000)
    MaxOpportunities  int     // Max opportunities to return (default: 10)
}

func (f *ManufacturingDemandFinder) FindHighDemandManufacturables(
    ctx context.Context,
    systemSymbol string,
    playerID int,
    config DemandFinderConfig,
) ([]*ManufacturingOpportunity, error)
```

**Algorithm:**

```
1. Query all markets in system
2. For each market, get import goods (what market wants to buy)
3. For each import good:
   a. Get purchase price (what market pays)
   b. Check if manufacturable (exists in ExportToImportMap)
   c. If price >= threshold AND manufacturable:
      - Build dependency tree via SupplyChainResolver
      - Create ManufacturingOpportunity
4. Sort by purchase price (descending)
5. Return top N opportunities
```

### Command: RunManufacturingArbitrageCoordinatorCommand

```go
// internal/application/trading/commands/run_manufacturing_arbitrage_coordinator.go

type RunManufacturingArbitrageCoordinatorCommand struct {
    PlayerID     int
    SystemSymbol string
}

type RunManufacturingArbitrageCoordinatorHandler struct {
    demandFinder    *ManufacturingDemandFinder
    shipPoolManager *ShipPoolManager
    assignmentRepo  container.ShipAssignmentRepository
    productionExec  *goods.ProductionExecutor
    mediator        common.Mediator
    clock           shared.Clock
    logger          container.ContainerLogger
}
```

**Coordinator Pattern (similar to ArbitrageCoordinator):**

```go
func (h *Handler) Handle(ctx context.Context, cmd Command) error {
    // Configuration
    maxWorkers := 5
    discoveryInterval := 2 * time.Minute
    assignmentInterval := 30 * time.Second

    var opportunities []*ManufacturingOpportunity
    assignedShips := make(map[string]bool)
    activeWorkers := 0
    workerCompletions := make(chan WorkerResult)

    // Discovery ticker
    discoveryTicker := time.NewTicker(discoveryInterval)
    assignmentTicker := time.NewTicker(assignmentInterval)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        case <-discoveryTicker.C:
            // Refresh opportunities
            opportunities = h.demandFinder.FindHighDemandManufacturables(ctx, ...)

        case <-assignmentTicker.C:
            // Get idle haulers (shared pool)
            idleShips := h.shipPoolManager.FindIdleLightHaulers(ctx, ...)

            // Assign ships to opportunities
            for _, opp := range opportunities {
                if activeWorkers >= maxWorkers {
                    break
                }
                ship := selectBestShip(idleShips, opp, assignedShips)
                if ship != nil {
                    go h.spawnWorker(ctx, ship, opp, workerCompletions)
                    activeWorkers++
                    assignedShips[ship.ShipSymbol()] = true
                }
            }

        case result := <-workerCompletions:
            activeWorkers--
            delete(assignedShips, result.ShipSymbol)
            h.logger.Log("INFO", fmt.Sprintf(
                "Worker completed: %s, profit: %d credits",
                result.Good, result.Profit,
            ))
        }
    }
}
```

### Worker: ManufacturingArbitrageWorker

```go
// internal/application/trading/commands/run_manufacturing_arbitrage_worker.go

type ManufacturingArbitrageWorkerCommand struct {
    PlayerID     int
    ShipSymbol   string
    Opportunity  *ManufacturingOpportunity
}

type WorkerResult struct {
    ShipSymbol    string
    Good          string
    QuantitySold  int
    Revenue       int
    Cost          int  // Calculated from ledger
    Profit        int
    Success       bool
    Error         error
}
```

**Worker Workflow:**

```
1. Record starting balance (from player query)

2. MANUFACTURE
   └── Call ProductionExecutor.ProduceGood(ship, opportunity.DependencyTree)
       ├── Recursively acquires all inputs (BUY or nested FABRICATE)
       ├── Delivers inputs to manufacturing waypoint
       ├── Polls until output appears
       └── Purchases manufactured output

3. TRANSPORT
   └── Navigate to sell market (opportunity.SellMarket)
       └── NavigateRouteCommand (handles multi-hop, refueling)

4. SELL
   └── SellCargoCommand (sells all manufactured goods)
       └── Records transaction in ledger (category: CARGO_TRADE)

5. CALCULATE PROFIT
   └── Query ledger for all transactions since step 1
       ├── Sum all DEBIT transactions (inputs, fuel, etc.)
       ├── Sum all CREDIT transactions (sale revenue)
       └── Profit = CREDIT - DEBIT

6. RETURN RESULT
   └── Signal completion to coordinator with profit
```

## Configuration

### Environment Variables

```bash
# Enable/disable manufacturing arbitrage
ST_MANUFACTURING_ARBITRAGE_ENABLED=true

# Maximum parallel manufacturing workers
ST_MANUFACTURING_MAX_WORKERS=5

# Minimum purchase price to consider (credits)
ST_MANUFACTURING_MIN_PRICE=1000

# Discovery interval (how often to scan for opportunities)
ST_MANUFACTURING_DISCOVERY_INTERVAL=2m

# Assignment interval (how often to assign ships)
ST_MANUFACTURING_ASSIGNMENT_INTERVAL=30s
```

## CLI Commands

### Start Manufacturing Arbitrage

```bash
./bin/spacetraders manufacturing start [--player <id>] [--system <symbol>]
```

Starts the manufacturing arbitrage coordinator as a background container.

### Check Status

```bash
./bin/spacetraders manufacturing status [--player <id>]
```

Output:
```
Manufacturing Arbitrage Status
==============================
Status: RUNNING
System: X1-DF55

Active Workers: 3/5
Ships Assigned: AGENT-7, AGENT-12, AGENT-15

Discovered Opportunities:
  1. ADVANCED_CIRCUITRY @ X1-DF55-A3 - 8,500 credits/unit (depth: 3)
  2. SHIP_PLATING @ X1-DF55-B7 - 6,200 credits/unit (depth: 2)
  3. ELECTRONICS @ X1-DF55-C1 - 3,100 credits/unit (depth: 2)

Recent Completions:
  - MACHINERY: +4,230 credits profit (2 min ago)
  - PLASTICS: +1,890 credits profit (8 min ago)

Total Profit (session): 18,450 credits
```

### Analyze Demand

```bash
./bin/spacetraders analyze demand [--player <id>] [--system <symbol>] [--top <n>]
```

Shows high-demand goods that can be manufactured (analysis only, no execution).

Output:
```
High-Demand Manufacturables in X1-DF55
======================================

1. ADVANCED_CIRCUITRY
   Import Market: X1-DF55-A3
   Purchase Price: 8,500 credits/unit
   Dependency Depth: 3 levels
   Required Inputs: ELECTRONICS, MICROPROCESSORS

2. SHIP_PLATING
   Import Market: X1-DF55-B7
   Purchase Price: 6,200 credits/unit
   Dependency Depth: 2 levels
   Required Inputs: ALUMINUM, MACHINERY

3. ELECTRONICS
   Import Market: X1-DF55-C1
   Purchase Price: 3,100 credits/unit
   Dependency Depth: 2 levels
   Required Inputs: SILICON_CRYSTALS, COPPER
```

### Stop Manufacturing

```bash
./bin/spacetraders manufacturing stop [--player <id>]
```

Gracefully stops the coordinator and all workers.

## Profit Tracking

### Using the Ledger

All costs and revenues are automatically tracked via the existing ledger system:

**Costs (DEBIT transactions):**
- Raw material purchases (category: `CARGO_TRADE`)
- Input purchases for fabrication (category: `CARGO_TRADE`)
- Fuel purchases (category: `REFUEL`)
- Input deliveries to manufacturing waypoints (sold at loss)

**Revenue (CREDIT transactions):**
- Sale of manufactured goods (category: `CARGO_TRADE`)

**Profit Calculation:**
```go
func calculateProfit(ledger LedgerRepository, playerID int, startTime time.Time) int {
    // Get all transactions since worker started
    txns := ledger.GetTransactionsSince(playerID, startTime)

    revenue := 0
    costs := 0

    for _, txn := range txns {
        if txn.Type == TransactionTypeCredit {
            revenue += txn.Amount
        } else {
            costs += txn.Amount
        }
    }

    return revenue - costs
}
```

### New Ledger Category (Optional)

Could add `MANUFACTURING_ARBITRAGE` category for clearer tracking:

```go
const CategoryManufacturingArbitrage Category = "MANUFACTURING_ARBITRAGE"
```

This allows filtering manufacturing profits separately from regular arbitrage.

## Testing Strategy

### BDD Tests

**`test/bdd/features/application/trading/manufacturing_demand_finder.feature`**

```gherkin
Feature: Manufacturing Demand Finder

  Scenario: Find high-demand manufacturable goods
    Given market X1-A1 imports "ELECTRONICS" at 3000 credits
    And market X1-A2 imports "FUEL" at 100 credits
    And market X1-A3 imports "ADVANCED_CIRCUITRY" at 8000 credits
    And "ELECTRONICS" is in the supply chain map
    And "ADVANCED_CIRCUITRY" is in the supply chain map
    And "FUEL" is NOT in the supply chain map
    When I find high-demand manufacturables with min price 1000
    Then I should get 2 opportunities
    And the first opportunity should be "ADVANCED_CIRCUITRY" at 8000 credits
    And the second opportunity should be "ELECTRONICS" at 3000 credits
    And "FUEL" should not be included (not manufacturable)

  Scenario: No opportunities when prices too low
    Given market X1-A1 imports "ELECTRONICS" at 500 credits
    And "ELECTRONICS" is in the supply chain map
    When I find high-demand manufacturables with min price 1000
    Then I should get 0 opportunities

  Scenario: Build dependency tree for opportunity
    Given market X1-A1 imports "MACHINERY" at 2000 credits
    And "MACHINERY" requires ["IRON"]
    And "IRON" is available at X1-B1
    When I find high-demand manufacturables
    Then the "MACHINERY" opportunity should have a dependency tree
    And the tree should have depth 1
    And the tree root should be "MACHINERY" with method FABRICATE
    And the tree should have child "IRON" with method BUY
```

**`test/bdd/features/application/trading/manufacturing_arbitrage_worker.feature`**

```gherkin
Feature: Manufacturing Arbitrage Worker

  Scenario: Successfully manufacture and sell for profit
    Given ship "AGENT-1" with 40 cargo capacity
    And a manufacturing opportunity for "MACHINERY" at X1-A1 for 2000 credits
    And "IRON" is available at X1-B1 for 500 credits
    And X1-C1 is a manufacturing waypoint for "MACHINERY"
    When the worker executes
    Then it should acquire "IRON" at X1-B1
    And it should deliver "IRON" to X1-C1
    And it should poll until "MACHINERY" appears
    And it should purchase "MACHINERY"
    And it should navigate to X1-A1
    And it should sell "MACHINERY"
    And the result should show positive profit

  Scenario: Handle production timeout gracefully
    Given ship "AGENT-1" with 40 cargo capacity
    And a manufacturing opportunity for "ADVANCED_CIRCUITRY"
    And inputs have been delivered to manufacturing waypoint
    And the context is cancelled after 30 seconds
    When the worker executes
    Then it should exit gracefully
    And it should release the ship back to idle
    And it should not report an error (context cancellation is expected)
```

**`test/bdd/features/application/trading/manufacturing_arbitrage_coordinator.feature`**

```gherkin
Feature: Manufacturing Arbitrage Coordinator

  Scenario: Discover opportunities and spawn workers
    Given 3 idle haulers in the system
    And high-demand opportunities exist:
      | good         | market | price |
      | ELECTRONICS  | X1-A1  | 3000  |
      | MACHINERY    | X1-A2  | 2000  |
    When the coordinator runs one discovery cycle
    Then it should find 2 opportunities
    When the coordinator runs one assignment cycle
    Then it should spawn 2 workers
    And 2 ships should be assigned

  Scenario: Respect max workers limit
    Given 10 idle haulers in the system
    And 10 high-demand opportunities exist
    And max workers is configured as 5
    When the coordinator runs assignment cycles
    Then at most 5 workers should be active at any time

  Scenario: Release ship when worker completes
    Given a worker processing "ELECTRONICS"
    And the ship "AGENT-1" is assigned
    When the worker completes successfully
    Then "AGENT-1" should be released back to idle pool
    And the coordinator should receive the profit result
```

## Implementation Phases

### Phase 1: Demand Finder (1-2 days)

**Files to create:**
- `internal/application/trading/services/manufacturing_demand_finder.go`
- `internal/domain/trading/manufacturing_opportunity.go`

**Tests:**
- `test/bdd/features/application/trading/manufacturing_demand_finder.feature`
- `test/bdd/steps/manufacturing_demand_finder_steps.go`

**Validation:** Can discover and rank high-demand manufacturable goods

### Phase 2: Worker Implementation (2-3 days)

**Files to create:**
- `internal/application/trading/commands/run_manufacturing_arbitrage_worker.go`

**Tests:**
- `test/bdd/features/application/trading/manufacturing_arbitrage_worker.feature`
- `test/bdd/steps/manufacturing_arbitrage_worker_steps.go`

**Validation:** Worker can manufacture good and sell for profit

### Phase 3: Coordinator (2 days)

**Files to create:**
- `internal/application/trading/commands/run_manufacturing_arbitrage_coordinator.go`

**Tests:**
- `test/bdd/features/application/trading/manufacturing_arbitrage_coordinator.feature`
- `test/bdd/steps/manufacturing_arbitrage_coordinator_steps.go`

**Validation:** Coordinator manages multiple workers using shared idle pool

### Phase 4: CLI & Integration (1-2 days)

**Files to create:**
- `internal/adapters/cli/manufacturing.go`

**Files to update:**
- `pkg/proto/daemon/daemon.proto` (add StartManufacturingArbitrage, etc.)
- `internal/adapters/grpc/daemon_server.go`

**Validation:** End-to-end CLI → daemon → coordinator → worker flow

### Phase 5: Testing & Polish (1-2 days)

- Integration testing with live API
- Performance tuning
- Documentation updates

**Total: 7-10 days**

## Key Design Decisions

### 1. Shared Idle Hauler Pool

**Decision:** Use the same idle pool as arbitrage, no dedicated ships.

**Rationale:**
- Simpler fleet management
- Natural load balancing (manufacturing gets ships when arbitrage doesn't need them)
- No manual ship role assignment needed
- Ships are fungible

**Trade-off:** Manufacturing operations are slower (30-60+ min), so they hold ships longer. If arbitrage needs all ships, manufacturing may starve.

**Mitigation:** Max workers limit (default 5) prevents manufacturing from taking all ships.

### 2. No Upfront Cost Estimation

**Decision:** Don't estimate costs before execution; calculate profit after from ledger.

**Rationale:**
- SpaceTraders uses dynamic, market-driven production (no fixed recipes)
- Cost estimation would be inaccurate without quantity data
- Ledger already tracks all transactions
- Simpler implementation

**Trade-off:** Can't filter out unprofitable opportunities before execution.

**Mitigation:** Focus on high-value targets (high purchase prices) where profit is likely.

### 3. Focus on Demand, Not Cost

**Decision:** Prioritize by purchase price (demand), not estimated profit margin.

**Rationale:**
- High purchase prices indicate real demand
- Manufacturing cost is unknowable until execution
- Simple, transparent ranking

### 4. Parallel Execution with Limits

**Decision:** Max 5 parallel workers (configurable).

**Rationale:**
- Balance manufacturing throughput vs ship availability
- Don't starve other operations (arbitrage, contracts)
- Manufacturing is slow; more workers = more throughput

## Risk Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Unprofitable production | Medium | Low | Focus on high-price goods; learn from results |
| Ship starvation for arbitrage | Medium | Medium | Max workers limit; shared pool |
| Long production times | High | Low | Expected; workers poll indefinitely |
| Market prices change mid-execution | Medium | Low | Accept as normal market dynamics |
| Scout tours not running | Low | High | Document dependency; check at startup |

## Success Criteria

### MVP
- [ ] Discover high-demand manufacturable goods
- [ ] Manufacture and sell goods for profit
- [ ] Track profit via ledger transactions
- [ ] CLI commands for start/status/stop

### Production Ready
- [ ] Coordinate 5 parallel manufacturing workers
- [ ] Integrate with shared idle hauler pool
- [ ] Full BDD test coverage
- [ ] Graceful shutdown and error handling
- [ ] Configurable via environment variables

### Stretch Goals
- [ ] Metrics integration (Prometheus)
- [ ] Profit analysis dashboard
- [ ] Intelligent opportunity selection (learn from results)
- [ ] Multi-system manufacturing coordination

## References

### Existing Patterns
- `ArbitrageCoordinator` - Coordinator pattern with shared pool
- `ProductionExecutor` - Manufacturing workflow
- `SupplyChainResolver` - Dependency tree building
- `ContractFleetCoordinator` - Worker spawning and completion channels

### Documentation
- `GOODS_FACTORY_IMPLEMENTATION_PLAN.md` - Production system details
- `ARBITRAGE_TRADING_IMPLEMENTATION_PLAN.md` - Arbitrage patterns
- `CLAUDE.md` - Testing strategy and architecture

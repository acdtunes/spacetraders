# Goods Factory Implementation Plan

## Overview

Implement an automated goods production system that can fabricate any item in the SpaceTraders supply chain by recursively acquiring inputs, delivering them to manufacturing waypoints, and coordinating multi-ship fleets for parallel production.

## Design Goals

1. **Full Supply Chain Coverage**: Support production of any good from raw materials
2. **Intelligent Buy vs Make**: Always prefer purchasing when available, fabricate only when necessary
3. **Fleet Coordination**: Parallelize production across multiple ships
4. **Market-Driven Discovery**: Use market queries to find import/export locations
5. **Recursive Production**: Build complete dependency trees to raw materials

## User Requirements

Based on clarifying questions:

- **Execution Pattern**: Fleet Coordinator (like `ContractFleetCoordinator`)
- **Buy/Make Strategy**: Always buy if available at any price; fabricate only if not sold in system
- **Recursion Depth**: Fully recursive to raw materials (IRON_ORE, COPPER_ORE, etc.)
- **Fleet Management**: Multi-ship coordinator with dynamic idle ship discovery

## Supply Chain Data

The system uses the SpaceTraders `exportToImportMap` which maps produced goods to their required inputs:

```json
{
  "MACHINERY": ["IRON"],
  "ELECTRONICS": ["SILICON_CRYSTALS", "COPPER"],
  "ADVANCED_CIRCUITRY": ["ELECTRONICS", "MICROPROCESSORS"],
  "COPPER": ["COPPER_ORE"],
  "IRON": ["IRON_ORE"],
  ...
}
```

### Dependency Tree Example

Producing `ADVANCED_CIRCUITRY` requires:
```
ADVANCED_CIRCUITRY
├── ELECTRONICS
│   ├── SILICON_CRYSTALS (raw material - buy)
│   └── COPPER
│       └── COPPER_ORE (raw material - buy)
└── MICROPROCESSORS
    ├── SILICON_CRYSTALS (raw material - buy)
    └── COPPER
        └── COPPER_ORE (raw material - buy)
```

## Production Model: Market-Driven vs Recipe-Based

**CRITICAL UNDERSTANDING:** SpaceTraders uses a **dynamic, market-driven production system** rather than fixed conversion ratios.

### How Production Actually Works

1. **No Fixed Quantities**:
   - The game does NOT specify "2 IRON → 1 MACHINERY" style formulas
   - Production is organic and responds to market forces
   - Delivering inputs **enables** production but doesn't guarantee specific outputs

2. **Supply/Demand Driven**:
   - "As agents buy up supply from an export good, production tends to increase to meet the demand"
   - Markets produce more when demand is high
   - Production rate varies by market activity (WEAK, GROWING, STRONG)

3. **Import Constraint**:
   - "Exports are constrained by the supply of imports"
   - Delivering IRON to a market that imports IRON increases that market's ability to produce MACHINERY
   - But the exact amount produced is time-based and market-dependent

4. **Quantity Flexibility**:
   - User requests: "I want ADVANCED_CIRCUITRY"
   - System delivers: "I acquired some ADVANCED_CIRCUITRY" (variable amount)
   - Production is **opportunistic**, not deterministic

### Impact on Implementation

- **No quantity calculations**: Don't try to compute exact input amounts
- **Availability-focused**: Poll markets until desired export appears
- **Cargo-constrained**: Buy whatever quantity is available (up to ship capacity)
- **Market selection matters**: Choose markets with STRONG/GROWING activity for faster production

## Architecture

### Domain Layer (`internal/domain/goods/`)

#### Entities

**`GoodsFactory`** (Aggregate Root)
```go
type GoodsFactory struct {
    id             string
    playerID       int
    targetGood     string
    dependencyTree *SupplyChainNode
    status         FactoryStatus
    metadata       map[string]string
    lifecycle      *LifecycleStateMachine
}

type FactoryStatus string
const (
    FactoryStatusPending   FactoryStatus = "PENDING"
    FactoryStatusActive    FactoryStatus = "ACTIVE"
    FactoryStatusCompleted FactoryStatus = "COMPLETED"
    FactoryStatusFailed    FactoryStatus = "FAILED"
)
```

**Methods:**
- `Start() error` - Transition PENDING → ACTIVE
- `Complete() error` - Transition ACTIVE → COMPLETED
- `Fail(reason string) error` - Transition to FAILED
- `CanStart() bool` - Validation guard

#### Value Objects

**`SupplyChainNode`** (Recursive Tree Structure)
```go
type SupplyChainNode struct {
    good              string
    acquisitionMethod AcquisitionMethod
    children          []*SupplyChainNode
    marketActivity    string // WEAK, GROWING, STRONG (from market data)
    supplyLevel       string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
}

type AcquisitionMethod string
const (
    AcquisitionBuy       AcquisitionMethod = "BUY"
    AcquisitionFabricate AcquisitionMethod = "FABRICATE"
)
```

**Methods:**
- `IsLeaf() bool` - Check if raw material (no children)
- `TotalDepth() int` - Max depth of tree
- `FlattenToList() []*SupplyChainNode` - BFS traversal
- `RequiredRawMaterials() []string` - Leaf node good symbols (no quantities)
- `EstimateProductionTime() time.Duration` - Rough estimate based on depth and market activity

**NOTE:** No `quantity` field because SpaceTraders uses dynamic, market-driven production. The system will acquire whatever quantity is available at markets, not calculate exact amounts needed.

#### Domain Services

**`SupplyChainResolver`**
```go
type SupplyChainResolver struct {
    supplyChainMap map[string][]string // exportToImportMap
    marketRepo     market.MarketRepository
}
```

**Methods:**
- `BuildDependencyTree(ctx context.Context, targetGood string, systemSymbol string, playerID int) (*SupplyChainNode, error)`
  - Recursively traces dependencies
  - Detects cycles
  - Validates chain integrity
  - Queries markets to populate activity/supply levels
  - Returns root node of tree

**Algorithm:**
```
function BuildTree(ctx, good, systemSymbol, playerID, visited):
    if good in visited:
        return error "circular dependency detected"

    visited.add(good)
    node = new Node(good)

    // Check if available in any market (prefer buying)
    marketData = marketRepo.FindExportMarket(ctx, good, systemSymbol, playerID)
    if marketData != nil:
        node.acquisitionMethod = BUY
        node.marketActivity = marketData.Activity
        node.supplyLevel = marketData.Supply
        return node

    // Not available for purchase, must fabricate
    if good not in supplyChainMap:
        return error "unknown good, cannot produce or buy"

    node.acquisitionMethod = FABRICATE
    inputs = supplyChainMap[good]

    for each input in inputs:
        childNode = BuildTree(ctx, input, systemSymbol, playerID, visited)
        node.children.append(childNode)

    visited.remove(good)
    return node
```

#### Repository Ports (`ports.go`)

```go
type GoodsFactoryRepository interface {
    Create(ctx context.Context, factory *GoodsFactory) error
    Update(ctx context.Context, factory *GoodsFactory) error
    FindByID(ctx context.Context, id string, playerID int) (*GoodsFactory, error)
    FindActiveByPlayer(ctx context.Context, playerID int) ([]*GoodsFactory, error)
    Delete(ctx context.Context, id string, playerID int) error
}
```

### Application Layer (`internal/application/goods/`)

#### Commands

**`commands/run_factory_coordinator.go`**

Container Type: `GOODS_FACTORY_COORDINATOR`

Pattern: Fleet Coordinator (like `ContractFleetCoordinator`)

```go
type RunFactoryCoordinatorCommand struct {
    PlayerID     int
    TargetGood   string
    SystemSymbol string // Where to produce (default: current system)
}

type RunFactoryCoordinatorHandler struct {
    factoryRepo        ports.GoodsFactoryRepository
    shipRepo           navigation.ShipRepository
    assignmentRepo     container.ShipAssignmentRepository
    marketRepo         market.MarketRepository
    resolver           *SupplyChainResolver
    productionExecutor *ProductionExecutor
    mediator           common.Mediator
    clock              shared.Clock
}
```

**Workflow:**

1. **Load Supply Chain Map** (from config/embedded)
2. **Build Dependency Tree** using `SupplyChainResolver`:
   - Tree-building queries markets to determine BUY vs FABRICATE
   - Populates market activity and supply levels
   - No quantity calculations needed
3. **Flatten Tree** to get all required nodes
4. **Discover Idle Ships** via `FindIdleLightHaulers()`
5. **Create Worker Containers** for parallel branches:
   - Assign production nodes to workers
   - Transfer ships to workers
   - Store worker metadata (node, ship, target)
   - Workers will acquire whatever quantity is available
6. **Start Workers** in goroutines
7. **Wait on Completion Channels**:
   - Unbuffered channel per worker
   - Block until worker signals completion
   - Ship auto-released back to idle pool
8. **Loop Until Complete**:
   - Check if target good was acquired (any amount > 0)
   - Discover newly idle ships
   - Assign to remaining work
9. **Cleanup**:
   - Release all ships
   - Mark factory COMPLETED
   - Log cost metrics and quantity acquired

**Error Handling:**
- Worker failure → retry with different ship
- No idle ships → wait with timeout
- Market unavailable → backoff and retry
- Production timeout → fail with clear message (inputs delivered but no output)

**`commands/run_factory_worker.go`**

Container Type: `GOODS_FACTORY_WORKER`

Pattern: Single Workflow (like `ContractWorkflow`)

```go
type RunFactoryWorkerCommand struct {
    PlayerID      int
    ShipSymbol    string
    ProductionNode *SupplyChainNode
    FactoryID     string
}

type RunFactoryWorkerHandler struct {
    shipRepo           navigation.ShipRepository
    marketRepo         market.MarketRepository
    productionExecutor *ProductionExecutor
    mediator           common.Mediator
    clock              shared.Clock
}
```

**Workflow:**

1. **Receive Assigned Node** from metadata
2. **Check Acquisition Method**:
   - If `BUY` → find market, navigate, purchase whatever is available
   - If `FABRICATE` → recursively produce inputs, then manufacture
3. **For BUY**:
   a. Find best market selling the good (prefer STRONG activity, HIGH supply)
   b. Navigate to market and dock
   c. Purchase whatever quantity is available (up to cargo capacity)
   d. No quantity target - opportunistic acquisition
4. **For FABRICATE**:
   a. **Produce Inputs** (recursive):
      - For each child node in dependency tree
      - Execute production (BUY or nested FABRICATE)
      - Cargo will contain required inputs when complete
   b. **Find Manufacturing Waypoint**:
      - Query markets for waypoint that imports this good
      - Use `MarketLocator.FindImportMarket(good)`
      - Prefer markets with STRONG activity for faster production
   c. **Deliver Inputs**:
      - Navigate to manufacturing waypoint
      - Dock
      - Sell/deliver ALL cargo (all required inputs) to market
      - This enables the market to produce the output good
   d. **Poll for Production** (CRITICAL STEP):
      - Query database for market exports until output good appears
      - **Scout ships keep database fresh** with continuous market polling via tours
      - Production worker just reads from database (no direct API calls)
      - Polling intervals: [30s, 60s, 60s, 60s, ...] (starts fast, settles to 60s)
      - **No timeout** - polls indefinitely until good appears or context cancelled
      - Log each poll attempt with timestamp
      - Monitor supply level changes (production indicator)
      - **Markets produce goods over time**, not instantly
      - Exit via context cancellation (daemon stop, user stop command, etc.)
   e. **Purchase Output**:
      - Once good appears in market exports, query current availability
      - Purchase whatever quantity is available (up to cargo capacity)
      - No guarantee of specific quantity - market-driven
5. **Signal Completion** to coordinator via channel (include quantity acquired)
6. **Return** (ship auto-released, cargo contains produced good)

**State Management:**
- Store current step in container metadata (e.g., "delivering_inputs", "polling_for_production")
- Track poll attempts and timestamps
- Support interruption/resume from any step
- Log all market interactions for debugging

**Market Polling Details:**
```go
// Polling loop - NO TIMEOUT, polls until good appears or context cancelled
// NOTE: Queries database, which scout tours keep fresh with continuous market polling
// Scout tours create variable update intervals per market (depends on tour size/distances)
intervals := []time.Duration{
    30 * time.Second, // Initial poll - catch fast production
    60 * time.Second, // Settled interval - balance responsiveness vs queries
}

for attempt := 0; ; attempt++ { // Infinite loop - no timeout!
    // Check for context cancellation (daemon stop, user command, etc.)
    select {
    case <-ctx.Done():
        logger.Log("INFO", "Production polling cancelled", nil)
        return ctx.Err()
    default:
        // Continue polling
    }

    // Query database (kept fresh by scout ships touring markets)
    market, err := marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
    if err != nil {
        logger.Log("ERROR", fmt.Sprintf("Failed to query market data: %v", err), nil)
        interval := intervals[min(attempt, 1)]
        clock.Sleep(interval)
        continue
    }

    // Check if production complete
    if market.HasGood(targetGood) && market.Supply != "SCARCE" {
        logger.Log("INFO", fmt.Sprintf("Production complete: %s available after %d polls",
            targetGood, attempt+1), nil)
        return purchaseGood(ctx, ship, market, targetGood)
    }

    // Use 30s for first poll, then settle at 60s for all subsequent polls
    interval := intervals[min(attempt, 1)]
    logger.Log("INFO", fmt.Sprintf("Poll attempt %d: %s not yet available, waiting %s",
        attempt+1, targetGood, interval), nil)

    // Optional: Log warning for unusually long production times (informational only)
    if attempt > 20 { // ~20 minutes at 60s intervals
        logger.Log("WARN", fmt.Sprintf(
            "Production taking unusually long (%d polls, ~%d minutes)",
            attempt+1, attempt,
        ), nil)
    }

    clock.Sleep(interval)
}
```

#### Services

**`services/supply_chain_resolver.go`**

```go
type SupplyChainResolver struct {
    supplyChainMap map[string][]string
    marketRepo     market.MarketRepository
}

func NewSupplyChainResolver(
    chainMap map[string][]string,
    marketRepo market.MarketRepository,
) *SupplyChainResolver

func (r *SupplyChainResolver) BuildDependencyTree(
    ctx context.Context,
    targetGood string,
    systemSymbol string,
    playerID int,
) (*SupplyChainNode, error)

func (r *SupplyChainResolver) ValidateChain(targetGood string) error

func (r *SupplyChainResolver) DetectCycles(targetGood string) error
```

**Implementation Details:**
- DFS traversal to build tree
- Cycle detection via visited set
- Validate all inputs exist in map or are available in markets
- Handle raw materials (leaves - always BUY)
- Query markets during tree building to determine BUY vs FABRICATE
- Populate market activity and supply levels for each node
- **No quantity tracking** - tree represents dependencies only

**`services/production_executor.go`**

```go
type ProductionExecutor struct {
    mediator       common.Mediator
    shipRepo       navigation.ShipRepository
    marketRepo     market.MarketRepository
    marketLocator  *MarketLocator
    cargoManager   *CargoManager
    clock          shared.Clock
}

func (e *ProductionExecutor) ProduceGood(
    ctx context.Context,
    ship *navigation.Ship,
    node *SupplyChainNode,
    playerID int,
) (quantityAcquired int, error)
```

**Orchestration:**
1. If `node.acquisitionMethod == BUY`:
   - Find best market selling good (prefer STRONG activity, HIGH supply)
   - Navigate to market
   - Purchase whatever quantity is available (up to cargo capacity)
   - Return quantity acquired
2. If `node.acquisitionMethod == FABRICATE`:
   - Recursively produce inputs (multiple BUY/FABRICATE calls)
   - Find manufacturing waypoint (that imports this good)
   - Deliver ALL inputs to market
   - **Poll market** until output good appears in exports
   - Purchase whatever quantity of output is available
   - Return quantity acquired
3. Return (quantityAcquired, nil) with cargo containing produced good

**Key Behaviors:**
- No target quantities - acquire whatever is available
- Market polling is CRITICAL for fabrication
- Exponential backoff prevents API spam
- Timeout after configurable duration (default 10 minutes)

**Reuses Existing Commands:**
- `NavigateRouteCommand` - Ship movement
- `PurchaseCargoCommand` - Buying goods
- `SellCargoCommand` - Delivering inputs
- `DockCommand`, `OrbitCommand` - State transitions

**`services/market_locator.go`**

```go
type MarketLocator struct {
    marketRepo market.MarketRepository
}

type MarketResult struct {
    WaypointSymbol string
    Activity       string // WEAK, GROWING, STRONG
    Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
    Price          int    // sell_price (for exports) or purchase_price (for imports)
}

func (l *MarketLocator) FindImportMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*MarketResult, error)

func (l *MarketLocator) FindExportMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*MarketResult, error)

func (l *MarketLocator) FindBestExportMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (*MarketResult, error)
```

**Logic:**
- Query all markets in system
- Filter by import/export lists
- **FindBestExportMarket**: Return market with highest activity + supply
- **FindImportMarket**: Return first market that imports the good (prefer STRONG activity)
- Populate activity, supply, and price data for decision-making

**Market Selection Criteria (Best to Worst):**
1. STRONG activity + ABUNDANT/HIGH supply
2. GROWING activity + MODERATE/HIGH supply
3. Any activity + MODERATE supply
4. WEAK activity or SCARCE/LIMITED supply (last resort)

**Market API Fields:**
- `imports: []TradeGood` - Goods this market wants (will produce if delivered)
- `exports: []TradeGood` - Goods this market sells (produced goods)
- `tradeGoods[].supply` - SCARCE | LIMITED | MODERATE | HIGH | ABUNDANT
- `tradeGoods[].activity` - WEAK | GROWING | STRONG | RESTRICTED

### Adapters Layer

#### Persistence (`internal/adapters/persistence/`)

**`goods_factory_repository.go`**

```go
type GoodsFactoryRepositoryGORM struct {
    db *gorm.DB
}

func NewGoodsFactoryRepository(db *gorm.DB) *GoodsFactoryRepositoryGORM

func (r *GoodsFactoryRepositoryGORM) Create(ctx, factory) error
func (r *GoodsFactoryRepositoryGORM) Update(ctx, factory) error
func (r *GoodsFactoryRepositoryGORM) FindByID(ctx, id, playerID) (*GoodsFactory, error)
// ... other methods
```

**Database Model (`models.go`):**

```go
type GoodsFactory struct {
    ID              string    `gorm:"primaryKey"`
    PlayerID        int       `gorm:"index"`
    TargetGood      string
    SystemSymbol    string    // Where production is happening
    DependencyTree  string    // JSON-serialized SupplyChainNode
    Status          string    `gorm:"index"`
    Metadata        string    // JSON map
    QuantityAcquired int      // How many units were actually produced (set on completion)
    CreatedAt       time.Time
    UpdatedAt       time.Time
    StartedAt       *time.Time
    CompletedAt     *time.Time
}
```

**Conversion:**
- Domain entity ↔ Database model
- JSON serialization for nested tree
- Type-safe metadata marshaling

**NOTE:** `QuantityAcquired` is populated AFTER production, not before. No target quantity is set.

#### gRPC (`internal/adapters/grpc/`)

**Update `pkg/proto/daemon/daemon.proto`:**

```proto
service DaemonService {
    // Existing methods...
    rpc StartGoodsFactory(StartGoodsFactoryRequest) returns (StartGoodsFactoryResponse);
    rpc StopGoodsFactory(StopGoodsFactoryRequest) returns (StopGoodsFactoryResponse);
    rpc GetFactoryStatus(GetFactoryStatusRequest) returns (GetFactoryStatusResponse);
}

message StartGoodsFactoryRequest {
    int32 player_id = 1;
    string target_good = 2;
    string system_symbol = 3; // Optional: defaults to current system
}

message StartGoodsFactoryResponse {
    string factory_id = 1;
    string status = 2;
    string message = 3; // e.g., "Dependency tree built, 5 nodes to process"
}

message StopGoodsFactoryRequest {
    int32 player_id = 1;
    string factory_id = 2;
}

message StopGoodsFactoryResponse {
    string status = 1;
}

message GetFactoryStatusRequest {
    int32 player_id = 1;
    string factory_id = 2;
}

message GetFactoryStatusResponse {
    string factory_id = 1;
    string target_good = 2;
    string status = 3;
    string dependency_tree = 4; // JSON
    int32 quantity_acquired = 5; // 0 if not yet complete
    int32 nodes_completed = 6;
    int32 nodes_total = 7;
}
```

**NOTE:** No `quantity` input parameter - system acquires whatever is available

**Handler Implementation (`daemon_server.go`):**

```go
func (s *DaemonServer) StartGoodsFactory(
    ctx context.Context,
    req *pb.StartGoodsFactoryRequest,
) (*pb.StartGoodsFactoryResponse, error) {
    // Create coordinator container
    // Start container via ContainerRunner
    // Return factory ID
}
```

#### CLI (`internal/adapters/cli/`)

**`goods_factory.go`**

```go
var goodsCmd = &cobra.Command{
    Use:   "goods",
    Short: "Goods factory operations",
}

var goodsProduceCmd = &cobra.Command{
    Use:   "produce",
    Short: "Produce a good using supply chain fabrication",
    Run: func(cmd *cobra.Command, args []string) {
        // Resolve player
        // Send gRPC StartGoodsFactory
        // Display factory ID and status
    },
}

var goodsStatusCmd = &cobra.Command{
    Use:   "status",
    Short: "Check goods factory status",
    Run: func(cmd *cobra.Command, args []string) {
        // Send gRPC GetFactoryStatus
        // Display tree and progress
    },
}

var goodsStopCmd = &cobra.Command{
    Use:   "stop",
    Short: "Stop a running goods factory",
    Run: func(cmd *cobra.Command, args []string) {
        // Send gRPC StopGoodsFactory
    },
}
```

**Flags:**
- `--target` (required) - Good to produce
- `--system` (optional) - System where to produce (defaults to current system)
- `--factory-id` (for status/stop)

**Usage Examples:**
```bash
# Start production (acquires whatever quantity is available)
./bin/spacetraders goods produce --target ADVANCED_CIRCUITRY

# Start production in specific system
./bin/spacetraders goods produce --target MACHINERY --system X1-GZ7

# Check status (shows quantity acquired so far)
./bin/spacetraders goods status --factory-id abc-123

# Stop factory
./bin/spacetraders goods stop --factory-id abc-123
```

**Output Example:**
```
Starting goods factory...
Building dependency tree for ADVANCED_CIRCUITRY...
Dependency tree: 7 nodes (3 BUY, 4 FABRICATE)
Factory ID: fac-abc123

Status: ACTIVE
Progress: 3/7 nodes completed
Ships assigned: 2

To check status: spacetraders goods status --factory-id fac-abc123
```

#### Configuration

**Supply Chain Data Loading:**

Option 1: Embedded in code
```go
// internal/application/goods/supply_chain_data.go
var ExportToImportMap = map[string][]string{
    "MACHINERY": {"IRON"},
    "ELECTRONICS": {"SILICON_CRYSTALS", "COPPER"},
    // ... full map
}
```

Option 2: External JSON file
```go
// Load from config/supply_chain.json
func LoadSupplyChainMap(path string) (map[string][]string, error)
```

**Environment Variables:**
```bash
GOODS_SUPPLY_CHAIN_PATH=./config/supply_chain.json
GOODS_POLL_INTERVAL_INITIAL=30  # First poll interval in seconds
GOODS_POLL_INTERVAL_SETTLED=60  # Subsequent poll intervals in seconds
# No timeout - polls indefinitely until good appears or context cancelled
```

## Testing Strategy

### BDD Tests (`test/bdd/`)

#### Domain Tests

**`features/domain/goods/supply_chain_resolver.feature`**

```gherkin
Feature: Supply Chain Resolver

  Scenario: Resolve simple dependency
    Given a supply chain map with "MACHINERY" requiring "IRON"
    And "IRON" is available at market X1-A1 with activity "STRONG"
    When I build dependency tree for "MACHINERY" in system X1
    Then the tree should have root "MACHINERY" marked as FABRICATE
    And the tree should have 1 child "IRON" marked as BUY
    And "IRON" node should have market activity "STRONG"

  Scenario: Resolve multi-level dependency with market data
    Given a supply chain map with:
      | Output              | Inputs                      |
      | ADVANCED_CIRCUITRY  | ELECTRONICS, MICROPROCESSORS|
      | ELECTRONICS         | SILICON_CRYSTALS, COPPER    |
      | MICROPROCESSORS     | SILICON_CRYSTALS, COPPER    |
      | COPPER              | COPPER_ORE                  |
    And "SILICON_CRYSTALS" is available at X1-B1 with supply "ABUNDANT"
    And "COPPER_ORE" is available at X1-B2 with supply "HIGH"
    When I build dependency tree for "ADVANCED_CIRCUITRY" in system X1
    Then the tree depth should be 3
    And the raw materials should include "SILICON_CRYSTALS" and "COPPER_ORE"
    And all leaf nodes should be marked as BUY
    And all non-leaf nodes should be marked as FABRICATE

  Scenario: Prefer buying over fabricating when available
    Given a supply chain map with "MACHINERY" requiring "IRON"
    And "MACHINERY" is available at market X1-C1 with supply "MODERATE"
    When I build dependency tree for "MACHINERY" in system X1
    Then the tree should have root "MACHINERY" marked as BUY (not FABRICATE)
    And the tree should have 0 children
    Because the good is available for purchase

  Scenario: Detect circular dependency
    Given a supply chain map with:
      | Output | Inputs |
      | A      | B      |
      | B      | A      |
    When I build dependency tree for "A" in system X1
    Then I should get a cycle detection error
```

**`features/domain/goods/goods_factory.feature`**

```gherkin
Feature: Goods Factory Entity

  Scenario: Factory state transitions
    Given a goods factory in PENDING state
    When I start the factory
    Then the factory should be in ACTIVE state
    And the started_at timestamp should be set

  Scenario: Complete factory
    Given a goods factory in ACTIVE state
    When I complete the factory
    Then the factory should be in COMPLETED state
    And the completed_at timestamp should be set
```

#### Application Tests

**`features/application/goods/factory_coordinator.feature`**

```gherkin
Feature: Goods Factory Coordinator

  Scenario: Discover idle ships and assign workers
    Given 3 idle ships in the system
    And a production target "MACHINERY"
    When I start the factory coordinator
    Then it should discover 3 idle ships
    And it should create worker containers
    And it should transfer ships to workers

  Scenario: Wait for worker completion
    Given a factory coordinator with 2 active workers
    When worker 1 completes successfully
    Then the coordinator should receive completion signal
    And worker 1's ship should be released back to idle

  Scenario: Handle worker failures
    Given a factory coordinator with 1 active worker
    When the worker fails with insufficient fuel
    Then the coordinator should retry with a different ship
    And the failed worker should release its ship
```

**`features/application/goods/factory_worker.feature`**

```gherkin
Feature: Goods Factory Worker

  Scenario: Buy good when available in market
    Given a worker assigned to produce "IRON_ORE"
    And "IRON_ORE" is available at waypoint X1-A2 with supply "HIGH"
    When the worker executes
    Then it should navigate to X1-A2
    And it should purchase "IRON_ORE" (whatever quantity is available)
    And it should signal completion with quantity acquired

  Scenario: Fabricate good when not available
    Given a worker assigned to produce "MACHINERY"
    And "MACHINERY" is not sold in any market
    And "IRON" is available at waypoint X1-B3
    And waypoint X1-C5 imports "IRON" and exports "MACHINERY"
    When the worker executes
    Then it should first acquire "IRON" by purchasing it
    And it should find the manufacturing waypoint X1-C5
    And it should deliver all "IRON" to waypoint X1-C5
    And it should poll waypoint X1-C5 market for "MACHINERY"
    And when "MACHINERY" appears in exports, it should purchase it
    And it should signal completion with quantity acquired

  Scenario: Poll market for production with simple intervals
    Given a worker waiting for "ELECTRONICS" to be produced
    And inputs have been delivered to waypoint X1-D1
    And the current poll attempt is 1
    When the worker polls the market at X1-D1
    And "ELECTRONICS" is not yet in the exports list
    Then it should log "poll attempt 1: ELECTRONICS not yet available"
    And it should wait 30 seconds before next poll
    When the worker polls again (attempt 2)
    And "ELECTRONICS" is still not available
    Then it should wait 60 seconds before next poll
    When the worker polls again (attempt 3)
    And "ELECTRONICS" appears in exports with supply "MODERATE"
    Then it should purchase "ELECTRONICS" (whatever quantity is available)
    And signal completion with quantity acquired

  Scenario: Very long production time (no timeout)
    Given a worker waiting for "ADVANCED_CIRCUITRY" to be produced
    And inputs were delivered 20 minutes ago
    And the worker has polled 21 times
    And "ADVANCED_CIRCUITRY" has never appeared in market exports
    When the worker checks poll count
    Then it should log a warning "production taking unusually long"
    And it should continue polling (not fail)
    And it should wait 60 seconds before next poll
    When "ADVANCED_CIRCUITRY" finally appears (attempt 25)
    Then it should purchase "ADVANCED_CIRCUITRY" successfully
    And signal completion

  Scenario: Context cancellation during polling
    Given a worker polling for "MACHINERY" to be produced
    And the worker is on poll attempt 5
    When the daemon sends a shutdown signal
    Then the worker should detect context cancellation
    And it should exit gracefully without error
    And it should release the ship back to idle

  Scenario: Select best market based on activity and supply
    Given "COPPER_ORE" is available at multiple waypoints:
      | Waypoint | Activity | Supply    |
      | X1-A1    | WEAK     | SCARCE    |
      | X1-A2    | STRONG   | ABUNDANT  |
      | X1-A3    | GROWING  | MODERATE  |
    When the worker selects a market to buy from
    Then it should choose waypoint X1-A2
    Because it has STRONG activity and ABUNDANT supply
```

#### Step Definitions

**`test/bdd/steps/goods_factory_steps.go`**

```go
type GoodsFactoryContext struct {
    factory            *goods.GoodsFactory
    resolver           *services.SupplyChainResolver
    supplyChainMap     map[string][]string
    dependencyTree     *goods.SupplyChainNode
    error              error
    mockMarketRepo     *helpers.MockMarketRepository
    mockShipRepo       *helpers.MockShipRepository
    mockAssignmentRepo *helpers.MockShipAssignmentRepository
}

func (ctx *GoodsFactoryContext) aSupplyChainMapWith(data *godog.Table) error
func (ctx *GoodsFactoryContext) iBuildDependencyTreeFor(good string) error
func (ctx *GoodsFactoryContext) theTreeShouldHaveRoot(good string) error
// ... more step definitions
```

**`test/bdd/steps/supply_chain_steps.go`**

Similar pattern for resolver-specific scenarios.

### Test Helpers

**`test/helpers/mock_goods_factory_repository.go`**

```go
type MockGoodsFactoryRepository struct {
    factories map[string]*goods.GoodsFactory
    mu        sync.Mutex
}

func (m *MockGoodsFactoryRepository) Create(ctx, factory) error
func (m *MockGoodsFactoryRepository) Update(ctx, factory) error
// ... implement all interface methods
```

## Implementation Phases

### Phase 1: Domain Foundation (Day 1)
**Files to Create:**
- `internal/domain/goods/goods_factory.go`
- `internal/domain/goods/supply_chain_node.go`
- `internal/domain/goods/production_recipe.go`
- `internal/domain/goods/ports.go`
- `internal/domain/goods/errors.go`

**BDD Tests:**
- `test/bdd/features/domain/goods/goods_factory.feature`
- `test/bdd/steps/goods_factory_steps.go`

**Validation:** Run `make test-bdd` - all domain entity tests pass

### Phase 2: Supply Chain Resolver (Day 2)
**Files to Create:**
- `internal/application/goods/services/supply_chain_resolver.go`
- `internal/application/goods/supply_chain_data.go` (embedded map)

**BDD Tests:**
- `test/bdd/features/domain/goods/supply_chain_resolver.feature`
- `test/bdd/steps/supply_chain_steps.go`

**Validation:** Resolver builds complete trees, detects cycles

### Phase 3: Worker Command (Day 3-4)
**Files to Create:**
- `internal/application/goods/commands/run_factory_worker.go`
- `internal/application/goods/services/production_executor.go`
- `internal/application/goods/services/market_locator.go`

**BDD Tests:**
- `test/bdd/features/application/goods/factory_worker.feature`

**Integration:**
- Register worker handler in mediator
- Test with mock repositories
- Verify buy vs fabricate logic

**Validation:** Worker successfully produces simple goods

### Phase 4: Coordinator Command (Day 5-6)
**Files to Create:**
- `internal/application/goods/commands/run_factory_coordinator.go`

**BDD Tests:**
- `test/bdd/features/application/goods/factory_coordinator.feature`

**Integration:**
- Ship assignment locking
- Completion channel communication
- Worker container creation

**Validation:** Coordinator manages multiple workers in parallel

### Phase 5: Persistence (Day 7)
**Files to Create:**
- `internal/adapters/persistence/goods_factory_repository.go`
- Update `internal/adapters/persistence/models.go`

**Database Migration:**
- Add `goods_factories` table
- Add indexes (player_id, status)

**Testing:**
- Test repository CRUD operations
- Verify JSON serialization

### Phase 6: gRPC + CLI (Day 8)
**Files to Update:**
- `pkg/proto/daemon/daemon.proto`
- `internal/adapters/grpc/daemon_server.go`

**Files to Create:**
- `internal/adapters/cli/goods_factory.go`

**Testing:**
- Manual CLI testing
- gRPC integration tests

**Validation:** End-to-end CLI → daemon → coordinator → worker flow

### Phase 7: Integration Testing (Day 9-10)
**Testing:**
- Run against live SpaceTraders API (test server)
- Produce complex goods (ADVANCED_CIRCUITRY)
- Monitor ship movements, market interactions
- Verify profitability calculations

**Performance:**
- Measure coordinator overhead
- Optimize market queries (caching)
- Tune polling intervals

**Validation:** Successfully produce 10+ different goods

## Key Design Decisions

### 1. Buy vs Make Strategy

**Decision:** Always buy if available at any price

**Rationale:**
- Simplifies initial implementation
- Reduces ship movement overhead
- Leverages existing market liquidity
- Can be enhanced later with profitability thresholds

**Future Enhancement:**
- Calculate cost to fabricate vs cost to buy
- Use profitability threshold (like contracts: -5000)
- Consider fuel costs, travel time

### 2. Market Discovery and Data Freshness

**Decision:** Use `MarketRepository` queries against database

**Rationale:**
- Reuses existing infrastructure
- Database kept fresh by **scout ships** (continuous background market polling)
- No additional API calls needed from production workers
- Supports cheapest/best market queries
- Respects API rate limits (scouts handle all market polling)

**Architecture:**
```
Scout Ships (Background Containers)
    ↓
Poll all markets in system continuously
    ↓
Update database with fresh market data
    ↓
Production workers read from database
```

**Dependencies:**
- Requires scout ships to be running in target system
- Scout polling frequency determines market data freshness
- If scouts aren't running, fallback to direct API queries may be needed

**Alternatives Considered:**
- Direct API polling from production workers (redundant with scouts)
- Scan all markets at startup (high API cost)
- Use static waypoint mapping (outdated quickly)

### 3. Production Waiting

**Decision:** Poll database indefinitely with simple interval pattern (no timeout)

**Rationale:**
- Markets will eventually produce goods given sufficient inputs
- Production time is highly variable (WEAK vs STRONG markets, server load, etc.)
- No reliable way to predict timeout duration
- Scout tours keep database fresh with variable intervals
- Context cancellation provides exit mechanism (daemon stop, user command)
- Simple implementation - no timeout logic needed

**Algorithm:**
```
intervals = [30s, 60s, 60s, 60s, ...]  // Start fast, settle to 60s
loop forever:
    check context cancellation → exit if cancelled
    query database for market data (kept fresh by scout tours)
    if good available in exports:
        return success
    sleep(interval[min(attempt, 1)])  // 30s first, then 60s
    if attempt > 20:
        log warning (informational only)
```

**Why These Intervals:**
- **30s initial**: Catches fast production (STRONG markets, simple goods)
- **60s settled**: Balances responsiveness vs database load
- Works with scout tours of varying sizes (3-20 markets)
- Scout tour intervals are variable (depends on tour size, travel times)
- Early poll (30s) catches lucky timing (scout just visited)
- Settled poll (60s) efficient for longer production times

**Scout Tour Impact:**
Scout ships visit multiple markets in sequence:
```
Tour: [Market A → B → C → D → E]
Market C update frequency: ~70-140 seconds (varies by tour size)
Production worker at Market C polls at [30s, 60s, 60s, ...]
Eventually catches scout update within 60 seconds
```

**Exit Mechanisms:**
- Context cancellation (daemon shutdown, container stop command)
- Optional warning logs after 20+ polls (~20 minutes) - informational only
- No hard timeout - production will complete or user will intervene

### 4. Coordinator-Worker Communication

**Decision:** Unbuffered completion channels

**Rationale:**
- Proven pattern from `ContractFleetCoordinator`
- Guarantees coordinator receives signal
- Blocks worker until coordinator ready
- Simple error propagation

**Pattern:**
```go
completionChan := make(chan string)
go worker.Execute(completionChan)
shipSymbol := <-completionChan  // Block until complete
```

### 5. Dependency Tree Serialization

**Decision:** JSON in database, Go structs in memory

**Rationale:**
- Flexible schema for tree structure
- Easy debugging (readable JSON)
- Supports future extensions
- Standard library encoding/json

**Schema:**
```json
{
  "good": "ADVANCED_CIRCUITRY",
  "acquisition_method": "FABRICATE",
  "market_activity": "STRONG",
  "supply_level": "",
  "children": [
    {
      "good": "ELECTRONICS",
      "acquisition_method": "FABRICATE",
      "market_activity": "GROWING",
      "supply_level": "",
      "children": [
        {
          "good": "SILICON_CRYSTALS",
          "acquisition_method": "BUY",
          "market_activity": "STRONG",
          "supply_level": "ABUNDANT",
          "children": []
        }
      ]
    }
  ]
}
```

**NOTE:** No `quantity` field - quantities are determined at runtime based on market availability and cargo capacity

## Error Scenarios and Handling

### Market Unavailable
**Scenario:** Manufacturing waypoint doesn't import the expected good
**Handling:**
- Log error with good name and waypoint
- Try alternative waypoints in system
- If none found, fail with clear error message
- Allow manual configuration override

### Insufficient Cargo Space
**Scenario:** Ship can't hold all required inputs
**Handling:**
- Split into multiple trips (like `DeliveryExecutor`)
- Track partial progress in metadata
- Resume from last checkpoint on restart

### Ship Destroyed/Lost
**Scenario:** Assigned ship no longer exists
**Handling:**
- Detect via API 404 response
- Release assignment
- Reassign to different ship
- Log incident for investigation

### Circular Dependencies
**Scenario:** Supply chain map has cycle (A → B → A)
**Handling:**
- Detect during tree building (visited set)
- Fail fast with clear error
- Suggest manual correction to supply chain data

### Stuck Production / Very Long Wait Times
**Scenario:** Good takes unusually long to appear after inputs delivered (20+ minutes)
**Handling:**
- **No automatic timeout** - worker continues polling indefinitely
- Log warning after 20 polls (~20 minutes at 60s intervals): "Production taking unusually long"
- Warnings are informational only - do not fail the operation
- User can manually stop container via CLI if desired
- Context cancellation (daemon shutdown) will exit gracefully
- Ship remains assigned to container until production completes or user intervenes

**Possible Causes:**
- Market has very WEAK activity (slow production, may take 30+ minutes)
- Scout ships not running in system (database not being updated)
- Insufficient inputs delivered (should not happen with our logic)
- Market configuration changed (imports/exports updated by game)
- Network/API issues preventing market data refresh
- Server-side issues (SpaceTraders game lag, maintenance)

**Resolution:**
- Wait longer - production will eventually complete if inputs were correct
- Check scout ship status - ensure scouts are touring the system
- Manual intervention via CLI: `spacetraders container stop --container-id worker-123`
- Check market via API manually to verify it imports the delivered goods
- If truly stuck (market broken), inputs are lost and worker can be stopped

### Coordinator Crash
**Scenario:** Daemon stops during production
**Handling:**
- Workers continue in background
- On restart, detect INTERRUPTED status
- Resume or cleanup based on metadata
- Release orphaned ships (stale assignments)

## Performance Considerations

### Parallel Worker Execution
- Launch workers in goroutines
- Max workers = available ships
- Avoid thundering herd (rate limiting)

### Market Query Optimization
- Database queries only (no API calls from production workers)
- Scout ships keep database fresh with continuous market polling
- Batch queries when possible
- Reuse results across workers
- **Dependency:** Scout ships must be running in target system for fresh market data

### Database Writes
- Update factory status asynchronously
- Batch ship assignment updates
- Use transactions for atomic operations

### Estimated Resource Usage
- Memory: ~1MB per active factory (dependency tree)
- Database: ~1KB per factory row
- **API calls from production workers: 0** (scouts handle all market polling)
- **API calls total:** Navigation, purchase, sell commands only (~10-30 per good)
- Ships: 1-10 ships per factory (parallelism)
- **Scout ships:** Must be running in target system for market data freshness

## Monitoring and Observability

### Logging
- Coordinator: Worker assignment, completion events
- Worker: Acquisition decisions, navigation, market interactions
- Resolver: Tree building, cycle detection
- Executor: Production steps, market polls

### Metrics
- Factories started/completed/failed
- Average production time by good
- Ships utilized (idle vs active)
- Market queries per factory
- Success rate by good type

### CLI Status Output
```
Factory: abc-123
Target: ADVANCED_CIRCUITRY
Status: ACTIVE
Progress: 60% (3/5 nodes complete)
Quantity Acquired: 0 (production in progress)

Dependency Tree:
├── ADVANCED_CIRCUITRY [FABRICATE, STRONG activity] ⏳ IN_PROGRESS (polling for production)
    ├── ELECTRONICS [FABRICATE, GROWING activity] ✅ COMPLETED (ship SHIP-1, acquired 15 units)
    │   ├── SILICON_CRYSTALS [BUY, ABUNDANT supply] ✅ COMPLETED (acquired 30 units)
    │   └── COPPER [FABRICATE, MODERATE activity] ✅ COMPLETED (acquired 20 units)
    │       └── COPPER_ORE [BUY, HIGH supply] ✅ COMPLETED (acquired 40 units)
    └── MICROPROCESSORS [FABRICATE, STRONG activity] ⏳ IN_PROGRESS (ship SHIP-2, polling attempt 3/10)
        ├── SILICON_CRYSTALS [BUY, ABUNDANT supply] ✅ COMPLETED (acquired 25 units)
        └── COPPER [FABRICATE, MODERATE activity] 🔄 DELIVERING_INPUTS
            └── COPPER_ORE [BUY, HIGH supply] ✅ COMPLETED (acquired 35 units)

Active Workers: 2
Ships: SHIP-1 (idle), SHIP-2 (active at X1-D5), SHIP-3 (active at X1-C2)

Note: Quantities acquired are market-driven and may vary from run to run
```

## Future Enhancements

### Phase 2 Features
- **Profitability Analysis**: Calculate net cost vs market price
- **Inventory Management**: Store produced goods for reuse
- **Batch Production**: Produce multiple units efficiently
- **Multi-System**: Coordinate across jump gates

### Phase 3 Features
- **Priority Queue**: Parallelize independent branches better
- **Load Balancing**: Distribute work based on ship cargo capacity
- **Market Maker**: Sell surplus goods for profit
- **Supply Chain Optimization**: Find optimal production routes

### Phase 4 Features
- **Contract Integration**: Produce goods for contract fulfillment
- **Trading Integration**: Buy inputs from trading profits
- **Mining Integration**: Mine raw materials instead of buying

## Risk Mitigation

### Technical Risks
| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Circular dependencies in supply chain | Low | High | Cycle detection in resolver, validation tests |
| Market data staleness | Medium | Medium | TTL refresh, fallback to API query |
| Worker deadlock | Low | High | Timeout on all blocking operations, watchdog |
| Ship assignment race condition | Medium | Medium | Database-level locking, optimistic concurrency |
| Coordinator crash | Medium | High | Persistent state, resumable workers |

### Operational Risks
| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| API rate limiting | High | Medium | Exponential backoff, request queuing |
| Insufficient ships | Medium | Low | Clear error messages, queue requests |
| Unprofitable production | Medium | Low | Log cost analysis, warn user |
| Wrong supply chain data | Low | High | Validation on load, manual verification |

## Success Criteria

### MVP (Minimum Viable Product)
- ✅ Acquire simple goods (1-2 levels deep) via market purchases or fabrication
- ✅ Buy raw materials from best available markets (STRONG activity, HIGH supply)
- ✅ Successfully poll database for market changes (kept fresh by scout tours)
- ✅ Detect when goods appear in exports via continuous polling (no timeout)
- ✅ Coordinate 2+ ships in parallel
- ✅ Handle basic errors gracefully (market unavailable, context cancellation)
- ✅ CLI interface for start/status/stop
- ✅ Variable quantities accepted (acquire whatever is available, not fixed amounts)
- ✅ Graceful shutdown via context cancellation

### Production Ready
- ✅ Acquire complex goods (5+ levels deep) with recursive dependency resolution
- ✅ Intelligent buy vs fabricate decisions based on market availability
- ✅ Market activity-aware selection (prefer STRONG > GROWING > WEAK)
- ✅ Robust market polling with simple intervals [30s, 60s, 60s, ...] (no timeout)
- ✅ Works with scout tour architecture (variable update intervals per market)
- ✅ Coordinate 10+ ships efficiently across multiple production branches
- ✅ Comprehensive error handling and recovery (context cancellation, market failures, ship loss)
- ✅ Full BDD test coverage (>90%) with market-driven scenarios
- ✅ Observability (logs showing poll attempts, market status, quantities acquired, warnings for long waits)
- ✅ Documentation explaining market-driven production model
- ✅ Successfully produce at least 10 different goods with variable quantities

### Stretch Goals
- ✅ Cost tracking and profitability analysis (even without fixed quantities)
- ✅ Multi-system production across jump gates
- ✅ Integration with contracts (produce goods for delivery requirements)
- ✅ Integration with trading (use trading profits to fund production)
- ✅ Web dashboard showing real-time market polling and production progress
- ✅ Market trend analysis (track how long production typically takes per good)
- ✅ Intelligent retry with alternative manufacturing waypoints on timeout

## References

### Existing Patterns to Follow
- **ContractFleetCoordinator**: `internal/application/contract/commands/run_fleet_coordinator.go`
- **ContractWorkflow**: `internal/application/contract/commands/run_contract_workflow.go`
- **DeliveryExecutor**: `internal/application/contract/services/delivery_executor.go`
- **Container Entity**: `internal/domain/container/container.go`
- **Ship Assignment**: `internal/domain/container/ship_assignment.go`

### Documentation
- **ARCHITECTURE.md**: Hexagonal architecture principles
- **CLAUDE.md**: Testing strategy, patterns, commands
- **mining-operation-spec.md**: Similar coordinator pattern example

### External Resources
- SpaceTraders API: https://spacetraders.stoplight.io/
- Supply Chain Data: Provided JSON `exportToImportMap`
- CQRS Pattern: Application layer command handlers

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
    quantity          int
    acquisitionMethod AcquisitionMethod
    children          []*SupplyChainNode
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
- `RequiredRawMaterials() map[string]int` - Leaf nodes aggregated

**`ProductionRecipe`** (Immutable Recipe Data)
```go
type ProductionRecipe struct {
    outputGood string
    inputs     map[string]int // good_symbol → quantity
}
```

**Methods:**
- `RequiredInputs() map[string]int`
- `HasInput(good string) bool`
- `TotalInputCount() int`

#### Domain Services

**`SupplyChainResolver`**
```go
type SupplyChainResolver struct {
    supplyChainMap map[string][]string // exportToImportMap
}
```

**Methods:**
- `BuildDependencyTree(targetGood string, quantity int) (*SupplyChainNode, error)`
  - Recursively traces dependencies
  - Detects cycles
  - Validates chain integrity
  - Returns root node of tree

**Algorithm:**
```
function BuildTree(good, quantity):
    node = new Node(good, quantity)

    if good is raw material (not in supplyChainMap):
        node.acquisitionMethod = BUY
        return node

    inputs = supplyChainMap[good]
    for each input in inputs:
        childNode = BuildTree(input, quantity) // recursive
        node.children.append(childNode)

    node.acquisitionMethod = FABRICATE
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
    PlayerID   int
    TargetGood string
    Quantity   int
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
2. **Build Dependency Tree** using `SupplyChainResolver`
3. **Determine Production Strategy**:
   - Flatten tree to list
   - For each node, check if good available in market
   - Mark as BUY or FABRICATE
4. **Discover Idle Ships** via `FindIdleLightHaulers()`
5. **Create Worker Containers** for parallel branches:
   - Assign production nodes to workers
   - Transfer ships to workers
   - Store worker metadata (node, ship, target)
6. **Start Workers** in goroutines
7. **Wait on Completion Channels**:
   - Unbuffered channel per worker
   - Block until worker signals completion
   - Ship auto-released back to idle pool
8. **Loop Until Complete**:
   - Check if target good produced
   - Discover newly idle ships
   - Assign to remaining work
9. **Cleanup**:
   - Release all ships
   - Mark factory COMPLETED
   - Log profit/cost metrics

**Error Handling:**
- Worker failure → retry with different ship
- No idle ships → wait with timeout
- Market unavailable → backoff and retry

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
   - If `BUY` → find market, navigate, purchase
   - If `FABRICATE` → recursively produce inputs
3. **For FABRICATE**:
   a. **Produce Inputs** (recursive):
      - For each child node
      - Execute production (may spawn sub-operations)
   b. **Find Manufacturing Waypoint**:
      - Query markets for waypoint that imports this good
      - Use `MarketLocator.FindImportMarket(good)`
   c. **Deliver Inputs**:
      - Navigate to manufacturing waypoint
      - Dock
      - Sell/deliver required inputs to market
   d. **Wait for Production**:
      - Poll market until output good appears
      - Use exponential backoff (5s, 10s, 20s, max 60s)
      - Timeout after 10 minutes
   e. **Purchase Output**:
      - Query market for output good
      - Purchase produced good
4. **Signal Completion** to coordinator via channel
5. **Return** (ship auto-released)

**State Management:**
- Store current step in container metadata
- Support interruption/resume
- Log progress for debugging

#### Services

**`services/supply_chain_resolver.go`**

```go
type SupplyChainResolver struct {
    supplyChainMap map[string][]string
}

func NewSupplyChainResolver(chainMap map[string][]string) *SupplyChainResolver

func (r *SupplyChainResolver) BuildDependencyTree(
    targetGood string,
    quantity int,
) (*SupplyChainNode, error)

func (r *SupplyChainResolver) ValidateChain(targetGood string) error

func (r *SupplyChainResolver) DetectCycles(targetGood string) error
```

**Implementation Details:**
- DFS traversal to build tree
- Cycle detection via visited set
- Validate all inputs exist in map
- Handle raw materials (leaves)

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
) error
```

**Orchestration:**
1. If `node.acquisitionMethod == BUY`:
   - Find market selling good
   - Navigate to market
   - Purchase good
2. If `node.acquisitionMethod == FABRICATE`:
   - Recursively produce inputs
   - Find manufacturing waypoint
   - Deliver inputs
   - Wait for production
   - Purchase output
3. Return with cargo hold containing produced good

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

func (l *MarketLocator) FindImportMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (string, error)

func (l *MarketLocator) FindExportMarket(
    ctx context.Context,
    good string,
    systemSymbol string,
    playerID int,
) (string, error)
```

**Logic:**
- Query all markets in system
- Filter by import/export lists
- Return first match (or cheapest if multiple)

**Market API Fields:**
- `imports: []string` - Goods this market wants (will produce if delivered)
- `exports: []string` - Goods this market sells (produces)

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
    ID             string    `gorm:"primaryKey"`
    PlayerID       int       `gorm:"index"`
    TargetGood     string
    Quantity       int
    DependencyTree string    // JSON-serialized SupplyChainNode
    Status         string    `gorm:"index"`
    Metadata       string    // JSON map
    CreatedAt      time.Time
    UpdatedAt      time.Time
    StartedAt      *time.Time
    CompletedAt    *time.Time
}
```

**Conversion:**
- Domain entity ↔ Database model
- JSON serialization for nested tree
- Type-safe metadata marshaling

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
    int32 quantity = 3;
}

message StartGoodsFactoryResponse {
    string factory_id = 1;
    string status = 2;
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
}
```

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
- `--quantity` (default: 1) - How many units
- `--factory-id` (for status/stop)

**Usage Examples:**
```bash
# Start production
./bin/spacetraders goods produce --target ADVANCED_CIRCUITRY --quantity 10

# Check status
./bin/spacetraders goods status --factory-id abc-123

# Stop factory
./bin/spacetraders goods stop --factory-id abc-123
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
GOODS_PRODUCTION_TIMEOUT=600  # Max wait time in seconds
GOODS_POLL_INTERVAL=10        # Market poll interval
```

## Testing Strategy

### BDD Tests (`test/bdd/`)

#### Domain Tests

**`features/domain/goods/supply_chain_resolver.feature`**

```gherkin
Feature: Supply Chain Resolver

  Scenario: Resolve simple dependency
    Given a supply chain map with "MACHINERY" requiring "IRON"
    When I build dependency tree for "MACHINERY"
    Then the tree should have root "MACHINERY"
    And the tree should have 1 child "IRON"
    And "IRON" should be marked as raw material

  Scenario: Resolve multi-level dependency
    Given a supply chain map with:
      | Output              | Inputs                      |
      | ADVANCED_CIRCUITRY  | ELECTRONICS, MICROPROCESSORS|
      | ELECTRONICS         | SILICON_CRYSTALS, COPPER    |
      | MICROPROCESSORS     | SILICON_CRYSTALS, COPPER    |
      | COPPER              | COPPER_ORE                  |
    When I build dependency tree for "ADVANCED_CIRCUITRY"
    Then the tree depth should be 3
    And the raw materials should include "SILICON_CRYSTALS"
    And the raw materials should include "COPPER_ORE"

  Scenario: Detect circular dependency
    Given a supply chain map with:
      | Output | Inputs |
      | A      | B      |
      | B      | A      |
    When I build dependency tree for "A"
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
    And "IRON_ORE" is available at waypoint X1-A2
    When the worker executes
    Then it should navigate to X1-A2
    And it should purchase "IRON_ORE"
    And it should signal completion

  Scenario: Fabricate good when not available
    Given a worker assigned to produce "MACHINERY"
    And "MACHINERY" is not sold in any market
    And "IRON" is available at waypoint X1-B3
    When the worker executes
    Then it should first acquire "IRON"
    And it should find the manufacturing waypoint for "MACHINERY"
    And it should deliver "IRON" to the manufacturing waypoint
    And it should wait for "MACHINERY" to be produced
    And it should purchase "MACHINERY"

  Scenario: Poll market for production completion
    Given a worker waiting for "ELECTRONICS" to be produced
    And inputs have been delivered to the manufacturing waypoint
    When the worker polls the market
    And "ELECTRONICS" is not yet available
    Then it should wait 10 seconds
    And poll again
    When "ELECTRONICS" becomes available
    Then it should purchase "ELECTRONICS"
    And signal completion
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

### 2. Market Discovery

**Decision:** Use `MarketRepository` queries

**Rationale:**
- Reuses existing infrastructure
- Database-cached market data
- Handles TTL refresh automatically
- Supports cheapest/best market queries

**Alternatives Considered:**
- Scan all markets at startup (high API cost)
- Use static waypoint mapping (outdated quickly)

### 3. Production Waiting

**Decision:** Poll market with exponential backoff

**Rationale:**
- Simple implementation
- Matches existing navigation wait pattern
- Configurable timeout/intervals
- Handles varying production times

**Algorithm:**
```
intervals = [5s, 10s, 20s, 40s, 60s, 60s, ...]
for each interval:
    sleep(interval)
    check market
    if good available:
        return success
    if timeout exceeded:
        return error
```

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
  "quantity": 1,
  "acquisition_method": "FABRICATE",
  "children": [
    {
      "good": "ELECTRONICS",
      "quantity": 1,
      "acquisition_method": "FABRICATE",
      "children": [...]
    }
  ]
}
```

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

### Production Timeout
**Scenario:** Good never appears after inputs delivered
**Handling:**
- Wait up to 10 minutes (configurable)
- Log warning at checkpoints
- Fail with timeout error
- Ship returns to idle (inputs lost)

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
- Cache market data (TTL-based)
- Batch queries when possible
- Reuse results across workers

### Database Writes
- Update factory status asynchronously
- Batch ship assignment updates
- Use transactions for atomic operations

### Estimated Resource Usage
- Memory: ~1MB per active factory (dependency tree)
- Database: ~1KB per factory row
- API calls: ~50-200 per complex good (depth 3-5)
- Ships: 1-10 ships per factory (parallelism)

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
Target: ADVANCED_CIRCUITRY (qty: 1)
Status: ACTIVE
Progress: 60% (3/5 nodes complete)

Dependency Tree:
├── ADVANCED_CIRCUITRY [FABRICATE] ⏳ IN_PROGRESS
    ├── ELECTRONICS [FABRICATE] ✅ COMPLETED (ship SHIP-1)
    │   ├── SILICON_CRYSTALS [BUY] ✅ COMPLETED
    │   └── COPPER [FABRICATE] ✅ COMPLETED
    │       └── COPPER_ORE [BUY] ✅ COMPLETED
    └── MICROPROCESSORS [FABRICATE] ⏳ IN_PROGRESS (ship SHIP-2)
        ├── SILICON_CRYSTALS [BUY] ✅ COMPLETED
        └── COPPER [FABRICATE] 🔄 WAITING
            └── COPPER_ORE [BUY] ✅ COMPLETED

Active Workers: 2
Ships: SHIP-1 (idle), SHIP-2 (active), SHIP-3 (active)
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
- ✅ Produce simple goods (1-2 levels deep)
- ✅ Buy raw materials from markets
- ✅ Coordinate 2+ ships in parallel
- ✅ Handle basic errors gracefully
- ✅ CLI interface for start/status/stop

### Production Ready
- ✅ Produce complex goods (5+ levels deep)
- ✅ Intelligent buy vs fabricate decisions
- ✅ Coordinate 10+ ships efficiently
- ✅ Comprehensive error handling and recovery
- ✅ Full BDD test coverage (>90%)
- ✅ Observability (logs, metrics, status)
- ✅ Documentation and examples

### Stretch Goals
- ✅ Profitability optimization
- ✅ Multi-system production
- ✅ Integration with contracts/trading
- ✅ Web dashboard for monitoring

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

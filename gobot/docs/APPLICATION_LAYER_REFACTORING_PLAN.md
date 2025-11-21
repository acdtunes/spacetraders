# Application Layer Refactoring Plan

**Analysis Date**: 2025-11-21
**Scope**: Complete analysis of `internal/application/` layer
**Total Lines Analyzed**: 10,468 lines across 49 files
**Handlers Analyzed**: 35+ command and query handlers

## Executive Summary

This document presents a comprehensive analysis of the application layer, identifying critical refactoring opportunities related to code duplication, SOLID principle violations, cohesion, and coupling issues. The analysis reveals several high-priority issues that significantly impact maintainability, testability, and extensibility.

### Key Metrics

- **Total Files**: 49 Go files
- **Total Lines**: 10,468 lines of code
- **Largest Handler**: RunCoordinatorHandler (1,198 lines)
- **Most Critical Issue**: 90% code duplication between cargo handlers
- **Architecture Violations**: Application layer depends on infrastructure layer

### Critical Issues Summary

| Priority | Issue | Impact | Files Affected |
|----------|-------|--------|----------------|
| P1 | Purchase/Sell cargo duplication (90%) | High | 2 |
| P1 | Infrastructure coupling (DIP violation) | High | 10+ |
| P1 | God object: RunWorkflowHandler (798 lines, 8+ responsibilities) | High | 1 |
| P1 | God object: RunCoordinatorHandler (1,198 lines, 10+ responsibilities) | Very High | 1 |
| P2 | Player ID resolution duplication | Medium | 3+ |
| P2 | RouteExecutor mixed concerns (661 lines) | Medium | 1 |
| P2 | Open/Closed violations | Medium | Multiple |
| P3 | Common package low cohesion | Low | Multiple |

---

## 1. Code Duplication Issues

### 1.1 CRITICAL: Purchase/Sell Cargo Handler Duplication (90% overlap)

**Files Affected**:
- `internal/application/ship/commands/purchase_cargo.go` (158 lines)
- `internal/application/ship/commands/sell_cargo.go` (158 lines)

**Problem**: These two handlers are nearly identical with only the API call method differing.

**Duplicated Patterns**:

1. **Token Retrieval** (Lines 100-102 in both):
```go
func (h *PurchaseCargoHandler) getPlayerToken(ctx context.Context) (string, error) {
    return common.PlayerTokenFromContext(ctx)
}
```

2. **Ship Loading** (Lines 104-110 in both):
```go
func (h *PurchaseCargoHandler) loadShip(ctx context.Context, cmd *PurchaseCargoCommand) (*navigation.Ship, error) {
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    if err != nil {
        return nil, fmt.Errorf("ship not found: %w", err)
    }
    return ship, nil
}
```

3. **Docked Validation** (Lines 112-117 in both):
```go
func (h *PurchaseCargoHandler) validateShipDockedForPurchase(ship *navigation.Ship) error {
    if !ship.IsDocked() {
        return fmt.Errorf("ship must be docked to purchase cargo")
    }
    return nil
}
```

4. **Transaction Loop** (Lines 132-157 in both):
```go
// Only difference: Purchase vs Sell API call
for unitsRemaining > 0 {
    unitsToBuy := utils.Min(unitsRemaining, transactionLimit)
    result, err := h.apiClient.PurchaseCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToBuy, token)
    // ... accumulation logic (identical)
}
```

**Impact**:
- Bug fixes must be applied to both files
- Feature enhancements require dual implementation
- Violates DRY (Don't Repeat Yourself) principle
- Maintenance overhead increases with each change

**Refactoring Solution**:

Create a unified `CargoTransactionHandler` using the Strategy pattern:

```go
// application/ship/strategies/cargo_transaction.go
type CargoTransactionStrategy interface {
    Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (result, error)
    ValidatePreconditions(ship *navigation.Ship, units int) error
    AccumulateResult(result interface{}, accumulator *TransactionAccumulator)
}

type PurchaseStrategy struct { /* ... */ }
type SellStrategy struct { /* ... */ }

// application/ship/commands/cargo_transaction.go
type CargoTransactionHandler struct {
    strategy CargoTransactionStrategy
    shipRepo navigation.ShipRepository
    // ... other dependencies
}

func (h *CargoTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    // Unified transaction logic
    // Strategy pattern handles operation-specific behavior
}
```

**Estimated Effort**: Medium (2-3 days with testing)

---

### 1.2 MODERATE: Player ID Resolution Duplication

**Files Affected**:
- `internal/application/ship/queries/get_ship.go` (Lines 65-83)
- `internal/application/player/queries/get_player.go`
- `internal/application/ship/queries/list_ships.go`
- Multiple command handlers

**Problem**: Player ID resolution logic (by ID or agent symbol) is duplicated across multiple query handlers.

**Example from `get_ship.go`**:
```go
func (h *GetShipHandler) resolvePlayerID(ctx context.Context, playerID *int, agentSymbol string) (shared.PlayerID, error) {
    if playerID == nil && agentSymbol == "" {
        return shared.PlayerID{}, fmt.Errorf("either player_id or agent_symbol must be provided")
    }

    if playerID != nil {
        pid, err := shared.NewPlayerID(*playerID)
        if err != nil {
            return shared.PlayerID{}, fmt.Errorf("invalid player ID: %w", err)
        }
        return pid, nil
    }

    player, err := h.playerRepo.FindByAgentSymbol(ctx, agentSymbol)
    if err != nil {
        return shared.PlayerID{}, fmt.Errorf("failed to find player by agent symbol: %w", err)
    }

    return player.ID, nil
}
```

**Refactoring Solution**:

Extract to shared utility:

```go
// application/common/player_resolution.go
type PlayerResolver struct {
    playerRepo player.PlayerRepository
}

func NewPlayerResolver(playerRepo player.PlayerRepository) *PlayerResolver {
    return &PlayerResolver{playerRepo: playerRepo}
}

func (r *PlayerResolver) ResolvePlayerID(ctx context.Context, playerID *int, agentSymbol string) (shared.PlayerID, error) {
    // Unified resolution logic
}
```

**Estimated Effort**: Low (1 day)

---

### 1.3 MODERATE: Ship Reload After Operations

**Problem**: After mutating operations (navigate, refuel, transfer), many handlers reload the ship from repository using nearly identical code.

**Occurrences**:
- `internal/application/mining/commands/run_worker.go` (Lines 179-182)
- `internal/application/contract/commands/run_contract_workflow.go` (Lines 543-546)
- `internal/application/ship/route_executor.go` (Multiple locations)
- 10+ total locations

**Example Pattern**:
```go
ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
if err != nil {
    return fmt.Errorf("failed to reload ship: %w", err)
}
```

**Refactoring Solution**:

Option 1: Extract helper method in each handler base
Option 2: Return updated ship state from repository operations
Option 3: Create `ShipStateManager` service

**Estimated Effort**: Low-Medium (1-2 days)

---

### 1.4 MODERATE: Navigate + Dock Pattern

**Problem**: The pattern of "navigate to waypoint, then dock" is reimplemented in multiple handlers.

**Occurrences**:
- `internal/application/contract/commands/run_contract_workflow.go` (Lines 759-776)
- `internal/application/mining/commands/run_coordinator.go`
- `internal/application/mining/commands/run_transport_worker.go`
- `internal/application/scouting/commands/scout_tour.go`

**Example from `run_contract_workflow.go`**:
```go
func (h *RunWorkflowHandler) navigateAndDock(ctx context.Context, shipSymbol string, destination string, playerID shared.PlayerID) (*navigation.Ship, error) {
    ship, err := h.navigateToWaypoint(ctx, shipSymbol, destination, playerID)
    if err != nil {
        return nil, err
    }

    if err := h.dockShip(ctx, ship, playerID); err != nil {
        return nil, err
    }

    return ship, nil
}
```

**Refactoring Solution**:

Extract to `RouteExecutor` or create new `NavigationService`:

```go
// application/ship/services/navigation_service.go
type NavigationService struct {
    mediator common.Mediator
}

func (s *NavigationService) NavigateAndDock(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) (*navigation.Ship, error) {
    // Unified navigate + dock logic
}
```

**Estimated Effort**: Low (1 day)

---

## 2. Single Responsibility Principle (SRP) Violations

### 2.1 SEVERE: RunWorkflowHandler God Object (798 lines)

**File**: `internal/application/contract/commands/run_contract_workflow.go`

**Responsibilities** (should be 1, actually has 8+):

1. Contract negotiation orchestration (Lines 84-138)
2. Contract profitability evaluation
3. Contract acceptance
4. Cargo jettison logic (Lines 709-734)
5. Purchase trip planning and execution (Lines 501-549)
6. Multi-trip cargo transport logic (Lines 306-356)
7. Delivery execution
8. Contract fulfillment
9. Ship assignment management
10. Channel-based concurrency coordination

**Key Code Sections**:

**Main Handler** (Lines 84-138):
```go
func (h *RunWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*RunWorkflowCommand)

    // 1. Negotiate contract
    negotiateResp, err := h.mediator.Send(ctx, negotiateCmd)
    // ...

    // 2. Evaluate profitability
    evaluateCmd := &contractQuery.EvaluateContractProfitabilityCommand{...}
    // ...

    // 3. Accept contract
    acceptResp, err := h.mediator.Send(ctx, acceptCmd)
    // ...

    // 4. Jettison wrong cargo
    if err := h.jettisonWrongCargo(ctx, ship, contract, cmd.PlayerID); err != nil {
        // ...
    }

    // 5. Execute purchase trips
    // ...

    // 6. Execute deliveries
    // ...

    // 7. Fulfill contract
    fulfillResp, err := h.mediator.Send(ctx, fulfillCmd)
    // ...

    // 54 lines of orchestration mixing multiple concerns
}
```

**Purchase Trip Execution** (Lines 501-549):
```go
func (h *RunWorkflowHandler) executeSinglePurchaseTrip(...) (*navigation.Ship, int, bool, error) {
    // Calculate cargo space
    currentCargo := ship.Cargo.Units
    availableSpace := ship.Cargo.Capacity - currentCargo

    // Navigate to market
    ship, err = h.navigateToWaypoint(ctx, cmd.ShipSymbol, purchaseMarket.Symbol, cmd.PlayerID)

    // Purchase cargo with transaction splitting
    purchaseResult, err := h.mediator.Send(ctx, purchaseCmd)

    // Reload ship state
    ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)

    // Return updated state
    return ship, unitsPurchased, deliveryLocationReached, nil
}
```

**Jettison Cargo Logic** (Lines 709-734):
```go
func (h *RunWorkflowHandler) jettisonWrongCargo(ctx context.Context, ship *navigation.Ship, contract *domainContract.Contract, playerID shared.PlayerID) error {
    wrongItems := make([]shared.CargoItem, 0)

    for _, item := range ship.Cargo.Inventory {
        if item.Symbol != contract.Terms.Deliver[0].TradeSymbol {
            wrongItems = append(wrongItems, item)
        }
    }

    for _, item := range wrongItems {
        jettisonCmd := &shipCommands.JettisonCargoCommand{
            ShipSymbol: ship.Symbol,
            GoodSymbol: item.Symbol,
            Units:      item.Units,
            PlayerID:   playerID,
        }

        if _, err := h.mediator.Send(ctx, jettisonCmd); err != nil {
            logger.Log("ERROR", "Failed to jettison cargo", ...)
        }
    }

    return nil
}
```

**Impact**:
- Extremely difficult to test (requires mocking 8+ dependencies)
- High cyclomatic complexity
- Impossible to modify without regression risk
- Violates "reason to change" - ANY workflow step change requires modifying this class
- Cannot reuse individual workflow steps

**Refactoring Solution**:

Extract specialized services:

```go
// application/contract/services/contract_negotiation_service.go
type ContractNegotiationService struct {
    mediator common.Mediator
}

func (s *ContractNegotiationService) NegotiateAndEvaluate(ctx context.Context, factionSymbol string, playerID shared.PlayerID) (*Contract, bool, error) {
    // Negotiate + evaluate logic
}

// application/contract/services/contract_purchase_service.go
type ContractPurchaseService struct {
    mediator     common.Mediator
    shipRepo     navigation.ShipRepository
    marketFinder *MarketFinder
}

func (s *ContractPurchaseService) ExecutePurchasePhase(ctx context.Context, ship *navigation.Ship, contract *Contract, playerID shared.PlayerID) error {
    // All purchase trip logic
}

// application/contract/services/contract_delivery_service.go
type ContractDeliveryService struct {
    mediator common.Mediator
}

func (s *ContractDeliveryService) ExecuteDeliveryPhase(ctx context.Context, ship *navigation.Ship, contract *Contract, playerID shared.PlayerID) error {
    // All delivery logic
}

// application/contract/services/cargo_jettison_service.go
type CargoJettisonService struct {
    mediator common.Mediator
}

func (s *CargoJettisonService) JettisonIncorrectCargo(ctx context.Context, ship *navigation.Ship, requiredGood string, playerID shared.PlayerID) error {
    // Jettison logic
}

// Refactored handler
type RunWorkflowHandler struct {
    negotiationService *ContractNegotiationService
    purchaseService    *ContractPurchaseService
    deliveryService    *ContractDeliveryService
    jettisonService    *CargoJettisonService
    mediator           common.Mediator
}

func (h *RunWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*RunWorkflowCommand)

    // Orchestrate services (thin orchestration layer)
    contract, isProfitable, err := h.negotiationService.NegotiateAndEvaluate(...)
    if !isProfitable {
        return &RunWorkflowResponse{Success: false, Reason: "unprofitable"}, nil
    }

    if err := h.jettisonService.JettisonIncorrectCargo(...); err != nil {
        return nil, err
    }

    if err := h.purchaseService.ExecutePurchasePhase(...); err != nil {
        return nil, err
    }

    if err := h.deliveryService.ExecuteDeliveryPhase(...); err != nil {
        return nil, err
    }

    return &RunWorkflowResponse{Success: true}, nil
}
```

**Estimated Effort**: High (1-2 weeks with comprehensive testing)

---

### 2.2 SEVERE: RunCoordinatorHandler God Object (1,198 lines)

**File**: `internal/application/mining/commands/run_coordinator.go`

**Responsibilities** (should be 1, actually has 10+):

1. Fleet health checking
2. Miner ship management
3. Transport ship management
4. Transport request queue processing
5. Channel-based concurrency coordination
6. Ship-to-transport assignment
7. Market selling orchestration
8. Cargo consolidation logic
9. Transport routing decisions
10. Graceful shutdown handling
11. Error recovery and logging

**Impact**:
- **Largest single handler in codebase** (1,198 lines)
- Nearly impossible to test comprehensively
- Extremely high cyclomatic complexity
- Complex goroutine coordination across 5+ channels
- Any change carries massive regression risk
- Violates Open/Closed Principle (adding new coordinator behavior requires modification)

**Refactoring Solution**:

Extract specialized services:

```go
// application/mining/services/mining_fleet_manager.go
type MiningFleetManager struct {
    shipRepo navigation.ShipRepository
}

func (m *MiningFleetManager) LoadAndValidateFleet(ctx context.Context, minerSymbols, transportSymbols []string, playerID shared.PlayerID) (*Fleet, error) {
    // Fleet loading and health checking
}

// application/mining/services/transport_coordinator.go
type TransportCoordinator struct {
    mediator common.Mediator
}

func (t *TransportCoordinator) ProcessTransportRequest(ctx context.Context, minerSymbol string, availableTransports []*navigation.Ship) (*navigation.Ship, error) {
    // Transport assignment logic
}

func (t *TransportCoordinator) ExecuteTransportOperation(ctx context.Context, transport, miner *navigation.Ship) error {
    // Navigate, transfer, sell cargo
}

// application/mining/services/miner_health_checker.go
type MinerHealthChecker struct {
    shipRepo navigation.ShipRepository
}

func (m *MinerHealthChecker) CheckAndRecoverMiners(ctx context.Context, miners []*navigation.Ship) error {
    // Health monitoring and recovery
}

// application/mining/services/transport_request_handler.go
type TransportRequestHandler struct {
    coordinator *TransportCoordinator
}

func (h *TransportRequestHandler) StartRequestProcessing(ctx context.Context, requestChan <-chan string, assignChan chan<- string, transports []*navigation.Ship) {
    // Channel-based request processing
}

// Refactored coordinator
type RunCoordinatorHandler struct {
    fleetManager     *MiningFleetManager
    transportCoord   *TransportCoordinator
    healthChecker    *MinerHealthChecker
    requestHandler   *TransportRequestHandler
}

func (h *RunCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    // Thin orchestration layer
    fleet, err := h.fleetManager.LoadAndValidateFleet(...)

    // Start goroutines for each service
    go h.healthChecker.MonitorMiners(ctx, fleet.Miners)
    go h.requestHandler.StartRequestProcessing(ctx, ...)

    // Coordinate lifecycle
    <-ctx.Done()
    h.gracefulShutdown()

    return &RunCoordinatorResponse{}, nil
}
```

**Estimated Effort**: Very High (2-3 weeks with extensive testing)

---

### 2.3 MODERATE: NavigateRouteHandler (327 lines)

**File**: `internal/application/ship/commands/navigate_route.go`

**Responsibilities** (should be 1, actually has 5+):

1. Ship state validation and preparation
2. Waypoint graph loading and enrichment
3. Route planning via routing client
4. Route execution orchestration
5. Error handling and recovery (including in-transit waiting)

**Positive Note**: Handler has been partially refactored and already uses extracted services:
- `WaypointEnricher` (Lines 203-210)
- `RoutePlanner` (Line 220)
- `RouteExecutor` (Line 235)

**Remaining Issues**:

Lines 79-129 still contain orchestration logic mixing concerns:
```go
func (h *NavigateRouteHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*NavigateRouteCommand)

    // Validation
    ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
    if err != nil {
        return nil, fmt.Errorf("ship not found: %w", err)
    }

    // Wait for in-transit ships
    if ship.IsInTransit() {
        // ... complex waiting logic (Lines 95-120)
    }

    // Prepare ship (ensure in orbit)
    changed, err := ship.EnsureInOrbit()

    // Load graph
    graph, err := h.loadWaypointGraph(ctx, cmd.PlayerID)

    // Enrich waypoints
    enrichedGraph := h.waypointEnricher.EnrichWaypoints(graph)

    // Plan route
    route, err := h.routePlanner.PlanRoute(ctx, ship.CurrentLocation(), destinationWaypoint, enrichedGraph, ship.Fuel.Capacity, cmd.FlightMode)

    // Execute route
    finalShip, err := h.routeExecutor.ExecuteRoute(ctx, route, ship, cmd.PlayerID)

    return &NavigateRouteResponse{Ship: finalShip}, nil
}
```

**Refactoring Recommendation**:

Extract remaining concerns:
1. Ship state preparation → `ShipPreparationService`
2. In-transit waiting logic → `TransitWaitService`
3. Graph loading → Inject prepared graph instead of loading in handler

**Estimated Effort**: Low-Medium (3-5 days)

---

### 2.4 MODERATE: RouteExecutor Mixed Concerns (661 lines)

**File**: `internal/application/ship/route_executor.go`

**Problem**: This "service" class mixes multiple unrelated concerns.

**Concerns Identified**:

1. **Route execution orchestration** (Lines 99-192)
2. **Navigation state waiting** (Lines 395-485)
3. **Refuel decision logic** (Lines 239-253, 347-373)
4. **Flight mode selection** (Lines 255-274)
5. **Market scanning** (Lines 375-393)
6. **Time management** (sleep/wait logic throughout)
7. **Ship state synchronization** (multiple ship reload calls)

**Evidence - Market Scanning** (Lines 375-393):
```go
func (e *RouteExecutor) scanMarketIfPresent(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) {
    if e.marketScanner != nil && e.isMarketplace(segment.ToWaypoint) {
        logger := common.LoggerFromContext(ctx)
        logger.Log("INFO", "Marketplace detected - scanning market data", ...)

        if err := e.marketScanner.ScanAndSaveMarket(ctx, uint(playerID.Value()), segment.ToWaypoint.Symbol); err != nil {
            logger.Log("ERROR", "Market scan failed", ...)
        }
    }
}
```

**Evidence - Marketplace Detection** (Lines 652-660):
```go
func (e *RouteExecutor) isMarketplace(waypoint *shared.Waypoint) bool {
    for _, trait := range waypoint.Traits {
        if trait == "MARKETPLACE" {
            return true
        }
    }
    return false
}
```

This logic belongs in the domain layer (Waypoint value object).

**Refactoring Solution**:

```go
// application/ship/services/market_scanning_service.go
type MarketScanningService struct {
    marketScanner *MarketScanner
}

func (s *MarketScanningService) ScanIfMarketplace(ctx context.Context, waypoint *shared.Waypoint, playerID shared.PlayerID) error {
    if waypoint.HasTrait("MARKETPLACE") {
        return s.marketScanner.ScanAndSaveMarket(ctx, uint(playerID.Value()), waypoint.Symbol)
    }
    return nil
}

// application/ship/services/refuel_strategy_service.go
type RefuelStrategyService struct {
    strategy RefuelStrategy
}

func (s *RefuelStrategyService) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *RouteSegment) bool {
    return s.strategy.EvaluatePreDeparture(ship, segment)
}

// domain/shared/waypoint.go (add method to value object)
func (w *Waypoint) HasTrait(trait string) bool {
    for _, t := range w.Traits {
        if t == trait {
            return true
        }
    }
    return false
}

// Refactored RouteExecutor
type RouteExecutor struct {
    mediator          common.Mediator
    marketScanning    *MarketScanningService
    refuelStrategy    *RefuelStrategyService
    // ... reduced dependencies
}
```

**Estimated Effort**: Medium (1 week)

---

## 3. Open/Closed Principle (OCP) Violations

### 3.1 CRITICAL: Cargo Transaction Handlers

**Files**:
- `internal/application/ship/commands/purchase_cargo.go`
- `internal/application/ship/commands/sell_cargo.go`

**Problem**: Adding new transaction types (e.g., "trade", "donate", "transfer between players") requires creating a new handler with significant code duplication. The current design is NOT open for extension.

**Evidence**:

Both handlers have hardcoded operation logic:
```go
// purchase_cargo.go
func (h *PurchaseCargoHandler) executePurchaseTransactions(...) {
    for unitsRemaining > 0 {
        unitsToBuy := utils.Min(unitsRemaining, transactionLimit)
        result, err := h.apiClient.PurchaseCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToBuy, token)
        // ... accumulation logic
    }
}

// sell_cargo.go
func (h *SellCargoHandler) executeSellTransactions(...) {
    for unitsRemaining > 0 {
        unitsToSell := utils.Min(unitsRemaining, transactionLimit)
        result, err := h.apiClient.SellCargo(ctx, cmd.ShipSymbol, cmd.GoodSymbol, unitsToSell, token)
        // ... accumulation logic (identical)
    }
}
```

**Better Design**: Strategy Pattern

```go
// application/ship/strategies/cargo_transaction_strategy.go
type CargoTransactionStrategy interface {
    Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (TransactionResult, error)
    ValidatePreconditions(ship *navigation.Ship, units int) error
    GetTransactionType() string
}

type PurchaseStrategy struct {
    apiClient ports.APIClient
}

func (s *PurchaseStrategy) Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (TransactionResult, error) {
    return s.apiClient.PurchaseCargo(ctx, shipSymbol, goodSymbol, units, token)
}

func (s *PurchaseStrategy) ValidatePreconditions(ship *navigation.Ship, units int) error {
    availableSpace := ship.AvailableCargoSpace()
    if availableSpace < units {
        return fmt.Errorf("insufficient cargo space: need %d, have %d", units, availableSpace)
    }
    return nil
}

type SellStrategy struct {
    apiClient ports.APIClient
}

func (s *SellStrategy) Execute(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (TransactionResult, error) {
    return s.apiClient.SellCargo(ctx, shipSymbol, goodSymbol, units, token)
}

func (s *SellStrategy) ValidatePreconditions(ship *navigation.Ship, units int) error {
    // Check if ship has the cargo
    for _, item := range ship.Cargo.Inventory {
        if item.Symbol == goodSymbol && item.Units >= units {
            return nil
        }
    }
    return fmt.Errorf("insufficient cargo: need %d units of %s", units, goodSymbol)
}

// Unified handler
type CargoTransactionHandler struct {
    strategy       CargoTransactionStrategy
    shipRepo       navigation.ShipRepository
    playerRepo     player.PlayerRepository
    marketRepo     scoutingQuery.MarketRepository
}

func (h *CargoTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    // Unified transaction logic
    // Strategy handles operation-specific behavior
}
```

**Benefits**:
- Adding new transaction types requires only implementing the interface
- No modification of existing code
- Better testability (mock strategies)
- Cleaner separation of concerns

**Estimated Effort**: Medium (3-5 days with testing)

---

### 3.2 CRITICAL: Route Executor Refuel Logic

**File**: `internal/application/ship/route_executor.go`

**Problem**: Refuel decision logic is hardcoded and cannot be extended without modifying the class.

**Evidence**:

**Pre-departure refuel** (Lines 239-252):
```go
func (e *RouteExecutor) handlePreDepartureRefuel(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
    logger := common.LoggerFromContext(ctx)

    if ship.ShouldPreventDriftMode(segment, 0.9) {  // HARDCODED 90% threshold
        logger.Log("INFO", "Ship refueling to prevent DRIFT mode", ...)
        if err := e.refuelShip(ctx, ship, playerID); err != nil {
            return err
        }
    }

    return nil
}
```

**Post-arrival refuel** (Lines 347-372):
```go
func (e *RouteExecutor) handlePostArrivalRefueling(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
    // HARDCODED: Refuel if below 90% and waypoint has fuel
    if ship.ShouldRefuelOpportunistically(segment.ToWaypoint, 0.9) && !segment.RequiresRefuel {
        logger.Log("INFO", "Opportunistic refueling at fuel station", ...)
        if err := e.refuelShip(ctx, ship, playerID); err != nil {
            logger.Log("WARN", "Opportunistic refuel failed", ...)
        }
    }

    // HARDCODED: Required refuel flag
    if segment.RequiresRefuel {
        logger.Log("INFO", "Required refueling at designated fuel station", ...)
        if err := e.refuelShip(ctx, ship, playerID); err != nil {
            return fmt.Errorf("required refuel failed: %w", err)
        }
    }

    return nil
}
```

**Impact**:
- Cannot add new refuel strategies without modifying RouteExecutor
- Examples of strategies that can't be added:
  - "Refuel only at cheapest stations"
  - "Adaptive threshold based on system size"
  - "Minimize refuel stops for speed"
  - "Aggressive refueling for exploration"

**Better Design**: Strategy Pattern

```go
// application/ship/strategies/refuel_strategy.go
type RefuelStrategy interface {
    ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *RouteSegment) bool
    ShouldRefuelAfterArrival(ship *navigation.Ship, segment *RouteSegment) bool
    GetStrategyName() string
}

// Conservative strategy (current default behavior)
type ConservativeRefuelStrategy struct {
    threshold float64 // e.g., 0.9
}

func (s *ConservativeRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *RouteSegment) bool {
    return ship.ShouldPreventDriftMode(segment, s.threshold)
}

func (s *ConservativeRefuelStrategy) ShouldRefuelAfterArrival(ship *navigation.Ship, segment *RouteSegment) bool {
    return ship.ShouldRefuelOpportunistically(segment.ToWaypoint, s.threshold) && !segment.RequiresRefuel
}

// Cost-optimized strategy
type CostOptimizedRefuelStrategy struct {
    marketRepo scoutingQuery.MarketRepository
}

func (s *CostOptimizedRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *RouteSegment) bool {
    // Only refuel if this is one of the cheapest stations in the area
    fuelPrice := s.getFuelPrice(segment.FromWaypoint)
    avgPrice := s.getAreaAverageFuelPrice(segment.FromWaypoint.System)
    return fuelPrice <= avgPrice*0.8 // Only refuel at 20% discount
}

// Speed-optimized strategy
type SpeedOptimizedRefuelStrategy struct{}

func (s *SpeedOptimizedRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *RouteSegment) bool {
    // Minimize stops, only refuel when absolutely necessary
    return ship.Fuel.Current < segment.FuelRequired*1.1 // 10% buffer only
}

// Refactored RouteExecutor
type RouteExecutor struct {
    refuelStrategy RefuelStrategy
    // ...
}

func (e *RouteExecutor) handlePreDepartureRefuel(ctx context.Context, segment *domainNavigation.RouteSegment, ship *domainNavigation.Ship, playerID shared.PlayerID) error {
    if e.refuelStrategy.ShouldRefuelBeforeDeparture(ship, segment) {
        return e.refuelShip(ctx, ship, playerID)
    }
    return nil
}
```

**Benefits**:
- New strategies can be added without modifying RouteExecutor
- Strategies can be swapped at runtime or configuration
- Better testability (mock strategies)
- Clear separation of routing logic from refuel decision logic

**Estimated Effort**: Medium (1 week with testing)

---

### 3.3 MODERATE: Command Handler Registration

**Problem**: Adding new command handlers requires manual registration code in multiple places. Forgetting to register a handler results in runtime errors, not compile-time errors.

**Current Pattern** (inferred from mediator usage):
```go
// Somewhere in application setup/bootstrap
mediator.Register(reflect.TypeOf(NavigateRouteCommand{}), navigateRouteHandler)
mediator.Register(reflect.TypeOf(DockShipCommand{}), dockShipHandler)
mediator.Register(reflect.TypeOf(RefuelShipCommand{}), refuelShipHandler)
// ... 35+ manual registrations
```

**Impact**:
- Easy to forget registration (runtime panic)
- No compile-time type checking
- Difficult to maintain as handlers grow

**Better Design Options**:

**Option 1**: Auto-registration via reflection
```go
// Each handler registers itself
func init() {
    RegisterHandler(NavigateRouteCommand{}, &NavigateRouteHandler{})
}
```

**Option 2**: Use dependency injection container (e.g., Uber Dig, Google Wire)
```go
// wire.go (generated)
func InitializeMediator() *Mediator {
    // Auto-wired based on type signatures
}
```

**Option 3**: Compile-time registration verification
```go
// Use Go 1.18+ generics for type-safe registration
type Handler[TReq, TResp any] interface {
    Handle(ctx context.Context, req TReq) (TResp, error)
}

mediator.Register[NavigateRouteCommand, NavigateRouteResponse](handler)
```

**Estimated Effort**: Medium-High (depends on chosen approach)

---

## 4. Dependency Inversion Principle (DIP) Violations

### 4.1 CRITICAL: Direct Dependency on Infrastructure Ports

**Problem**: Application layer directly depends on infrastructure layer types (`infraPorts.APIClient`), violating hexagonal architecture.

**Files Affected** (10+ handlers):
1. `internal/application/ship/commands/purchase_cargo.go:51-53`
2. `internal/application/ship/commands/sell_cargo.go:48-53`
3. `internal/application/player/queries/get_player.go:25-28`
4. `internal/application/ship/market_scanner.go:16-21`
5. `internal/application/contract/commands/negotiate_contract.go`
6. `internal/application/mining/commands/extract_resources.go`
7. Multiple other handlers

**Example from `purchase_cargo.go`**:
```go
import (
    infraPorts "github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

type PurchaseCargoHandler struct {
    shipRepo   navigation.ShipRepository
    playerRepo player.PlayerRepository
    apiClient  infraPorts.APIClient  // VIOLATION: infrastructure dependency
    marketRepo scoutingQuery.MarketRepository
}
```

**Architecture Violation**:
```
Current (WRONG):
┌─────────────────────────┐
│  Application Layer      │
│  (commands/queries)     │
└───────────┬─────────────┘
            │ depends on
            ↓
┌─────────────────────────┐
│  Infrastructure Layer   │
│  (infraPorts.APIClient) │
└─────────────────────────┘
            ↓
    Domain Layer

Correct (Hexagonal Architecture):
┌─────────────────────────┐
│  Application Layer      │
│  (commands/queries)     │
└───────────┬─────────────┘
            │ depends on
            ↓
┌─────────────────────────┐
│  Domain Ports           │
│  (interfaces)           │
└───────────┬─────────────┘
            ↑
            │ implements
┌─────────────────────────┐
│  Infrastructure Layer   │
│  (adapters)             │
└─────────────────────────┘
```

**Impact**:
- Violates hexagonal architecture principles
- Application layer couples to infrastructure implementation details
- Cannot test handlers without infrastructure package
- Cannot swap API implementation without changing application code
- Dependency arrows point in wrong direction

**Refactoring Solution**:

**Step 1**: Define API port in domain layer
```go
// domain/ports/api_client.go
package ports

import "context"

type APIClient interface {
    // Ship operations
    PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*PurchaseCargoResult, error)
    SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*SellCargoResult, error)
    NavigateShip(ctx context.Context, shipSymbol, waypointSymbol string, token string) (*NavigationResult, error)
    DockShip(ctx context.Context, shipSymbol string, token string) (*DockResult, error)
    OrbitShip(ctx context.Context, shipSymbol string, token string) (*OrbitResult, error)
    RefuelShip(ctx context.Context, shipSymbol string, token string, units *int) (*RefuelResult, error)

    // Agent operations
    GetAgent(ctx context.Context, token string) (*Agent, error)

    // Contract operations
    GetContracts(ctx context.Context, token string, page int) (*ContractsResponse, error)
    AcceptContract(ctx context.Context, contractID string, token string) (*AcceptContractResult, error)
    DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*DeliverContractResult, error)
    FulfillContract(ctx context.Context, contractID string, token string) (*FulfillContractResult, error)

    // Market operations
    GetMarketInfo(ctx context.Context, systemSymbol, waypointSymbol string, token string) (*Market, error)

    // ... other operations
}

// Result types (also in domain)
type PurchaseCargoResult struct {
    Transaction Transaction
    Cargo       Cargo
    Agent       Agent
}

type SellCargoResult struct {
    Transaction Transaction
    Cargo       Cargo
    Agent       Agent
}

// ... other result types
```

**Step 2**: Infrastructure implements domain port
```go
// internal/infrastructure/api/spacetraders_client.go
package api

import (
    domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

type SpaceTradersClient struct {
    httpClient *http.Client
    baseURL    string
    rateLimiter *RateLimiter
}

// Verify interface compliance at compile time
var _ domainPorts.APIClient = (*SpaceTradersClient)(nil)

func (c *SpaceTradersClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.PurchaseCargoResult, error) {
    // Implementation
}

// ... implement all interface methods
```

**Step 3**: Application uses domain port
```go
// internal/application/ship/commands/purchase_cargo.go
package commands

import (
    domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

type PurchaseCargoHandler struct {
    shipRepo   navigation.ShipRepository
    playerRepo player.PlayerRepository
    apiClient  domainPorts.APIClient  // ✅ Now depends on domain interface
    marketRepo scoutingQuery.MarketRepository
}
```

**Step 4**: Wire dependencies at composition root
```go
// cmd/spacetraders-daemon/main.go
func main() {
    // Infrastructure
    apiClient := api.NewSpaceTradersClient(...)

    // Application (receives infrastructure via domain interface)
    purchaseHandler := commands.NewPurchaseCargoHandler(
        shipRepo,
        playerRepo,
        apiClient, // SpaceTradersClient implements domainPorts.APIClient
        marketRepo,
    )

    mediator.RegisterHandler(purchaseHandler)
}
```

**Benefits**:
- ✅ Correct dependency direction (application → domain ← infrastructure)
- ✅ Application can be tested with mock API client (implements domain port)
- ✅ Infrastructure can be swapped without changing application
- ✅ Follows hexagonal architecture principles

**Estimated Effort**: High (1-2 weeks to move interface and update all handlers)

---

### 4.2 SEVERE: RouteExecutor Command Type Duplication

**File**: `internal/application/ship/route_executor.go`

**Problem**: Lines 13-46 define LOCAL command type duplicates to avoid circular imports!

**Evidence**:
```go
// Lines 13-46
// Local command type definitions to avoid circular imports
// These mirror the actual command types in the commands subpackage
type (
    OrbitShipCommand struct {
        ShipSymbol string
        PlayerID   shared.PlayerID
    }

    DockShipCommand struct {
        ShipSymbol string
        PlayerID   shared.PlayerID
    }

    RefuelShipCommand struct {
        ShipSymbol string
        PlayerID   shared.PlayerID
        Units      *int
    }

    SetFlightModeCommand struct {
        ShipSymbol string
        FlightMode string
        PlayerID   shared.PlayerID
    }

    NavigateDirectCommand struct {
        ShipSymbol      string
        DestinationSymbol string
        PlayerID        shared.PlayerID
    }
)
```

**Root Cause**: Circular dependency
```
RouteExecutor (ship/) → needs command types from ship/commands/
Commands (ship/commands/) → might need RouteExecutor or other ship/ services
```

**Impact**:
- Extreme coupling disguised as decoupling
- Type safety broken (local types != actual command types)
- Maintenance nightmare (must keep types in sync manually)
- No compile-time verification of type matching
- Indicates poor package structure

**Refactoring Solutions**:

**Option 1**: Extract command types to separate package
```go
// internal/application/ship/types/commands.go
package types

import "github.com/andrescamacho/spacetraders-go/internal/domain/shared"

type OrbitShipCommand struct {
    ShipSymbol string
    PlayerID   shared.PlayerID
}

type DockShipCommand struct {
    ShipSymbol string
    PlayerID   shared.PlayerID
}

// ... all command types

// Both route_executor.go and commands/*.go import from types/
```

**Option 2**: Use interfaces instead of concrete types
```go
// application/ship/ports.go
package ship

type OrbitRequest interface {
    GetShipSymbol() string
    GetPlayerID() shared.PlayerID
}

type DockRequest interface {
    GetShipSymbol() string
    GetPlayerID() shared.PlayerID
}

// RouteExecutor uses interfaces
func (e *RouteExecutor) orbitShip(ctx context.Context, req OrbitRequest) error {
    cmd := &OrbitShipCommand{
        ShipSymbol: req.GetShipSymbol(),
        PlayerID:   req.GetPlayerID(),
    }
    _, err := e.mediator.Send(ctx, cmd)
    return err
}

// Commands implement interfaces
func (c *OrbitShipCommand) GetShipSymbol() string { return c.ShipSymbol }
func (c *OrbitShipCommand) GetPlayerID() shared.PlayerID { return c.PlayerID }
```

**Option 3**: Move RouteExecutor to different package
```go
// internal/application/routing/route_executor.go
package routing

import "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"

// Can now directly import and use commands
func (e *RouteExecutor) orbitShip(ctx context.Context, shipSymbol string, playerID shared.PlayerID) error {
    cmd := &commands.OrbitShipCommand{
        ShipSymbol: shipSymbol,
        PlayerID:   playerID,
    }
    _, err := e.mediator.Send(ctx, cmd)
    return err
}
```

**Recommended**: Option 1 (extract to types package) - cleanest and most maintainable

**Estimated Effort**: Medium (1 week to restructure and update imports)

---

### 4.3 MODERATE: Mediator Tight Coupling

**File**: `internal/application/common/mediator.go`

**Problem**: Reflection-based dispatch creates runtime coupling between handlers and request types.

**Evidence** (Lines 67-94):
```go
func (m *mediator) Send(ctx context.Context, request Request) (Response, error) {
    requestType := reflect.TypeOf(request)  // RUNTIME reflection
    handler, ok := m.handlers[requestType]
    if !ok {
        // RUNTIME error - not caught at compile time
        return nil, fmt.Errorf("no handler registered for type %s", requestType)
    }

    // Build middleware chain
    next := handler.Handle
    for i := len(m.middlewares) - 1; i >= 0; i-- {
        middleware := m.middlewares[i]
        currentNext := next
        next = func(ctx context.Context, req Request) (Response, error) {
            return middleware(ctx, req, currentNext)
        }
    }

    return next(ctx, request)
}
```

**Issues**:
1. **Runtime errors** instead of compile-time errors for missing handlers
2. **No type safety** - any type can be Request/Response
3. **Difficult to trace** - handler execution hidden behind reflection
4. **No request/response correlation** - cannot verify command returns expected response type

**Current Marker Interfaces** (Lines 10-13):
```go
type Request interface{}   // Any type is a request
type Response interface{}  // Any type is a response
```

**Alternative**: Strongly-typed mediator using Go 1.18+ generics

```go
// application/common/typed_mediator.go
package common

import "context"

type Handler[TRequest any, TResponse any] interface {
    Handle(ctx context.Context, req TRequest) (TResponse, error)
}

type TypedMediator interface {
    Send[TRequest any, TResponse any](ctx context.Context, req TRequest) (TResponse, error)
    Register[TRequest any, TResponse any](handler Handler[TRequest, TResponse])
    RegisterMiddleware(middleware Middleware)
}

type typedMediator struct {
    handlers    map[string]any  // type name → Handler[TReq, TResp]
    middlewares []Middleware
}

func NewTypedMediator() TypedMediator {
    return &typedMediator{
        handlers: make(map[string]any),
    }
}

func (m *typedMediator) Register[TRequest any, TResponse any](handler Handler[TRequest, TResponse]) {
    var zeroReq TRequest
    reqType := reflect.TypeOf(zeroReq).String()
    m.handlers[reqType] = handler
}

func (m *typedMediator) Send[TRequest any, TResponse any](ctx context.Context, req TRequest) (TResponse, error) {
    reqType := reflect.TypeOf(req).String()
    handlerAny, ok := m.handlers[reqType]

    var zero TResponse
    if !ok {
        return zero, fmt.Errorf("no handler registered for type %s", reqType)
    }

    // Type assertion with compile-time safety
    handler, ok := handlerAny.(Handler[TRequest, TResponse])
    if !ok {
        return zero, fmt.Errorf("handler type mismatch for %s", reqType)
    }

    // Apply middlewares and execute
    return handler.Handle(ctx, req)
}

// Usage example
type NavigateRouteHandler struct {
    // ...
}

func (h *NavigateRouteHandler) Handle(ctx context.Context, cmd NavigateRouteCommand) (NavigateRouteResponse, error) {
    // Implementation
}

// Registration (compile-time type checking)
mediator.Register[NavigateRouteCommand, NavigateRouteResponse](navigateHandler)

// Invocation (compile-time type checking)
response, err := mediator.Send[NavigateRouteCommand, NavigateRouteResponse](ctx, command)
```

**Benefits**:
- Compile-time type safety for request/response matching
- Better IDE support (autocomplete, type inference)
- Catches registration errors earlier
- Still flexible with middleware support

**Trade-offs**:
- More verbose registration syntax
- Still uses some reflection (but with type constraints)

**Estimated Effort**: Medium-High (1-2 weeks to refactor all handlers)

---

## 5. Low Cohesion Examples

### 5.1 CRITICAL: RouteExecutor Mixed Concerns

Already covered in detail in Section 2.4 (SRP Violations).

**Summary**: RouteExecutor mixes 7 unrelated concerns including route execution, market scanning, refuel decisions, time management, and ship state synchronization.

**Cohesion Score**: 3/10 (low)

**Recommended Actions**:
1. Extract `MarketScanningService`
2. Extract `RefuelStrategyService`
3. Extract `ShipStateSynchronizer`
4. Move waypoint trait checking to domain layer
5. Keep RouteExecutor focused ONLY on route segment orchestration

---

### 5.2 CRITICAL: RunWorkflowHandler God Object

Already covered in detail in Section 2.1 (SRP Violations).

**Summary**: 798 lines handling contract negotiation, evaluation, acceptance, cargo jettison, purchase trips, delivery trips, fulfillment, and ship assignment.

**Cohesion Score**: 2/10 (extremely low)

**Recommended Actions**: Extract 4+ specialized services as outlined in Section 2.1

---

### 5.3 MODERATE: Common Package Mixed Utilities

**Location**: `internal/application/common/`

**Problem**: The "common" package contains unrelated utilities that violate cohesion principles.

**Current Contents**:

1. **mediator.go** (101 lines) - Request dispatching via mediator pattern
2. **auth.go** (69 lines) - Authentication middleware and token management
3. **logger.go** (60 lines) - Logger context utilities
4. **route_dto.go** (45 lines) - Route data transfer objects
5. **player_token.go** - Player token context utilities

**Issues**:
- "Common" becomes a dumping ground for miscellaneous utilities
- Low discoverability (developers don't know where to look)
- Everything depends on everything (high coupling)
- Violates "cohesion by feature" principle

**Impact**:
- Difficult to understand what "common" provides
- Changes to one utility might affect unrelated code
- No clear ownership or responsibility

**Better Design**: Organize by feature/responsibility

```
internal/application/
├── mediator/
│   ├── mediator.go          (mediator pattern implementation)
│   ├── middleware.go         (middleware types)
│   └── registration.go       (handler registration utilities)
├── auth/
│   ├── token.go              (token management)
│   ├── middleware.go         (auth middleware)
│   └── context.go            (auth context utilities)
├── logging/
│   ├── logger.go             (logger implementation)
│   ├── context.go            (logger context utilities)
│   └── middleware.go         (logging middleware)
└── ship/
    ├── commands/
    ├── queries/
    └── dtos/
        └── route_dto.go      (DTOs specific to ship domain)
```

**Benefits**:
- Clear organization by responsibility
- Better discoverability
- Easier to understand dependencies
- Clear ownership boundaries

**Estimated Effort**: Low (1-2 days to reorganize)

---

## 6. High Coupling Examples

### 6.1 CRITICAL: Transitive Infrastructure Coupling

Already covered in detail in Section 4.1 (DIP Violations).

**Summary**: Application handlers couple to infrastructure through `infraPorts.APIClient`, creating transitive dependency on HTTP clients, rate limiters, and retry logic.

**Impact**: Cannot test handlers without infrastructure package, cannot swap implementations, violates hexagonal architecture.

---

### 6.2 CRITICAL: Mediator as Hub-and-Spoke Coupling

**Problem**: Almost every handler depends on the mediator, creating a hub-and-spoke coupling pattern where everything couples through a central point.

**Evidence**:

**Handler Dependencies on Mediator**:
1. `RunWorkflowHandler` (contract/commands/run_contract_workflow.go:62)
2. `RunCoordinatorHandler` (mining/commands/run_coordinator.go)
3. `RunWorkerHandler` (mining/commands/run_worker.go)
4. `RouteExecutor` (ship/route_executor.go:66-71)
5. 10+ other handlers

**Example from RunWorkflowHandler**:
```go
type RunWorkflowHandler struct {
    mediator           common.Mediator  // Couples to mediator
    shipRepo           navigation.ShipRepository
    contractRepo       domainContract.ContractRepository
    shipAssignmentRepo domainContainer.ShipAssignmentRepository
}

func (h *RunWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    // Cascading mediator calls
    negotiateResp, err := h.mediator.Send(ctx, &NegotiateContractCommand{...})
    acceptResp, err := h.mediator.Send(ctx, &AcceptContractCommand{...})
    navigateResp, err := h.mediator.Send(ctx, &NavigateRouteCommand{...})
    purchaseResp, err := h.mediator.Send(ctx, &PurchaseCargoCommand{...})
    deliverResp, err := h.mediator.Send(ctx, &DeliverContractCommand{...})
    fulfillResp, err := h.mediator.Send(ctx, &FulfillContractCommand{...})
    // ...
}
```

**Call Chain Complexity**:
```
User Request
    → RunWorkflowHandler.Handle()
        → mediator.Send(NegotiateContractCommand)
            → NegotiateContractHandler.Handle()
                → mediator.Send(GetPlayerQuery)
        → mediator.Send(AcceptContractCommand)
            → AcceptContractHandler.Handle()
        → mediator.Send(NavigateRouteCommand)
            → NavigateRouteHandler.Handle()
                → mediator.Send(OrbitShipCommand)
                → mediator.Send(DockShipCommand)
                → mediator.Send(NavigateDirectCommand)
        → mediator.Send(PurchaseCargoCommand)
        → mediator.Send(DeliverContractCommand)
        → mediator.Send(FulfillContractCommand)
```

**Issues**:
1. **Hidden dependencies** - Difficult to understand what a handler actually depends on
2. **Testing complexity** - Must register all transitive handlers in tests
3. **Debugging difficulty** - Call stacks become very deep
4. **Change amplification** - Mediator interface changes break 35+ handlers
5. **Circular dependency risk** - Handler A → Mediator → Handler B → Mediator → Handler A

**Analysis**:

This is a **trade-off, not necessarily a violation**. The mediator pattern explicitly creates this coupling to reduce direct coupling between handlers. However, at scale (35+ handlers), this can become problematic.

**Considerations**:

**Pros of current approach**:
- Handlers don't directly depend on each other
- Easy to add/remove handlers
- Cross-cutting concerns via middleware

**Cons of current approach**:
- Everything couples to mediator
- Hidden dependencies
- Complex call chains

**Alternatives to Consider**:

**Option 1**: Domain services for related operations
```go
// Instead of:
negotiateResp, err := h.mediator.Send(ctx, &NegotiateContractCommand{...})
acceptResp, err := h.mediator.Send(ctx, &AcceptContractCommand{...})

// Use domain service:
type ContractService interface {
    NegotiateAndAccept(ctx, factionSymbol, playerID) (*Contract, error)
}

contract, err := h.contractService.NegotiateAndAccept(ctx, ...)
```

**Option 2**: Sagas for complex workflows
```go
type ContractWorkflowSaga struct {
    negotiation *ContractNegotiationStep
    purchase    *ContractPurchaseStep
    delivery    *ContractDeliveryStep
    fulfillment *ContractFulfillmentStep
}

func (s *ContractWorkflowSaga) Execute(ctx context.Context) error {
    // Orchestrate steps with compensating transactions
}
```

**Recommendation**: Keep mediator for simple command/query dispatch, but extract domain services for complex multi-step operations.

**Estimated Effort**: High (2-3 weeks to refactor complex handlers)

---

### 6.3 SEVERE: RouteExecutor Command Type Duplication

Already covered in Section 4.2 (DIP Violations).

**Summary**: Local command type duplication to avoid circular imports indicates extreme coupling disguised as decoupling.

---

### 6.4 MODERATE: Repository Coupling Across Domains

**Problem**: Handlers couple to repositories from multiple bounded contexts, indicating either incorrect domain boundaries or orchestrator pattern.

**Example 1: PurchaseCargoHandler** (ship/commands/purchase_cargo.go:48-53):
```go
type PurchaseCargoHandler struct {
    shipRepo   navigation.ShipRepository      // navigation domain
    playerRepo player.PlayerRepository        // player domain
    apiClient  infraPorts.APIClient           // infrastructure
    marketRepo scoutingQuery.MarketRepository // scouting domain
}
```

**Cross-domain dependencies**: 3 different domains + infrastructure

**Example 2: RunWorkflowHandler** (contract/commands/run_contract_workflow.go:61-66):
```go
type RunWorkflowHandler struct {
    mediator           common.Mediator
    shipRepo           navigation.ShipRepository         // navigation domain
    contractRepo       domainContract.ContractRepository // contract domain
    shipAssignmentRepo domainContainer.ShipAssignmentRepository // container domain
}
```

**Cross-domain dependencies**: 3 different domains + mediator

**Impact**:
- Handlers become coupled to 3-4+ domains
- Difficult to test (requires 4+ mock repositories)
- Suggests domains might not be properly bounded
- High fan-out coupling (one handler → many repositories)

**Analysis**:

This is **somewhat expected in orchestrator handlers** (RunWorkflowHandler, RunCoordinatorHandler), but the coupling is still high for basic operations like PurchaseCargoHandler.

**Questions to ask**:
1. Should PurchaseCargoHandler know about the player domain?
   - Maybe player token should be in context (via middleware)
2. Should it know about market repository?
   - Maybe transaction limits should be part of the command or a separate query

**Refactoring Options**:

**Option 1**: Use facades to reduce direct dependencies
```go
// application/ship/facades/cargo_transaction_facade.go
type CargoTransactionFacade struct {
    shipRepo   navigation.ShipRepository
    playerRepo player.PlayerRepository
    marketRepo scoutingQuery.MarketRepository
    apiClient  ports.APIClient
}

func (f *CargoTransactionFacade) GetTransactionContext(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*TransactionContext, error) {
    // Aggregates data from multiple repositories
    // Returns single context object
}

// Handler now depends on single facade
type PurchaseCargoHandler struct {
    transactionFacade *CargoTransactionFacade
}
```

**Option 2**: Use middleware for cross-cutting concerns
```go
// Player token already in context via middleware
// Don't need playerRepo in handler

type PurchaseCargoHandler struct {
    shipRepo   navigation.ShipRepository
    marketRepo scoutingQuery.MarketRepository
    apiClient  ports.APIClient
    // playerRepo removed - token from context
}
```

**Option 3**: Domain services for cross-domain operations
```go
// domain/ship/services/cargo_service.go
type CargoService interface {
    PurchaseCargo(ctx, shipSymbol, goodSymbol, units, playerID) error
}

// Handler delegates to domain service
type PurchaseCargoHandler struct {
    cargoService domain.CargoService
}
```

**Estimated Effort**: Medium (1-2 weeks depending on approach)

---

### 6.5 CRITICAL: Channel-Based Coupling in Mining Commands

**File**: `internal/application/mining/commands/run_worker.go`

**Problem**: RunWorkerCommand has channels as dependencies, tightly coupling to specific concurrency implementation.

**Evidence** (Lines 28-31):
```go
type RunWorkerCommand struct {
    ShipSymbol           string
    AsteroidFieldSymbol  string
    PlayerID             shared.PlayerID

    // Channel dependencies (tight coupling to Go channels)
    TransportRequestChan chan<- string           // Send miner symbol to request transport
    TransportAssignChan  <-chan string           // Receive assigned transport symbol
    TransferCompleteChan chan<- TransferComplete // Signal transfer completion
}
```

**Similar Issue in RunCoordinatorHandler**: Manages 5+ channels

**Impact**:
- Tight coupling to Go channel-based concurrency
- Cannot use alternative coordination mechanisms:
  - Message queues (RabbitMQ, Kafka)
  - Actor model (Akka, Orleans)
  - Event bus
  - gRPC streaming
- Makes testing difficult (must set up channels in tests)
- Hard to reason about in isolation
- Cannot distribute across multiple processes

**Example Usage**:
```go
// Worker requests transport
h.TransportRequestChan <- cmd.ShipSymbol

// Worker waits for transport assignment
transportSymbol := <-cmd.TransportAssignChan

// Worker signals completion
h.TransferCompleteChan <- TransferComplete{
    MinerSymbol:     cmd.ShipSymbol,
    TransportSymbol: transportSymbol,
}
```

**Refactoring Solution**: Abstract concurrency coordination

```go
// application/mining/ports/transport_coordinator.go
package ports

type TransportCoordinator interface {
    RequestTransport(ctx context.Context, minerSymbol string) (transportSymbol string, error)
    SignalTransferComplete(ctx context.Context, transfer TransferComplete) error
    Shutdown(ctx context.Context) error
}

// In-process channel-based implementation
type ChannelTransportCoordinator struct {
    requestChan  chan string
    assignChan   chan string
    completeChan chan TransferComplete
}

func (c *ChannelTransportCoordinator) RequestTransport(ctx context.Context, minerSymbol string) (string, error) {
    select {
    case c.requestChan <- minerSymbol:
        select {
        case transport := <-c.assignChan:
            return transport, nil
        case <-ctx.Done():
            return "", ctx.Err()
        }
    case <-ctx.Done():
        return "", ctx.Err()
    }
}

// Future: Distributed message queue implementation
type RabbitMQTransportCoordinator struct {
    connection *amqp.Connection
    // ...
}

func (c *RabbitMQTransportCoordinator) RequestTransport(ctx context.Context, minerSymbol string) (string, error) {
    // Publish to queue, wait for response
}

// Refactored command
type RunWorkerCommand struct {
    ShipSymbol          string
    AsteroidFieldSymbol string
    PlayerID            shared.PlayerID
    Coordinator         ports.TransportCoordinator  // Interface, not channels
}

// Handler uses abstraction
func (h *RunWorkerHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
    cmd := request.(*RunWorkerCommand)

    // Request transport (implementation agnostic)
    transportSymbol, err := cmd.Coordinator.RequestTransport(ctx, cmd.ShipSymbol)

    // Signal completion (implementation agnostic)
    err = cmd.Coordinator.SignalTransferComplete(ctx, TransferComplete{...})
}
```

**Benefits**:
- Decoupled from specific concurrency mechanism
- Can swap implementations (channels → message queue)
- Easier to test (mock coordinator)
- Can distribute across processes
- Better abstractions

**Estimated Effort**: High (1-2 weeks to refactor mining coordination)

---

## 7. Mediator Pattern Analysis

### 7.1 Implementation Review

**File**: `internal/application/common/mediator.go` (101 lines)

**Interface** (Lines 20-25):
```go
type Mediator interface {
    Send(ctx context.Context, request Request) (Response, error)
    Register(requestType reflect.Type, handler RequestHandler) error
    RegisterMiddleware(middleware Middleware)
}
```

### 7.2 Strengths

1. **Clean separation** of request/response from handlers
2. **Middleware support** for cross-cutting concerns (auth, logging, telemetry)
3. **Type-safe helper** for registration (Lines 96-100)
4. **Excellent middleware implementation** (Lines 79-93 with proper chain building)

**Example: PlayerTokenMiddleware** (auth.go:40-69):
```go
func PlayerTokenMiddleware(playerRepo player.PlayerRepository) common.Middleware {
    return func(ctx context.Context, request common.Request, next common.HandlerFunc) (common.Response, error) {
        // Check if request needs player token
        if needsToken, ok := request.(interface{ NeedsPlayerToken() bool }); ok && needsToken.NeedsPlayerToken() {
            token, err := getPlayerTokenFromContext(ctx)
            if err != nil {
                // Fetch from repository and add to context
            }
            return next(ctx, request)
        }
        return next(ctx, request)
    }
}
```

This is **excellent design** for cross-cutting authentication.

### 7.3 Weaknesses

1. **Reflection-based dispatch** - Runtime errors instead of compile-time errors
2. **Marker interfaces** - Any type can be Request/Response (no type safety)
3. **Manual registration** - Must register 35+ handlers explicitly
4. **No request/response correlation** - Cannot verify matching types
5. **Complex debugging** - Reflection hides call stacks

**Evidence of Runtime Errors** (Lines 67-77):
```go
func (m *mediator) Send(ctx context.Context, request Request) (Response, error) {
    requestType := reflect.TypeOf(request)
    handler, ok := m.handlers[requestType]
    if !ok {
        // RUNTIME ERROR - should be compile-time
        return nil, fmt.Errorf("no handler registered for type %s", requestType)
    }
    // ...
}
```

### 7.4 Usage Patterns

**Good Usage**: Cross-cutting concerns via middleware
```go
// Centralized authentication
middleware := PlayerTokenMiddleware(playerRepo)
mediator.RegisterMiddleware(middleware)

// All commands automatically get token injection
```

**Problematic Usage**: Deep call chains through mediator
```go
// RunWorkflowHandler creates 6+ mediator calls
negotiateResp, err := h.mediator.Send(ctx, negotiateCmd)
acceptResp, err := h.mediator.Send(ctx, acceptCmd)
navigateResp, err := h.mediator.Send(ctx, navigateCmd)
purchaseResp, err := h.mediator.Send(ctx, purchaseCmd)
deliverResp, err := h.mediator.Send(ctx, deliverCmd)
fulfillResp, err := h.mediator.Send(ctx, fulfillCmd)
```

This creates hidden dependencies and makes debugging difficult.

### 7.5 Recommendations

1. **Add typed mediator** using Go 1.18+ generics (see Section 4.3)
2. **Add auto-registration** via reflection or code generation
3. **Add circuit breaker middleware** for resilience
4. **Add telemetry middleware** for observability (OpenTelemetry)
5. **Consider extracting domain services** for complex multi-step operations instead of mediator chains

**Example: Circuit Breaker Middleware**
```go
func CircuitBreakerMiddleware(cb *CircuitBreaker) common.Middleware {
    return func(ctx context.Context, request common.Request, next common.HandlerFunc) (common.Response, error) {
        if cb.IsOpen() {
            return nil, fmt.Errorf("circuit breaker open")
        }

        resp, err := next(ctx, request)

        if err != nil {
            cb.RecordFailure()
        } else {
            cb.RecordSuccess()
        }

        return resp, err
    }
}
```

**Example: Telemetry Middleware**
```go
func TelemetryMiddleware(tracer trace.Tracer) common.Middleware {
    return func(ctx context.Context, request common.Request, next common.HandlerFunc) (common.Response, error) {
        requestType := reflect.TypeOf(request).String()
        ctx, span := tracer.Start(ctx, fmt.Sprintf("Handler.%s", requestType))
        defer span.End()

        resp, err := next(ctx, request)

        if err != nil {
            span.RecordError(err)
        }

        return resp, err
    }
}
```

---

## 8. Refactoring Roadmap

### Phase 1: Critical Fixes (High Impact, Low-Medium Risk)

**Timeline**: 2-3 weeks

#### 1.1 Extract Cargo Transaction Handler ⭐ Priority 1
- **Problem**: 90% duplication between purchase_cargo.go and sell_cargo.go
- **Solution**: Create unified handler with strategy pattern
- **Files**:
  - Create: `application/ship/strategies/cargo_transaction_strategy.go`
  - Create: `application/ship/commands/cargo_transaction.go`
  - Refactor: `purchase_cargo.go`, `sell_cargo.go`
- **Tests**: BDD tests in `test/bdd/features/application/cargo_transactions.feature`
- **Estimated Effort**: 3-5 days
- **Risk**: Low (well-isolated change)

#### 1.2 Fix Infrastructure Coupling ⭐ Priority 1
- **Problem**: Application depends on `infraPorts.APIClient` (architecture violation)
- **Solution**: Move API port interface to domain layer
- **Files**:
  - Create: `domain/ports/api_client.go`
  - Update: 10+ handlers using `infraPorts.APIClient`
  - Update: Infrastructure implementation
- **Tests**: Verify existing BDD tests still pass
- **Estimated Effort**: 1-2 weeks
- **Risk**: Medium (touches many files, requires careful import management)

#### 1.3 Extract Player Resolution Utility ⭐ Priority 2
- **Problem**: Player ID resolution duplicated in 3+ query handlers
- **Solution**: Extract to `application/common/player_resolution.go`
- **Files**:
  - Create: `application/common/player_resolution.go`
  - Update: `get_ship.go`, `get_player.go`, `list_ships.go`
- **Tests**: Unit tests for resolver, verify existing BDD tests pass
- **Estimated Effort**: 1-2 days
- **Risk**: Low

#### 1.4 Add Refuel Strategy Pattern ⭐ Priority 2
- **Problem**: Hardcoded 0.9 thresholds everywhere
- **Solution**: Create `RefuelStrategy` interface with implementations
- **Files**:
  - Create: `application/ship/strategies/refuel_strategy.go`
  - Update: `route_executor.go`
- **Tests**: BDD tests for different strategies
- **Estimated Effort**: 3-5 days
- **Risk**: Low-Medium

### Phase 2: God Object Decomposition (High Impact, High Risk)

**Timeline**: 3-4 weeks

**⚠️ CRITICAL**: These refactorings require extensive BDD test coverage BEFORE starting.

#### 2.1 Decompose RunWorkflowHandler ⭐ Priority 1
- **Problem**: 798 lines, 8+ responsibilities
- **Solution**: Extract 4 specialized services
- **Services to Create**:
  1. `ContractNegotiationService` - Negotiate + evaluate profitability
  2. `ContractPurchaseService` - Purchase trip orchestration
  3. `ContractDeliveryService` - Delivery trip orchestration
  4. `CargoJettisonService` - Cargo jettison logic
- **Files**:
  - Create: 4 service files in `application/contract/services/`
  - Refactor: `run_contract_workflow.go` (thin orchestration layer)
- **Tests**:
  - BDD tests for each service
  - Integration tests for workflow orchestration
  - Existing contract workflow tests must pass
- **Estimated Effort**: 1-2 weeks
- **Risk**: High (complex business logic, many dependencies)

#### 2.2 Decompose RunCoordinatorHandler ⭐ Priority 1
- **Problem**: 1,198 lines, 10+ responsibilities
- **Solution**: Extract 4+ specialized services
- **Services to Create**:
  1. `MiningFleetManager` - Fleet loading and health checking
  2. `TransportCoordinator` - Transport assignment and operations
  3. `MinerHealthChecker` - Health monitoring and recovery
  4. `TransportRequestHandler` - Request queue processing
- **Files**:
  - Create: 4+ service files in `application/mining/services/`
  - Refactor: `run_coordinator.go` (thin orchestration layer)
- **Tests**:
  - BDD tests for each service
  - Concurrent behavior tests
  - Existing mining coordinator tests must pass
- **Estimated Effort**: 2-3 weeks
- **Risk**: Very High (complex concurrency, critical production code)

#### 2.3 Refactor RouteExecutor ⭐ Priority 2
- **Problem**: 661 lines mixing route execution, market scanning, refuel decisions
- **Solution**: Extract MarketScanningService and RefuelStrategyService
- **Files**:
  - Create: `application/ship/services/market_scanning_service.go`
  - Create: `application/ship/services/refuel_strategy_service.go` (if not done in Phase 1)
  - Refactor: `route_executor.go`
  - Add: `domain/shared/waypoint.go` - HasTrait() method
- **Tests**:
  - BDD tests for route execution with different strategies
  - Market scanning tests
- **Estimated Effort**: 1 week
- **Risk**: Medium

### Phase 3: Extensibility Improvements (Medium Impact, Low Risk)

**Timeline**: 2-3 weeks

#### 3.1 Fix Package Circular Dependency ⭐ Priority 2
- **Problem**: RouteExecutor duplicates command types (lines 13-46)
- **Solution**: Extract command types to `application/ship/types/`
- **Files**:
  - Create: `application/ship/types/commands.go`
  - Update: All command files
  - Update: `route_executor.go`
  - Update: Imports across ship package
- **Tests**: Verify existing tests pass
- **Estimated Effort**: 3-5 days
- **Risk**: Medium (requires careful import refactoring)

#### 3.2 Add Strategy Pattern for Cargo Transactions
- **Status**: Should be completed as part of Phase 1.1
- If not done, add here with same specs

### Phase 4: Coupling Reduction (Low Priority)

**Timeline**: 2-3 weeks

#### 4.1 Reorganize Common Package ⭐ Priority 3
- **Problem**: Mixed unrelated utilities in `common/`
- **Solution**: Split into feature-specific packages
- **New Structure**:
  - `application/mediator/` - Mediator pattern
  - `application/auth/` - Authentication
  - `application/logging/` - Logging utilities
  - Move DTOs to respective domains
- **Files**: Reorganize 5+ files
- **Tests**: Verify existing tests pass
- **Estimated Effort**: 2-3 days
- **Risk**: Low (mostly moving files)

#### 4.2 Abstract Concurrency Coordination ⭐ Priority 3
- **Problem**: Channel-based coupling in mining commands
- **Solution**: Create `TransportCoordinator` interface
- **Files**:
  - Create: `application/mining/ports/transport_coordinator.go`
  - Create: `application/mining/coordination/channel_coordinator.go` (channel implementation)
  - Update: `run_worker.go`, `run_coordinator.go`
- **Tests**:
  - BDD tests with mock coordinator
  - Integration tests with channel coordinator
- **Estimated Effort**: 1 week
- **Risk**: Medium (changes concurrency model)

#### 4.3 Add Typed Mediator (Optional)
- **Problem**: Reflection-based dispatch lacks compile-time safety
- **Solution**: Add generic-based typed mediator (Go 1.18+)
- **Files**:
  - Create: `application/mediator/typed_mediator.go`
  - Gradually migrate handlers
- **Tests**: Verify type safety at compile time
- **Estimated Effort**: 1-2 weeks
- **Risk**: Low (additive change, old mediator still works)

---

## 9. Testing Strategy

### 9.1 Pre-Refactoring Testing

**Before ANY refactoring, ensure**:

1. **All existing BDD tests pass** (100% pass rate)
   ```bash
   make test-bdd
   ```

2. **Document current test coverage**
   ```bash
   make test-coverage
   # Review coverage.html
   ```

3. **Identify critical paths with NO test coverage**
   - Add BDD tests for untested paths BEFORE refactoring

### 9.2 Refactoring Testing Approach

**For each refactoring**:

1. **Write new BDD tests** for the refactored component (if not already covered)
2. **Run existing tests** to establish baseline
3. **Perform refactoring**
4. **Run all tests** - they should still pass
5. **Add new tests** for new functionality (if any)
6. **Review coverage** - should maintain or improve

### 9.3 Test Organization

**BDD Test Structure**:
```
test/bdd/features/
├── application/
│   ├── cargo_transactions.feature       (NEW - Phase 1.1)
│   ├── player_resolution.feature        (NEW - Phase 1.3)
│   ├── refuel_strategies.feature        (NEW - Phase 1.4)
│   ├── contract_negotiation.feature     (NEW - Phase 2.1)
│   ├── contract_purchase.feature        (NEW - Phase 2.1)
│   ├── contract_delivery.feature        (NEW - Phase 2.1)
│   ├── mining_fleet_management.feature  (NEW - Phase 2.2)
│   ├── transport_coordination.feature   (NEW - Phase 2.2)
│   └── navigate_ship_handler.feature    (EXISTING)
├── domain/
│   ├── navigation/
│   │   ├── ship_entity.feature          (EXISTING - 50+ scenarios)
│   │   └── route_entity.feature         (EXISTING)
│   └── shared/
│       └── waypoint.feature             (NEW - HasTrait method)
└── ...
```

### 9.4 Test Coverage Goals

**Minimum Coverage Standards**:
- **Domain Layer**: 90%+ (business logic critical)
- **Application Layer**: 80%+ (orchestration logic)
- **Handlers**: 75%+ (integration scenarios)

**Critical Paths (Must be 100% covered)**:
- Contract workflow execution
- Mining coordinator operations
- Route planning and execution
- Cargo transactions
- Ship navigation state transitions

---

## 10. Risk Assessment

### High-Risk Refactorings

| Refactoring | Risk Level | Mitigation Strategy |
|-------------|------------|---------------------|
| Decompose RunCoordinatorHandler | ⚠️ Very High | - Comprehensive BDD tests first<br>- Incremental extraction<br>- Feature flags for gradual rollout<br>- Extensive integration testing |
| Decompose RunWorkflowHandler | ⚠️ High | - BDD tests for each extracted service<br>- Integration tests for workflow<br>- Code review by domain expert |
| Fix Infrastructure Coupling | ⚠️ Medium-High | - Incremental migration (one handler at a time)<br>- Maintain backward compatibility during transition<br>- Comprehensive test suite |
| Fix Package Circular Dependency | ⚠️ Medium | - Create new package first<br>- Move types incrementally<br>- Update imports carefully<br>- Verify compilation at each step |

### Low-Risk Refactorings (Can proceed with confidence)

- Extract Player Resolution Utility
- Extract Cargo Transaction Handler
- Reorganize Common Package
- Add Strategy Pattern for Refuel Decisions

---

## 11. Success Metrics

### Code Quality Metrics

**Pre-Refactoring Baseline**:
- Largest handler: 1,198 lines
- Average handler size: ~215 lines
- Total duplication: ~300+ lines
- Handlers with 4+ dependencies: 10+

**Post-Refactoring Goals**:
- Largest handler: <400 lines
- Average handler size: <150 lines
- Duplication eliminated: 90%+ reduction
- Handlers with 4+ dependencies: <5

### Architecture Metrics

**Pre-Refactoring**:
- DIP violations: 10+ files
- OCP violations: Multiple handlers
- SRP violations: 3 critical handlers

**Post-Refactoring Goals**:
- DIP violations: 0 (all dependencies on domain ports)
- OCP violations: Significant reduction via strategy patterns
- SRP violations: <2 handlers (orchestrators may have multiple concerns)

### Test Metrics

**Pre-Refactoring Baseline**:
- BDD test coverage: TBD
- Unit test coverage: TBD
- Integration test coverage: TBD

**Post-Refactoring Goals**:
- BDD test coverage: 80%+ application layer
- Unit test coverage: 90%+ domain layer
- Integration test coverage: 75%+ critical workflows
- All tests passing: 100%

---

## 12. Next Steps

### Immediate Actions (This Week)

1. ✅ **Review this document** with team
2. 📊 **Establish baseline metrics**
   - Run `make test-coverage`
   - Document current test pass rate
   - Measure current duplication
3. 🎯 **Prioritize Phase 1 refactorings**
   - Start with 1.1 (Cargo Transaction Handler)
   - Quick win, low risk, high impact
4. 📝 **Create BDD tests** for areas lacking coverage
   - Focus on contract workflows
   - Focus on mining coordinator

### Week 2-3

5. 🔧 **Execute Phase 1.1** (Cargo Transaction Handler)
6. 🔧 **Execute Phase 1.3** (Player Resolution Utility)
7. 📊 **Measure improvement**

### Month 2

8. 🔧 **Execute Phase 1.2** (Infrastructure Coupling)
9. 📝 **Prepare for Phase 2** (Write comprehensive BDD tests)
10. 🔧 **Start Phase 2.3** (RouteExecutor - lower risk than coordinators)

### Month 3+

11. 🔧 **Execute Phase 2.1** (RunWorkflowHandler)
12. 🔧 **Execute Phase 2.2** (RunCoordinatorHandler) - Save for last due to high risk
13. 📊 **Final metrics review**

---

## 13. Conclusion

The application layer has significant technical debt accumulated, with **10,468 lines** across **49 files** and **35+ handlers**. The most critical issues are:

1. **90% code duplication** in cargo handlers (immediate quick win)
2. **Architecture violations** (application → infrastructure coupling)
3. **God objects** (RunCoordinatorHandler at 1,198 lines, RunWorkflowHandler at 798 lines)
4. **High coupling** through mediator and infrastructure dependencies
5. **Low cohesion** in large handlers mixing unrelated concerns

The proposed refactoring plan is organized into **4 phases** over **2-3 months**, prioritizing:
- **High impact, low risk** changes first (Phase 1)
- **High impact, high risk** changes with extensive testing (Phase 2)
- **Extensibility improvements** for future features (Phase 3)
- **Nice-to-have** improvements for long-term maintainability (Phase 4)

**Key Success Factors**:
1. ✅ Comprehensive BDD test coverage BEFORE refactoring
2. ✅ Incremental approach (one refactoring at a time)
3. ✅ Continuous testing and validation
4. ✅ Team review and approval at each phase
5. ✅ Measurement and metrics tracking

By following this plan, we can significantly improve code maintainability, testability, and extensibility while minimizing risk of regressions.

---

**Document Version**: 1.0
**Last Updated**: 2025-11-21
**Status**: Awaiting Team Review

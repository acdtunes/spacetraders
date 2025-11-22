# BDD Test Implementation Guide - Goods Factory

**Document Version:** 1.0
**Date:** 2025-11-22
**Purpose:** Guide for implementing application-level BDD step definitions

## Overview

This document provides templates and guidance for implementing BDD step definitions for the goods factory worker and coordinator features. The feature files already exist with complete scenarios, but the application-level step definitions need to be implemented.

**Current Status:**
- ✅ Domain-level BDD tests implemented (`goods_factory_steps.go` - 938 lines)
- ✅ Feature files complete (34 scenarios across 2 files)
- ❌ Application-level worker step definitions - **NOT IMPLEMENTED**
- ❌ Application-level coordinator step definitions - **NOT IMPLEMENTED**

**Estimated Effort:** 8-12 hours for complete implementation

---

## Architecture

### Test Organization

```
test/bdd/
├── features/
│   ├── domain/goods/
│   │   └── goods_factory.feature (domain tests - ✅ implemented)
│   ├── application/goods/
│   │   ├── factory_worker.feature (16 scenarios - needs steps)
│   │   └── factory_coordinator.feature (18 scenarios - needs steps)
├── steps/
│   ├── goods_factory_steps.go (domain steps - ✅ implemented)
│   ├── factory_worker_application_steps.go (❌ needs implementation)
│   └── factory_coordinator_application_steps.go (❌ needs implementation)
```

### Step Definition Strategy

1. **Worker Steps**: Focus on ProductionExecutor behavior with mocked dependencies
2. **Coordinator Steps**: Focus on parallel execution orchestration
3. **Mock Infrastructure**: Extensive mocking of mediator, repositories, services
4. **Integration Points**: Test command handlers in isolation

---

## Mock Infrastructure Needed

### Core Mocks

```go
// Mediator Mock
type MockMediator struct {
    Commands map[string]func(context.Context, common.Request) (common.Response, error)
    Calls    []MockMediatorCall
}

type MockMediatorCall struct {
    CommandType string
    Request     common.Request
    Response    common.Response
    Error       error
}

func (m *MockMediator) Send(ctx context.Context, req common.Request) (common.Response, error) {
    // Record call
    call := MockMediatorCall{CommandType: reflect.TypeOf(req).Name(), Request: req}

    // Execute handler if registered
    if handler, ok := m.Commands[call.CommandType]; ok {
        resp, err := handler(ctx, req)
        call.Response = resp
        call.Error = err
        m.Calls = append(m.Calls, call)
        return resp, err
    }

    return nil, fmt.Errorf("no handler for command: %s", call.CommandType)
}

// Ship Repository Mock
type MockShipRepository struct {
    Ships map[string]*navigation.Ship
    Calls []string
}

func (m *MockShipRepository) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
    m.Calls = append(m.Calls, "FindBySymbol:"+symbol)

    if ship, ok := m.Ships[symbol]; ok {
        return ship, nil
    }
    return nil, fmt.Errorf("ship not found: %s", symbol)
}

// Market Repository Mock
type MockMarketRepository struct {
    Markets map[string]*market.MarketData
    Calls   []string
}

func (m *MockMarketRepository) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.MarketData, error) {
    m.Calls = append(m.Calls, "GetMarketData:"+waypointSymbol)

    if marketData, ok := m.Markets[waypointSymbol]; ok {
        return marketData, nil
    }
    return nil, fmt.Errorf("market not found: %s", waypointSymbol)
}

func (m *MockMarketRepository) FindCheapestMarketSelling(ctx context.Context, good string, systemSymbol string, playerID int) (*market.Market, error) {
    m.Calls = append(m.Calls, "FindCheapestMarketSelling:"+good)
    // Implementation based on test scenario needs
    return nil, fmt.Errorf("not implemented")
}

// Market Locator Mock
type MockMarketLocator struct {
    ExportMarkets map[string]*services.MarketResult
    ImportMarkets map[string]*services.MarketResult
}

func (m *MockMarketLocator) FindExportMarket(ctx context.Context, good string, systemSymbol string, playerID int) (*services.MarketResult, error) {
    if result, ok := m.ExportMarkets[good]; ok {
        return result, nil
    }
    return nil, fmt.Errorf("no export market for %s", good)
}

// Ship Assignment Repository Mock
type MockShipAssignmentRepository struct {
    Assignments map[string]*container.ShipAssignment
}

func (m *MockShipAssignmentRepository) FindByShipSymbol(ctx context.Context, shipSymbol string, playerID int) (*container.ShipAssignment, error) {
    if assignment, ok := m.Assignments[shipSymbol]; ok {
        return assignment, nil
    }
    return nil, nil // No assignment = ship is idle
}
```

---

## Worker Step Definitions Template

### File: `factory_worker_application_steps.go`

```go
package steps

import (
    "context"
    "github.com/cucumber/godog"

    "github.com/andrescamacho/spacetraders-go/internal/application/common"
    "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
    "github.com/andrescamacho/spacetraders-go/internal/domain/goods"
    "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
    "github.com/andrescamacho/spacetraders-go/internal/domain/market"
    "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FactoryWorkerContext holds test state for worker scenarios
type FactoryWorkerContext struct {
    // Mocks
    mockMediator      *MockMediator
    mockShipRepo      *MockShipRepository
    mockMarketRepo    *MockMarketRepository
    mockMarketLocator *MockMarketLocator
    mockClock         *shared.MockClock

    // System under test
    productionExecutor *services.ProductionExecutor

    // Test data
    ship       *navigation.Ship
    node       *goods.SupplyChainNode
    result     *services.ProductionResult
    err        error

    // Scenario context
    systemSymbol string
    playerID     int
}

// NewFactoryWorkerContext creates a new worker test context
func NewFactoryWorkerContext() *FactoryWorkerContext {
    return &FactoryWorkerContext{
        mockMediator:      NewMockMediator(),
        mockShipRepo:      NewMockShipRepository(),
        mockMarketRepo:    NewMockMarketRepository(),
        mockMarketLocator: NewMockMarketLocator(),
        mockClock:         shared.NewMockClock(),
        systemSymbol:      "X1-TEST",
        playerID:          1,
    }
}

// Step: a ship "HAULER-1" at waypoint "X1-TEST-A1" with cargo capacity {int}
func (ctx *FactoryWorkerContext) aShipAtWaypointWithCargoCapacity(shipSymbol, waypointSymbol string, capacity int) error {
    ctx.ship = navigation.NewShip(
        shipSymbol,
        "SHIP_LIGHT_HAULER",
        navigation.ShipNavStatus_Docked,
        waypointSymbol,
        capacity,
        ctx.playerID,
    )

    ctx.mockShipRepo.Ships[shipSymbol] = ctx.ship
    return nil
}

// Step: a supply chain node for good "SILICON_CRYSTALS" with method "BUY"
func (ctx *FactoryWorkerContext) aSupplyChainNodeForGoodWithMethod(good, method string) error {
    var acquisitionMethod goods.AcquisitionMethod
    if method == "BUY" {
        acquisitionMethod = goods.AcquisitionBuy
    } else {
        acquisitionMethod = goods.AcquisitionFabricate
    }

    ctx.node = goods.NewSupplyChainNode(good, acquisitionMethod)
    return nil
}

// Step: a market at "X1-TEST-M1" selling "SILICON_CRYSTALS" for {int} credits with {int} units available
func (ctx *FactoryWorkerContext) aMarketSellingGoodForCreditsWithUnitsAvailable(
    waypointSymbol, good string,
    price, units int,
) error {
    marketData := market.NewMarketData(waypointSymbol, []market.TradeGood{
        market.NewTradeGood(good, price, units, "EXPORT"),
    })

    ctx.mockMarketRepo.Markets[waypointSymbol] = marketData

    // Configure market locator to return this market
    ctx.mockMarketLocator.ExportMarkets[good] = &services.MarketResult{
        WaypointSymbol: waypointSymbol,
        Price:          price,
        Activity:       "STRONG",
        Supply:         "ABUNDANT",
    }

    return nil
}

// Step: the mediator will navigate ship to "X1-TEST-M1" successfully
func (ctx *FactoryWorkerContext) theMediatorWillNavigateShipToSuccessfully(destination string) error {
    ctx.mockMediator.Commands["NavigateRouteCommand"] = func(ctxCmd context.Context, req common.Request) (common.Response, error) {
        // Update ship location
        ctx.ship.UpdateLocation(destination)
        return &NavigateRouteResponse{Success: true}, nil
    }

    ctx.mockMediator.Commands["DockShipCommand"] = func(ctxCmd context.Context, req common.Request) (common.Response, error) {
        return &DockShipResponse{Success: true}, nil
    }

    return nil
}

// Step: the mediator will purchase {int} units of "SILICON_CRYSTALS" for {int} credits
func (ctx *FactoryWorkerContext) theMediatorWillPurchaseUnitsOfGoodForCredits(
    units int,
    good string,
    totalCost int,
) error {
    ctx.mockMediator.Commands["PurchaseCargoCommand"] = func(ctxCmd context.Context, req common.Request) (common.Response, error) {
        return &PurchaseCargoResponse{
            UnitsAdded: units,
            TotalCost:  totalCost,
        }, nil
    }

    return nil
}

// Step: I execute the production worker for the node
func (ctx *FactoryWorkerContext) iExecuteTheProductionWorkerForTheNode() error {
    ctx.productionExecutor = services.NewProductionExecutor(
        ctx.mockMediator,
        ctx.mockShipRepo,
        ctx.mockMarketRepo,
        ctx.mockMarketLocator,
        ctx.mockClock,
    )

    ctxExec := context.Background()
    ctx.result, ctx.err = ctx.productionExecutor.ProduceGood(
        ctxExec,
        ctx.ship,
        ctx.node,
        ctx.systemSymbol,
        ctx.playerID,
    )

    return nil
}

// Step: the production should succeed with {int} units acquired
func (ctx *FactoryWorkerContext) theProductionShouldSucceedWithUnitsAcquired(expectedUnits int) error {
    if ctx.err != nil {
        return fmt.Errorf("expected success but got error: %v", ctx.err)
    }

    if ctx.result.QuantityAcquired != expectedUnits {
        return fmt.Errorf("expected %d units but got %d", expectedUnits, ctx.result.QuantityAcquired)
    }

    return nil
}

// Step: the total cost should be {int} credits
func (ctx *FactoryWorkerContext) theTotalCostShouldBeCredits(expectedCost int) error {
    if ctx.result.TotalCost != expectedCost {
        return fmt.Errorf("expected cost %d but got %d", expectedCost, ctx.result.TotalCost)
    }

    return nil
}

// Step: the mediator should have received commands: navigate, dock, purchase
func (ctx *FactoryWorkerContext) theMediatorShouldHaveReceivedCommands(commandsTable *godog.Table) error {
    expectedCommands := extractTableColumn(commandsTable, "command")
    actualCommands := make([]string, len(ctx.mockMediator.Calls))

    for i, call := range ctx.mockMediator.Calls {
        actualCommands[i] = call.CommandType
    }

    if !slicesEqual(expectedCommands, actualCommands) {
        return fmt.Errorf("expected commands %v but got %v", expectedCommands, actualCommands)
    }

    return nil
}

// RegisterWorkerSteps registers all worker step definitions
func RegisterWorkerSteps(sc *godog.ScenarioContext) {
    ctx := NewFactoryWorkerContext()

    sc.Step(`^a ship "([^"]*)" at waypoint "([^"]*)" with cargo capacity (\d+)$`, ctx.aShipAtWaypointWithCargoCapacity)
    sc.Step(`^a supply chain node for good "([^"]*)" with method "([^"]*)"$`, ctx.aSupplyChainNodeForGoodWithMethod)
    sc.Step(`^a market at "([^"]*)" selling "([^"]*)" for (\d+) credits with (\d+) units available$`, ctx.aMarketSellingGoodForCreditsWithUnitsAvailable)
    sc.Step(`^the mediator will navigate ship to "([^"]*)" successfully$`, ctx.theMediatorWillNavigateShipToSuccessfully)
    sc.Step(`^the mediator will purchase (\d+) units of "([^"]*)" for (\d+) credits$`, ctx.theMediatorWillPurchaseUnitsOfGoodForCredits)
    sc.Step(`^I execute the production worker for the node$`, ctx.iExecuteTheProductionWorkerForTheNode)
    sc.Step(`^the production should succeed with (\d+) units acquired$`, ctx.theProductionShouldSucceedWithUnitsAcquired)
    sc.Step(`^the total cost should be (\d+) credits$`, ctx.theTotalCostShouldBeCredits)
    sc.Step(`^the mediator should have received commands:$`, ctx.theMediatorShouldHaveReceivedCommands)
}
```

---

## Coordinator Step Definitions Template

### File: `factory_coordinator_application_steps.go`

```go
package steps

import (
    "context"
    "github.com/cucumber/godog"

    "github.com/andrescamacho/spacetraders-go/internal/application/goods/commands"
    "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
    "github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// FactoryCoordinatorContext holds test state for coordinator scenarios
type FactoryCoordinatorContext struct {
    // Mocks (same as worker)
    mockMediator           *MockMediator
    mockShipRepo           *MockShipRepository
    mockMarketRepo         *MockMarketRepository
    mockShipAssignmentRepo *MockShipAssignmentRepository
    mockClock              *shared.MockClock

    // Services
    resolver           *services.SupplyChainResolver
    marketLocator      *services.MarketLocator
    dependencyAnalyzer *services.DependencyAnalyzer

    // System under test
    coordinatorHandler *commands.RunFactoryCoordinatorHandler

    // Test data
    command  *commands.RunFactoryCoordinatorCommand
    response *commands.RunFactoryCoordinatorResponse
    err      error

    // Test ships
    idleShips []*navigation.Ship
}

// NewFactoryCoordinatorContext creates a new coordinator test context
func NewFactoryCoordinatorContext() *FactoryCoordinatorContext {
    ctx := &FactoryCoordinatorContext{
        mockMediator:           NewMockMediator(),
        mockShipRepo:           NewMockShipRepository(),
        mockMarketRepo:         NewMockMarketRepository(),
        mockShipAssignmentRepo: NewMockShipAssignmentRepository(),
        mockClock:              shared.NewMockClock(),
    }

    // Initialize services
    supplyChainMap := config.DefaultSupplyChainMap()
    ctx.resolver = services.NewSupplyChainResolver(supplyChainMap, ctx.mockMarketRepo)
    ctx.marketLocator = services.NewMarketLocator(ctx.mockMarketRepo)
    ctx.dependencyAnalyzer = services.NewDependencyAnalyzer()

    return ctx
}

// Step: {int} idle hauler ships available in the system
func (ctx *FactoryCoordinatorContext) idleHaulerShipsAvailableInSystem(shipCount int) error {
    ctx.idleShips = make([]*navigation.Ship, shipCount)

    for i := 0; i < shipCount; i++ {
        shipSymbol := fmt.Sprintf("HAULER-%d", i+1)
        ship := navigation.NewShip(
            shipSymbol,
            "SHIP_LIGHT_HAULER",
            navigation.ShipNavStatus_Docked,
            "X1-TEST-START",
            100,
            1,
        )

        ctx.idleShips[i] = ship
        ctx.mockShipRepo.Ships[shipSymbol] = ship
        // No assignment = idle
    }

    return nil
}

// Step: a supply chain for "ADVANCED_CIRCUITRY" requiring {int} nodes
func (ctx *FactoryCoordinatorContext) aSupplyChainForGoodRequiringNodes(good string, nodeCount int) error {
    // Build real dependency tree using resolver
    // Mock market data to control BUY vs FABRICATE decisions

    // Example: Set up markets for raw materials
    ctx.mockMarketRepo.Markets["X1-TEST-RAW"] = market.NewMarketData(
        "X1-TEST-RAW",
        []market.TradeGood{
            market.NewTradeGood("SILICON_CRYSTALS", 100, 50, "EXPORT"),
        },
    )

    return nil
}

// Step: I execute the factory coordinator for "ADVANCED_CIRCUITRY"
func (ctx *FactoryCoordinatorContext) iExecuteTheFactoryCoordinatorForGood(good string) error {
    // Create coordinator handler
    ctx.coordinatorHandler = commands.NewRunFactoryCoordinatorHandler(
        ctx.mockMediator,
        ctx.mockShipRepo,
        ctx.mockMarketRepo,
        ctx.mockShipAssignmentRepo,
        ctx.resolver,
        ctx.marketLocator,
        ctx.mockClock,
    )

    // Create command
    ctx.command = &commands.RunFactoryCoordinatorCommand{
        PlayerID:     1,
        TargetGood:   good,
        SystemSymbol: "X1-TEST",
    }

    // Execute
    ctxExec := context.Background()
    resp, err := ctx.coordinatorHandler.Handle(ctxExec, ctx.command)

    ctx.err = err
    if resp != nil {
        ctx.response = resp.(*commands.RunFactoryCoordinatorResponse)
    }

    return nil
}

// Step: the coordination should complete successfully
func (ctx *FactoryCoordinatorContext) theCoordinationShouldCompleteSuccessfully() error {
    if ctx.err != nil {
        return fmt.Errorf("expected success but got error: %v", ctx.err)
    }

    if !ctx.response.Completed {
        return fmt.Errorf("expected completed=true but got false")
    }

    return nil
}

// Step: {int} ships should have been utilized
func (ctx *FactoryCoordinatorContext) shipsShouldHaveBeenUtilized(expectedShips int) error {
    if ctx.response.ShipsUsed != expectedShips {
        return fmt.Errorf("expected %d ships but got %d", expectedShips, ctx.response.ShipsUsed)
    }

    return nil
}

// Step: all {int} nodes should be completed
func (ctx *FactoryCoordinatorContext) allNodesShouldBeCompleted(expectedNodes int) error {
    if ctx.response.NodesCompleted != expectedNodes {
        return fmt.Errorf("expected %d nodes completed but got %d", expectedNodes, ctx.response.NodesCompleted)
    }

    return nil
}

// RegisterCoordinatorSteps registers all coordinator step definitions
func RegisterCoordinatorSteps(sc *godog.ScenarioContext) {
    ctx := NewFactoryCoordinatorContext()

    sc.Step(`^(\d+) idle hauler ships available in the system$`, ctx.idleHaulerShipsAvailableInSystem)
    sc.Step(`^a supply chain for "([^"]*)" requiring (\d+) nodes$`, ctx.aSupplyChainForGoodRequiringNodes)
    sc.Step(`^I execute the factory coordinator for "([^"]*)"$`, ctx.iExecuteTheFactoryCoordinatorForGood)
    sc.Step(`^the coordination should complete successfully$`, ctx.theCoordinationShouldCompleteSuccessfully)
    sc.Step(`^(\d+) ships should have been utilized$`, ctx.shipsShouldHaveBeenUtilized)
    sc.Step(`^all (\d+) nodes should be completed$`, ctx.allNodesShouldBeCompleted)
}
```

---

## Implementation Checklist

### Phase 1: Mock Infrastructure (2-3 hours)
- [ ] Create `test/bdd/mocks/mediator_mock.go`
- [ ] Create `test/bdd/mocks/ship_repository_mock.go`
- [ ] Create `test/bdd/mocks/market_repository_mock.go`
- [ ] Create `test/bdd/mocks/market_locator_mock.go`
- [ ] Create `test/bdd/mocks/ship_assignment_repository_mock.go`
- [ ] Add helper functions for mock configuration

### Phase 2: Worker Steps (3-4 hours)
- [ ] Implement ~20 step definitions for factory_worker.feature
- [ ] Test BUY operations (navigate, dock, purchase)
- [ ] Test FABRICATE operations (recursive inputs, polling)
- [ ] Test error scenarios (no markets, no cargo)
- [ ] Test market-driven behavior

### Phase 3: Coordinator Steps (3-4 hours)
- [ ] Implement ~25 step definitions for factory_coordinator.feature
- [ ] Test fleet discovery
- [ ] Test parallel execution
- [ ] Test sequential fallback
- [ ] Test metrics tracking

### Phase 4: Integration (1-2 hours)
- [ ] Register steps in godog test suite
- [ ] Run tests and fix issues
- [ ] Add documentation

---

## Running BDD Tests

```bash
# Run all BDD tests
make bdd

# Run specific feature
go test -v ./test/bdd -godog.features="features/application/goods/factory_worker.feature"

# Run specific scenario
go test -v ./test/bdd -godog.features="features/application/goods/factory_worker.feature" \
    -godog.tags="@worker-buy"

# Run with verbose output
go test -v ./test/bdd -godog.format=pretty
```

---

## Best Practices

1. **Keep Mocks Simple**: Focus on behavior verification, not implementation details
2. **Test Observable Behavior**: Verify outputs, not internals
3. **Use Table-Driven Tests**: Leverage Godog's table support for data scenarios
4. **Isolate Units**: Test one command handler at a time
5. **Mock External Systems**: Mediator, repositories, and services should be mocked
6. **Verify Side Effects**: Check that expected commands were sent to mediator
7. **Use Descriptive Step Names**: Make scenarios readable as documentation

---

## Example Scenario Walkthrough

### Scenario: Worker buys raw material

**Feature File:**
```gherkin
Scenario: Worker successfully purchases raw material from market
    Given a ship "HAULER-1" at waypoint "X1-TEST-A1" with cargo capacity 100
    And a supply chain node for good "SILICON_CRYSTALS" with method "BUY"
    And a market at "X1-TEST-M1" selling "SILICON_CRYSTALS" for 100 credits with 50 units available
    And the mediator will navigate ship to "X1-TEST-M1" successfully
    And the mediator will purchase 50 units of "SILICON_CRYSTALS" for 5000 credits
    When I execute the production worker for the node
    Then the production should succeed with 50 units acquired
    And the total cost should be 5000 credits
    And the mediator should have received commands:
        | command             |
        | NavigateRouteCommand |
        | DockShipCommand      |
        | PurchaseCargoCommand |
```

**Step Implementation Flow:**
1. Setup: Create mock ship, node, and market
2. Configure: Set mediator expectations
3. Execute: Call ProductionExecutor.ProduceGood()
4. Verify: Check result, cost, and mediator calls

---

## Notes

- This is a **template and guide**, not a complete implementation
- Actual implementation requires 8-12 hours of focused work
- Mock infrastructure is the critical foundation
- Start with simplest scenarios first (BUY operations)
- Add complexity gradually (FABRICATE, errors, parallel execution)
- Use existing domain-level tests as reference for patterns

---

## Future Enhancements

- Integration tests against live SpaceTraders API
- Performance benchmarks
- Chaos testing (random failures, network issues)
- Load testing (multiple concurrent factories)
- End-to-end profitability validation

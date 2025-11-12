# Application Layer BDD Tests - Navigate Ship Handler

## Overview

This directory contains comprehensive BDD tests for the `NavigateShipHandler` application service. These tests verify **navigation business rules** using real repositories with in-memory SQLite and mock only the external API.

## Test Architecture

```
NavigateShipHandler (REAL - under test)
  ↓
├─ PlayerRepository (REAL - GORM + SQLite)
├─ WaypointRepository (REAL - GORM + SQLite)
├─ SystemGraphRepository (REAL - GORM + SQLite)
├─ ShipRepository (MOCK - wraps API calls)
├─ GraphProvider (REAL - caching logic)
│   ├─ SystemGraphRepository (REAL)
│   └─ GraphBuilder (MOCK - returns configured data)
├─ RoutingClient (MOCK - returns configured routes)
└─ Domain Entities (REAL - Ship, Route, Waypoint, Fuel)

External Dependencies (MOCKED):
  - SpaceTraders API (MockSpaceTradersAPIClient)
  - OR-Tools Routing Service (MockRoutingClient)
```

## Why This Architecture?

1. **Test Business Logic, Not Integration**: We're testing the handler's business rules, not API reliability
2. **Fast Execution**: No network calls = tests run in milliseconds
3. **Deterministic**: No flaky tests from external services
4. **Real Repository Behavior**: Database queries, caching, and enrichment work correctly
5. **Real Domain Rules**: Ship state machine, fuel management, and route execution are fully tested

## Test Coverage (50+ Scenarios)

### Caching and Enrichment (3 scenarios)
- Load graph from database cache when available
- Build graph from API when cache is empty
- Merge graph structure with waypoint trait data

### Validation Rules (4 scenarios)
- Reject navigation with empty waypoint cache
- Reject navigation when ship location missing from cache
- Reject navigation when destination missing from cache
- Route not found - provide detailed statistics

### Idempotency (2 scenarios)
- Ship already at destination - return success immediately
- Wait for previous IN_TRANSIT command to complete

### 90% Opportunistic Refueling Rule (4 scenarios)
- Refuel when arriving at fuel station with < 90% fuel
- Skip refuel when fuel >= 90%
- Skip refuel at non-fuel-station
- Skip opportunistic refuel when segment already requires refuel

### Pre-Departure Refuel to Prevent DRIFT (4 scenarios)
- Refuel before departure when DRIFT mode planned at fuel station
- Skip when using CRUISE mode
- Skip when fuel >= 90%
- Skip when not at fuel station

### Refuel Before Departure (1 scenario)
- Refuel at start location when route requires it

### Flight Mode Setting (1 scenario)
- Set flight mode before each navigation segment

### Wait for Arrival Timing (2 scenarios)
- Wait for ship to arrive with 3-second buffer
- Handle arrival time in the past

### IN_TRANSIT Handling (1 scenario - covered in idempotency)
- Wait for previous command completion

### Auto-Sync State (1 scenario)
- Re-sync ship state after every API call (dock, refuel, orbit, navigate)

### Error Handling (2 scenarios)
- Mark route as failed when navigation fails
- Handle dock failure during refuel sequence

### Multi-Segment Navigation (2 scenarios)
- Execute multi-segment route with planned refueling
- Execute route with both opportunistic and planned refueling

### State Machine (2 scenarios)
- Ship transitions through states during navigation (DOCKED → IN_ORBIT → IN_TRANSIT → IN_ORBIT)
- Refuel sequence follows dock-refuel-orbit pattern

### Edge Cases (4 scenarios)
- Ship with zero fuel capacity navigates without refueling
- Single waypoint system - already at destination
- Route with only refuel before departure
- Calculate correct wait time with buffer

## Running the Tests

```bash
# Run all navigate ship handler tests
make test-bdd-navigate

# Run with pretty output
go test ./test/bdd/... -v -godog.format=pretty -godog.paths=test/bdd/features/application/

# Run specific tag
go test ./test/bdd/... -v -godog.tags=@refueling

# Run multiple tags
go test ./test/bdd/... -v -godog.tags="@90-percent-rule && @refueling"
```

## Key Files

- **Feature File**: `test/bdd/features/application/navigate_ship_handler.feature` (50+ scenarios)
- **Step Definitions**: `test/bdd/steps/navigate_ship_handler_steps.go` (1600+ lines)
- **Test Registration**: `test/bdd/bdd_test.go` (adds InitializeNavigateShipHandlerScenario)

## Test Context Structure

```go
type NavigateShipTestContext struct {
    // Real database and repositories
    db                *gorm.DB
    playerRepo        *persistence.GormPlayerRepository
    waypointRepo      *persistence.GormWaypointRepository
    systemGraphRepo   *persistence.GormSystemGraphRepository

    // Mock external dependencies
    mockAPIClient     *MockSpaceTradersAPIClient
    mockRoutingClient *MockRoutingClient

    // Real application components
    graphProvider     *graph.SystemGraphProvider
    handler           *appNavigation.NavigateShipHandler

    // Test tracking
    apiCallLog        []string              // Track API calls made
    shipState         map[string]*ShipState // Track ship state
    waypointCache     map[string]*shared.Waypoint
    response          *appNavigation.NavigateShipResponse
    err               error
}
```

## Mock API Client Features

The `MockSpaceTradersAPIClient` tracks all API calls and returns configured data:

```go
// Track calls
ctx.apiCallLog = ["GetShip(SCOUT-1)", "Navigate(SCOUT-1 -> X1-GZ7-B1)", "Refuel(SCOUT-1)"]

// Configure ship data
ctx.mockAPIClient.shipDataToReturn["SCOUT-1"] = &common.ShipData{...}

// Configure errors
ctx.mockAPIClient.errorToReturn["NavigateShip"] = errors.New("Insufficient fuel")
```

## Mock Routing Client Features

The `MockRoutingClient` returns configured route plans:

```go
// Configure route
ctx.routePlans["X1-GZ7-A1->X1-GZ7-B1"] = &common.RouteResponse{
    Steps: []*common.RouteStepData{
        {Action: common.RouteActionTravel, Waypoint: "X1-GZ7-B1", FuelCost: 30},
    },
}
```

## Example Scenario

```gherkin
@refueling @90-percent-rule
Scenario: Opportunistically refuel when arriving at fuel station with less than 90% fuel
  Given an in-memory database is initialized
  And a player "TEST-PLAYER" exists with token "test-token"
  And system "X1-GZ7" has waypoints cached
  And waypoint "X1-GZ7-B1" is a fuel station
  And ship "SCOUT-1" has 100 fuel capacity
  And ship "SCOUT-1" starts at "X1-GZ7-A1" with 100 fuel
  And navigation to "X1-GZ7-B1" consumes 50 fuel
  When I navigate "SCOUT-1" to "X1-GZ7-B1"
  Then ship should arrive at "X1-GZ7-B1" with 50% fuel
  And opportunistic refuel should trigger
  And ship should dock at "X1-GZ7-B1"
  And ship should refuel to 100/100
  And ship should orbit after refuel
```

## Benefits

1. **Comprehensive Coverage**: 50+ scenarios cover all navigation business rules
2. **Living Documentation**: Gherkin scenarios document navigation behavior
3. **Fast Feedback**: Tests run in < 1 second (no network calls)
4. **Regression Safety**: Catch breaking changes in navigation logic
5. **Refactoring Confidence**: Tests verify behavior, not implementation
6. **CI/CD Ready**: Can run on every commit

## Comparison with Domain Layer Tests

| Aspect | Domain Layer Tests | Application Layer Tests |
|--------|-------------------|-------------------------|
| **What's Tested** | Pure domain entities | Application service with repositories |
| **Dependencies** | Zero (pure Go) | Database (in-memory), mocked API |
| **Focus** | Business rules in isolation | Business rules with persistence |
| **Speed** | Fastest (~ms) | Fast (~100ms) |
| **Example** | Ship.Dock() state transition | NavigateShipHandler with DB caching |
| **Directory** | `features/domain/` | `features/application/` |

## Future Enhancements

1. Add scenarios for concurrent navigation (multiple ships)
2. Add performance benchmarks for route planning
3. Add scenarios for route cancellation
4. Add scenarios for fuel shortage mid-route
5. Add scenarios for destination waypoint changes

## Maintenance

- **Update scenarios** when navigation business rules change
- **Add scenarios** for new navigation features
- **Tag scenarios** by priority (@critical, @smoke)
- **Run regularly** in CI/CD pipeline
- **Review coverage** periodically for gaps

## Notes for Developers

- Steps are registered in `InitializeNavigateShipHandlerScenario()`
- Each scenario gets a fresh database (via `reset()`)
- Mock API client tracks all calls in `apiCallLog`
- Use `@wip` tag for work-in-progress scenarios
- Use `@skip` tag to temporarily disable scenarios

## Related Documentation

- Python BDD Tests: `/Users/andres.camacho/Development/Personal/spacetraders/bot/tests/bdd/`
- Domain Layer Tests: `/Users/andres.camacho/Development/Personal/spacetraders/gobot/test/bdd/features/domain/`
- Navigate Ship Handler: `/Users/andres.camacho/Development/Personal/spacetraders/gobot/internal/application/navigation/navigate_ship.go`

# Scouting BDD Tests Implementation Plan

## Overview

This document outlines the comprehensive plan for implementing BDD (Behavior-Driven Development) tests for the application/scouting layer commands and queries. The tests will follow the same patterns established in the contract BDD tests, using real GORM repositories with in-memory SQLite and mocking external dependencies.

## Goals

1. Achieve comprehensive test coverage for all scouting application layer handlers
2. Use BDD style tests in `test/bdd/` directory (following project standards)
3. Mock SpaceTraders API but use real in-memory SQLite database
4. Do NOT mock repositories - use real GORM implementations
5. Follow the unified context pattern from contract tests to eliminate step conflicts

## Commands & Queries to Test

### Commands

#### 1. AssignScoutingFleetCommand
**Location**: `internal/application/scouting/commands/assign_scouting_fleet.go`

**Request**:
- `PlayerID shared.PlayerID`
- `SystemSymbol string`

**Response**:
- `AssignedShips []string` - Ship symbols assigned to scouting
- `Assignments map[string][]string` - ship_symbol -> markets assigned
- `ReusedContainers []string` - Container IDs that were reused
- `ContainerIDs []string` - All container IDs (new + reused)

**Purpose**: Automatically assigns all probe/satellite ships to market scouting, filtering out FUEL_STATION marketplaces. Delegates to ScoutMarketsCommand internally.

**Dependencies**:
- `navigation.ShipRepository` - Load ships
- `system.WaypointRepository` - Load marketplaces
- `system.ISystemGraphProvider` - Navigation graph
- `routing.RoutingClient` - VRP optimization
- `daemon.DaemonClient` - Container creation
- `container.ShipAssignmentRepository` - Track assignments

**Test Scenarios**:
- ✅ Successfully assign all probe/drone ships to markets
- ✅ Filter out FUEL_STATION marketplaces from assignment
- ✅ Reuse existing containers for ships that already have them
- ✅ No scout ships available (should return empty assignments)
- ✅ System has no marketplaces
- ✅ Player doesn't exist (error case)

#### 2. ScoutMarketsCommand
**Location**: `internal/application/scouting/commands/scout_markets.go`

**Request**:
- `PlayerID shared.PlayerID`
- `ShipSymbols []string`
- `SystemSymbol string`
- `Markets []string`
- `Iterations int` - Number of iterations (-1 for infinite)

**Response**:
- `ContainerIDs []string` - All container IDs (new + reused)
- `Assignments map[string][]string` - ship_symbol -> markets assigned
- `ReusedContainers []string` - Subset of ContainerIDs that were reused

**Purpose**: Orchestrates fleet deployment for market scouting. Uses VRP optimization to distribute markets across multiple ships. Idempotent: reuses existing containers.

**Key Logic**:
- Stops existing scouting containers for cleanup
- Identifies container reuse opportunities
- Uses VRP PartitionFleet for multi-ship optimization
- Single ship: assigns all markets to that ship
- Creates ScoutTourContainer for each assignment

**Dependencies**:
- `navigation.ShipRepository` - Load ship configurations
- `system.ISystemGraphProvider` - Get navigation graph
- `routing.RoutingClient` - VRP fleet partitioning
- `daemon.DaemonClient` - Create/stop scout tour containers
- `container.ShipAssignmentRepository` - Query/release ship assignments

**Test Scenarios**:
- ✅ Single ship: assign all markets to that ship
- ✅ Multiple ships: use VRP optimization for distribution
- ✅ Container reuse: detect and reuse existing containers (idempotent)
- ✅ Stop existing containers before creating new ones
- ✅ Ship not found (error case)
- ✅ Empty markets list
- ✅ Invalid iterations value

#### 3. ScoutTourCommand
**Location**: `internal/application/scouting/commands/scout_tour.go`

**Request**:
- `PlayerID shared.PlayerID`
- `ShipSymbol string`
- `Markets []string` - Waypoint symbols to scout
- `Iterations int` - Number of complete tours (-1 for infinite)

**Response**:
- `MarketsVisited int`
- `TourOrder []string` - Order in which markets were visited
- `Iterations int`

**Purpose**: Executes a market scouting tour with a single ship.

**Two Modes**:
1. **Stationary scout** (1 market): Navigates to market once, then scans every 60 seconds
2. **Multi-market tour** (2+ markets): Navigates between markets in sequence

**Key Features**:
- Tour rotation: Starts from ship's current location for idempotency
- Uses `NavigateRouteCommand` (HIGH-LEVEL) for navigation
- Uses `MarketScanner` service to scan and save market data
- Context cancellation support for graceful shutdown

**Dependencies**:
- `navigation.ShipRepository` - Load ship data
- `common.Mediator` - Send NavigateRouteCommand
- `ship.MarketScanner` - Scan and save market data

**Test Scenarios**:
- ✅ Stationary scout: navigate once, scan repeatedly
- ✅ Multi-market tour: navigate between markets in sequence
- ✅ Tour rotation: start from current ship location
- ✅ Finite iterations: complete specified number of tours
- ✅ Infinite iterations: continuous touring (test with context cancellation)
- ✅ Ship not found (error case)
- ✅ Empty markets list (error case)
- ✅ Navigation failure handling

### Queries

#### 4. GetMarketDataQuery
**Location**: `internal/application/scouting/queries/get_market_data.go`

**Request**:
- `PlayerID shared.PlayerID`
- `WaypointSymbol string`

**Response**:
- `Market *market.Market`

**Purpose**: Retrieves market data for a single waypoint from database.

**Dependencies**:
- `MarketRepository` interface:
  - `GetMarketData(ctx, playerID, waypointSymbol)` → `*market.Market`

**Test Scenarios**:
- ✅ Retrieve existing market data successfully
- ✅ Market not found (returns nil or error)
- ✅ Player doesn't exist (error case)

#### 5. ListMarketDataQuery
**Location**: `internal/application/scouting/queries/get_market_data.go`

**Request**:
- `PlayerID shared.PlayerID`
- `SystemSymbol string`
- `MaxAgeMinutes int`

**Response**:
- `Markets []market.Market`

**Purpose**: Retrieves all markets in a system with age filtering.

**Dependencies**:
- `MarketRepository` interface:
  - `ListMarketsInSystem(ctx, playerID, systemSymbol, maxAgeMinutes)` → `[]market.Market`

**Test Scenarios**:
- ✅ List all markets in system successfully
- ✅ Filter by age: exclude stale data
- ✅ Empty results (no markets in system)
- ✅ Player doesn't exist (error case)

## Testing Strategy

### Real Components (Database Integration)

Following contract test patterns:

1. **PlayerRepository** - GORM with in-memory SQLite
   - Used for player authentication and token management
   - Enables database persistence verification

2. **MarketRepository** - GORM with in-memory SQLite (needs implementation)
   - Store and retrieve market scan data
   - Verify market data persistence after scout operations

3. **Database** - SharedTestDB pattern
   - Single in-memory SQLite instance
   - Truncate all tables before each scenario
   - Real GORM models auto-migrated

### Mocked Components

1. **ShipRepository** - Mock (complex ship state)
   - Probe/drone ships with specific frame types
   - Current location, fuel, cargo state
   - Configurable via test fixtures

2. **WaypointRepository** - Mock (waypoint data)
   - Marketplaces with traits (MARKETPLACE, FUEL_STATION)
   - System membership
   - Type information

3. **ShipAssignmentRepository** - Mock (container tracking)
   - Track which containers are assigned to which ships
   - Container reuse detection
   - Release assignments

4. **DaemonClient** - Mock (container operations)
   - CreateScoutTourContainer tracking
   - StopContainer tracking
   - Container ID generation

5. **RoutingClient** - Mock (VRP optimization)
   - PartitionFleet returns configurable assignments
   - PlanRoute for navigation planning

6. **GraphProvider** - Mock (navigation graphs)
   - System graphs with waypoint connectivity
   - Distance calculations

7. **APIClient** - Mock (SpaceTraders API)
   - GetMarket responses
   - Navigation API calls (if needed)

8. **Mediator** - Mock (command dispatch)
   - For ScoutTour's NavigateRouteCommand dependency
   - Track command dispatch calls

9. **MarketScanner** - Mock (market scanning service)
   - Simulate market data collection
   - Track scan calls

10. **Clock** - Mock (time-dependent behavior)
    - Control time for 60-second scan intervals
    - Test age-based filtering

## File Structure

### Feature Files
**Directory**: `test/bdd/features/application/scouting/`

Files to create:
1. `assign_scouting_fleet.feature` - AssignScoutingFleetCommand scenarios
2. `scout_markets.feature` - ScoutMarketsCommand scenarios
3. `scout_tour.feature` - ScoutTourCommand scenarios
4. `get_market_data.feature` - GetMarketDataQuery scenarios
5. `list_market_data.feature` - ListMarketDataQuery scenarios

### Step Definitions
**File**: `test/bdd/steps/scouting_application_steps.go`

Single unified context for all scouting handlers (following contract test pattern).

## Implementation Details

### Unified Context Structure

```go
type scoutingApplicationContext struct {
    // Shared infrastructure
    db           *gorm.DB
    playerRepo   *persistence.GormPlayerRepository
    marketRepo   *persistence.GormMarketRepository  // Real GORM implementation

    // All handlers
    assignFleetHandler   *commands.AssignScoutingFleetHandler
    scoutMarketsHandler  *commands.ScoutMarketsHandler
    scoutTourHandler     *commands.ScoutTourHandler
    getMarketHandler     *queries.GetMarketDataHandler
    listMarketsHandler   *queries.ListMarketDataHandler

    // Generic state tracking
    lastError    error
    lastResponse interface{}  // Can be any handler's response type

    // Mocks
    apiClient             *helpers.MockAPIClient
    shipRepo              *helpers.MockShipRepository
    waypointRepo          *helpers.MockWaypointRepository
    assignmentRepo        *helpers.MockShipAssignmentRepository
    daemonClient          *helpers.MockDaemonClient
    routingClient         *helpers.MockRoutingClient
    graphProvider         *helpers.MockGraphProvider
    mediator              *helpers.MockMediator
    marketScanner         *helpers.MockMarketScanner
    clock                 *shared.MockClock

    // Test fixtures (for assertions)
    testShips      map[string]*navigation.Ship
    testWaypoints  map[string]*system.Waypoint
    testMarkets    map[string]*market.Market
}
```

### Reset Method Pattern

```go
func (ctx *scoutingApplicationContext) reset() {
    // 1. Clear state
    ctx.lastError = nil
    ctx.lastResponse = nil
    ctx.testShips = make(map[string]*navigation.Ship)
    ctx.testWaypoints = make(map[string]*system.Waypoint)
    ctx.testMarkets = make(map[string]*market.Market)

    // 2. Truncate all tables (test isolation)
    helpers.TruncateAllTables()

    // 3. Use shared test DB with REAL repositories
    ctx.db = helpers.SharedTestDB
    ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
    ctx.marketRepo = persistence.NewGormMarketRepository(helpers.SharedTestDB)

    // 4. Create all mocks
    ctx.apiClient = helpers.NewMockAPIClient()
    ctx.shipRepo = helpers.NewMockShipRepository()
    ctx.waypointRepo = helpers.NewMockWaypointRepository()
    ctx.assignmentRepo = helpers.NewMockShipAssignmentRepository()
    ctx.daemonClient = helpers.NewMockDaemonClient()
    ctx.routingClient = helpers.NewMockRoutingClient()
    ctx.graphProvider = helpers.NewMockGraphProvider()
    ctx.mediator = helpers.NewMockMediator()
    ctx.marketScanner = helpers.NewMockMarketScanner()
    ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

    // 5. Setup default mock behaviors
    ctx.setupDefaultMockBehaviors()

    // 6. Create all handlers with shared infrastructure
    ctx.assignFleetHandler = commands.NewAssignScoutingFleetHandler(
        ctx.shipRepo,
        ctx.waypointRepo,
        ctx.graphProvider,
        ctx.routingClient,
        ctx.daemonClient,
        ctx.assignmentRepo,
    )
    ctx.scoutMarketsHandler = commands.NewScoutMarketsHandler(
        ctx.shipRepo,
        ctx.graphProvider,
        ctx.routingClient,
        ctx.daemonClient,
        ctx.assignmentRepo,
    )
    ctx.scoutTourHandler = commands.NewScoutTourHandler(
        ctx.shipRepo,
        ctx.mediator,
        ctx.marketScanner,
    )
    ctx.getMarketHandler = queries.NewGetMarketDataHandler(ctx.marketRepo)
    ctx.listMarketsHandler = queries.NewListMarketDataHandler(ctx.marketRepo)
}
```

### Default Mock Behaviors

```go
func (ctx *scoutingApplicationContext) setupDefaultMockBehaviors() {
    // Ship repository: return test ships
    ctx.shipRepo.SetListShipsFunc(func(context.Context, shared.PlayerID) ([]*navigation.Ship, error) {
        ships := make([]*navigation.Ship, 0, len(ctx.testShips))
        for _, ship := range ctx.testShips {
            ships = append(ships, ship)
        }
        return ships, nil
    })

    ctx.shipRepo.SetGetShipFunc(func(ctx context.Context, playerID shared.PlayerID, shipSymbol string) (*navigation.Ship, error) {
        if ship, ok := ctx.testShips[shipSymbol]; ok {
            return ship, nil
        }
        return nil, fmt.Errorf("ship not found: %s", shipSymbol)
    })

    // Waypoint repository: return test waypoints
    ctx.waypointRepo.SetListWaypointsWithTraitFunc(func(ctx context.Context, systemSymbol string, trait string) ([]*system.Waypoint, error) {
        waypoints := make([]*system.Waypoint, 0)
        for _, wp := range ctx.testWaypoints {
            if wp.SystemSymbol == systemSymbol && wp.HasTrait(trait) {
                waypoints = append(waypoints, wp)
            }
        }
        return waypoints, nil
    })

    // Daemon client: generate container IDs
    containerIDCounter := 0
    ctx.daemonClient.SetCreateScoutTourContainerFunc(func(ctx context.Context, req *daemon.CreateScoutTourContainerRequest) (*daemon.CreateContainerResponse, error) {
        containerIDCounter++
        containerID := fmt.Sprintf("container-%d", containerIDCounter)
        return &daemon.CreateContainerResponse{ContainerID: containerID}, nil
    })

    ctx.daemonClient.SetStopContainerFunc(func(ctx context.Context, containerID string) error {
        return nil
    })

    // Routing client: default VRP behavior
    ctx.routingClient.SetPartitionFleetFunc(func(ctx context.Context, req *routing.PartitionFleetRequest) (*routing.PartitionFleetResponse, error) {
        // Default: assign all markets to first ship
        if len(req.ShipConfigs) > 0 && len(req.Markets) > 0 {
            return &routing.PartitionFleetResponse{
                Assignments: []*routing.ShipMarketAssignment{
                    {
                        ShipSymbol: req.ShipConfigs[0].ShipSymbol,
                        Markets:    req.Markets,
                    },
                },
            }, nil
        }
        return &routing.PartitionFleetResponse{Assignments: []*routing.ShipMarketAssignment{}}, nil
    })

    // Graph provider: return empty graph by default
    ctx.graphProvider.SetGetGraphFunc(func(ctx context.Context, systemSymbol string) (*system.Graph, error) {
        return &system.Graph{
            Nodes: make(map[string]*system.Waypoint),
            Edges: make(map[string]map[string]int),
        }, nil
    })

    // Assignment repository: default empty
    ctx.assignmentRepo.SetGetShipAssignmentsFunc(func(ctx context.Context, playerID shared.PlayerID, shipSymbol string) ([]*container.ShipAssignment, error) {
        return []*container.ShipAssignment{}, nil
    })

    // Market scanner: default success
    ctx.marketScanner.SetScanAndSaveFunc(func(ctx context.Context, playerID shared.PlayerID, waypointSymbol string) error {
        return nil
    })

    // Mediator: default success for NavigateRouteCommand
    ctx.mediator.SetSendFunc(func(ctx context.Context, request interface{}) (interface{}, error) {
        // Detect NavigateRouteCommand and return success
        return &commands.NavigateRouteResponse{}, nil
    })
}
```

## Test Scenario Details

### 1. assign_scouting_fleet.feature

```gherkin
Feature: Assign Scouting Fleet Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Successfully assign probe ships to markets
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a drone ship "SHIP-2" for player 1 at waypoint "X1-A2-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    And a marketplace "X1-A2-MARKET" in system "X1-A1"
    And a marketplace "X1-A3-MARKET" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And 2 ships should be assigned
    And container IDs should be returned
    And assignments should map ships to markets

  Scenario: Filter out fuel station marketplaces
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    And a fuel station marketplace "X1-A2-FUEL" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And 1 ship should be assigned
    And "X1-A2-FUEL" should not be in any assignment

  Scenario: No scout ships available
    Given a player with ID 1 and token "test-token" exists in the database
    And a frigate ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And 0 ships should be assigned
    And assignments should be empty

  Scenario: Reuse existing containers
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And "SHIP-1" has an existing container "container-old"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And the existing container should be stopped
    And a new container should be created for "SHIP-1"
    And reused containers should include "container-old"
```

### 2. scout_markets.feature

```gherkin
Feature: Scout Markets Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Single ship assigns all markets
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    And a marketplace "X1-A2-MARKET" in system "X1-A1"
    When I execute scout markets command for player 1 with ships ["SHIP-1"] and markets ["X1-A1-MARKET", "X1-A2-MARKET"] with 5 iterations
    Then the command should succeed
    And "SHIP-1" should be assigned all markets
    And 1 container should be created

  Scenario: Multiple ships use VRP optimization
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a probe ship "SHIP-2" for player 1 at waypoint "X1-A2-MARKET"
    And VRP assigns ["X1-A1-MARKET"] to "SHIP-1" and ["X1-A2-MARKET"] to "SHIP-2"
    When I execute scout markets command for player 1 with ships ["SHIP-1", "SHIP-2"] and markets ["X1-A1-MARKET", "X1-A2-MARKET"] with 10 iterations
    Then the command should succeed
    And "SHIP-1" should be assigned ["X1-A1-MARKET"]
    And "SHIP-2" should be assigned ["X1-A2-MARKET"]
    And 2 containers should be created

  Scenario: Container reuse (idempotent)
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And "SHIP-1" has an existing container "container-1"
    When I execute scout markets command for player 1 with ships ["SHIP-1"] and markets ["X1-A1-MARKET"] with 5 iterations
    Then the command should succeed
    And the existing container should be stopped
    And reused containers should include "container-1"
```

### 3. scout_tour.feature

```gherkin
Feature: Scout Tour Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Stationary scout (1 market)
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET"] with 1 iteration
    Then the command should succeed
    And the ship should navigate to "X1-A1-MARKET" once
    And the market should be scanned once
    And 1 market should be visited

  Scenario: Multi-market tour
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    And a marketplace "X1-A2-MARKET" in system "X1-A1"
    And a marketplace "X1-A3-MARKET" in system "X1-A1"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET", "X1-A2-MARKET", "X1-A3-MARKET"] with 2 iterations
    Then the command should succeed
    And the ship should navigate 6 times
    And all markets should be scanned 2 times each
    And 6 markets should be visited in total

  Scenario: Tour rotation starts from current location
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A2-MARKET"
    And a marketplace "X1-A1-MARKET" in system "X1-A1"
    And a marketplace "X1-A2-MARKET" in system "X1-A1"
    And a marketplace "X1-A3-MARKET" in system "X1-A1"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET", "X1-A2-MARKET", "X1-A3-MARKET"] with 1 iteration
    Then the command should succeed
    And the tour order should start with "X1-A2-MARKET"
    And the tour order should be ["X1-A2-MARKET", "X1-A3-MARKET", "X1-A1-MARKET"]

  Scenario: Empty markets list
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets [] with 1 iteration
    Then the command should return an error containing "no markets"
```

### 4. get_market_data.feature

```gherkin
Feature: Get Market Data Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Retrieve existing market data
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1
    When I execute get market data query for waypoint "X1-A1-MARKET" with player 1
    Then the query should succeed
    And the market data should be returned

  Scenario: Market not found
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute get market data query for waypoint "X1-NONEXISTENT" with player 1
    Then the query should return no market data
```

### 5. list_market_data.feature

```gherkin
Feature: List Market Data Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: List all markets in system
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1
    And market data exists for waypoint "X1-A2-MARKET" with player 1
    And market data exists for waypoint "X1-A3-MARKET" with player 1
    When I execute list market data query for system "X1-A1" with player 1 and max age 60 minutes
    Then the query should succeed
    And 3 markets should be returned

  Scenario: Filter by age
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1 scanned 10 minutes ago
    And market data exists for waypoint "X1-A2-MARKET" with player 1 scanned 70 minutes ago
    When I execute list market data query for system "X1-A1" with player 1 and max age 60 minutes
    Then the query should succeed
    And 1 market should be returned
    And "X1-A1-MARKET" should be in the results
    And "X1-A2-MARKET" should not be in the results

  Scenario: Empty results
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute list market data query for system "X1-EMPTY" with player 1 and max age 60 minutes
    Then the query should succeed
    And 0 markets should be returned
```

## Step Definition Categories

### Given Steps (Database & Test Fixtures)

**Player Setup**:
- `a player with ID (\d+) and token "([^"]*)" exists in the database`

**Ship Setup**:
- `a probe ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"`
- `a drone ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"`
- `a frigate ship "([^"]*)" for player (\d+) at waypoint "([^"]*)"`

**Waypoint Setup**:
- `a marketplace "([^"]*)" in system "([^"]*)"`
- `a fuel station marketplace "([^"]*)" in system "([^"]*)"`

**Container Setup**:
- `"([^"]*)" has an existing container "([^"]*)"`

**Market Data Setup**:
- `market data exists for waypoint "([^"]*)" with player (\d+)`
- `market data exists for waypoint "([^"]*)" with player (\d+) scanned (\d+) minutes ago`

**Mock Configuration**:
- `VRP assigns \["([^"]*)"\] to "([^"]*)" and \["([^"]*)"\] to "([^"]*)"`

**Time Setup**:
- `the current time is "([^"]*)"`

### When Steps (Command/Query Execution)

**Commands**:
- `I execute assign scouting fleet command for player (\d+) in system "([^"]*)"`
- `I execute scout markets command for player (\d+) with ships \[([^\]]*)\] and markets \[([^\]]*)\] with (\d+) iterations`
- `I execute scout tour command for player (\d+) with ship "([^"]*)" and markets \[([^\]]*)\] with (\d+) iterations`

**Queries**:
- `I execute get market data query for waypoint "([^"]*)" with player (\d+)`
- `I execute list market data query for system "([^"]*)" with player (\d+) and max age (\d+) minutes`

### Then Steps (Assertions)

**Success/Error**:
- `the command should succeed`
- `the query should succeed`
- `the command should return an error containing "([^"]*)"`
- `the query should return no market data`

**Response Assertions**:
- `(\d+) ships should be assigned`
- `(\d+) containers should be created`
- `(\d+) markets should be returned`
- `container IDs should be returned`
- `assignments should map ships to markets`
- `assignments should be empty`
- `the market data should be returned`

**Assignment Assertions**:
- `"([^"]*)" should be assigned all markets`
- `"([^"]*)" should be assigned \[([^\]]*)\]`
- `"([^"]*)" should not be in any assignment`
- `"([^"]*)" should be in the results`
- `"([^"]*)" should not be in the results`

**Container Assertions**:
- `the existing container should be stopped`
- `a new container should be created for "([^"]*)"`
- `reused containers should include "([^"]*)"`

**Navigation Assertions**:
- `the ship should navigate to "([^"]*)" once`
- `the ship should navigate (\d+) times`
- `the market should be scanned once`
- `all markets should be scanned (\d+) times each`
- `(\d+) markets should be visited in total`

**Tour Order Assertions**:
- `the tour order should start with "([^"]*)"`
- `the tour order should be \[([^\]]*)\]`

## Implementation Approach

### Phase 1: Setup & Infrastructure
1. Create feature file directory structure
2. Create `scouting_application_steps.go` with unified context
3. Implement `reset()` method with all repositories and mocks
4. Implement `setupDefaultMockBehaviors()` method
5. Register step definitions with Godog

### Phase 2: Given Steps (Fixtures)
1. Player setup steps (reuse from contract tests)
2. Ship fixture creation (probe, drone, frigate)
3. Waypoint fixture creation (marketplaces, fuel stations)
4. Container assignment fixtures
5. Market data fixtures with timestamps
6. VRP mock configuration steps

### Phase 3: When Steps (Execution)
1. AssignScoutingFleetCommand execution
2. ScoutMarketsCommand execution
3. ScoutTourCommand execution
4. GetMarketDataQuery execution
5. ListMarketDataQuery execution

### Phase 4: Then Steps (Assertions)
1. Success/error assertions
2. Response data assertions
3. Assignment verification
4. Container tracking verification
5. Navigation and scanning verification
6. Database persistence verification

### Phase 5: Feature Files
1. `assign_scouting_fleet.feature` - 4-5 scenarios
2. `scout_markets.feature` - 3-4 scenarios
3. `scout_tour.feature` - 4-5 scenarios
4. `get_market_data.feature` - 2 scenarios
5. `list_market_data.feature` - 3 scenarios

### Phase 6: Testing & Refinement
1. Run all scenarios: `make test-bdd`
2. Fix any failing scenarios
3. Add edge cases as needed
4. Verify database persistence
5. Verify mock call tracking

## Additional Requirements

### MarketRepository Implementation

Currently, the `MarketRepository` interface is defined in the queries file but may not have a GORM implementation. We need to:

1. Check if `GormMarketRepository` exists in `internal/adapters/persistence/`
2. If not, create it with methods:
   - `GetMarketData(ctx, playerID, waypointSymbol)` → `*market.Market`
   - `UpsertMarketData(ctx, playerID, waypointSymbol, goods, timestamp)` → error
   - `ListMarketsInSystem(ctx, playerID, systemSymbol, maxAgeMinutes)` → `[]market.Market`
3. Add `Market` model to `internal/adapters/persistence/models.go` if needed
4. Auto-migrate in SharedTestDB setup

### Test Helpers

May need to create or enhance:
1. `MockMediator` - For ScoutTour's NavigateRouteCommand dependency
2. `MockMarketScanner` - For ScoutTour's market scanning
3. Ship fixture builders for different frame types
4. Waypoint fixture builders with traits

## Success Criteria

1. ✅ All 5 feature files created with comprehensive scenarios
2. ✅ Unified context pattern eliminates step definition conflicts
3. ✅ Real GORM repositories used (PlayerRepository, MarketRepository)
4. ✅ All external dependencies mocked (API, Daemon, Routing, etc.)
5. ✅ All scenarios pass: `make test-bdd`
6. ✅ Database persistence verified in Then steps
7. ✅ Mock call tracking verified (container creation, VRP calls, etc.)
8. ✅ Code coverage maintained or improved
9. ✅ Tests follow BDD best practices (readable, maintainable, isolated)
10. ✅ Documentation updated if needed

## References

- Existing contract BDD tests: `test/bdd/features/application/accept_contract.feature`
- Contract step definitions: `test/bdd/steps/contract_application_steps.go`
- Mock helpers: `test/helpers/mock_*.go`
- Shared test DB: `test/helpers/shared_test_db.go`
- CLAUDE.md testing guidelines

## Timeline

**Estimated Effort**: 8-12 hours for complete implementation

**Breakdown**:
- Phase 1 (Setup): 1-2 hours
- Phase 2 (Given steps): 2-3 hours
- Phase 3 (When steps): 1-2 hours
- Phase 4 (Then steps): 2-3 hours
- Phase 5 (Feature files): 1-2 hours
- Phase 6 (Testing): 1-2 hours

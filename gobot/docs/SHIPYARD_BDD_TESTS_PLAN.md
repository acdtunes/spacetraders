# Shipyard BDD Tests Implementation Plan

**Status:** Planning
**Created:** 2025-11-21
**Pattern Source:** `test/bdd/features/application/contract/` tests

## Overview

Create comprehensive BDD tests for the shipyard application layer commands and queries, following the unified context pattern established by the contract tests. All tests will use in-memory SQLite with real repositories and mock the SpaceTraders API.

## Components Under Test

### 1. GetShipyardListingsQuery
- **Location:** `internal/application/shipyard/queries/get_shipyard_listings.go`
- **Purpose:** Retrieve available ships at a shipyard
- **Dependencies:** APIClient, PlayerRepository

### 2. PurchaseShipCommand
- **Location:** `internal/application/shipyard/commands/purchase_ship.go`
- **Purpose:** Purchase a single ship from a shipyard
- **Features:**
  - Auto-discovers nearest shipyard if not specified
  - Navigates purchasing ship to shipyard location
  - Docks ship if currently in orbit
  - Validates sufficient credits
  - Validates ship type availability
- **Dependencies:** ShipRepository, PlayerRepository, WaypointRepository, WaypointProvider, APIClient, Mediator

### 3. BatchPurchaseShipsCommand
- **Location:** `internal/application/shipyard/commands/batch_purchase_ships.go`
- **Purpose:** Purchase multiple ships of the same type
- **Features:**
  - Quantity-based purchases
  - Budget constraints
  - Partial success handling
  - Uses PurchaseShipCommand internally
- **Dependencies:** PlayerRepository, APIClient, Mediator

## Test Patterns from Contract Tests

### Unified Context Pattern

**Key Insight:** Use a single context struct for ALL shipyard-related test scenarios to eliminate step definition conflicts and share infrastructure.

```go
type shipyardApplicationContext struct {
    // Database and repositories (REAL, not mocked)
    db               *gorm.DB
    playerRepo       *persistence.GormPlayerRepository
    shipRepo         *persistence.GormShipRepository
    waypointRepo     *persistence.GormWaypointRepository

    // All handlers under test
    getListingsHandler      *queries.GetShipyardListingsHandler
    purchaseShipHandler     *commands.PurchaseShipHandler
    batchPurchaseHandler    *commands.BatchPurchaseShipsHandler

    // Mocks
    apiClient        *helpers.MockAPIClient
    mediator         *helpers.MockMediator
    waypointProvider *helpers.MockWaypointProvider

    // State tracking for assertions
    lastError        error
    lastShipyard     *shipyard.Shipyard
    lastPurchaseResp *commands.PurchaseShipResponse
    lastBatchResp    *commands.BatchPurchaseShipsResponse
    lastShip         *navigation.Ship
}
```

### Reset Pattern (Before Each Scenario)

1. Truncate all database tables
2. Get shared test database connection
3. Create REAL GORM repositories (not mocks)
4. Create mock API client with default behaviors
5. Create mock mediator with default behaviors
6. Create mock waypoint provider
7. Initialize all handlers with shared dependencies

### Test Lifecycle

**Given Steps:**
- Create test data in database using real repositories
- Configure mock API behaviors (shipyard data, purchase responses)
- Setup mock mediator responses (navigation, docking)
- Configure waypoint provider data

**When Steps:**
- Create command/query request object
- Create context with player token using `common.WithPlayerToken()`
- Execute handler's Handle() method
- Store results (error, response, entities)

**Then Steps:**
- Assert on lastError for success/failure
- Validate response data structure and content
- Reload entities from database to verify persistence
- Verify state changes (credits deducted, ship ownership transferred)
- Check domain entity states

## Implementation Plan

### Phase 1: Enhance MockAPIClient

**File:** `test/helpers/mock_api_client.go`

**Add shipyard-specific storage and handlers:**

```go
type MockAPIClient struct {
    // ... existing fields ...

    // Shipyard storage
    shipyards map[string]*infraports.ShipyardData

    // Custom function handlers for flexibility
    getShipyardFunc  func(ctx context.Context, token, waypointSymbol string) (*infraports.ShipyardData, error)
    purchaseShipFunc func(ctx context.Context, token, shipType, waypointSymbol string) (*infraports.ShipPurchaseResult, error)
}
```

**Methods to add/enhance:**

1. `SetShipyardData(waypointSymbol string, data *infraports.ShipyardData)`
   - Store shipyard data in mock for retrieval

2. `SetGetShipyardFunc(fn func(...) (*infraports.ShipyardData, error))`
   - Allow custom behavior injection

3. `SetPurchaseShipFunc(fn func(...) (*infraports.ShipPurchaseResult, error))`
   - Allow custom purchase behavior

4. `GetShipyard()` - Update to return stored data or use custom function

5. `PurchaseShip()` - Update to use custom function handler

6. `ResetShipyardMocks()` - Clear shipyard state for test isolation

### Phase 2: Create Mock Mediator

**New File:** `test/helpers/mock_mediator.go`

Required because PurchaseShipCommand internally uses:
- `NavigateRouteCommand` - to move purchasing ship to shipyard
- `DockShipCommand` - to dock ship before purchase

**Implementation:**

```go
type MockMediator struct {
    sendFunc func(ctx context.Context, request interface{}) (interface{}, error)
}

func (m *MockMediator) Send(ctx context.Context, request interface{}) (interface{}, error) {
    if m.sendFunc != nil {
        return m.sendFunc(ctx, request)
    }

    // Default behaviors based on request type
    switch req := request.(type) {
    case *commands.NavigateRouteCommand:
        return &commands.NavigateRouteResponse{Success: true}, nil
    case *commands.DockShipCommand:
        return &commands.DockShipResponse{Success: true}, nil
    default:
        return nil, fmt.Errorf("unsupported request type: %T", request)
    }
}

func (m *MockMediator) SetSendFunc(fn func(ctx context.Context, request interface{}) (interface{}, error)) {
    m.sendFunc = fn
}

func (m *MockMediator) RegisterHandler(handler interface{}) error {
    return nil // No-op for tests
}
```

### Phase 3: Create Mock WaypointProvider

**New File:** `test/helpers/mock_waypoint_provider.go`

Required for shipyard auto-discovery and navigation planning.

**Implementation:**

```go
type MockWaypointProvider struct {
    waypoints map[string]*system.Waypoint
    findFunc  func(systemSymbol string, filter func(*system.Waypoint) bool) ([]*system.Waypoint, error)
}

func (m *MockWaypointProvider) GetWaypoint(ctx context.Context, waypointSymbol string) (*system.Waypoint, error) {
    if wp, exists := m.waypoints[waypointSymbol]; exists {
        return wp, nil
    }
    return nil, fmt.Errorf("waypoint not found: %s", waypointSymbol)
}

func (m *MockWaypointProvider) FindWaypoints(ctx context.Context, systemSymbol string, filter func(*system.Waypoint) bool) ([]*system.Waypoint, error) {
    if m.findFunc != nil {
        return m.findFunc(systemSymbol, filter)
    }
    // Default: return all waypoints in system that match filter
    var results []*system.Waypoint
    for _, wp := range m.waypoints {
        if strings.HasPrefix(wp.Symbol(), systemSymbol) && filter(wp) {
            results = append(results, wp)
        }
    }
    return results, nil
}

func (m *MockWaypointProvider) SetWaypoint(wp *system.Waypoint) {
    m.waypoints[wp.Symbol()] = wp
}
```

### Phase 4: Create Test Fixtures

**New File:** `test/helpers/shipyard_fixtures.go`

**Helper functions for building test data:**

```go
// CreateTestShipyardData builds a ShipyardData with configurable listings
func CreateTestShipyardData(waypointSymbol string, listings ...infraports.ShipListingData) *infraports.ShipyardData {
    shipTypes := make([]infraports.ShipTypeInfo, len(listings))
    for i, listing := range listings {
        shipTypes[i] = infraports.ShipTypeInfo{Type: listing.Type}
    }

    return &infraports.ShipyardData{
        Symbol:          waypointSymbol,
        ShipTypes:       shipTypes,
        Ships:           listings,
        Transactions:    []map[string]interface{}{},
        ModificationFee: 0,
    }
}

// CreateTestShipListing builds a ShipListingData with sensible defaults
func CreateTestShipListing(shipType string, price int) infraports.ShipListingData {
    return infraports.ShipListingData{
        Type:          shipType,
        Name:          fmt.Sprintf("%s Ship", shipType),
        Description:   fmt.Sprintf("A %s class vessel", shipType),
        PurchasePrice: price,
        Frame:         map[string]interface{}{"symbol": "FRAME_" + shipType},
        Reactor:       map[string]interface{}{"symbol": "REACTOR_" + shipType},
        Engine:        map[string]interface{}{"symbol": "ENGINE_" + shipType},
        Modules:       []map[string]interface{}{},
        Mounts:        []map[string]interface{}{},
    }
}

// CreateTestShipPurchaseResult builds a ShipPurchaseResult
func CreateTestShipPurchaseResult(agentSymbol, shipSymbol, shipType, waypointSymbol string, price, newCredits int) *infraports.ShipPurchaseResult {
    return &infraports.ShipPurchaseResult{
        Agent: &player.AgentData{
            AccountID: agentSymbol,
            Symbol:    agentSymbol,
            Credits:   newCredits,
        },
        Ship: &navigation.ShipData{
            Symbol: shipSymbol,
            Nav: navigation.ShipNavData{
                WaypointSymbol: waypointSymbol,
                Status:         "DOCKED",
            },
        },
        Transaction: &infraports.ShipPurchaseTransaction{
            WaypointSymbol: waypointSymbol,
            ShipSymbol:     shipSymbol,
            ShipType:       shipType,
            Price:          price,
            AgentSymbol:    agentSymbol,
            Timestamp:      time.Now().Format(time.RFC3339),
        },
    }
}

// CreateTestWaypoint creates a waypoint with optional shipyard trait
func CreateTestWaypoint(symbol string, x, y int, hasShipyard bool) *system.Waypoint {
    traits := []string{}
    if hasShipyard {
        traits = append(traits, "SHIPYARD")
    }
    return system.NewWaypoint(symbol, "PLANET", x, y, traits)
}
```

### Phase 5: Create Feature File

**New File:** `test/bdd/features/application/shipyard/shipyard_operations.feature`

```gherkin
Feature: Shipyard Operations
  As a SpaceTraders agent
  I want to interact with shipyards to purchase ships
  So that I can expand my fleet

  Background:
    Given a player "TEST-AGENT" exists with 500000 credits
    And a ship "TEST-AGENT-1" exists for player "TEST-AGENT" at waypoint "X1-SYSTEM-A1"
    And the ship "TEST-AGENT-1" is docked
    And a waypoint "X1-SYSTEM-S1" exists with a shipyard at coordinates (100, 100)
    And a waypoint "X1-SYSTEM-A1" exists at coordinates (0, 0)

  # ============================================================================
  # GetShipyardListingsQuery Tests
  # ============================================================================

  Scenario: Get shipyard with multiple ship types available
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price   |
      | SHIP_MINING_DRONE | 50000   |
      | SHIP_PROBE        | 25000   |
      | SHIP_LIGHT_HAULER | 100000  |
    When I query shipyard listings for "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the query should succeed
    And the shipyard should have 3 ship types available
    And the shipyard should have a listing for "SHIP_MINING_DRONE" priced at 50000
    And the shipyard should have a listing for "SHIP_PROBE" priced at 25000
    And the shipyard should have a listing for "SHIP_LIGHT_HAULER" priced at 100000

  Scenario: Get shipyard with no ships available
    Given the shipyard at "X1-SYSTEM-S1" has no ships for sale
    When I query shipyard listings for "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the query should succeed
    And the shipyard should have 0 ship types available

  Scenario: Query shipyard listings fails when player not found
    When I query shipyard listings for "X1-SYSTEM-S1" as "NONEXISTENT-PLAYER"
    Then the query should fail with error "player not found"

  Scenario: Query shipyard listings fails when API returns error
    Given the API will return an error when getting shipyard "X1-SYSTEM-S1"
    When I query shipyard listings for "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the query should fail

  # ============================================================================
  # PurchaseShipCommand Tests
  # ============================================================================

  Scenario: Purchase ship when already docked at shipyard
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the player "TEST-AGENT" should have 450000 credits remaining
    And a new ship should be created for player "TEST-AGENT"
    And the new ship should be at waypoint "X1-SYSTEM-S1"
    And the new ship should be docked

  Scenario: Purchase ship when at different location (requires navigation)
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will succeed from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the mediator should have been called to navigate from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase ship when in orbit at shipyard (requires docking)
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is in orbit
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should succeed
    And the mediator should have been called to dock the ship
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase ship with auto-discovered nearest shipyard
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And waypoint "X1-SYSTEM-S1" is the nearest shipyard to "X1-SYSTEM-A1"
    And navigation will succeed from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" without specifying shipyard as "TEST-AGENT"
    Then the purchase should succeed
    And the shipyard "X1-SYSTEM-S1" should have been auto-discovered
    And the player "TEST-AGENT" should have 450000 credits remaining

  Scenario: Purchase fails when insufficient credits
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price   |
      | SHIP_MINING_DRONE | 600000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "insufficient credits"

  Scenario: Purchase fails when ship type not available
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "ship type not available"

  Scenario: Purchase fails when no shipyards in system
    Given there are no shipyards in system "X1-SYSTEM"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" without specifying shipyard as "TEST-AGENT"
    Then the purchase should fail with error "no shipyards found"

  Scenario: Purchase fails when purchasing ship not found
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    When I purchase a "SHIP_MINING_DRONE" ship using "NONEXISTENT-SHIP" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail with error "ship not found"

  Scenario: Purchase fails when API purchase fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    And the API will return an error when purchasing a ship
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail

  Scenario: Purchase fails when navigation fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will fail from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I purchase a "SHIP_MINING_DRONE" ship using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the purchase should fail

  # ============================================================================
  # BatchPurchaseShipsCommand Tests
  # ============================================================================

  Scenario: Batch purchase multiple ships successfully
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 3 ships should have been purchased
    And the player "TEST-AGENT" should have 350000 credits remaining
    And all purchased ships should be at waypoint "X1-SYSTEM-S1"

  Scenario: Batch purchase limited by quantity
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 5 "SHIP_PROBE" ships with max budget 200000 using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 5 ships should have been purchased
    And the player "TEST-AGENT" should have 375000 credits remaining

  Scenario: Batch purchase limited by budget
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 10 "SHIP_MINING_DRONE" ships with max budget 125000 using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 2 ships should have been purchased
    And the player "TEST-AGENT" should have 400000 credits remaining

  Scenario: Batch purchase limited by player credits
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 20 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 10 ships should have been purchased
    And the player "TEST-AGENT" should have 0 credits remaining

  Scenario: Batch purchase with partial success (runs out of credits mid-batch)
    Given the player "TEST-AGENT" has 125000 credits
    And the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 5 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed with partial results
    And 2 ships should have been purchased
    And the player "TEST-AGENT" should have 25000 credits remaining

  Scenario: Batch purchase with zero quantity returns empty result
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    When I batch purchase 0 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should succeed
    And 0 ships should have been purchased
    And the player "TEST-AGENT" should have 500000 credits remaining

  Scenario: Batch purchase fails when first purchase fails
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_MINING_DRONE | 50000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-A1"
    And navigation will fail from "X1-SYSTEM-A1" to "X1-SYSTEM-S1"
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should fail
    And 0 ships should have been purchased

  Scenario: Batch purchase fails when ship type not available
    Given the shipyard at "X1-SYSTEM-S1" has the following ships:
      | Type              | Price  |
      | SHIP_PROBE        | 25000  |
    And the ship "TEST-AGENT-1" is at waypoint "X1-SYSTEM-S1"
    And the ship "TEST-AGENT-1" is docked
    When I batch purchase 3 "SHIP_MINING_DRONE" ships using "TEST-AGENT-1" at "X1-SYSTEM-S1" as "TEST-AGENT"
    Then the batch purchase should fail with error "ship type not available"
```

### Phase 6: Create Step Definitions

**New File:** `test/bdd/steps/shipyard_application_steps.go`

**Structure outline:**

```go
package steps

import (
    "context"
    "fmt"
    "github.com/cucumber/godog"
    "gorm.io/gorm"
    // ... other imports
)

// Unified context for all shipyard application tests
type shipyardApplicationContext struct {
    // Database
    db *gorm.DB

    // Real repositories (not mocked)
    playerRepo   *persistence.GormPlayerRepository
    shipRepo     *persistence.GormShipRepository
    waypointRepo *persistence.GormWaypointRepository

    // Handlers under test
    getListingsHandler   *queries.GetShipyardListingsHandler
    purchaseShipHandler  *commands.PurchaseShipHandler
    batchPurchaseHandler *commands.BatchPurchaseShipsHandler

    // Mocks
    apiClient        *helpers.MockAPIClient
    mediator         *helpers.MockMediator
    waypointProvider *helpers.MockWaypointProvider

    // State tracking for assertions
    lastError            error
    lastShipyard         *shipyard.Shipyard
    lastPurchaseResp     *commands.PurchaseShipResponse
    lastBatchResp        *commands.BatchPurchaseShipsResponse
    lastShip             *navigation.Ship
    mediatorCallLog      []string  // Track mediator calls
    autoDiscoveredShipyard string
}

// Reset before each scenario
func resetShipyardApplicationContext() *shipyardApplicationContext {
    // 1. Truncate all tables
    helpers.TruncateAllTables()

    // 2. Get shared test DB
    db := helpers.SharedTestDB

    // 3. Create real repositories
    playerRepo := persistence.NewGormPlayerRepository(db)
    shipRepo := persistence.NewGormShipRepository(db)
    waypointRepo := persistence.NewGormWaypointRepository(db)

    // 4. Create mock API client
    apiClient := helpers.NewMockAPIClient()
    // Set default behaviors...

    // 5. Create mock mediator
    mediator := helpers.NewMockMediator()

    // 6. Create mock waypoint provider
    waypointProvider := helpers.NewMockWaypointProvider()

    // 7. Create handlers
    getListingsHandler := queries.NewGetShipyardListingsHandler(apiClient, playerRepo)
    purchaseShipHandler := commands.NewPurchaseShipHandler(
        shipRepo, playerRepo, waypointRepo, waypointProvider, apiClient, mediator,
    )
    batchPurchaseHandler := commands.NewBatchPurchaseShipsHandler(
        playerRepo, apiClient, mediator,
    )

    return &shipyardApplicationContext{
        db:                   db,
        playerRepo:           playerRepo,
        shipRepo:             shipRepo,
        waypointRepo:         waypointRepo,
        getListingsHandler:   getListingsHandler,
        purchaseShipHandler:  purchaseShipHandler,
        batchPurchaseHandler: batchPurchaseHandler,
        apiClient:            apiClient,
        mediator:             mediator,
        waypointProvider:     waypointProvider,
        mediatorCallLog:      []string{},
    }
}

// ============================================================================
// Given Steps (Test Setup)
// ============================================================================

func (ctx *shipyardApplicationContext) aPlayerExistsWithCredits(agentSymbol string, credits int) error
func (ctx *shipyardApplicationContext) aShipExistsForPlayerAtWaypoint(shipSymbol, agentSymbol, waypointSymbol string) error
func (ctx *shipyardApplicationContext) theShipIsDocked(shipSymbol string) error
func (ctx *shipyardApplicationContext) theShipIsInOrbit(shipSymbol string) error
func (ctx *shipyardApplicationContext) aWaypointExistsWithShipyardAtCoordinates(waypointSymbol string, x, y int) error
func (ctx *shipyardApplicationContext) aWaypointExistsAtCoordinates(waypointSymbol string, x, y int) error
func (ctx *shipyardApplicationContext) theShipyardHasTheFollowingShips(waypointSymbol string, table *godog.Table) error
func (ctx *shipyardApplicationContext) theShipyardHasNoShipsForSale(waypointSymbol string) error
func (ctx *shipyardApplicationContext) theAPIWillReturnAnErrorWhenGettingShipyard(waypointSymbol string) error
func (ctx *shipyardApplicationContext) theShipIsAtWaypoint(shipSymbol, waypointSymbol string) error
func (ctx *shipyardApplicationContext) navigationWillSucceedFromTo(fromWaypoint, toWaypoint string) error
func (ctx *shipyardApplicationContext) navigationWillFailFromTo(fromWaypoint, toWaypoint string) error
func (ctx *shipyardApplicationContext) waypointIsTheNearestShipyardTo(shipyardWaypoint, fromWaypoint string) error
func (ctx *shipyardApplicationContext) thereAreNoShipyardsInSystem(systemSymbol string) error
func (ctx *shipyardApplicationContext) theAPIWillReturnAnErrorWhenPurchasingAShip() error
func (ctx *shipyardApplicationContext) thePlayerHasCredits(agentSymbol string, credits int) error

// ============================================================================
// When Steps (Execute Commands/Queries)
// ============================================================================

func (ctx *shipyardApplicationContext) iQueryShipyardListingsFor(waypointSymbol, agentSymbol string) error
func (ctx *shipyardApplicationContext) iPurchaseAShipUsingAt(shipType, purchasingShipSymbol, waypointSymbol, agentSymbol string) error
func (ctx *shipyardApplicationContext) iPurchaseAShipUsingWithoutSpecifyingShipyard(shipType, purchasingShipSymbol, agentSymbol string) error
func (ctx *shipyardApplicationContext) iBatchPurchaseShipsUsingAt(quantity int, shipType, purchasingShipSymbol, waypointSymbol, agentSymbol string) error
func (ctx *shipyardApplicationContext) iBatchPurchaseShipsWithMaxBudgetUsingAt(quantity int, shipType string, maxBudget int, purchasingShipSymbol, waypointSymbol, agentSymbol string) error

// ============================================================================
// Then Steps (Assertions)
// ============================================================================

func (ctx *shipyardApplicationContext) theQueryShouldSucceed() error
func (ctx *shipyardApplicationContext) theQueryShouldFail() error
func (ctx *shipyardApplicationContext) theQueryShouldFailWithError(expectedError string) error
func (ctx *shipyardApplicationContext) theShipyardShouldHaveShipTypesAvailable(expectedCount int) error
func (ctx *shipyardApplicationContext) theShipyardShouldHaveAListingForPricedAt(shipType string, price int) error
func (ctx *shipyardApplicationContext) thePurchaseShouldSucceed() error
func (ctx *shipyardApplicationContext) thePurchaseShouldFail() error
func (ctx *shipyardApplicationContext) thePurchaseShouldFailWithError(expectedError string) error
func (ctx *shipyardApplicationContext) thePlayerShouldHaveCreditsRemaining(agentSymbol string, expectedCredits int) error
func (ctx *shipyardApplicationContext) aNewShipShouldBeCreatedForPlayer(agentSymbol string) error
func (ctx *shipyardApplicationContext) theNewShipShouldBeAtWaypoint(waypointSymbol string) error
func (ctx *shipyardApplicationContext) theNewShipShouldBeDocked() error
func (ctx *shipyardApplicationContext) theMediatorShouldHaveBeenCalledToNavigateFromTo(fromWaypoint, toWaypoint string) error
func (ctx *shipyardApplicationContext) theMediatorShouldHaveBeenCalledToDockTheShip() error
func (ctx *shipyardApplicationContext) theShipyardShouldHaveBeenAutoDiscovered(expectedShipyard string) error
func (ctx *shipyardApplicationContext) theBatchPurchaseShouldSucceed() error
func (ctx *shipyardApplicationContext) theBatchPurchaseShouldSucceedWithPartialResults() error
func (ctx *shipyardApplicationContext) theBatchPurchaseShouldFail() error
func (ctx *shipyardApplicationContext) theBatchPurchaseShouldFailWithError(expectedError string) error
func (ctx *shipyardApplicationContext) shipsShouldHaveBeenPurchased(expectedCount int) error
func (ctx *shipyardApplicationContext) allPurchasedShipsShouldBeAtWaypoint(waypointSymbol string) error

// Register steps with godog
func InitializeShipyardApplicationScenario(sc *godog.ScenarioContext) {
    ctx := resetShipyardApplicationContext()

    // Reset before each scenario
    sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
        ctx = resetShipyardApplicationContext()
        return ctx, nil
    })

    // Register Given steps
    sc.Step(`^a player "([^"]*)" exists with (\d+) credits$`, ctx.aPlayerExistsWithCredits)
    // ... all other steps ...
}
```

## Testing Strategy

### Success Paths to Cover

1. **Query Operations:**
   - ✅ Get shipyard with listings
   - ✅ Get empty shipyard
   - ✅ Data conversion accuracy

2. **Single Purchase:**
   - ✅ Purchase when already at shipyard and docked
   - ✅ Purchase when navigation required
   - ✅ Purchase when docking required
   - ✅ Auto-discovery of nearest shipyard

3. **Batch Purchase:**
   - ✅ Multiple successful purchases
   - ✅ Limited by quantity
   - ✅ Limited by budget
   - ✅ Limited by player credits
   - ✅ Partial success scenarios
   - ✅ Zero quantity edge case

### Error Paths to Cover

1. **Query Errors:**
   - ✅ Player not found
   - ✅ API failure

2. **Purchase Errors:**
   - ✅ Insufficient credits
   - ✅ Ship type not available
   - ✅ No shipyards found
   - ✅ Purchasing ship not found
   - ✅ API purchase failure
   - ✅ Navigation failure
   - ✅ Player not found

3. **Batch Purchase Errors:**
   - ✅ First purchase fails
   - ✅ Ship type not available

## Verification Checklist

After implementation, verify:

- [ ] All 25+ scenarios pass
- [ ] Real repositories used (SQLite in-memory)
- [ ] SpaceTraders API properly mocked
- [ ] No test pollution between scenarios
- [ ] State properly reset before each scenario
- [ ] Database persistence verified in assertions
- [ ] Mock behaviors correctly configured
- [ ] Error messages properly asserted
- [ ] Credits deduction verified
- [ ] Ship ownership transfer verified
- [ ] Navigation/docking calls tracked
- [ ] Follows unified context pattern from contract tests
- [ ] Code coverage for all handlers
- [ ] `make test-bdd` passes without regressions

## Files Summary

### New Files (5):
1. `test/bdd/features/application/shipyard/shipyard_operations.feature` - Feature file with 25+ scenarios
2. `test/bdd/steps/shipyard_application_steps.go` - Step definitions with unified context
3. `test/helpers/mock_mediator.go` - Mock mediator for command orchestration
4. `test/helpers/mock_waypoint_provider.go` - Mock waypoint provider for shipyard discovery
5. `test/helpers/shipyard_fixtures.go` - Test data builders

### Modified Files (1):
1. `test/helpers/mock_api_client.go` - Enhanced with shipyard storage and handlers

## Implementation Notes

1. **Unified Context Pattern:** Single context eliminates step definition conflicts
2. **Real Repositories:** Use GORM with in-memory SQLite, matching contract test pattern
3. **Idempotent Setup:** Truncate all tables before each scenario
4. **Mock Flexibility:** Custom function handlers allow scenario-specific behaviors
5. **Token Context:** Use `common.WithPlayerToken()` for authentication
6. **Persistence Verification:** Always reload from DB to verify saves
7. **Error Handling:** Test both success and failure paths comprehensively
8. **State Tracking:** Log mediator calls for verification in assertions

## Next Steps

1. Implement Phase 1: Enhance MockAPIClient
2. Implement Phase 2: Create MockMediator
3. Implement Phase 3: Create MockWaypointProvider
4. Implement Phase 4: Create test fixtures
5. Implement Phase 5: Create feature file
6. Implement Phase 6: Create step definitions
7. Run tests and iterate
8. Verify coverage and patterns

---

**End of Plan**

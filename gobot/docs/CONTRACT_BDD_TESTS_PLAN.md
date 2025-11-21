# Contract Application Layer BDD Tests - Implementation Plan

## Overview

This document outlines the plan to create comprehensive BDD tests for all contract application layer commands and queries. The tests will use real GORM repositories with shared in-memory SQLite database, mock only the API client, and implement Clock interface for time operations to ensure fast test execution.

## Testing Strategy

### What We Test
- **Application layer commands:** Accept, Deliver, Fulfill, Negotiate, Rebalance, Run Fleet Coordinator, Run Contract Workflow
- **Application layer queries:** Evaluate Contract Profitability
- **Integration points:** Command handlers orchestrating domain entities, repositories, and API calls

### What We Mock
- ✅ **API Client** - Mock SpaceTraders HTTP API
- ✅ **Clock** - Mock time operations for fast tests
- ❌ **Repositories** - Use REAL GORM repositories with in-memory SQLite
- ❌ **Database** - Use REAL SQLite (in-memory) to test actual SQL operations

### Key Principles
1. **Real persistence layer** - Test actual repository implementations and SQL
2. **Isolated scenarios** - `TruncateAllTables()` before each scenario
3. **Fast execution** - MockClock makes all sleeps/waits instant
4. **Backward compatible** - Production code defaults to RealClock

## Historical Context

Previously, comprehensive BDD tests existed for contract commands but were deleted in commit `7c18367`. Key files that existed:

**Feature files (deleted):**
- `test/bdd/features/application/accept_contract.feature` (25 lines, 4 scenarios)
- `test/bdd/features/application/deliver_contract.feature` (51 lines, 6 scenarios)
- `test/bdd/features/application/fulfill_contract.feature` (43 lines, 5 scenarios)
- `test/bdd/features/application/negotiate_contract.feature` (112 lines, 5 scenarios)
- `test/bdd/features/application/evaluate_contract_profitability.feature` (94 lines, 8 scenarios)

**Step definitions (deleted):**
- `test/bdd/steps/accept_contract_steps.go`
- `test/bdd/steps/deliver_contract_steps.go`
- `test/bdd/steps/negotiate_contract_steps.go`

These tests used:
- `helpers.SharedTestDB` - SQLite `:memory:` database
- Real GORM repositories (GormContractRepository, GormPlayerRepository)
- `helpers.TruncateAllTables()` for test isolation
- Mock API client only

## Phase 1: Refactor Code to Use Clock Interface

### Existing Clock Infrastructure

The codebase already has Clock interface in `internal/domain/shared/clock.go`:

```go
type Clock interface {
    Now() time.Time
    Sleep(d time.Duration)
}

type RealClock struct{}  // Uses actual time.Now() and time.Sleep()
type MockClock struct {  // Instant operations for tests
    CurrentTime time.Time
}
```

### 1.1 Update Contract Domain Entity

**File:** `internal/domain/contract/contract.go`

**Current Issue:** Line 139 uses `time.Now()` directly in `IsExpired()` method

**Changes:**
```go
type Contract struct {
    contractID    string
    playerID      shared.PlayerID
    factionSymbol string
    contractType  string
    terms         Terms
    accepted      bool
    fulfilled     bool
    clock         shared.Clock  // ADD THIS
}

// Update constructor to accept optional Clock parameter
func NewContract(
    contractID string,
    playerID shared.PlayerID,
    factionSymbol string,
    contractType string,
    terms Terms,
    clock shared.Clock,  // ADD THIS (optional, defaults to RealClock)
) (*Contract, error) {
    if clock == nil {
        clock = shared.NewRealClock()  // Production default
    }

    // ... existing validation ...

    return &Contract{
        contractID:    contractID,
        playerID:      playerID,
        factionSymbol: factionSymbol,
        contractType:  contractType,
        terms:         terms,
        accepted:      false,
        fulfilled:     false,
        clock:         clock,  // ADD THIS
    }, nil
}

// Update IsExpired to use clock
func (c *Contract) IsExpired() bool {
    deadline, err := time.Parse(time.RFC3339, c.terms.Deadline)
    if err != nil {
        return false
    }
    return c.clock.Now().UTC().After(deadline)  // Use c.clock instead of time.Now()
}
```

**Impact:**
- All existing calls to `NewContract()` need to pass `nil` for clock (or omit if made optional via variadic params)
- Production code automatically uses RealClock
- Tests can inject MockClock for instant time control

### 1.2 Update Fleet Coordinator Command

**File:** `internal/application/contract/commands/run_fleet_coordinator.go`

**Current Issues:**
- Multiple `time.Sleep()` calls: 10s, 30s sleeps throughout
- Multiple `time.After()` calls: 30s, 1 minute, 30 minute timeouts
- Currently uses `time.Now()` for logging timestamps

**Changes:**
```go
type RunFleetCoordinatorHandler struct {
    // ... existing fields ...
    clock shared.Clock  // ADD THIS
}

func NewRunFleetCoordinatorHandler(
    // ... existing params ...
    clock shared.Clock,  // ADD THIS (optional)
) *RunFleetCoordinatorHandler {
    if clock == nil {
        clock = shared.NewRealClock()
    }
    return &RunFleetCoordinatorHandler{
        // ... existing fields ...
        clock: clock,
    }
}

// Replace all time operations:
// time.Sleep(10 * time.Second) → h.clock.Sleep(10 * time.Second)
// time.Sleep(30 * time.Second) → h.clock.Sleep(30 * time.Second)
// time.Now() → h.clock.Now()

// For time.After(), use pattern like:
timer := time.NewTimer(30 * time.Second)
// becomes:
sleepDone := make(chan struct{})
go func() {
    h.clock.Sleep(30 * time.Second)
    close(sleepDone)
}()
select {
    case <-sleepDone:
    // ...
}
```

**Test Impact:**
- With MockClock: `h.clock.Sleep(30 * time.Minute)` completes instantly
- Tests run in milliseconds instead of actual wait times
- Time advances predictably: `mockClock.Advance(1 * time.Hour)`

### 1.3 Check Other Command Handlers

**Files to audit for time operations:**
- `internal/application/contract/commands/run_contract_workflow.go`
- `internal/application/contract/commands/rebalance_fleet.go`
- Any other handlers using `time.Now()`, `time.Sleep()`, `time.After()`

**Pattern to find:**
```bash
grep -r "time\.Now\|time\.Sleep\|time\.After" internal/application/contract/commands/
```

**If found, apply same Clock injection pattern.**

### 1.4 Update Persistence Layer (If Needed)

**Check:** Do any repositories use `time.Now()` for timestamps?
- If yes, inject Clock into repository constructors
- If using GORM auto-timestamps (CreatedAt, UpdatedAt), no changes needed

## Phase 2: Test Infrastructure Setup

### 2.1 Update BDD Test Suite

**File:** `test/bdd/bdd_test.go`

**Changes:**

```go
func TestFeatures(t *testing.T) {
    suite := godog.TestSuite{
        ScenarioInitializer: InitializeScenario,
        Options: &godog.Options{
            Format:   "pretty",
            Paths:    []string{
                "features/domain",
                "features/utils",
                "features/application",  // ADD THIS
            },
            TestingT: t,
        },
    }

    if suite.Run() != 0 {
        t.Fatal("non-zero status returned, failed to run feature tests")
    }
}

func InitializeScenario(sc *godog.ScenarioContext) {
    // ... existing registrations ...

    // Contract application layer tests (ADD THESE)
    steps.InitializeAcceptContractHandlerScenario(sc)
    steps.InitializeDeliverContractHandlerScenario(sc)
    steps.InitializeFulfillContractHandlerScenario(sc)
    steps.InitializeNegotiateContractHandlerScenario(sc)
    steps.InitializeEvaluateProfitabilityHandlerScenario(sc)
    steps.InitializeRebalanceFleetHandlerScenario(sc)
    steps.InitializeRunFleetCoordinatorHandlerScenario(sc)
    steps.InitializeRunContractWorkflowHandlerScenario(sc)
}
```

### 2.2 Create Context Helper

**File:** `test/helpers/context_helpers.go` (NEW)

```go
package helpers

import (
    "context"
    "github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// ContextWithToken creates a context with player token for command handlers
// This is required because handlers call common.PlayerTokenFromContext(ctx)
func ContextWithToken(token string) context.Context {
    ctx := context.Background()
    return common.ContextWithPlayerToken(ctx, token)
}
```

**Note:** Check if `common.ContextWithPlayerToken()` exists. If not, create it or use the existing pattern in the codebase.

### 2.3 Verify Shared Test DB

**File:** `test/helpers/shared_test_db.go` (already exists)

**Verify it includes:**
- `SharedTestDB *gorm.DB` - Singleton SQLite `:memory:` instance
- `InitializeSharedTestDB()` - Called once in TestMain
- `TruncateAllTables()` - Clears all data before each scenario
- `ContractModel` in AutoMigrate list

**No changes needed if already correct.**

## Phase 3: Create Application Feature Files

### Directory Structure

Create: `test/bdd/features/application/`

This will contain 8 feature files covering all contract commands and queries.

### 3.1 Accept Contract Feature

**File:** `test/bdd/features/application/accept_contract.feature`

```gherkin
Feature: Accept Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Accept unaccepted contract
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-1" for player 1 in the database
    When I execute accept contract command for "CONTRACT-1" with player 1
    Then the command should succeed
    And the contract should be marked as accepted
    And the contract should still not be fulfilled
    And the contract should be persisted with accepted status

  Scenario: Accept already accepted contract
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 in the database
    When I try to execute accept contract command for "CONTRACT-2" with player 1
    Then the command should return an error containing "contract already accepted"

  Scenario: Accept non-existent contract
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute accept contract command for "NON-EXISTENT" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: Accept contract with API integration
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-3" for player 1 in the database
    And the API will successfully accept the contract
    When I execute accept contract command for "CONTRACT-3" with player 1
    Then the command should succeed
    And the contract should be persisted with accepted status
```

**Estimated:** ~30 lines, 4 scenarios

### 3.2 Deliver Contract Feature

**File:** `test/bdd/features/application/deliver_contract.feature`

```gherkin
Feature: Deliver Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Deliver cargo for valid delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-1" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I execute deliver contract command for "CONTRACT-1" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should succeed
    And the delivery for "IRON_ORE" should show 50 units fulfilled

  Scenario: Deliver remaining cargo completes delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 with 50 of 100 "IRON_ORE" already delivered to waypoint "X1-A1"
    When I execute deliver contract command for "CONTRACT-2" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should succeed
    And the delivery for "IRON_ORE" should show 100 units fulfilled

  Scenario: Cannot deliver more than required
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-3" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-3" with 150 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "exceeds required"

  Scenario: Cannot deliver invalid trade symbol
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-4" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-4" with 50 units of "COPPER_ORE" from ship "SHIP-1"
    Then the command should return an error containing "invalid trade symbol"

  Scenario: Contract not found error
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute deliver contract command for "NON-EXISTENT" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "contract not found"

  Scenario: Player not found error
    Given an accepted contract "CONTRACT-5" for player 999 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-5" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "player not found"
```

**Estimated:** ~60 lines, 6 scenarios

### 3.3 Fulfill Contract Feature

**File:** `test/bdd/features/application/fulfill_contract.feature`

```gherkin
Feature: Fulfill Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Fulfill contract with all deliveries complete
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-1" for player 1 with all deliveries complete
    When I execute fulfill contract command for "CONTRACT-1" with player 1
    Then the command should succeed
    And the contract should be marked as fulfilled

  Scenario: Cannot fulfill contract with incomplete deliveries
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 with incomplete deliveries
    When I try to execute fulfill contract command for "CONTRACT-2" with player 1
    Then the command should return an error containing "deliveries not complete"

  Scenario: Cannot fulfill non-existent contract
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute fulfill contract command for "NON-EXISTENT" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: Cannot fulfill contract for wrong player
    Given a player with ID 1 and token "test-token" exists in the database
    And a player with ID 2 and token "test-token-2" exists in the database
    And an accepted contract "CONTRACT-3" for player 2 with all deliveries complete
    When I try to execute fulfill contract command for "CONTRACT-3" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: API integration success
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-4" for player 1 with all deliveries complete
    And the API will successfully fulfill the contract
    When I execute fulfill contract command for "CONTRACT-4" with player 1
    Then the command should succeed
    And the contract should be persisted with fulfilled status
```

**Estimated:** ~50 lines, 5 scenarios

### 3.4 Negotiate Contract Feature

**File:** `test/bdd/features/application/negotiate_contract.feature`

```gherkin
Feature: Negotiate Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Negotiate new contract successfully
    Given a player with ID 1 and token "test-token" exists in the database
    And a docked ship "SHIP-1" for player 1 exists in the database
    And the API will successfully negotiate a contract
    When I execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should succeed
    And a new contract should be created in the database

  Scenario: Resume existing contract (error 4511)
    Given a player with ID 1 and token "test-token" exists in the database
    And a docked ship "SHIP-1" for player 1 exists in the database
    And an existing unaccepted contract "CONTRACT-1" for player 1 exists
    And the API will return error 4511 for contract negotiation
    When I execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should succeed
    And the existing contract "CONTRACT-1" should be returned

  Scenario: Cannot negotiate with undocked ship
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" in orbit for player 1 exists in the database
    When I try to execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should return an error containing "ship must be docked"

  Scenario: Cannot negotiate with ship in transit
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" in transit for player 1 exists in the database
    When I try to execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should return an error containing "ship must be docked"

  Scenario: Ship not found error
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute negotiate contract command for ship "NON-EXISTENT" with player 1
    Then the command should return an error containing "ship not found"

  Scenario: Player not found error
    When I try to execute negotiate contract command for ship "SHIP-1" with player 999
    Then the command should return an error containing "player not found"
```

**Estimated:** ~70 lines, 6 scenarios

### 3.5 Evaluate Profitability Query

**File:** `test/bdd/features/application/evaluate_contract_profitability.feature`

```gherkin
Feature: Evaluate Contract Profitability Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Profitable contract with single delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    And an unaccepted contract "CONTRACT-1" with payment 100000/200000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 500 credits at "X1-MARKET"
    And fuel cost per trip is 10000 credits
    When I execute evaluate profitability query for "CONTRACT-1" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable
    And net profit should be 240000
    And total payment should be 300000
    And purchase cost should be 50000
    And fuel cost should be 10000

  Scenario: Acceptable small loss within threshold
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 50 for player 1 exists
    And an unaccepted contract "CONTRACT-2" with payment 50000/100000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 1200 credits at "X1-MARKET"
    And fuel cost per trip is 5000 credits
    When I execute evaluate profitability query for "CONTRACT-2" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable
    And profitability reason should be "acceptable loss within threshold"

  Scenario: Unacceptable loss exceeding threshold
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 50 for player 1 exists
    And an unaccepted contract "CONTRACT-3" with payment 10000/20000 requiring 100 "IRON_ORE"
    And market price for "IRON_ORE" is 1500 credits at "X1-MARKET"
    And fuel cost per trip is 10000 credits
    When I execute evaluate profitability query for "CONTRACT-3" with ship "SHIP-1"
    Then the query should succeed
    And the contract should not be profitable
    And profitability reason should contain "loss exceeds"

  Scenario: Multi-delivery contract
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 200 for player 1 exists
    And an unaccepted contract "CONTRACT-4" with payment 150000/300000 requiring:
      | TradeSymbol | Units |
      | IRON_ORE    | 100   |
      | COPPER_ORE  | 100   |
    And market prices:
      | TradeSymbol | Price |
      | IRON_ORE    | 500   |
      | COPPER_ORE  | 600   |
    And fuel cost per trip is 15000 credits
    When I execute evaluate profitability query for "CONTRACT-4" with ship "SHIP-1"
    Then the query should succeed
    And the contract should be evaluated as profitable

  Scenario: No cheapest market found
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    And an unaccepted contract "CONTRACT-5" with payment 100000/200000 requiring 100 "IRON_ORE"
    And no market data exists for "IRON_ORE"
    When I try to execute evaluate profitability query for "CONTRACT-5" with ship "SHIP-1"
    Then the query should return an error containing "no market found"

  Scenario: Ship not found error
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-6" with payment 100000/200000 requiring 100 "IRON_ORE"
    When I try to execute evaluate profitability query for "CONTRACT-6" with ship "NON-EXISTENT"
    Then the query should return an error containing "ship not found"

  Scenario: Contract not found error
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" with cargo capacity 100 for player 1 exists
    When I try to execute evaluate profitability query for "NON-EXISTENT" with ship "SHIP-1"
    Then the query should return an error containing "contract not found"
```

**Estimated:** ~110 lines, 7 scenarios

### 3.6-3.8 Workflow Command Features

**Files to create:**
- `rebalance_fleet.feature` (~60 lines, 4-6 scenarios)
- `run_fleet_coordinator.feature` (~80 lines, 5-7 scenarios)
- `run_contract_workflow.feature` (~120 lines, 6-8 scenarios)

**Note:** These are complex workflow commands. Start with basic happy path scenarios, then add error handling cases. Focus on testing:
- Coordinator logic and ship assignment
- Multi-step orchestration
- Error recovery and retry logic
- State transitions

## Phase 4: Create Step Definition Files

### Step File Pattern

All 8 step definition files follow this structure:

```go
package steps

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/cucumber/godog"
    "gorm.io/gorm"

    "github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
    "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands" // or queries
    "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
    "github.com/andrescamacho/spacetraders-go/internal/domain/player"
    "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
    "github.com/andrescamacho/spacetraders-go/test/helpers"
)

type <command>HandlerContext struct {
    // Test data
    contracts   map[string]*contract.Contract
    players     map[int]*player.Player
    playerID    shared.PlayerID

    // Response/Error tracking
    response    *commands.<Command>Response
    err         error

    // REAL dependencies (NO MOCK REPOS!)
    db           *gorm.DB
    contractRepo *persistence.GormContractRepository
    playerRepo   *persistence.GormPlayerRepository
    shipRepo     *persistence.GormShipRepository  // if needed

    // Mock dependencies
    apiClient    *helpers.MockAPIClient
    clock        *shared.MockClock

    // Handler
    handler      *commands.<Command>Handler
}

func (ctx *<command>HandlerContext) reset() {
    ctx.contracts = make(map[string]*contract.Contract)
    ctx.players = make(map[int]*player.Player)
    ctx.response = nil
    ctx.err = nil

    // Truncate all tables for test isolation
    if err := helpers.TruncateAllTables(); err != nil {
        panic(fmt.Errorf("failed to truncate tables: %w", err))
    }

    // Use shared test DB with REAL GORM repositories
    ctx.db = helpers.SharedTestDB
    ctx.contractRepo = persistence.NewGormContractRepository(helpers.SharedTestDB)
    ctx.playerRepo = persistence.NewGormPlayerRepository(helpers.SharedTestDB)
    // ctx.shipRepo = persistence.NewGormShipRepository(helpers.SharedTestDB) // if needed

    // Mock API client
    ctx.apiClient = helpers.NewMockAPIClient()

    // Mock clock starting at fixed time (can be overridden in Given steps)
    ctx.clock = shared.NewMockClock(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

    // Create handler with real repos + mock clock
    ctx.handler = commands.New<Command>Handler(
        ctx.contractRepo,
        ctx.playerRepo,
        ctx.apiClient,
        ctx.clock,  // Inject mock clock
    )
}

// Given steps

func (ctx *<command>HandlerContext) theCurrentTimeIs(timeStr string) error {
    t, err := time.Parse(time.RFC3339, timeStr)
    if err != nil {
        return fmt.Errorf("invalid time format: %w", err)
    }
    ctx.clock.SetTime(t)
    return nil
}

func (ctx *<command>HandlerContext) aPlayerWithIDAndTokenExistsInTheDatabase(playerID int, token string) error {
    pid, err := shared.NewPlayerID(playerID)
    if err != nil {
        return err
    }
    ctx.playerID = pid

    p := player.NewPlayer(pid, fmt.Sprintf("AGENT-%d", playerID), token)
    ctx.players[playerID] = p

    // Save to database using REAL repository
    return ctx.playerRepo.Save(context.Background(), p)
}

// When steps

func (ctx *<command>HandlerContext) iExecute<Command>CommandFor(contractID string, playerID int) error {
    pid, err := shared.NewPlayerID(playerID)
    if err != nil {
        return err
    }

    // Get player token from test data
    p, exists := ctx.players[playerID]
    if !exists {
        return fmt.Errorf("player %d not set up in test", playerID)
    }

    // Create context with token
    cmdCtx := helpers.ContextWithToken(p.Token())

    // Create command
    cmd := &commands.<Command>Command{
        ContractID: contractID,
        PlayerID:   pid,
    }

    // Execute handler
    response, err := ctx.handler.Handle(cmdCtx, cmd)

    // Store response and error
    ctx.err = err
    if err == nil {
        ctx.response = response.(*commands.<Command>Response)
    } else {
        ctx.response = nil
    }

    return nil
}

// Then steps

func (ctx *<command>HandlerContext) theCommandShouldSucceed() error {
    if ctx.err != nil {
        return fmt.Errorf("expected success but got error: %v", ctx.err)
    }
    if ctx.response == nil {
        return fmt.Errorf("expected response but got nil")
    }
    return nil
}

func (ctx *<command>HandlerContext) theCommandShouldReturnAnErrorContaining(expectedError string) error {
    if ctx.err == nil {
        return fmt.Errorf("expected error containing '%s' but command succeeded", expectedError)
    }

    errMsg := strings.ToLower(ctx.err.Error())
    expectedLower := strings.ToLower(expectedError)

    if !strings.Contains(errMsg, expectedLower) {
        return fmt.Errorf("expected error containing '%s' but got '%v'", expectedError, ctx.err)
    }

    return nil
}

// Register steps

func Initialize<Command>HandlerScenario(ctx *godog.ScenarioContext) {
    handlerCtx := &<command>HandlerContext{}

    ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
        handlerCtx.reset()
        return ctx, nil
    })

    // Register steps
    ctx.Step(`^the current time is "([^"]*)"$`, handlerCtx.theCurrentTimeIs)
    ctx.Step(`^a player with ID (\d+) and token "([^"]*)" exists in the database$`, handlerCtx.aPlayerWithIDAndTokenExistsInTheDatabase)
    ctx.Step(`^I execute <command> command for "([^"]*)" with player (\d+)$`, handlerCtx.iExecute<Command>CommandFor)
    ctx.Step(`^the command should succeed$`, handlerCtx.theCommandShouldSucceed)
    ctx.Step(`^the command should return an error containing "([^"]*)"$`, handlerCtx.theCommandShouldReturnAnErrorContaining)
    // ... more step registrations ...
}
```

### 4.1-4.8 Step Definition Files

**Create these 8 files in `test/bdd/steps/`:**

1. `accept_contract_handler_steps.go` - Accept contract command tests
2. `deliver_contract_handler_steps.go` - Deliver contract command tests
3. `fulfill_contract_handler_steps.go` - Fulfill contract command tests
4. `negotiate_contract_handler_steps.go` - Negotiate contract command tests
5. `evaluate_profitability_handler_steps.go` - Profitability query tests
6. `rebalance_fleet_handler_steps.go` - Rebalance fleet command tests
7. `run_fleet_coordinator_handler_steps.go` - Fleet coordinator command tests
8. `run_contract_workflow_handler_steps.go` - Contract workflow command tests

### Clock Usage in Steps

**Time control in tests:**

```go
// Set specific time
ctx.clock.SetTime(time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC))

// Advance time (instant, no actual wait)
ctx.clock.Advance(1 * time.Hour)

// Sleep is instant in tests
ctx.clock.Sleep(30 * time.Minute)  // Completes immediately

// Check current mock time
currentTime := ctx.clock.Now()
```

## Phase 5: Validation and Testing

### 5.1 Run Individual Feature Files

```bash
# Test accept contract
go test ./test/bdd/... -v -godog.paths=test/bdd/features/application/accept_contract.feature

# Test deliver contract
go test ./test/bdd/... -v -godog.paths=test/bdd/features/application/deliver_contract.feature

# Test fulfill contract
go test ./test/bdd/... -v -godog.paths=test/bdd/features/application/fulfill_contract.feature

# etc.
```

### 5.2 Run All Application Tests

```bash
go test ./test/bdd/... -v -godog.paths=test/bdd/features/application/
```

### 5.3 Run Full Test Suite

```bash
make test
# or
make test-fast
```

### 5.4 Expected Results

✅ **Fast execution:** All tests should complete in seconds (MockClock eliminates waits)
✅ **High coverage:** Test all command/query paths including error cases
✅ **Isolated scenarios:** Each scenario starts with clean database state
✅ **Real SQL testing:** Actual GORM operations execute against SQLite

## Summary

### Production Code Changes: ~5-10 files

**Must update:**
- `internal/domain/contract/contract.go` - Add Clock field and inject in constructor
- `internal/application/contract/commands/run_fleet_coordinator.go` - Add Clock, replace time operations
- Potentially 2-5 more command handlers if they use time operations

**Backward compatibility:**
- All Clock parameters default to RealClock
- Production behavior unchanged
- Tests inject MockClock

### New Test Files: ~17 files

**Feature files:** 8 files in `test/bdd/features/application/`
**Step definitions:** 8 files in `test/bdd/steps/`
**Helpers:** 1 file in `test/helpers/` (context helper)

### Updated Files: 1 file

**Test suite:** `test/bdd/bdd_test.go` - Add application paths and step registrations

### Key Success Metrics

1. ✅ Tests run in < 10 seconds (MockClock makes sleeps instant)
2. ✅ Real GORM repositories test actual SQL operations
3. ✅ Clean test isolation via TruncateAllTables()
4. ✅ Controllable time via MockClock for deadline/expiry testing
5. ✅ Zero production impact (RealClock used by default)
6. ✅ Comprehensive coverage of all contract commands and queries

## Implementation Order

### Priority 1: Core Commands (Week 1)
1. Refactor Contract entity with Clock
2. Create accept_contract.feature + steps
3. Create deliver_contract.feature + steps
4. Create fulfill_contract.feature + steps
5. Create negotiate_contract.feature + steps

### Priority 2: Query (Week 1-2)
6. Create evaluate_contract_profitability.feature + steps

### Priority 3: Workflow Commands (Week 2)
7. Refactor workflow handlers with Clock
8. Create rebalance_fleet.feature + steps
9. Create run_fleet_coordinator.feature + steps
10. Create run_contract_workflow.feature + steps

## References

- Clock interface: `internal/domain/shared/clock.go`
- Shared test DB: `test/helpers/shared_test_db.go`
- Existing contract steps (domain): `test/bdd/steps/contract_steps.go`
- Mock API client: `test/helpers/mock_api_client.go`
- Git commit with Clock pattern: `40c280d` (perf optimization)
- Git commit with deleted tests: `7c18367` (cleanup)

# BDD Tests for SpaceTraders Go Bot - Domain Layer

This directory contains Behavior-Driven Development (BDD) tests for the domain layer of the SpaceTraders Go bot using **Godog** (Cucumber for Go).

## Overview

BDD tests use Gherkin syntax to describe behavior in a human-readable format that can be executed as tests. These tests focus **exclusively on the domain layer** and have **zero external dependencies** - they test pure business logic.

## Directory Structure

```
test/bdd/
├── README.md                              # This file
├── bdd_test.go                            # Test suite runner
├── features/                              # Gherkin feature files
│   └── domain/
│       ├── navigation/
│       │   ├── ship_entity.feature        # Ship entity behaviors
│       │   └── route_entity.feature       # Route entity behaviors
│       ├── container/
│       │   └── container_entity.feature   # Container entity behaviors
│       └── shared/
│           ├── waypoint_value_object.feature    # Waypoint value object
│           ├── fuel_value_object.feature        # Fuel value object
│           ├── flight_mode_value_object.feature # Flight mode calculations
│           └── cargo_value_object.feature       # Cargo value object
└── steps/                                 # Step definitions (Go code)
    ├── ship_steps.go                      # Ship entity step implementations
    ├── route_steps.go                     # Route entity step implementations
    ├── container_steps.go                 # Container entity step implementations
    └── value_object_steps.go              # Value object step implementations
```

## Prerequisites

The BDD framework (Godog) is already installed. Dependencies are in `go.mod`:

```
github.com/cucumber/godog v0.15.1
```

## Running BDD Tests

### Run all BDD tests

```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/gobot
go test ./test/bdd/... -v
```

### Run tests for specific features

```bash
# Run only ship entity tests
go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/navigation/ship_entity.feature

# Run only value object tests
go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/shared/
```

### Run with different output formats

```bash
# Pretty format (default, colored output)
go test ./test/bdd/... -v -godog.format=pretty

# JSON format
go test ./test/bdd/... -v -godog.format=json

# JUnit XML format (for CI/CD)
go test ./test/bdd/... -v -godog.format=junit:report.xml

# Progress format (simple dots)
go test ./test/bdd/... -v -godog.format=progress
```

### Run specific scenarios by tags

Add tags to scenarios in feature files:

```gherkin
@critical @ship
Scenario: Create ship with valid data
  When I create a ship...
```

Then run:

```bash
go test ./test/bdd/... -v -godog.tags=@critical
go test ./test/bdd/... -v -godog.tags=@ship
go test ./test/bdd/... -v -godog.tags="@critical && @ship"
```

## Feature Coverage

### Ship Entity (`ship_entity.feature`)

Tests for the Ship aggregate root including:

- **Initialization**: Valid/invalid creation scenarios
- **State Machine**: DOCKED ↔ IN_ORBIT ↔ IN_TRANSIT transitions
- **Fuel Management**: Consumption, refueling, capacity validation
- **Navigation Calculations**: Distance, fuel requirements, travel time
- **Flight Mode Selection**: Optimal mode based on fuel availability
- **Cargo Management**: Capacity checks, space availability
- **State Queries**: Status checks, location validation

**Example Scenarios:**
- Create ship with valid data
- Invalid ship creation (empty symbol, negative values, etc.)
- Navigate between states (depart, dock, start transit, arrive)
- Consume and refuel operations
- Calculate optimal flight mode

### Route Entity (`route_entity.feature`)

Tests for the Route aggregate root including:

- **RouteSegment**: Immutable value object creation
- **Route Creation**: Validation of connected segments
- **Fuel Validation**: Segment fuel requirements vs ship capacity
- **Route Execution**: Status transitions, segment progression
- **Calculations**: Total distance, fuel, travel time
- **Segment Navigation**: Current segment, remaining segments

**Example Scenarios:**
- Create route with valid/invalid segments
- Disconnected segments fail validation
- Segment fuel exceeds capacity fails
- Execute route (PLANNED → EXECUTING → COMPLETED)
- Calculate route totals

### Container Entity (`container_entity.feature`)

Tests for the Container entity including:

- **Lifecycle**: State transitions through PENDING → RUNNING → COMPLETED/FAILED
- **Iteration Management**: Counter, max iterations, should continue
- **Restart Logic**: Restart eligibility, restart count limits
- **Metadata**: Dynamic metadata storage and retrieval
- **Status Queries**: Running, finished, stopping checks
- **Runtime Calculation**: Duration tracking

**Example Scenarios:**
- Create container with metadata
- State transitions (start, complete, fail, stop)
- Iteration tracking and limits
- Restart policy enforcement
- Runtime duration calculation

### Value Objects

#### Waypoint (`waypoint_value_object.feature`)

- Creation with validation
- Distance calculations (Euclidean)
- Orbital relationships

#### Fuel (`fuel_value_object.feature`)

- Immutable fuel state
- Percentage calculations
- Consume/add operations (return new instance)
- Travel capability checks with safety margin
- Full/empty status

#### Flight Mode (`flight_mode_value_object.feature`)

- Fuel cost calculations for CRUISE, DRIFT, BURN, STEALTH
- Travel time calculations
- Optimal mode selection based on fuel availability

#### Cargo (`cargo_value_object.feature`)

- CargoItem creation and validation
- Cargo manifest with inventory
- Item queries (has item, get units)
- Capacity calculations
- Empty/full status

## Writing New BDD Tests

### 1. Create a Feature File

Create a new `.feature` file in the appropriate subdirectory:

```gherkin
Feature: My New Feature
  As a user
  I want to do something
  So that I achieve a goal

  Scenario: Description of behavior
    Given some initial context
    When I perform an action
    Then I expect an outcome
```

### 2. Implement Step Definitions

Add step implementations in the appropriate `*_steps.go` file:

```go
func (ctx *myContext) iPerformAnAction() error {
    // Implementation
    return nil
}

func (ctx *myContext) iExpectAnOutcome() error {
    // Verification
    return nil
}
```

### 3. Register Steps with Godog

In your `*_steps.go` file's initialize function:

```go
func InitializeMyScenario(sc *godog.ScenarioContext) {
    ctx := &myContext{}

    sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
        ctx.reset()
        return ctx, nil
    })

    sc.Step(`^I perform an action$`, ctx.iPerformAnAction)
    sc.Step(`^I expect an outcome$`, ctx.iExpectAnOutcome)
}
```

### 4. Register in Test Suite

Add to `bdd_test.go`:

```go
func InitializeScenario(sc *godog.ScenarioContext) {
    steps.InitializeShipScenario(sc)
    steps.InitializeRouteScenario(sc)
    steps.InitializeContainerScenario(sc)
    steps.InitializeValueObjectScenarios(sc)
    steps.InitializeMyScenario(sc) // Add your new scenario
}
```

## BDD Testing Patterns

### Pattern 1: Given-When-Then Structure

```gherkin
Scenario: Standard pattern
  Given a ship with 100 units of fuel    # Setup
  When the ship consumes 30 units        # Action
  Then the ship should have 70 units     # Verification
```

### Pattern 2: Error Validation

```gherkin
Scenario: Invalid input handling
  When I attempt to create a ship with empty ship_symbol
  Then ship creation should fail with error "ship_symbol cannot be empty"
```

### Pattern 3: State Machine Testing

```gherkin
Scenario: State transition
  Given a docked ship at "X1-A1"
  When the ship departs
  Then the ship should be in orbit
  And the ship should not be docked
```

### Pattern 4: Table-Driven Tests

```gherkin
Scenario: Background data setup
  Given test waypoints are available:
    | symbol  | x     | y    |
    | X1-A1   | 0.0   | 0.0  |
    | X1-B2   | 100.0 | 0.0  |
  When I calculate distance from "X1-A1" to "X1-B2"
  Then the distance should be 100.0
```

## Design Principles

### 1. Pure Domain Testing

- **No external dependencies**: No databases, APIs, or external services
- **Direct domain entity usage**: Tests instantiate and manipulate domain objects directly
- **Fast execution**: Tests run in milliseconds

### 2. Immutability Focus

Value objects are immutable - operations return new instances:

```gherkin
Scenario: Fuel consumption returns new fuel object
  Given fuel with current 100 and capacity 100
  When I consume 30 units of fuel
  Then the new fuel should have current 70
  And the original fuel should still have current 100
```

### 3. Comprehensive Coverage

Each feature file covers:
- Happy path scenarios
- Error/validation scenarios
- Edge cases (zero, negative, boundary values)
- State transitions
- Invariant enforcement

### 4. Readable Scenarios

Scenarios should read like documentation:

```gherkin
# Good - Clear intent
Scenario: Cannot navigate without sufficient fuel
  Given a ship at "X1-A1" with 0 units of fuel
  When I check if the ship can navigate to "X1-C3"
  Then the result should be false

# Avoid - Too technical
Scenario: CanNavigateTo returns false
  Given ship.fuel.current = 0
  When ship.CanNavigateTo(waypoint)
  Then result == false
```

## Integration with Go Testing

BDD tests integrate with Go's standard testing framework:

```bash
# Run with standard go test
go test ./test/bdd/... -v

# Generate coverage report
go test ./test/bdd/... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Run with race detector
go test ./test/bdd/... -race

# Run specific test
go test ./test/bdd/... -run TestFeatures
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: BDD Tests
on: [push, pull_request]

jobs:
  bdd-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Run BDD Tests
        run: |
          go test ./test/bdd/... -v \
            -godog.format=junit:bdd-report.xml

      - name: Publish Test Results
        uses: EnricoMi/publish-unit-test-result-action@v2
        if: always()
        with:
          files: bdd-report.xml
```

## Troubleshooting

### Step Definition Not Found

```
Error: step is undefined
```

**Solution**: Ensure step is registered in `InitializeXXXScenario()` and the regex pattern matches.

### Import Cycle

```
Error: import cycle not allowed
```

**Solution**: BDD tests should only import from `internal/domain/` - never import from application or infrastructure layers.

### Step Regex Issues

**Problem**: Step isn't matching

**Solution**: Check regex patterns:
```go
// Match integers (including negative)
ctx.Step(`^I create with value (-?\d+)$`, func(val int) error { ... })

// Match floats
ctx.Step(`^distance is ([0-9.]+)$`, func(dist float64) error { ... })

// Match quoted strings
ctx.Step(`^symbol "([^"]*)"$`, func(symbol string) error { ... })

// Match boolean
ctx.Step(`^result is (true|false)$`, func(result bool) error { ... })
```

## Comparison with Python Implementation

This Go implementation mirrors the Python BDD structure at `/Users/andres.camacho/Development/Personal/spacetraders/bot/tests/bdd/`:

| Aspect | Python (pytest-bdd) | Go (godog) |
|--------|---------------------|------------|
| Feature files | `.feature` in `features/` | `.feature` in `features/` |
| Step definitions | `@given/@when/@then` decorators | `ctx.Step()` registration |
| Test execution | `pytest tests/bdd/` | `go test ./test/bdd/...` |
| Context sharing | Pytest fixtures | Step context structs |
| Setup/Teardown | `@pytest.fixture` | `sc.Before/After()` |

## Benefits of BDD for Domain Layer

1. **Living Documentation**: Feature files serve as executable specifications
2. **Business Language**: Scenarios use domain terminology, not technical jargon
3. **Fast Feedback**: Pure domain tests run in milliseconds
4. **Regression Safety**: Comprehensive scenario coverage catches breaking changes
5. **Refactoring Confidence**: Tests describe behavior, not implementation
6. **Onboarding**: New developers understand domain rules by reading scenarios

## Application Layer Tests

### Navigate Ship Handler Tests

Located in `features/application/navigate_ship_handler.feature`, these tests verify navigation business rules using:

- **Real repositories** with in-memory SQLite database
- **Mock API client** (no HTTP calls)
- **Mock routing client** (no OR-Tools service)

Run with: `make test-bdd-navigate`

These tests verify:
- Caching and enrichment logic (graph from database vs API)
- Validation rules (empty cache, missing waypoints)
- Idempotency (already at destination, IN_TRANSIT handling)
- 90% opportunistic refueling rule
- Pre-departure refuel to prevent DRIFT mode
- Refuel before departure when route requires it
- Flight mode setting before each segment
- Wait-for-arrival timing with buffer
- Auto-sync after every API call
- Error handling and route failure
- Multi-segment route execution
- State machine transitions (DOCKED → IN_ORBIT → IN_TRANSIT)

#### Test Architecture

```
NavigateShipHandler (real)
  ↓
Repositories (real, in-memory SQLite)
  ├─ PlayerRepository
  ├─ WaypointRepository
  ├─ SystemGraphRepository
  └─ ShipRepository (mock with real domain conversion)
  ↓
Mock API Client (no HTTP)
Mock Routing Client (no gRPC)
```

**Why mock only external API?**

- Tests business logic, not API integration
- Fast execution (no network calls)
- Deterministic results
- Real repository behavior ensures database queries work
- Real domain entities ensure business rules are enforced

**Example scenario:**

```gherkin
@refueling @90-percent-rule
Scenario: Opportunistically refuel when arriving at fuel station with less than 90% fuel
  Given system "X1-GZ7" has waypoints cached
  And waypoint "X1-GZ7-B1" is a fuel station
  And ship "SCOUT-1" starts at "X1-GZ7-A1" with 100 fuel
  And navigation to "X1-GZ7-B1" consumes 50 fuel
  When I navigate "SCOUT-1" to "X1-GZ7-B1"
  Then ship should arrive at "X1-GZ7-B1" with 50% fuel
  And opportunistic refuel should trigger
  And ship should dock, refuel to 100/100, and orbit
```

## Next Steps

1. **Expand Coverage**: Add scenarios for complex domain behaviors
2. **Add Tags**: Tag scenarios by importance (@critical), area (@navigation)
3. **Performance Tests**: Add scenarios for performance-critical paths
4. **Documentation**: Keep feature files synchronized with domain changes
5. **CI Integration**: Run BDD tests on every commit

## Resources

- [Godog Documentation](https://github.com/cucumber/godog)
- [Gherkin Reference](https://cucumber.io/docs/gherkin/reference/)
- [BDD Best Practices](https://cucumber.io/docs/bdd/)
- [Domain-Driven Design](https://martinfowler.com/bliki/DomainDrivenDesign.html)

## Contact

For questions or issues with BDD tests, refer to the main project documentation or open an issue.

# Domain Layer Refactoring Plan

**Date:** 2025-11-21
**Status:** Planning
**Scope:** `internal/domain/` package refactoring

---

## Executive Summary

This document outlines refactoring opportunities identified in the domain layer through systematic analysis of code duplication, SOLID principle violations, cohesion, and coupling issues.

**Key Findings:**
- 15 refactoring opportunities identified
- ~200+ lines of duplicated code
- 1 major SRP violation (Ship entity - 521 lines)
- Multiple instances of scattered business logic

**Expected Impact:**
- Reduce code duplication by 200+ lines
- Improve testability and maintainability
- Better adherence to SOLID principles
- Clearer separation of concerns

---

## Table of Contents

1. [High Severity Issues (P0-P1)](#high-severity-issues)
2. [Medium Severity Issues (P2-P3)](#medium-severity-issues)
3. [Code Smells](#code-smells)
4. [Refactoring Roadmap](#refactoring-roadmap)
5. [Implementation Guidelines](#implementation-guidelines)

---

## High Severity Issues

### 1. Duplicate State Machine Pattern (P0)

**Severity:** HIGH
**Impact:** 150+ lines of duplicated code
**Priority:** P0 (Immediate)

#### Problem

Three entities implement nearly identical lifecycle state machines:

- **Container** (`internal/domain/container/container.go:139-213`)
- **MiningOperation** (`internal/domain/mining/mining_operation.go:115-170`)
- **Route** (`internal/domain/navigation/route.go:167-200`)

**Duplicated Logic:**
- State transitions: `Start()`, `Complete()`, `Fail()`, `Stop()`
- State queries: `IsRunning()`, `IsFinished()`
- Timestamp tracking: `createdAt`, `updatedAt`, `startedAt`, `stoppedAt`
- Runtime calculations: `RuntimeDuration()`

#### Solution

Extract common state machine behavior into reusable component:

```go
// domain/shared/lifecycle_state_machine.go
type LifecycleStateMachine struct {
    status    LifecycleStatus
    createdAt time.Time
    updatedAt time.Time
    startedAt *time.Time
    stoppedAt *time.Time
    lastError error
    clock     Clock
}

func NewLifecycleStateMachine(clock Clock) *LifecycleStateMachine { ... }

func (sm *LifecycleStateMachine) Start() error { ... }
func (sm *LifecycleStateMachine) Complete() error { ... }
func (sm *LifecycleStateMachine) Fail(err error) error { ... }
func (sm *LifecycleStateMachine) Stop() error { ... }
func (sm *LifecycleStateMachine) IsRunning() bool { ... }
func (sm *LifecycleStateMachine) IsFinished() bool { ... }
func (sm *LifecycleStateMachine) RuntimeDuration() time.Duration { ... }
```

**Refactor entities to use composition:**

```go
type Container struct {
    lifecycle *shared.LifecycleStateMachine
    // ... other container-specific fields
}
```

#### Action Items

- [ ] Create `domain/shared/lifecycle_state_machine.go`
- [ ] Write BDD tests for lifecycle state machine
- [ ] Refactor Container to use LifecycleStateMachine
- [ ] Refactor MiningOperation to use LifecycleStateMachine
- [ ] Refactor Route to use LifecycleStateMachine
- [ ] Update all related BDD tests

---

### 2. Ship Entity SRP Violation (P0)

**Severity:** HIGH
**Impact:** 521-line entity with multiple responsibilities
**Priority:** P0 (Immediate)

#### Problem

Ship entity (`internal/domain/navigation/ship.go`) violates Single Responsibility Principle with 6 distinct responsibilities:

1. **State Management** (lines 239-317): 8 methods for navigation state transitions
2. **Fuel Management** (lines 319-362): 3 methods for fuel operations
3. **Navigation Calculations** (lines 364-400): 5 methods for distance/time/fuel calculations
4. **Cargo Management** (lines 402-434): 5 methods for cargo queries
5. **State Queries** (lines 436-456): 4 methods for status checks
6. **Route Execution Decisions** (lines 463-520): 2 complex refueling decision methods

**Impact:**
- Difficult to test in isolation
- High complexity (30+ methods)
- Mixes business logic with domain calculations
- Hard to maintain and extend

#### Solution

**Extract Domain Services:**

1. **ShipFuelService** - Fuel management and refueling decisions
   ```go
   // domain/navigation/ship_fuel_service.go
   type ShipFuelService struct {
       clock Clock
   }

   func (s *ShipFuelService) ShouldRefuelOpportunistically(ship *Ship, safetyMargin float64) bool
   func (s *ShipFuelService) ShouldPreventDriftMode(ship *Ship, fuelCost int, safetyMargin int) bool
   func (s *ShipFuelService) CalculateRefuelAmount(ship *Ship, strategy RefuelStrategy) int
   ```

2. **ShipNavigationCalculator** - Navigation math and calculations
   ```go
   // domain/navigation/ship_navigation_calculator.go
   type ShipNavigationCalculator struct {}

   func (c *ShipNavigationCalculator) TravelTime(ship *Ship, destination *Waypoint, mode FlightMode) int
   func (c *ShipNavigationCalculator) FuelRequired(ship *Ship, destination *Waypoint, mode FlightMode) int
   func (c *ShipNavigationCalculator) CanNavigateTo(ship *Ship, destination *Waypoint) bool
   func (c *ShipNavigationCalculator) SelectOptimalFlightMode(ship *Ship, destination *Waypoint, safetyMargin int) FlightMode
   ```

3. **Keep in Ship Entity:**
   - Core state: location, fuel, cargo, navigation status
   - Simple state transitions: `Depart()`, `Dock()`, `Orbit()`, `StartTransit()`, `Arrive()`
   - Simple getters and state queries
   - Direct fuel/cargo operations: `ConsumeFuel()`, `Refuel()`, `HasCargoSpace()`

**Expected Reduction:** Ship entity from 521 lines to ~250 lines

#### Action Items

- [ ] Create `domain/navigation/ship_fuel_service.go`
- [ ] Extract fuel decision methods to ShipFuelService
- [ ] Create `domain/navigation/ship_navigation_calculator.go`
- [ ] Extract navigation calculation methods to ShipNavigationCalculator
- [ ] Update Ship entity to use services where appropriate
- [ ] Write BDD tests for new services
- [ ] Update existing BDD tests to reflect new structure

---

### 3. Scattered Fuel Logic (P1)

**Severity:** HIGH
**Impact:** Low cohesion across 3+ files
**Priority:** P1 (High)

#### Problem

Fuel-related logic scattered across multiple files without cohesive organization:

- **Fuel value object:** `internal/domain/shared/fuel.go`
- **Fuel operations in Ship:** `internal/domain/navigation/ship.go:320-362`
- **Fuel decision logic in Ship:** `internal/domain/navigation/ship.go:465-520`
- **Flight mode fuel calculations:** `internal/domain/shared/flight_mode.go:39-50`

**Impact:**
- Difficult to understand complete fuel management behavior
- Changes require updates across multiple files
- Duplicated fuel percentage calculations

#### Solution

Centralize fuel management in ShipFuelService (see Issue #2) and consolidate calculations:

1. Move all refueling decision logic to ShipFuelService
2. Use `Fuel.Percentage()` method instead of recalculating
3. Document fuel safety policies in one place

**Example Consolidation:**

```go
// BEFORE: Ship.go duplicates fuel percentage calculation
fuelPercentage := float64(s.fuel.Current) / float64(s.fuelCapacity)

// AFTER: Use Fuel value object method
fuelPercentage := s.fuel.Percentage() / 100.0
```

#### Action Items

- [ ] Consolidate fuel logic in ShipFuelService
- [ ] Remove duplicate fuel percentage calculations in Ship (lines 486, 518)
- [ ] Use `Fuel.Percentage()` method consistently
- [ ] Document fuel safety policies and thresholds
- [ ] Update BDD tests

---

### 4. Duplicate Distance Calculation (P1)

**Severity:** HIGH
**Impact:** ~30 lines duplicated across 3 locations
**Priority:** P1 (High)

#### Problem

Same "find nearest waypoint" pattern repeated 3 times:

- `internal/domain/contract/fleet_assigner.go:89-100`
- `internal/domain/contract/fleet_assigner.go:212-222`
- `internal/domain/contract/ship_selector.go:99-100`

**Pattern:**
```go
for _, ship := range ships {
    minDistance := math.MaxFloat64
    currentLocation := ship.CurrentLocation()
    for _, targetWaypoint := range targetWaypoints {
        distance := currentLocation.DistanceTo(targetWaypoint)
        if distance < minDistance {
            minDistance = distance
        }
    }
    totalDistance += minDistance
}
```

#### Solution

Extract to utility method in `domain/shared/waypoint.go`:

```go
// FindNearestWaypoint returns the nearest waypoint from a list and its distance
func FindNearestWaypoint(from *Waypoint, targets []*Waypoint) (*Waypoint, float64) {
    if len(targets) == 0 {
        return nil, 0
    }

    nearest := targets[0]
    minDistance := from.DistanceTo(targets[0])

    for _, target := range targets[1:] {
        distance := from.DistanceTo(target)
        if distance < minDistance {
            minDistance = distance
            nearest = target
        }
    }

    return nearest, minDistance
}

// CalculateTotalDistanceToNearestWaypoints calculates sum of distances from each ship to its nearest target
func CalculateTotalDistanceToNearestWaypoints(ships []*Ship, targets []*Waypoint) float64 {
    total := 0.0
    for _, ship := range ships {
        _, distance := FindNearestWaypoint(ship.CurrentLocation(), targets)
        total += distance
    }
    return total
}
```

#### Action Items

- [ ] Add `FindNearestWaypoint()` to `domain/shared/waypoint.go`
- [ ] Add `CalculateTotalDistanceToNearestWaypoints()` helper
- [ ] Replace duplicate implementations in FleetAssigner
- [ ] Replace duplicate implementation in ShipSelector
- [ ] Write BDD tests for new utility methods

---

### 5. Contract Profitability Misplaced (P1)

**Severity:** HIGH
**Impact:** 107 lines of complex logic in wrong place
**Priority:** P1 (High)

#### Problem

Complex profitability calculation embedded in Contract entity (`internal/domain/contract/contract.go:142-248`):

- 107-line `CalculateProfitability()` method
- Complex domain service logic (not entity behavior)
- Mixes contract state management with profitability analysis
- Contract entity becomes responsible for financial calculations

**Impact:**
- Contract entity too complex
- Difficult to test profitability logic in isolation
- Violates SRP

#### Solution

Extract to dedicated domain service:

```go
// domain/contract/contract_profitability_service.go
type ContractProfitabilityService struct {
    // Dependencies if needed
}

func NewContractProfitabilityService() *ContractProfitabilityService {
    return &ContractProfitabilityService{}
}

func (s *ContractProfitabilityService) CalculateProfitability(
    contract *Contract,
    ships []*Ship,
    markets map[string]*Market,
) (*ContractProfitability, error) {
    // Move 107-line calculation here
}

func (s *ContractProfitabilityService) EstimateRevenue(...) int
func (s *ContractProfitabilityService) EstimateTravelCosts(...) int
func (s *ContractProfitabilityService) EstimatePurchaseCosts(...) int
```

**Contract entity simplifies to:**
```go
// Contract just delegates to service
func (c *Contract) CalculateProfitability(service *ContractProfitabilityService, ...) (*ContractProfitability, error) {
    return service.CalculateProfitability(c, ships, markets)
}
```

#### Action Items

- [ ] Create `domain/contract/contract_profitability_service.go`
- [ ] Move profitability calculation logic to service
- [ ] Update Contract entity to delegate to service
- [ ] Write BDD tests for ContractProfitabilityService
- [ ] Update existing contract BDD tests

---

## Medium Severity Issues

### 6. ShipRepository Interface Segregation Violation (P2)

**Severity:** MEDIUM
**Impact:** Fat interface with 10 methods mixing concerns
**Priority:** P2 (Medium)

#### Problem

ShipRepository interface (`internal/domain/navigation/ports.go:9-39`) violates Interface Segregation Principle:

**Mixed Concerns:**
- Query operations: `FindBySymbol`, `GetShipData`, `FindAllByPlayer`
- Navigation commands: `Navigate`, `Dock`, `Orbit`, `Refuel`, `SetFlightMode`
- Cargo operations: `JettisonCargo`

**Impact:**
- Implementations must implement all methods even if only needing subset
- Difficult to mock in tests (must mock all 10 methods)
- Violates ISP

#### Solution

Split into focused interfaces:

```go
// domain/navigation/ports.go

// ShipQueryRepository handles ship data queries
type ShipQueryRepository interface {
    FindBySymbol(ctx context.Context, symbol string) (*Ship, error)
    FindAllByPlayer(ctx context.Context, playerID uuid.UUID) ([]*Ship, error)
    GetShipData(ctx context.Context, agentSymbol, shipSymbol string) (*Ship, error)
}

// ShipCommandRepository handles ship actions
type ShipCommandRepository interface {
    Navigate(ctx context.Context, agentSymbol, shipSymbol, waypointSymbol string) (*NavigationResult, error)
    Dock(ctx context.Context, agentSymbol, shipSymbol string) error
    Orbit(ctx context.Context, agentSymbol, shipSymbol string) error
    Refuel(ctx context.Context, agentSymbol, shipSymbol string, units int) (*RefuelResult, error)
    SetFlightMode(ctx context.Context, agentSymbol, shipSymbol string, mode shared.FlightMode) error
}

// ShipCargoRepository handles cargo operations
type ShipCargoRepository interface {
    JettisonCargo(ctx context.Context, agentSymbol, shipSymbol, cargoSymbol string, units int) (*Cargo, error)
}

// ShipRepository combines all interfaces for convenience
type ShipRepository interface {
    ShipQueryRepository
    ShipCommandRepository
    ShipCargoRepository
}
```

#### Action Items

- [ ] Split ShipRepository into focused interfaces
- [ ] Update adapter implementations
- [ ] Update command handlers to use specific interfaces
- [ ] Update dependency injection to use focused interfaces where possible
- [ ] Update tests to mock only needed interfaces

---

### 7. FlightMode Open/Closed Violation (P2)

**Severity:** MEDIUM
**Impact:** Hard to extend with new flight modes
**Priority:** P2 (Medium)

#### Problem

`SelectOptimalFlightMode` function (`internal/domain/shared/flight_mode.go:76-104`) uses hard-coded if-else logic:

```go
func SelectOptimalFlightMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
    burnCost := int(float64(fuelCost) * burnConfig.FuelRate / cruiseConfig.FuelRate)
    if currentFuel == burnCost+safetyMargin && safetyMargin < burnCost {
        return FlightModeBurn
    }
    if currentFuel > burnCost+safetyMargin {
        return FlightModeBurn
    }
    // ... more if-else chains
}
```

**Impact:**
- Adding new flight modes requires modifying this function
- Not open for extension
- Complex branching logic (cyclomatic complexity ~8)

#### Solution

Strategy pattern with priority-based selection:

```go
// domain/shared/flight_mode_selector.go
type FlightModeStrategy interface {
    CanUse(currentFuel, fuelCost, safetyMargin int) bool
    Priority() int  // Higher = preferred
    Mode() FlightMode
}

type BurnModeStrategy struct{}
func (s *BurnModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool {
    burnCost := calculateBurnCost(fuelCost)
    return currentFuel > burnCost+safetyMargin
}
func (s *BurnModeStrategy) Priority() int { return 3 }
func (s *BurnModeStrategy) Mode() FlightMode { return FlightModeBurn }

type FlightModeSelector struct {
    strategies []FlightModeStrategy
}

func (s *FlightModeSelector) SelectOptimalMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
    // Sort by priority, return first usable mode
    for _, strategy := range s.strategies {
        if strategy.CanUse(currentFuel, fuelCost, safetyMargin) {
            return strategy.Mode()
        }
    }
    return FlightModeDrift // Safe default
}
```

#### Action Items

- [ ] Create `domain/shared/flight_mode_selector.go`
- [ ] Implement strategy interfaces for each flight mode
- [ ] Replace `SelectOptimalFlightMode` with selector
- [ ] Write BDD tests for selector
- [ ] Update existing tests

---

### 8. Data Clump: Timestamp Fields (P3)

**Severity:** MEDIUM
**Impact:** Same 4-5 fields repeated across 3+ entities
**Priority:** P3 (Low)

#### Problem

Same timestamp fields repeated across entities:

- **Container:** `internal/domain/container/container.go:74-78`
- **MiningOperation:** `internal/domain/mining/mining_operation.go:43-47`
- **ShipAssignment:** `internal/domain/container/ship_assignment.go:29-31`

**Pattern:**
```go
createdAt time.Time
updatedAt time.Time
startedAt *time.Time
stoppedAt *time.Time
```

#### Solution

Extract to value object (already part of LifecycleStateMachine refactoring):

```go
// domain/shared/lifecycle_timestamps.go
type LifecycleTimestamps struct {
    CreatedAt time.Time
    UpdatedAt time.Time
    StartedAt *time.Time
    StoppedAt *time.Time
}

func (t *LifecycleTimestamps) RuntimeDuration() time.Duration {
    if t.StartedAt == nil || t.StoppedAt == nil {
        return 0
    }
    return t.StoppedAt.Sub(*t.StartedAt)
}
```

**Note:** This is automatically addressed by the LifecycleStateMachine refactoring (Issue #1).

#### Action Items

- [ ] Handled by LifecycleStateMachine refactoring
- [ ] Verify all entities use consistent timestamp handling

---

### 9. Magic Numbers (P3)

**Severity:** MEDIUM
**Impact:** Unclear business rules
**Priority:** P3 (Low)

#### Problem

Hard-coded magic numbers throughout domain layer:

| Location | Value | Context |
|----------|-------|---------|
| `container/container.go:114` | 3 | Max restarts |
| `contract/fleet_assigner.go:12-18` | 2 | Max ships per waypoint |
| `navigation/ship.go:399` | 4 | Fuel safety margin |
| `contract/contract.go:170` | -5000 | Min profit threshold |

#### Solution

Extract to named constants with business reasoning:

```go
// domain/container/container.go
const (
    // MaxRestartAttempts defines the maximum number of automatic restart attempts
    // for a failed container. This prevents infinite restart loops while allowing
    // recovery from transient failures.
    MaxRestartAttempts = 3
)

// domain/contract/fleet_assigner.go
const (
    // MaxShipsPerWaypoint limits concurrent ships at a waypoint to prevent
    // resource contention and market saturation.
    MaxShipsPerWaypoint = 2
)

// domain/navigation/ship.go
const (
    // DefaultFuelSafetyMargin is the minimum fuel reserve (in units) to maintain
    // for safety during navigation. Prevents running out of fuel due to
    // miscalculations or unexpected detours.
    DefaultFuelSafetyMargin = 4
)

// domain/contract/contract.go
const (
    // MinProfitThreshold is the minimum acceptable profit for a contract.
    // Negative values allow accepting marginally unprofitable contracts
    // for strategic reasons (reputation, market access, etc.).
    MinProfitThreshold = -5000
)
```

#### Action Items

- [ ] Extract magic numbers to named constants
- [ ] Add business reasoning in comments
- [ ] Document domain policies
- [ ] Consider making some values configurable (future enhancement)

---

## Code Smells

### 10. Long Method: SelectOptimalFlightMode (P2)

**Location:** `internal/domain/shared/flight_mode.go:76-104`
**Complexity:** Cyclomatic complexity ~8
**Lines:** 29 lines

**Issue:** Complex branching logic with multiple special cases.

**Solution:** Addressed by FlightMode Strategy Pattern refactoring (Issue #7).

---

### 11. Feature Envy: Ship methods operating on Fuel (P2)

**Locations:**
- `internal/domain/navigation/ship.go:465-520`

**Issue:** Ship methods extensively query Fuel object state:

```go
func (s *Ship) ShouldRefuelOpportunistically(...) bool {
    fuelPercentage := float64(s.fuel.Current) / float64(s.fuelCapacity)
    return fuelPercentage < safetyMargin
}
```

**Solution:** Move to Fuel value object or ShipFuelService (addressed by Issues #2 and #3):

```go
// Option 1: Fuel value object method
func (f *Fuel) IsBelowSafetyMargin(margin float64) bool {
    return f.Percentage()/100.0 < margin
}

// Option 2: ShipFuelService method
func (s *ShipFuelService) ShouldRefuelOpportunistically(ship *Ship, margin float64) bool {
    return ship.Fuel().IsBelowSafetyMargin(margin)
}
```

---

### 12. Missing Abstractions

#### 12.1 No Explicit Fleet Domain Services (P3)

**Location:** `internal/domain/contract/`

**Issue:** FleetAssigner and ShipSelector are domain services but not explicitly modeled:
- Located in contract package
- Apply to general ship navigation, not contract-specific
- No clear "fleet" domain concept

**Recommendation:** Consider creating `domain/fleet/` package with explicit domain services.

---

#### 12.2 Missing Route Builder (P3)

**Location:** `internal/domain/navigation/route.go:77-104`

**Issue:** Complex route validation in constructor. Route building with segments should use builder pattern.

**Recommendation:**
```go
// domain/navigation/route_builder.go
type RouteBuilder struct {
    shipSymbol  string
    origin      *shared.Waypoint
    destination *shared.Waypoint
    segments    []RouteSegment
    clock       shared.Clock
}

func NewRouteBuilder(shipSymbol string) *RouteBuilder
func (b *RouteBuilder) AddSegment(segment RouteSegment) *RouteBuilder
func (b *RouteBuilder) Build() (*Route, error) // Validates and constructs
```

---

## Refactoring Roadmap

### Phase 1: Foundation (P0 - Immediate)

**Goal:** Eliminate major duplication and SRP violations

#### Sprint 1.1: State Machine Extraction
- [ ] Create `domain/shared/lifecycle_state_machine.go`
- [ ] Write comprehensive BDD tests for state machine
- [ ] Refactor Container entity
- [ ] Refactor MiningOperation entity
- [ ] Refactor Route entity
- [ ] Update all related BDD tests

**Estimated Effort:** 2-3 days
**Impact:** -150 lines of duplication

#### Sprint 1.2: Ship Entity Decomposition
- [ ] Create `domain/navigation/ship_fuel_service.go`
- [ ] Create `domain/navigation/ship_navigation_calculator.go`
- [ ] Extract methods to appropriate services
- [ ] Update Ship entity
- [ ] Write BDD tests for new services
- [ ] Update existing ship BDD tests

**Estimated Effort:** 3-4 days
**Impact:** Ship.go from 521 to ~250 lines

---

### Phase 2: High-Value Improvements (P1 - High Priority)

#### Sprint 2.1: Fuel Logic Consolidation
- [ ] Consolidate fuel logic in ShipFuelService
- [ ] Remove duplicate fuel percentage calculations
- [ ] Use `Fuel.Percentage()` consistently
- [ ] Document fuel policies
- [ ] Update BDD tests

**Estimated Effort:** 1-2 days
**Impact:** Better cohesion, -20 lines duplication

#### Sprint 2.2: Distance Calculations
- [ ] Add `FindNearestWaypoint()` to waypoint.go
- [ ] Add helper methods for common patterns
- [ ] Replace duplicate implementations
- [ ] Write BDD tests

**Estimated Effort:** 1 day
**Impact:** -30 lines duplication

#### Sprint 2.3: Contract Profitability Service
- [ ] Create `domain/contract/contract_profitability_service.go`
- [ ] Extract profitability calculation
- [ ] Update Contract entity
- [ ] Write BDD tests
- [ ] Update existing tests

**Estimated Effort:** 2 days
**Impact:** Better SRP compliance, improved testability

---

### Phase 3: SOLID Compliance (P2 - Medium Priority)

#### Sprint 3.1: Interface Segregation
- [ ] Split ShipRepository interface
- [ ] Update adapter implementations
- [ ] Update command handlers
- [ ] Update dependency injection
- [ ] Update tests

**Estimated Effort:** 2-3 days
**Impact:** Better ISP compliance, easier testing

#### Sprint 3.2: FlightMode Extensibility
- [ ] Create flight mode selector with strategies
- [ ] Implement strategy for each mode
- [ ] Replace `SelectOptimalFlightMode`
- [ ] Write BDD tests
- [ ] Update existing tests

**Estimated Effort:** 2 days
**Impact:** Better OCP compliance, more maintainable

---

### Phase 4: Polish (P3 - Low Priority)

#### Sprint 4.1: Magic Numbers
- [ ] Extract to named constants
- [ ] Add business reasoning comments
- [ ] Document domain policies

**Estimated Effort:** 1 day
**Impact:** Better code readability

#### Sprint 4.2: Missing Abstractions (Optional)
- [ ] Consider fleet domain services
- [ ] Consider route builder pattern
- [ ] Evaluate value vs. effort

**Estimated Effort:** 2-3 days (if pursued)
**Impact:** Better domain model clarity

---

## Implementation Guidelines

### Testing Strategy

**CRITICAL:** All refactoring must maintain or improve test coverage.

1. **BDD Tests First:**
   - Write BDD tests for new components before refactoring
   - Ensure existing BDD tests pass after refactoring
   - Add new scenarios for extracted functionality

2. **Test Location:**
   - All tests in `test/bdd/` directory
   - Never create `*_test.go` files alongside production code
   - Follow existing BDD test patterns

3. **Coverage Target:**
   - Maintain current domain coverage (68.3%)
   - Aim to improve coverage with new tests

### Refactoring Principles

1. **Incremental Changes:**
   - Make small, focused changes
   - Each change should compile and pass tests
   - Commit frequently with clear messages

2. **Preserve Behavior:**
   - Refactoring should not change behavior
   - Run full BDD test suite after each change
   - If behavior must change, document why

3. **Domain-Driven Design:**
   - Maintain clear ubiquitous language
   - Keep domain layer independent of infrastructure
   - Use value objects for immutable concepts
   - Use entities for things with identity and lifecycle
   - Use domain services for operations that don't belong to entities

4. **Hexagonal Architecture:**
   - Dependencies point inward (Domain ← Application ← Adapters)
   - Domain has zero external dependencies
   - Use ports (interfaces) for all dependencies

### Code Review Checklist

For each refactoring PR:

- [ ] BDD tests exist and pass
- [ ] No new dependencies in domain layer
- [ ] Code follows Go conventions (gofmt, golint)
- [ ] Clear commit messages explaining changes
- [ ] Documentation updated if needed
- [ ] No breaking changes to public APIs (or documented)
- [ ] Performance implications considered

### Risk Mitigation

**High-Risk Refactorings:**
- Ship entity decomposition (touches many files)
- State machine extraction (affects 3 entities)
- Interface splits (affects adapters)

**Mitigation Strategies:**
1. Feature flags for gradual rollout (if applicable)
2. Comprehensive BDD tests before refactoring
3. Pair programming for complex changes
4. Extra review scrutiny
5. Rollback plan documented

---

## Success Metrics

### Quantitative Metrics

- **Code Duplication:** Reduce by 200+ lines
- **Ship Entity Size:** Reduce from 521 to ~250 lines
- **Test Coverage:** Maintain or improve 68.3%
- **Cyclomatic Complexity:** Reduce in key methods
- **Build Time:** Should not increase

### Qualitative Metrics

- **Maintainability:** Easier to add new features
- **Testability:** Services can be tested in isolation
- **Clarity:** Clearer separation of concerns
- **Extensibility:** New flight modes, states easier to add

---

## Appendix A: Files Requiring Changes

### Phase 1 (P0)

**New Files:**
- `internal/domain/shared/lifecycle_state_machine.go`
- `internal/domain/navigation/ship_fuel_service.go`
- `internal/domain/navigation/ship_navigation_calculator.go`
- `test/bdd/features/domain/shared/lifecycle_state_machine.feature`
- `test/bdd/steps/lifecycle_state_machine_steps.go`

**Modified Files:**
- `internal/domain/container/container.go`
- `internal/domain/mining/mining_operation.go`
- `internal/domain/navigation/route.go`
- `internal/domain/navigation/ship.go`
- Multiple BDD test files

### Phase 2 (P1)

**New Files:**
- `internal/domain/contract/contract_profitability_service.go`

**Modified Files:**
- `internal/domain/shared/waypoint.go`
- `internal/domain/contract/contract.go`
- `internal/domain/contract/fleet_assigner.go`
- `internal/domain/contract/ship_selector.go`
- Multiple BDD test files

### Phase 3 (P2)

**New Files:**
- `internal/domain/shared/flight_mode_selector.go`

**Modified Files:**
- `internal/domain/navigation/ports.go`
- `internal/domain/shared/flight_mode.go`
- Adapter implementations (persistence, API, gRPC)
- Multiple BDD test files

---

## Appendix B: Summary Table

| # | Issue | Severity | Priority | LOC Impact | Effort (days) |
|---|-------|----------|----------|------------|---------------|
| 1 | Duplicate State Machine | HIGH | P0 | -150 | 2-3 |
| 2 | Ship SRP Violation | HIGH | P0 | -271 | 3-4 |
| 3 | Scattered Fuel Logic | HIGH | P1 | -20 | 1-2 |
| 4 | Duplicate Distance Calc | HIGH | P1 | -30 | 1 |
| 5 | Contract Profitability | HIGH | P1 | -107 | 2 |
| 6 | ShipRepository ISP | MEDIUM | P2 | - | 2-3 |
| 7 | FlightMode OCP | MEDIUM | P2 | +50/-29 | 2 |
| 8 | Timestamp Data Clump | MEDIUM | P3 | (Part of #1) | - |
| 9 | Magic Numbers | MEDIUM | P3 | +10 | 1 |
| **Total** | | | | **-547 lines** | **14-18 days** |

---

## Conclusion

This refactoring plan addresses systematic issues in the domain layer while maintaining backward compatibility and test coverage. The phased approach allows for incremental improvements with manageable risk.

**Next Steps:**
1. Review and approve this plan
2. Create GitHub issues for each sprint
3. Begin Phase 1, Sprint 1.1 (State Machine Extraction)
4. Track progress in project board

**Questions or Concerns:** Discuss with team before beginning implementation.

---

**Document Version:** 1.0
**Last Updated:** 2025-11-21
**Author:** Domain Layer Analysis
**Status:** Awaiting Approval

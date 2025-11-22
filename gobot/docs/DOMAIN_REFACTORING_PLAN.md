# Domain Layer Refactoring Plan

**Date:** 2025-11-21
**Status:** Ready for Implementation
**Scope:** `internal/domain/` package improvements

---

## Executive Summary

This plan addresses code quality issues in the domain layer to improve maintainability, testability, and adherence to SOLID principles.

**Key Opportunities:**
- 8 refactoring opportunities identified
- ~370 lines of code can be eliminated through deduplication
- Several SOLID principle violations to address
- Estimated effort: 10-14 days

**Expected Benefits:**
- Reduced code duplication by ~370 lines
- Better separation of concerns
- Improved testability through focused components
- Easier to extend and maintain

---

## Refactoring Opportunities

### Priority 0 (Immediate) - Foundation Work

#### 1. Extract Lifecycle State Machine Pattern

**Problem:**
Three entities (Container, MiningOperation, Route) implement nearly identical state machine logic with duplicated lifecycle methods and timestamp tracking.

**Current Duplication:**
- **Container** (`internal/domain/container/container.go:139-213`)
- **MiningOperation** (`internal/domain/mining/mining_operation.go:115-170`)
- **Route** (`internal/domain/navigation/route.go:167-200`)

All three implement:
- State transitions: `Start()`, `Complete()`, `Fail()`, `Stop()`
- State queries: `IsRunning()`, `IsFinished()`
- Timestamps: `createdAt`, `updatedAt`, `startedAt`, `stoppedAt`
- Runtime calculations: `RuntimeDuration()`

**Solution:**
Create a reusable lifecycle state machine component using composition.

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

func NewLifecycleStateMachine(clock Clock) *LifecycleStateMachine
func (sm *LifecycleStateMachine) Start() error
func (sm *LifecycleStateMachine) Complete() error
func (sm *LifecycleStateMachine) Fail(err error) error
func (sm *LifecycleStateMachine) Stop() error
func (sm *LifecycleStateMachine) IsRunning() bool
func (sm *LifecycleStateMachine) IsFinished() bool
func (sm *LifecycleStateMachine) RuntimeDuration() time.Duration
```

Refactor entities to use composition:
```go
type Container struct {
    lifecycle *shared.LifecycleStateMachine
    // ... container-specific fields
}
```

**Impact:**
- Eliminates ~150 lines of duplication
- Single source of truth for lifecycle behavior
- Easier to test lifecycle logic in isolation

**Effort:** 2-3 days

---

#### 2. Complete Ship Entity Decomposition

**Problem:**
Ship entity is 516 lines with multiple responsibilities. While `ShipFuelService` and `ShipNavigationCalculator` have been extracted, the Ship entity still contains many wrapper methods and hasn't been sufficiently reduced.

**Current State:**
- Ship.go: 516 lines (target: ~250 lines)
- ShipFuelService: EXISTS (146 lines) ✓
- ShipNavigationCalculator: EXISTS (67 lines) ✓

**Remaining Work:**
Remove unnecessary wrapper methods from Ship entity. Ship should only contain:
- Core state management (dock, orbit, transit)
- Direct fuel/cargo operations
- Simple getters and state queries

Move complex logic entirely to services:
- Refueling decisions → ShipFuelService
- Navigation calculations → ShipNavigationCalculator

**Example Cleanup:**
```go
// REMOVE from Ship entity (lines 459-516)
func (s *Ship) ShouldRefuelOpportunistically(...) bool
func (s *Ship) ShouldPreventDriftMode(...) bool

// Keep simple operations
func (s *Ship) ConsumeFuel(amount int) error
func (s *Ship) Refuel(amount int) error
```

**Impact:**
- Ship.go from 516 to ~250 lines
- Clearer separation of concerns
- Better SRP compliance

**Effort:** 1-2 days

---

### Priority 1 (High) - High-Value Improvements

#### 3. Eliminate Duplicate Distance Calculations

**Problem:**
The "find nearest waypoint" pattern is duplicated across 3 locations:

1. `internal/domain/contract/fleet_assigner.go:87-100`
2. `internal/domain/contract/fleet_assigner.go:211-224`
3. `internal/domain/contract/ship_selector.go:93-106`

**Duplicated Pattern:**
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

**Solution:**
Extract to utility methods in `domain/shared/waypoint.go`:

```go
// FindNearestWaypoint returns the nearest waypoint from a list and its distance
func FindNearestWaypoint(from *Waypoint, targets []*Waypoint) (*Waypoint, float64)

// CalculateTotalDistanceToNearestWaypoints calculates sum of distances
func CalculateTotalDistanceToNearestWaypoints(ships []*Ship, targets []*Waypoint) float64
```

**Impact:**
- Eliminates ~30 lines of duplication
- Reusable navigation utilities
- Consistent distance calculations

**Effort:** 1 day

---

#### 4. Extract Contract Profitability Service

**Problem:**
Contract entity contains a 76-line profitability calculation method (`EvaluateProfitability` at lines 181-256) that performs complex domain service logic rather than entity behavior.

**Current Location:**
`internal/domain/contract/contract.go:181-256`

**Issues:**
- Complex financial calculations embedded in entity
- Violates Single Responsibility Principle
- Difficult to test profitability logic in isolation
- Contract becomes responsible for business analysis

**Solution:**
Extract to dedicated domain service:

```go
// domain/contract/contract_profitability_service.go
type ContractProfitabilityService struct{}

func NewContractProfitabilityService() *ContractProfitabilityService

func (s *ContractProfitabilityService) EvaluateProfitability(
    contract *Contract,
    context ProfitabilityContext,
) (*ProfitabilityEvaluation, error)

func (s *ContractProfitabilityService) EstimateRevenue(...) int
func (s *ContractProfitabilityService) EstimateTravelCosts(...) int
func (s *ContractProfitabilityService) EstimatePurchaseCosts(...) int
```

Contract entity simplifies to delegation:
```go
func (c *Contract) EvaluateProfitability(
    service *ContractProfitabilityService,
    context ProfitabilityContext,
) (*ProfitabilityEvaluation, error) {
    return service.EvaluateProfitability(c, context)
}
```

**Impact:**
- Better SRP compliance
- Easier to test profitability logic
- Contract entity focuses on contract lifecycle

**Effort:** 2 days

---

### Priority 2 (Medium) - SOLID Compliance

#### 5. Split ShipRepository Interface (ISP)

**Problem:**
ShipRepository interface violates Interface Segregation Principle with 10 methods mixing different concerns.

**Current Interface** (`internal/domain/navigation/ports.go:9-39`):
- Query operations: `FindBySymbol`, `GetShipData`, `FindAllByPlayer`
- Navigation commands: `Navigate`, `Dock`, `Orbit`, `Refuel`, `SetFlightMode`
- Cargo operations: `JettisonCargo`

**Issues:**
- Implementations must implement all methods even if only needing a subset
- Difficult to mock in tests (must mock all 10 methods)
- Violates ISP

**Solution:**
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

// ShipRepository combines all for convenience
type ShipRepository interface {
    ShipQueryRepository
    ShipCommandRepository
    ShipCargoRepository
}
```

**Impact:**
- Better ISP compliance
- Easier testing (mock only needed interfaces)
- Clear separation of query/command responsibilities

**Effort:** 2-3 days

---

#### 6. Apply Strategy Pattern to FlightMode Selection

**Problem:**
`SelectOptimalFlightMode` function (`internal/domain/shared/flight_mode.go:76-104`) uses hard-coded if-else logic that violates Open/Closed Principle.

**Current Implementation:**
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

**Issues:**
- Adding new flight modes requires modifying this function
- Not open for extension
- Complex branching logic (cyclomatic complexity ~8)

**Solution:**
Strategy pattern with priority-based selection:

```go
// domain/shared/flight_mode_selector.go
type FlightModeStrategy interface {
    CanUse(currentFuel, fuelCost, safetyMargin int) bool
    Priority() int  // Higher = preferred
    Mode() FlightMode
}

type BurnModeStrategy struct{}
func (s *BurnModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool
func (s *BurnModeStrategy) Priority() int { return 3 }
func (s *BurnModeStrategy) Mode() FlightMode { return FlightModeBurn }

type FlightModeSelector struct {
    strategies []FlightModeStrategy
}

func (s *FlightModeSelector) SelectOptimalMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
    for _, strategy := range s.strategies {
        if strategy.CanUse(currentFuel, fuelCost, safetyMargin) {
            return strategy.Mode()
        }
    }
    return FlightModeDrift // Safe default
}
```

**Impact:**
- Better OCP compliance
- Easy to add new flight modes
- More maintainable and testable

**Effort:** 2 days

---

### Priority 3 (Low) - Code Quality Polish

#### 7. Extract Magic Numbers to Named Constants

**Problem:**
Hard-coded magic numbers throughout domain layer make business rules unclear.

**Current Magic Numbers:**

| Location | Line | Value | Context |
|----------|------|-------|---------|
| `container/container.go` | 114 | 3 | Max restarts |
| `contract/fleet_assigner.go` | 14, 18 | 2 | Max ships per waypoint/market |
| `navigation/ship.go` | 394 | 4 | Fuel safety margin |
| `contract/contract.go` | 178 | -5000 | Min profit threshold |

**Solution:**
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

**Impact:**
- Better code readability
- Documented business policies
- Easier to adjust thresholds

**Effort:** 1 day

---

#### 8. Polish Fuel Logic Consistency

**Problem:**
Minor inconsistencies in fuel logic usage across the codebase.

**Findings:**
- Ship.go has duplicate fuel percentage calculations (lines 482, 514)
- Already uses `Fuel.Percentage()` method (good!)
- ShipFuelService consolidates most logic (good!)

**Solution:**
Minor cleanup to ensure complete consistency:

1. Review all fuel percentage calculations use `Fuel.Percentage()`
2. Ensure all refueling decisions go through ShipFuelService
3. Document fuel safety policies in ShipFuelService

**Impact:**
- Complete fuel logic consolidation
- Consistent fuel handling patterns

**Effort:** 0.5 days

---

## Implementation Roadmap

### Phase 1: Foundation (P0) - 3-5 Days

**Sprint 1.1: Lifecycle State Machine Extraction**
- Create `domain/shared/lifecycle_state_machine.go`
- Write comprehensive BDD tests for state machine
- Refactor Container entity to use lifecycle component
- Refactor MiningOperation entity to use lifecycle component
- Refactor Route entity to use lifecycle component
- Update all related BDD tests

**Estimated:** 2-3 days
**Impact:** -150 lines of duplication

**Sprint 1.2: Complete Ship Entity Cleanup**
- Remove unnecessary wrapper methods from Ship entity
- Ensure all complex logic is in services
- Update Ship entity to focus on core responsibilities
- Update BDD tests

**Estimated:** 1-2 days
**Impact:** Ship.go from 516 to ~250 lines

---

### Phase 2: High-Value Improvements (P1) - 3-4 Days

**Sprint 2.1: Distance Calculation Utilities**
- Add `FindNearestWaypoint()` to `domain/shared/waypoint.go`
- Add `CalculateTotalDistanceToNearestWaypoints()` helper
- Replace duplicate implementations in FleetAssigner (2 locations)
- Replace duplicate implementation in ShipSelector
- Write BDD tests for new utilities

**Estimated:** 1 day
**Impact:** -30 lines duplication

**Sprint 2.2: Contract Profitability Service**
- Create `domain/contract/contract_profitability_service.go`
- Extract profitability calculation logic to service
- Update Contract entity to delegate to service
- Write BDD tests for service
- Update existing contract BDD tests

**Estimated:** 2 days
**Impact:** Better SRP, improved testability

**Sprint 2.3: Fuel Logic Polish**
- Review and consolidate all fuel logic
- Ensure consistent use of `Fuel.Percentage()`
- Document fuel policies in ShipFuelService
- Update BDD tests

**Estimated:** 0.5 days
**Impact:** Complete fuel consolidation

---

### Phase 3: SOLID & Polish (P2-P3) - 4-5 Days

**Sprint 3.1: Interface Segregation**
- Split ShipRepository into focused interfaces
- Update adapter implementations
- Update command handlers to use specific interfaces
- Update dependency injection
- Update tests

**Estimated:** 2-3 days
**Impact:** Better ISP compliance, easier testing

**Sprint 3.2: FlightMode Strategy Pattern**
- Create `domain/shared/flight_mode_selector.go`
- Implement strategy interfaces for each flight mode
- Replace `SelectOptimalFlightMode` with selector
- Write BDD tests for selector
- Update existing tests

**Estimated:** 2 days
**Impact:** Better OCP compliance

**Sprint 3.3: Magic Numbers Extraction**
- Extract all magic numbers to named constants
- Add business reasoning comments
- Document domain policies

**Estimated:** 1 day
**Impact:** Better readability

---

## Testing Strategy

**CRITICAL:** All refactoring must maintain or improve test coverage.

### BDD Testing Approach

1. **Tests First:**
   - Write BDD tests for new components BEFORE refactoring
   - Ensure existing BDD tests pass AFTER refactoring
   - Add new scenarios for extracted functionality

2. **Test Location:**
   - All tests in `test/bdd/` directory
   - NEVER create `*_test.go` files alongside production code
   - Follow existing BDD test patterns

3. **Coverage Target:**
   - Maintain current domain coverage
   - Aim to improve coverage with new tests
   - Focus on behavior-driven scenarios

### Test Execution

```bash
# Run all BDD tests
make test-bdd

# Run specific feature
go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/shared/lifecycle_state_machine.feature

# Generate coverage
make test-coverage
```

---

## Implementation Guidelines

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

---

## Success Metrics

### Quantitative Metrics

- **Code Duplication:** Reduce by ~370 lines
- **Ship Entity Size:** Reduce from 516 to ~250 lines
- **Test Coverage:** Maintain or improve current coverage
- **Cyclomatic Complexity:** Reduce in SelectOptimalFlightMode and profitability calculations

### Qualitative Metrics

- **Maintainability:** Easier to add new features
- **Testability:** Services can be tested in isolation
- **Clarity:** Clearer separation of concerns
- **Extensibility:** New flight modes and states easier to add

---

## Priority Summary

| Priority | Issues | Total Effort | Impact |
|----------|--------|--------------|--------|
| P0 (Immediate) | #1, #2 | 3-5 days | -150 LOC, Ship cleanup |
| P1 (High) | #3, #4, #8 | 3-4 days | -30 LOC, better SRP |
| P2 (Medium) | #5, #6 | 4-5 days | Better SOLID compliance |
| P3 (Low) | #7 | 1 day | Better readability |
| **Total** | **8 issues** | **10-14 days** | **~370 LOC reduction** |

---

## Getting Started

1. **Review this plan** with the team
2. **Create GitHub issues** for each sprint
3. **Begin Phase 1, Sprint 1.1** (Lifecycle State Machine)
4. **Track progress** using project board

---

**Document Version:** 1.0
**Created:** 2025-11-21
**Status:** Ready for Implementation

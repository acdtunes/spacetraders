# Remaining Domain Layer Improvement Opportunities

**Status:** Ready for Future Implementation
**Last Updated:** 2025-11-22

---

## Overview

This document outlines remaining opportunities to improve the domain layer's code quality, maintainability, and adherence to SOLID principles. These improvements build on recent refactoring work that successfully integrated the LifecycleStateMachine pattern into MiningOperation and eliminated distance calculation duplication.

---

## High-Impact Opportunities

### 1. Complete Lifecycle State Machine Adoption

**Current State:**
- ✅ MiningOperation uses LifecycleStateMachine
- ⏳ Container still has inline lifecycle management
- ⏳ Route still has inline lifecycle management

**Opportunity:**

Extend lifecycle state machine pattern to remaining entities:

**Container Entity** (316 lines):
- Needs special handling for STOPPING and INTERRUPTED states
- Current lifecycle fields: status, createdAt, updatedAt, startedAt, stoppedAt, lastError
- Consider: Extend LifecycleStateMachine to support STOPPING state or manage it separately
- Estimated effort: 3-4 days (complex due to special states)

**Route Entity** (navigation/route.go):
- Simpler than Container - uses PLANNED, EXECUTING, COMPLETED, FAILED, ABORTED
- Could benefit from lifecycle component for timestamp/error management
- Estimated effort: 1-2 days

**Benefits:**
- Eliminate ~74 more lines of duplicate lifecycle code
- Consistent lifecycle behavior across all domain entities
- Single source of truth for state transitions

---

### 2. Ship Entity Decomposition

**Current State:**
- Ship.go: 516 lines (target: ~250 lines)
- ShipFuelService: EXISTS (146 lines) ✓
- ShipNavigationCalculator: EXISTS (67 lines) ✓

**Opportunity:**

The Ship entity still contains wrapper methods that simply delegate to services. These should be removed to enforce proper Tell-Don't-Ask principle.

**Cleanup Example:**
```go
// REMOVE these wrapper methods from Ship:
func (s *Ship) ShouldRefuelOpportunistically(...) bool
func (s *Ship) ShouldPreventDriftMode(...) bool

// These are ShipFuelService concerns, not Ship entity concerns
// Callers should use the service directly
```

**Keep in Ship:**
- Core state management (Dock, Orbit, Navigate)
- Direct fuel/cargo operations (ConsumeFuel, Refuel, AddCargo)
- Simple getters and state queries
- Navigation state tracking

**Move to Services:**
- All refueling decisions → ShipFuelService
- All navigation calculations → ShipNavigationCalculator

**Benefits:**
- Ship.go reduced from 516 to ~250 lines
- Clearer separation of concerns
- Better Single Responsibility Principle compliance

**Estimated Effort:** 2 days

---

### 3. Extract Contract Profitability Service

**Current State:**
Contract entity contains a 76-line `EvaluateProfitability` method (lines 181-256) that performs complex financial calculations.

**Problem:**
- Complex business logic embedded in entity
- Violates Single Responsibility Principle
- Difficult to test profitability logic in isolation
- Contract entity responsible for both contract lifecycle AND business analysis

**Solution:**

Extract to dedicated domain service:

```go
// domain/contract/contract_profitability_service.go
type ContractProfitabilityService struct{}

func (s *ContractProfitabilityService) EvaluateProfitability(
    contract *Contract,
    context ProfitabilityContext,
) (*ProfitabilityEvaluation, error)

// Private helper methods:
func (s *ContractProfitabilityService) estimateRevenue(...) int
func (s *ContractProfitabilityService) estimateTravelCosts(...) int
func (s *ContractProfitabilityService) estimatePurchaseCosts(...) int
```

Contract simplifies to delegation:
```go
func (c *Contract) EvaluateProfitability(
    service *ContractProfitabilityService,
    context ProfitabilityContext,
) (*ProfitabilityEvaluation, error) {
    return service.EvaluateProfitability(c, context)
}
```

**Benefits:**
- Better SRP compliance
- Easier to test profitability logic in isolation
- Contract entity focuses on contract lifecycle
- Financial analysis logic can evolve independently

**Estimated Effort:** 2 days

---

## SOLID Principle Improvements

### 4. Split ShipRepository Interface (ISP Violation)

**Current State:**
ShipRepository interface has 10 methods mixing different concerns (navigation/ports.go:9-39):
- Query operations: `FindBySymbol`, `GetShipData`, `FindAllByPlayer`
- Navigation commands: `Navigate`, `Dock`, `Orbit`, `Refuel`, `SetFlightMode`
- Cargo operations: `JettisonCargo`

**Problem:**
- Implementations must implement all methods even if only needing a subset
- Difficult to mock in tests (must mock all 10 methods)
- Violates Interface Segregation Principle

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

**Benefits:**
- Better ISP compliance
- Easier testing (mock only needed interfaces)
- Clear separation of query/command responsibilities
- Enables CQRS pattern adoption in the future

**Estimated Effort:** 2-3 days

---

### 5. Apply Strategy Pattern to FlightMode Selection (OCP Violation)

**Current State:**
`SelectOptimalFlightMode` function (shared/flight_mode.go:76-104) uses hard-coded if-else logic.

**Problem:**
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

- Adding new flight modes requires modifying this function
- Violates Open/Closed Principle
- Cyclomatic complexity ~8

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
func (s *BurnModeStrategy) CanUse(currentFuel, fuelCost, safetyMargin int) bool { ... }
func (s *BurnModeStrategy) Priority() int { return 3 }
func (s *BurnModeStrategy) Mode() FlightMode { return FlightModeBurn }

type CruiseModeStrategy struct{}
// ... similar implementation

type FlightModeSelector struct {
    strategies []FlightModeStrategy
}

func (s *FlightModeSelector) SelectOptimalMode(currentFuel, fuelCost, safetyMargin int) FlightMode {
    // Sort strategies by priority (highest first)
    for _, strategy := range s.strategies {
        if strategy.CanUse(currentFuel, fuelCost, safetyMargin) {
            return strategy.Mode()
        }
    }
    return FlightModeDrift // Safe default
}
```

**Benefits:**
- Better OCP compliance (open for extension, closed for modification)
- Easy to add new flight modes without touching existing code
- Each strategy testable in isolation
- More maintainable

**Estimated Effort:** 2 days

---

## Minor Quality Improvements

### 6. Fuel Logic Consistency Polish

**Current State:**
- Ship.go has some inline fuel logic
- Most fuel logic properly consolidated in ShipFuelService
- All fuel percentage calculations use `Fuel.Percentage()` ✓

**Opportunity:**

Minor cleanup to ensure complete consistency:
1. Review that all fuel percentage calculations use `Fuel.Percentage()`
2. Ensure all refueling decisions go through ShipFuelService
3. Document fuel safety policies in ShipFuelService

**Benefits:**
- Complete fuel logic consolidation
- Consistent fuel handling patterns
- Better documentation of fuel policies

**Estimated Effort:** 0.5 days

---

## Implementation Guidelines

### Testing Requirements
- All refactoring MUST maintain or improve test coverage
- Write BDD tests BEFORE refactoring
- All tests in `test/bdd/` directory (NEVER create `*_test.go` files alongside production code)
- Run full BDD test suite after each change: `make test-bdd`

### Refactoring Principles
1. **Incremental Changes:** Make small, focused changes that compile and pass tests
2. **Preserve Behavior:** Refactoring should not change behavior
3. **Domain-Driven Design:** Maintain clear ubiquitous language
4. **Hexagonal Architecture:** Dependencies point inward (Domain ← Application ← Adapters)

### Code Review Checklist
- [ ] BDD tests exist and pass
- [ ] No new dependencies in domain layer
- [ ] Code follows Go conventions (gofmt, golint)
- [ ] Clear commit messages explaining changes
- [ ] No breaking changes to public APIs (or documented)

---

## Priority Recommendations

**Immediate Value (1-2 weeks):**
1. Extract Contract Profitability Service (2 days)
2. Ship Entity Cleanup (2 days)
3. Fuel Logic Polish (0.5 days)

**High Impact (2-3 weeks):**
4. Route Entity Lifecycle Integration (1-2 days)
5. ShipRepository Interface Segregation (2-3 days)
6. FlightMode Strategy Pattern (2 days)

**Complex but Valuable (3-4 weeks):**
7. Container Entity Lifecycle Integration (3-4 days)

---

## Success Metrics

### Quantitative
- **Code Duplication:** Further reduce by ~150 lines
- **Ship Entity Size:** Reduce from 516 to ~250 lines
- **Test Coverage:** Maintain 100% domain coverage
- **Cyclomatic Complexity:** Reduce in flight mode selection

### Qualitative
- **Maintainability:** Easier to add new features
- **Testability:** Services tested in isolation
- **Clarity:** Clear separation of concerns
- **Extensibility:** New flight modes and states easier to add

---

**Document Version:** 1.0
**Created:** 2025-11-22
**Status:** Ready for Implementation

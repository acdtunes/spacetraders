# BDD Test Suite Migration Plan

**Status:** Planning
**Owner:** TBD
**Start Date:** TBD
**Target Completion:** 8 weeks from start
**Estimated Effort:** 145-225 person-hours

---

## Executive Summary

Convert the entire test suite (117 test files) from subprocess-based test execution to idiomatic pytest-bdd with Gherkin scenarios and proper step definitions. The current implementation uses a temporary bridge that spawns separate pytest processes for each test module, which is inefficient, non-idiomatic, and difficult to maintain.

**Key Goals:**
- ✅ Replace subprocess bridge with first-class pytest-bdd step definitions
- ✅ Express all tests as business-readable Gherkin scenarios
- ✅ Create domain-oriented step library with shared and domain-specific steps
- ✅ Preserve 100% test coverage
- ✅ Improve test execution performance by ~40% (<30s vs ~50s+)
- ✅ Enable single pytest process execution
- ✅ Create comprehensive testing documentation

---

## Current State Analysis

### Test Suite Inventory

| Category | Files | Lines | Status |
|----------|-------|-------|--------|
| **Domain Tests** | 94 | ~28,843 | Legacy pytest via subprocess |
| **Unit Tests** | 23 | ~5,424 | Legacy pytest via subprocess |
| **BDD Tests** | 1 | 1,009 | Proper pytest-bdd (reference) |
| **Feature Files** | 15 | 4,771 | Shell scripts only |
| **Total** | **133** | **~40,047** | **Mixed state** |

### Domain Distribution

```
Domain                Test Files    Lines      Priority
──────────────────────────────────────────────────────────────
trade                 21           ~6,200     HIGH (Phase 2)
routing               18           ~5,400     HIGH (Phase 2)
scouting              11           ~3,300     MEDIUM (Phase 3)
circuit_breaker       10           ~3,000     PILOT (Phase 1)
navigation            9            ~2,700     MEDIUM (Phase 3)
contracts             6            ~1,800     MEDIUM (Phase 3)
refueling             5            ~1,500     MEDIUM (Phase 3)
touring               5            ~1,500     MEDIUM (Phase 3)
infrastructure        2            ~600       LOW (Phase 3)
operations            3            ~900       HIGH (Phase 2)
mining                1            ~300       LOW (Phase 3)
analysis              1            ~300       LOW (Phase 3)
caching               1            ~300       LOW (Phase 3)
visualization         1            ~300       LOW (Phase 3)
──────────────────────────────────────────────────────────────
unit/*                23           ~5,424     DEFERRED (Phase 4)
```

### Technical Debt: The Bridge Mechanism

**File:** `tests/bdd/steps/test_domain_features.py`

**Problem:**
```python
def _run_pytest_on_module(domain: str, module: str):
    """Execute a pytest run for the given domain test module in a subprocess."""
    process = subprocess.run(
        [sys.executable, "-m", "pytest", str(module_path), "-q"],
        capture_output=True,
        text=True,
        cwd=REPO_ROOT.parent,
    )
    return process
```

**Issues:**
1. **Performance:** ~200ms overhead per file × 94 files = ~19s wasted
2. **Isolation:** Tests can't share fixtures or state
3. **Non-idiomatic:** Feature files contain module names, not behaviors
4. **Maintenance:** Two parallel test systems
5. **Discovery:** `pytest.ini` excludes domains from pytest discovery
6. **Parallelization:** Sequential subprocess execution prevents parallel runs

### Current Feature File Anti-Pattern

```gherkin
# ❌ CURRENT: Not business-readable
Feature: Circuit Breaker validation

  Scenario Outline: Validate Circuit Breaker module <module>
    When I execute the "circuit_breaker" domain module "<module>"
    Then the module should pass

  Examples:
    | module |
    | test_circuit_breaker_smart_skip.py |
```

```gherkin
# ✅ TARGET: Business-readable behavior
Feature: Circuit Breaker Price Validation

  Scenario: Skip failed segment when independent segments remain profitable
    Given a 5-segment multileg trade route
    And segments 4 and 5 are independent of segment 3
    When segment 3 fails due to price spike
    And remaining profit exceeds 5000 credits
    Then the circuit breaker should skip segment 3
    And segments 4 and 5 should execute successfully
    And final profit should be approximately 120000 credits
```

---

## Target Architecture

### Directory Structure

```
tests/
├── features/                    # Gherkin feature files (business-readable)
│   ├── trade/
│   │   ├── circuit_breaker.feature
│   │   ├── multileg_trading.feature
│   │   └── price_validation.feature
│   ├── routing/
│   │   ├── ortools_optimization.feature
│   │   └── fuel_aware_pathfinding.feature
│   ├── scouting/
│   │   ├── market_coordination.feature
│   │   └── tour_optimization.feature
│   ├── contracts/
│   │   ├── fulfillment.feature
│   │   └── negotiation.feature
│   ├── navigation/
│   │   ├── smart_navigator.feature
│   │   └── checkpoints.feature
│   ├── refueling/
│   │   ├── intermediate_refuel.feature
│   │   └── fuel_station_bugs.feature
│   ├── touring/
│   │   ├── tour_optimization.feature
│   │   └── cache_management.feature
│   ├── mining/
│   │   └── profit_calculation.feature
│   ├── operations/
│   │   └── mcp_tools.feature
│   ├── analysis/
│   │   └── dependency_analysis.feature
│   ├── caching/
│   │   └── cache_validation.feature
│   ├── infrastructure/
│   │   └── component_interactions.feature
│   ├── visualization/
│   │   └── coordinates.feature
│   └── unit/
│       └── (TBD based on Phase 4 analysis)
│
├── bdd/
│   └── steps/
│       ├── common/              # Shared steps across all domains
│       │   ├── __init__.py
│       │   ├── navigation_steps.py      # Given ship at waypoint X
│       │   ├── market_steps.py          # Given market prices
│       │   ├── assertion_steps.py       # Then result should be
│       │   └── mock_setup_steps.py      # Common mock configuration
│       │
│       ├── fixtures/            # Reusable fixture library
│       │   ├── __init__.py
│       │   ├── mock_api.py      # MockAPIClient (from tests/mock_api.py)
│       │   ├── mock_db.py       # Database fixtures
│       │   ├── mock_ships.py    # Ship fixtures
│       │   ├── mock_navigator.py
│       │   └── mock_daemon.py
│       │
│       ├── trade_steps.py       # Trade-specific steps
│       ├── routing_steps.py     # Routing-specific steps
│       ├── scouting_steps.py    # Scouting-specific steps
│       ├── circuit_breaker_steps.py
│       ├── contracts_steps.py
│       ├── mining_steps.py
│       ├── navigation_steps.py  # Navigation-specific (domain-level)
│       ├── refueling_steps.py
│       ├── touring_steps.py
│       ├── operations_steps.py
│       ├── analysis_steps.py
│       ├── caching_steps.py
│       ├── infrastructure_steps.py
│       ├── visualization_steps.py
│       └── unit_steps.py
│
├── conftest.py                  # pytest-bdd configuration (enhance)
├── pytest.ini                   # Remove norecursedirs exclusion
└── mock_api.py                  # MIGRATE TO: bdd/steps/fixtures/mock_api.py
```

### Step Definition Pattern

```python
# tests/bdd/steps/circuit_breaker_steps.py
"""Step definitions for circuit breaker price validation scenarios."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios
from .fixtures.mock_api import MockAPIClient
from .fixtures.mock_ships import create_mock_ship
from spacetraders_bot.operations.multileg_trader import (
    MultiLegRoute,
    RouteSegment,
    TradeAction,
    execute_multileg_route,
)

# Load all circuit breaker scenarios
scenarios('../features/trade/circuit_breaker.feature')


@pytest.fixture
def circuit_breaker_context():
    """Shared context for circuit breaker scenarios."""
    return {
        'route': None,
        'ship': None,
        'api': None,
        'db': None,
        'result': None,
        'executed_segments': [],
        'transactions': [],
    }


@given(parsers.parse('a {segment_count:d}-segment multileg trade route'))
def setup_route(circuit_breaker_context, segment_count):
    """Create multileg route with specified segments."""
    # Extract logic from test_circuit_breaker_smart_skip.py:682-759
    segments = []
    for i in range(segment_count):
        # Build segment based on test data
        segment = RouteSegment(...)
        segments.append(segment)

    circuit_breaker_context['route'] = MultiLegRoute(
        segments=segments,
        total_profit=sum(s.cumulative_profit for s in segments[-1:]),
        total_distance=sum(s.distance for s in segments),
        total_fuel_cost=sum(s.fuel_cost for s in segments),
        estimated_time_minutes=sum(s.distance for s in segments) * 0.5
    )


@given(parsers.parse('segments {seg1:d} and {seg2:d} are independent of segment {seg3:d}'))
def segments_are_independent(circuit_breaker_context, seg1, seg2, seg3):
    """Mark segments as independent (no cargo/credit dependencies)."""
    # This is verified implicitly by route structure
    # Independent segments use different goods
    pass


@when(parsers.parse('segment {index:d} fails due to {reason}'))
def simulate_segment_failure(circuit_breaker_context, index, reason):
    """Simulate segment failure (price spike, cargo overflow, etc.)."""
    # Mock market data to trigger failure
    if reason == "price spike":
        # Extract logic from test_circuit_breaker_smart_skip.py:656-662
        circuit_breaker_context['api'].get_market = Mock(return_value={
            'tradeGoods': [
                {'symbol': 'CLOTHING', 'sellPrice': 1360, 'purchasePrice': 1900}
            ]
        })


@then(parsers.parse('the circuit breaker should skip segment {index:d}'))
def verify_segment_skipped(circuit_breaker_context, index):
    """Verify segment was skipped."""
    executed = circuit_breaker_context['executed_segments']
    assert index not in executed, f"Segment {index} should have been skipped"


@then(parsers.parse('segments {seg1:d} and {seg2:d} should execute successfully'))
def verify_segments_executed(circuit_breaker_context, seg1, seg2):
    """Verify specified segments executed."""
    executed = circuit_breaker_context['executed_segments']
    assert seg1 in executed, f"Segment {seg1} should have executed"
    assert seg2 in executed, f"Segment {seg2} should have executed"


@then(parsers.parse('final profit should be approximately {profit:d} credits'))
def verify_final_profit(circuit_breaker_context, profit):
    """Verify final profit within tolerance."""
    actual_profit = circuit_breaker_context.get('final_profit', 0)
    tolerance = profit * 0.1  # 10% tolerance
    assert abs(actual_profit - profit) <= tolerance, \
        f"Profit {actual_profit:,} not within {tolerance:,} of {profit:,}"
```

### Common Steps Library

```python
# tests/bdd/steps/common/navigation_steps.py
"""Shared navigation steps used across multiple domains."""

from pytest_bdd import given, when, then, parsers


@given(parsers.parse('ship "{ship_symbol}" is at waypoint "{waypoint}"'))
def ship_at_waypoint(context, ship_symbol, waypoint):
    """Position ship at specific waypoint."""
    context.setdefault('ships', {})[ship_symbol] = {
        'nav': {'waypointSymbol': waypoint, 'status': 'DOCKED'},
        'fuel': {'current': 400, 'capacity': 400},
        'cargo': {'capacity': 40, 'units': 0, 'inventory': []},
    }


@given(parsers.parse('ship "{ship_symbol}" has {fuel:d} fuel'))
def ship_has_fuel(context, ship_symbol, fuel):
    """Set ship fuel level."""
    if ship_symbol not in context.get('ships', {}):
        context.setdefault('ships', {})[ship_symbol] = {}
    context['ships'][ship_symbol]['fuel'] = {
        'current': fuel,
        'capacity': 400
    }


@when(parsers.parse('ship "{ship_symbol}" navigates to "{destination}"'))
def ship_navigates(context, ship_symbol, destination):
    """Execute navigation."""
    # Use SmartNavigator or mock navigation
    ship = context['ships'][ship_symbol]
    context['navigator'].execute_route(ship, destination)


@then(parsers.parse('ship "{ship_symbol}" should arrive at "{waypoint}"'))
def verify_arrival(context, ship_symbol, waypoint):
    """Verify ship arrived at destination."""
    ship = context['ships'][ship_symbol]
    assert ship['nav']['waypointSymbol'] == waypoint
```

---

## Implementation Plan

### Phase 1: Foundation & Reference Implementation (Week 1)

**Goal:** Establish patterns and validate approach with pilot domain

**Pilot Domain:** `circuit_breaker` (10 tests, ~3,000 lines)

**Why circuit_breaker?**
- Self-contained domain with clear business logic
- Well-documented test scenarios
- Medium complexity (not too simple, not too complex)
- High value (frequently modified)

#### Tasks

1. **Create step library structure** (Day 1-2)
   - [ ] Create `tests/bdd/steps/common/` directory
   - [ ] Create `tests/bdd/steps/fixtures/` directory
   - [ ] Move `tests/mock_api.py` → `tests/bdd/steps/fixtures/mock_api.py`
   - [ ] Create `tests/bdd/steps/fixtures/mock_db.py`
   - [ ] Create `tests/bdd/steps/fixtures/mock_ships.py`
   - [ ] Create `tests/bdd/steps/common/__init__.py`
   - [ ] Create `tests/bdd/steps/fixtures/__init__.py`

2. **Migrate pilot domain: circuit_breaker** (Day 3-4)
   - [ ] Analyze 10 circuit_breaker test files
   - [ ] Map regression functions to business scenarios
   - [ ] Rewrite `tests/features/circuit_breaker.feature` → `tests/features/trade/circuit_breaker.feature`
   - [ ] Create `tests/bdd/steps/circuit_breaker_steps.py`
   - [ ] Implement step definitions for all 10 scenarios
   - [ ] Extract common steps to `tests/bdd/steps/common/`
   - [ ] Run tests and verify 100% pass rate

3. **Documentation** (Day 5)
   - [ ] Create `TESTING_GUIDE.md` with BDD patterns section
   - [ ] Document step definition patterns
   - [ ] Document fixture usage
   - [ ] Add examples from circuit_breaker domain
   - [ ] Create contribution guide for writing new BDD tests

4. **Validation** (Day 5)
   - [ ] Run coverage report: `pytest tests/features/trade/circuit_breaker.feature --cov`
   - [ ] Compare coverage vs. legacy tests
   - [ ] Measure execution time (target: <500ms for 10 tests)
   - [ ] Code review with team

#### Deliverables

- ✅ Step library structure created
- ✅ 10 circuit_breaker scenarios passing
- ✅ Common steps library established
- ✅ Fixture library documented
- ✅ `TESTING_GUIDE.md` created
- ✅ Performance benchmark: <500ms (vs ~2s with bridge)

#### Success Criteria

- [ ] All 10 circuit_breaker tests pass without subprocess
- [ ] Test execution <500ms
- [ ] Zero coverage loss vs legacy tests
- [ ] Team approval of patterns

---

### Phase 2: Core Domain Migration (Weeks 2-3)

**Goal:** Migrate high-value, frequently-modified domains

**Domains:** `routing` (18 tests), `trade` (21 tests), `operations` (3 tests)
**Total:** 42 tests, ~12,500 lines

#### Week 2: Routing Domain (18 tests)

**Priority:** HIGH - OR-Tools optimization, VRP, TSP, complex algorithms

**Reference:** `tests/domain/routing/test_ortools_mining_steps.py` (already proper BDD)

##### Tasks

1. **Feature file organization** (Day 1)
   - [ ] Create `tests/features/routing/` directory
   - [ ] Analyze 18 routing test files
   - [ ] Group into logical feature files:
     - `ortools_optimization.feature` (VRP, TSP, partitioning)
     - `fuel_aware_routing.feature` (fuel calculations, refuel insertion)
     - `graph_operations.feature` (graph building, edge creation)
     - `routing_bugs.feature` (regression scenarios)

2. **Step definitions** (Day 2-3)
   - [ ] Create `tests/bdd/steps/routing_steps.py`
   - [ ] Migrate `test_ortools_mining_steps.py` steps as reference
   - [ ] Implement remaining routing steps
   - [ ] Extract graph-related steps to common library

3. **Migration** (Day 4-5)
   - [ ] Convert each routing test to Gherkin scenario
   - [ ] Run tests incrementally
   - [ ] Verify coverage preservation
   - [ ] Delete legacy tests after verification

##### Deliverables

- ✅ 18 routing scenarios passing
- ✅ `tests/features/routing/*.feature` created
- ✅ `tests/bdd/steps/routing_steps.py` complete
- ✅ Legacy `tests/domain/routing/` directory removed

#### Week 3: Trade & Operations Domains (24 tests)

**Trade Domain:** 21 tests (multileg trading, fleet optimization, price validation)
**Operations Domain:** 3 tests (MCP tools, API operations)

##### Tasks

1. **Trade domain migration** (Day 1-3)
   - [ ] Create `tests/features/trade/` directory
   - [ ] Feature files:
     - `multileg_trading.feature`
     - `circuit_breaker.feature` (enhance Phase 1 version)
     - `price_validation.feature`
     - `fleet_optimization.feature`
   - [ ] Create `tests/bdd/steps/trade_steps.py`
   - [ ] Implement step definitions
   - [ ] Verify 21 scenarios passing

2. **Operations domain migration** (Day 4)
   - [ ] Create `tests/features/operations/` directory
   - [ ] Feature files:
     - `mcp_tools.feature`
     - `api_operations.feature`
   - [ ] Create `tests/bdd/steps/operations_steps.py`
   - [ ] Implement step definitions
   - [ ] Verify 3 scenarios passing

3. **Cleanup** (Day 5)
   - [ ] Delete legacy domain directories
   - [ ] Update documentation
   - [ ] Coverage validation
   - [ ] Performance benchmark

##### Deliverables

- ✅ 42 total scenarios passing (Phase 2 complete)
- ✅ 3 domain directories removed
- ✅ Updated coverage report
- ✅ Performance improvement documented

---

### Phase 3: Remaining Domains (Weeks 4-5)

**Goal:** Complete migration of all remaining domains

**Domains:** `scouting`, `navigation`, `contracts`, `refueling`, `touring`, `infrastructure`, `mining`, `analysis`, `caching`, `visualization`
**Total:** 52 tests, ~13,200 lines

#### Week 4: Medium Priority Domains (27 tests)

1. **Scouting** (11 tests) - Day 1-2
2. **Navigation** (9 tests) - Day 3
3. **Contracts** (6 tests) - Day 4
4. **Refueling** (1 test) - Day 5

#### Week 5: Low Priority Domains (25 tests)

1. **Touring** (5 tests) - Day 1
2. **Refueling** (remaining 4 tests) - Day 2
3. **Infrastructure** (2 tests) - Day 3
4. **Single-test domains** (6 tests total) - Day 4
   - Mining (1)
   - Analysis (1)
   - Caching (1)
   - Visualization (1)
5. **Cleanup & validation** - Day 5

#### Process (Per Domain)

1. **Analyze** - Map test functions to business scenarios
2. **Design** - Write Gherkin feature files
3. **Implement** - Create step definitions
4. **Verify** - Run coverage and validate
5. **Delete** - Remove legacy tests

#### Deliverables

- ✅ All 94 domain tests migrated to BDD
- ✅ `tests/domain/` directory removed
- ✅ 100% coverage preservation verified
- ✅ Updated `TESTING_GUIDE.md` with all patterns

---

### Phase 4: Unit Tests Migration (Week 6)

**Goal:** Migrate unit tests to BDD where appropriate

**Scope:** 23 unit tests across 4 categories

#### Analysis (Day 1)

**Unit Test Categories:**
- `cli/` (1 test) - Main CLI entry point
- `core/` (7 tests) - API client, smart navigator, routing, OR-Tools
- `helpers/` (1 test) - Path utilities
- `operations/` (14 tests) - Operation modules

**Decision Matrix:**

| Test Type | Approach | Rationale |
|-----------|----------|-----------|
| **Behavioral tests** (e.g., CLI commands) | Migrate to BDD | Benefits from business-readable scenarios |
| **Integration tests** (e.g., component interactions) | Migrate to BDD | Clear acceptance criteria |
| **Pure unit tests** (e.g., utility functions) | Keep as pytest | No business behavior, just logic |

#### Migration Strategy (Day 2-4)

1. **Identify behavioral tests** (Day 2)
   - [ ] Review 23 unit tests
   - [ ] Classify as behavioral vs. pure unit
   - [ ] Document decisions

2. **Migrate behavioral tests** (Day 3)
   - [ ] Create `tests/features/unit/` if needed
   - [ ] Write Gherkin scenarios for behavioral tests
   - [ ] Create `tests/bdd/steps/unit_steps.py`

3. **Keep pure unit tests** (Day 4)
   - [ ] Move to `tests/unit/` with direct pytest discovery
   - [ ] Update `pytest.ini` to include unit tests
   - [ ] Verify all unit tests pass

#### Deliverables

- ✅ Unit tests categorized and migrated
- ✅ `tests/features/unit/` created if needed
- ✅ `pytest.ini` updated for unit test discovery
- ✅ Documentation updated

---

### Phase 5: Bridge Removal & Cleanup (Week 7)

**Goal:** Remove all legacy infrastructure

#### Tasks

1. **Delete bridge mechanism** (Day 1)
   - [ ] Delete `tests/bdd/steps/test_domain_features.py`
   - [ ] Delete old feature file shells in `tests/features/*.feature`
   - [ ] Update `tests/conftest.py` to remove bridge references

2. **Update pytest configuration** (Day 2)
   - [ ] Edit `pytest.ini`:
     - Remove `norecursedirs = domain unit`
     - Add BDD-specific configuration
   - [ ] Verify pytest discovery works for all tests
   - [ ] Test with `pytest --collect-only`

3. **Remove legacy infrastructure** (Day 3)
   - [ ] Delete `tests/domain/` directory (should be empty)
   - [ ] Clean up unused fixtures
   - [ ] Remove duplicate mock utilities
   - [ ] Archive old test files if needed

4. **Documentation cleanup** (Day 4)
   - [ ] Complete `TESTING_GUIDE.md`
   - [ ] Add pytest-bdd patterns section
   - [ ] Document all step patterns by domain
   - [ ] Add contribution guide
   - [ ] Update `CLAUDE.md` testing section

5. **Final validation** (Day 5)
   - [ ] Run full test suite: `pytest tests/`
   - [ ] Verify 100% pass rate
   - [ ] Check coverage: `pytest --cov=src`
   - [ ] Performance benchmark
   - [ ] Code review

#### Deliverables

- ✅ Bridge mechanism deleted
- ✅ Legacy test directories removed
- ✅ `pytest.ini` updated
- ✅ `TESTING_GUIDE.md` complete
- ✅ `CLAUDE.md` updated
- ✅ All tests passing

---

### Phase 6: Optimization & Documentation (Week 8)

**Goal:** Optimize performance and finalize documentation

#### Tasks

1. **Performance optimization** (Day 1-2)
   - [ ] Measure test suite execution time
   - [ ] Identify slow fixtures
   - [ ] Optimize fixture setup/teardown
   - [ ] Enable pytest-xdist for parallel execution
   - [ ] Benchmark: target <30s for full suite

2. **Verification script** (Day 3)
   - [ ] Create `scripts/verify_bdd_coverage.py`
   - [ ] Compare old test assertions vs. new step coverage
   - [ ] Generate mapping report: `docs/TEST_MIGRATION_MAP.md`
   - [ ] Document coverage gaps (if any)

3. **Documentation finalization** (Day 4)
   - [ ] Complete `TESTING_GUIDE.md` with all patterns
   - [ ] Add domain-specific examples
   - [ ] Document fixture library
   - [ ] Create contributor guide for BDD tests
   - [ ] Add troubleshooting section

4. **CI/CD updates** (Day 5)
   - [ ] Update GitHub Actions workflow
   - [ ] Add BDD test reporting
   - [ ] Configure pytest markers
   - [ ] Test CI/CD pipeline

#### Deliverables

- ✅ Performance benchmarks documented
- ✅ Coverage verification script
- ✅ Complete `TESTING_GUIDE.md`
- ✅ Updated CI/CD configuration
- ✅ Test suite execution <30s

---

## Migration Workflow (Per Domain)

### Step-by-Step Process

#### 1. Analysis Phase

```bash
# Review domain tests
ls tests/domain/<domain>/test_*.py

# Count tests
pytest tests/domain/<domain>/ --collect-only

# Analyze test structure
cat tests/domain/<domain>/test_*.py | grep "def test_\|def regression_"
```

**Checklist:**
- [ ] List all test functions
- [ ] Identify test patterns (setup, assertions, mocks)
- [ ] Map to business scenarios
- [ ] Group related scenarios

#### 2. Design Phase

Create feature file with business-readable scenarios:

```gherkin
# tests/features/<domain>/<feature>.feature
Feature: <Business Feature Name>

  Background:
    Given common setup for all scenarios

  Scenario: <Business Scenario 1>
    Given <precondition>
    When <action>
    Then <expected outcome>

  Scenario Outline: <Parameterized Scenario>
    Given <precondition with <parameter>>
    When <action>
    Then <expected outcome>

    Examples:
      | parameter | expected |
      | value1    | result1  |
      | value2    | result2  |
```

**Checklist:**
- [ ] Business-readable scenario names
- [ ] Clear Given/When/Then structure
- [ ] Use Background for common setup
- [ ] Use Scenario Outline for data-driven tests
- [ ] Avoid technical implementation details

#### 3. Implementation Phase

Create step definitions:

```python
# tests/bdd/steps/<domain>_steps.py
"""Step definitions for <domain> domain."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios

# Load scenarios
scenarios('../features/<domain>/<feature>.feature')

@pytest.fixture
def <domain>_context():
    """Shared context for <domain> scenarios."""
    return {}

@given(parsers.parse('<step pattern>'))
def step_given(context):
    """Setup precondition."""
    pass

@when(parsers.parse('<step pattern>'))
def step_when(context):
    """Execute action."""
    pass

@then(parsers.parse('<step pattern>'))
def step_then(context):
    """Verify outcome."""
    pass
```

**Checklist:**
- [ ] Import necessary modules
- [ ] Load feature file with `scenarios()`
- [ ] Create context fixture
- [ ] Implement all Given steps
- [ ] Implement all When steps
- [ ] Implement all Then steps
- [ ] Extract common steps to `common/`
- [ ] Add docstrings

#### 4. Verification Phase

```bash
# Run new BDD tests
pytest tests/features/<domain>/ -v

# Check coverage
pytest tests/features/<domain>/ --cov=src/<domain_module> --cov-report=term

# Compare with legacy coverage
pytest tests/domain/<domain>/ --cov=src/<domain_module> --cov-report=term

# Performance benchmark
pytest tests/features/<domain>/ --durations=10
```

**Checklist:**
- [ ] All scenarios pass
- [ ] Coverage ≥ legacy coverage
- [ ] No failing assertions
- [ ] Performance acceptable

#### 5. Cleanup Phase

```bash
# Delete legacy tests
rm -rf tests/domain/<domain>/

# Update documentation
vim docs/TEST_MIGRATION_MAP.md

# Commit changes
git add tests/features/<domain>/ tests/bdd/steps/<domain>_steps.py
git commit -m "refactor: migrate <domain> tests to BDD"
```

**Checklist:**
- [ ] Legacy tests deleted
- [ ] Migration documented
- [ ] Changes committed
- [ ] Code review requested

---

## Handling Overlapping and Bug/Fix Tests

### Critical Decision: Where Do Bug Tests Go?

**Rule:** Bug/fix tests are **integrated into domain feature files**, NOT separated into bug-specific files.

#### ❌ Don't Separate Bugs

```
tests/features/
├── routing/
│   ├── bugs.feature          # ❌ AVOID
│   ├── ortools_bugs.feature  # ❌ AVOID
```

#### ✅ Integrate Into Domain Features

```
tests/features/
├── routing/
│   ├── ortools_optimization.feature  # Contains bug scenarios
│   ├── fuel_aware_routing.feature    # Contains bug scenarios
```

**Rationale:**
- **Business-readable**: Scenarios describe expected behavior, not historical bugs
- **Single source of truth**: Related behaviors stay together
- **Natural grouping**: Prevents duplication
- **Better names**: "Prevent X" is clearer than "Bug fix for X"

---

### Identifying Test Overlaps

During Phase 1 analysis, you'll discover many tests overlap. Here's how to handle each type:

#### Type A: Exact Duplicates

**Characteristic:** Same behavior, same assertions, different names

**Example:**
```python
# test_circuit_breaker_buy_spike.py
def test_buy_price_spike():
    assert detect_price_spike(1000, 1360) == True

# test_circuit_breaker_profitability.py
def test_profitability_with_spike():
    assert detect_price_spike(1000, 1360) == True  # Duplicate!
```

**Action:** **MERGE** into one scenario, delete duplicate

```gherkin
Scenario: Detect buy price spike above threshold
  Given expected buy price is 1000 credits
  When actual market price is 1360 credits
  Then price spike should be detected
  And circuit breaker should trigger
```

---

#### Type B: Overlapping Coverage (Subset)

**Characteristic:** One test is complete subset of another

**Example:**
```python
# test_smart_navigator_basic.py
def test_navigation_with_refuel():
    # Tests: route validation, refuel insertion, execution
    pass

# test_intermediate_refuel_bug.py
def test_refuel_insertion():
    # Tests ONLY: refuel insertion (subset)
    pass
```

**Action:** **KEEP comprehensive scenario**, add edge case ONLY if bug revealed new behavior

```gherkin
Feature: Smart Navigation

  Scenario: Navigate with automatic refuel stops
    Given ship at X1-HU87-A1 with 50 fuel
    And destination X1-HU87-Z9 requires 200 fuel
    When I execute navigation
    Then route should insert refuel stop at nearest fuel station
    And ship should refuel before continuing
    And ship should arrive at destination

  # Only add if bug was specific edge case not covered above
  @regression @gh-issue-89
  Scenario: Insert intermediate refuel when fuel station is between waypoints
    Given ship at X1-HU87-A1 with 100 fuel
    And fuel station X1-HU87-M5 is 80 units away
    And destination X1-HU87-Z9 is 150 units away
    When I execute navigation
    Then route should refuel at X1-HU87-M5 (not bypass it)
```

---

#### Type C: Different Edge Cases (Related)

**Characteristic:** Same feature, different failure conditions

**Example:**
```python
# test_circuit_breaker_buy_spike.py
def test_buy_spike_segment_1():
    # Buy price spike at segment 1
    pass

# test_circuit_breaker_stale_sell_price.py
def test_sell_price_stale_segment_3():
    # Stale sell price at segment 3
    pass

# test_circuit_breaker_cargo_overflow.py
def test_cargo_overflow_segment_2():
    # Cargo overflow at segment 2
    pass
```

**Action:** **USE SCENARIO OUTLINE** to consolidate similar cases

```gherkin
Feature: Circuit Breaker Price Validation

  Scenario Outline: Detect price validation failures at any segment
    Given a <segment_count>-segment trade route
    When <failure_type> occurs at segment <segment_index>
    Then circuit breaker should trigger
    And segment should be <action>

    Examples:
      | segment_count | failure_type        | segment_index | action  |
      | 3             | buy price spike     | 1             | skipped |
      | 5             | stale sell price    | 3             | skipped |
      | 4             | cargo overflow      | 2             | aborted |
      | 6             | market unavailable  | 4             | skipped |
```

---

#### Type D: Multiple Bugs, Same Feature

**Characteristic:** Different bugs affecting same feature, distinct failure modes

**Example:**
```python
# test_ortools_duplicate_waypoint_bug.py
def test_duplicate_waypoint_handling():
    pass

# test_ortools_crossing_edges_bug.py
def test_crossing_edges_prevention():
    pass

# test_ortools_orbital_jitter_bug.py
def test_orbital_position_stability():
    pass
```

**Action:** **KEEP SEPARATE** scenarios with clear names

```gherkin
Feature: OR-Tools Route Optimization

  @regression @gh-issue-45
  Scenario: Handle duplicate waypoint visits correctly
    Given a route with waypoint X1-HU87-A1 visited twice
    When I optimize the route
    Then both visits should be preserved
    And route should not collapse duplicates

  @regression @gh-issue-78
  Scenario: Prevent crossing edges in route solutions
    Given waypoints arranged in crossing pattern
    When I solve VRP problem
    Then solution should not contain crossing edges

  @regression @gh-issue-112
  Scenario: Maintain stable orbital coordinates
    Given orbital waypoints with slight position jitter
    When I calculate distances
    Then coordinates should be normalized
    And distance calculations should be consistent
```

**Rationale:** Each represents **distinct failure mode** - clarity > consolidation

---

### Real Examples from Codebase

#### Example 1: Circuit Breaker Domain Overlaps

**Legacy Tests:**
```
tests/domain/circuit_breaker/
├── test_circuit_breaker_buy_spike_simple.py       # Simple spike
├── test_circuit_breaker_price_spike_profitability_bug.py  # Spike + profit
├── test_circuit_breaker_buy_only_segment.py       # Buy-only edge
├── test_circuit_breaker_smart_skip.py             # Full integration
```

**Analysis:**
- `buy_spike_simple.py` is **SUBSET** of `price_spike_profitability_bug.py`
- `smart_skip.py` is **COMPREHENSIVE** integration test
- `buy_only_segment.py` is **DISTINCT** edge case

**BDD Consolidation:**

```gherkin
# tests/features/trade/circuit_breaker.feature
Feature: Circuit Breaker Price Validation

  # Consolidates buy_spike_simple + profitability
  Scenario: Skip segment when price spike makes trade unprofitable
    Given a 3-segment trade route
    And segment 2 expects CLOTHING buy price of 1000 credits
    When actual market price spikes to 1360 credits (36% increase)
    Then segment 2 should be skipped
    And profitability should be recalculated
    And remaining segments should execute if profitable

  # Distinct edge case - keep separate
  Scenario: Handle buy-only segments during circuit breaker
    Given a segment with only BUY actions
    When circuit breaker triggers on buy price
    Then segment should be skipped
    And no sell validation should occur

  # Integration test - keep separate
  Scenario: Smart skip improves profit by continuing independent segments
    Given a 5-segment route with segments 4-5 independent of segment 3
    When segment 3 fails due to price spike
    Then circuit breaker should skip segment 3
    And segments 4-5 should execute successfully
    And final profit should be ~120k (vs 65k with abort-all)
```

---

#### Example 2: Routing Domain Overlaps

**Legacy Tests:**
```
tests/domain/routing/
├── test_ortools_real_coordinates.py               # Basic coordinates
├── test_ortools_orbital_jitter.py                 # Orbital stability
├── test_ortools_orbital_branching_bug.py          # Orbital graph
├── test_routing_critical_bugs_fix.py              # Multiple fixes
```

**Analysis:**
- `real_coordinates.py` and `orbital_jitter.py` **OVERLAP** (both test coordinates)
- `orbital_branching_bug.py` is **DISTINCT** graph structure issue
- `critical_bugs_fix.py` should be **SPLIT** into separate scenarios

**BDD Consolidation:**

```gherkin
# tests/features/routing/graph_operations.feature
Feature: Graph Construction and Coordinate Handling

  # Consolidates real_coordinates + orbital_jitter
  Scenario: Build graph with real coordinate distances
    Given waypoints with actual coordinate positions
    When I build the system graph
    Then edge distances should match Euclidean calculations
    And orbital positions should be normalized to prevent jitter

  # Distinct issue - keep separate
  @regression @gh-issue-89
  Scenario: Handle orbital branching in graph structure
    Given a planet with 3 orbital moons
    When I construct waypoint graph
    Then orbitals should branch from parent correctly
    And zero-distance edges should exist between orbitals
```

---

### Consolidation Strategy

#### Step 1: Create Overlap Matrix (During Analysis)

```markdown
## Circuit Breaker Test Mapping

| Legacy Test | Core Behavior | Overlaps With | Decision |
|-------------|---------------|---------------|----------|
| buy_spike_simple | Detect price spike | price_spike_profitability | MERGE |
| price_spike_profitability | Spike + profit check | buy_spike_simple | KEEP (comprehensive) |
| buy_only_segment | Buy-only edge case | - | KEEP (distinct) |
| smart_skip | Full integration | All above | KEEP (integration) |
| selective_salvage | Salvage logic | - | KEEP (distinct) |
```

#### Step 2: Apply Consolidation Rules

**MERGE if:**
- ✅ Exact duplicate assertions
- ✅ One test is complete subset of another
- ✅ Same behavior, different test data → Use Scenario Outline

**KEEP SEPARATE if:**
- ✅ Different failure modes
- ✅ Distinct edge cases
- ✅ Different acceptance criteria
- ✅ Regression for specific GitHub issue

#### Step 3: Document Decisions

In `docs/TEST_MIGRATION_MAP.md`:

```markdown
## Circuit Breaker Domain

### Merged Tests
- `test_buy_spike_simple.py` + `test_price_spike_profitability_bug.py`
  → **Scenario**: "Skip segment when price spike makes trade unprofitable"
  → **Reason**: Simple spike test was subset of profitability test
  → **Coverage Verified**: Lines 234-267 in multileg_trader.py

### Kept Separate
- `test_buy_only_segment.py`
  → **Scenario**: "Handle buy-only segments during circuit breaker"
  → **Reason**: Distinct edge case not covered by other tests
  → **Coverage**: Lines 312-328 in multileg_trader.py

- `test_smart_skip.py`
  → **Scenario**: "Smart skip improves profit by continuing independent segments"
  → **Reason**: Integration test covering full smart skip workflow
  → **Coverage**: Lines 234-412 in multileg_trader.py (full circuit breaker)
```

---

### Tagging Bug Fixes for Tracking

Use pytest markers to track regression tests:

```gherkin
  @regression @bug-fix @gh-issue-123
  Scenario: Exclude fuel stations from market scout tours
    Given a scout tour with 5 markets
    And 2 fuel-only stations in the system
    When I generate the scout tour
    Then fuel stations should be excluded
    And only markets with trade goods should be visited
```

```python
# tests/bdd/steps/scouting_steps.py
import pytest
from pytest_bdd import scenarios

scenarios('../features/scouting/tour_optimization.feature')
```

**Run regression tests only:**
```bash
pytest -m regression tests/features/
```

**Run specific GitHub issue:**
```bash
pytest -m "gh-issue-123" tests/features/
```

---

### Coverage Verification

#### Ensure No Coverage Loss

```bash
# Before migration
pytest tests/domain/circuit_breaker/ \
  --cov=src/operations/multileg_trader \
  --cov-report=term > legacy_coverage.txt

# After migration
pytest tests/features/trade/circuit_breaker.feature \
  --cov=src/operations/multileg_trader \
  --cov-report=term > bdd_coverage.txt

# Compare (should be ≥)
diff legacy_coverage.txt bdd_coverage.txt
```

#### Create Coverage Comparison Script

```python
# scripts/verify_coverage.py
#!/usr/bin/env python3
"""Verify BDD migration preserves test coverage."""

import subprocess
import sys
from pathlib import Path

def run_coverage(test_path, module):
    """Run coverage for given test path."""
    result = subprocess.run(
        [
            "pytest",
            str(test_path),
            f"--cov={module}",
            "--cov-report=json",
            "--quiet",
        ],
        capture_output=True,
        text=True,
    )

    import json
    with open("coverage.json") as f:
        data = json.load(f)

    return set(data["files"][module]["executed_lines"])

def compare_coverage(domain, legacy_path, bdd_path, module):
    """Compare coverage between legacy and BDD tests."""
    print(f"\n=== Verifying {domain} coverage ===")

    legacy_lines = run_coverage(legacy_path, module)
    bdd_lines = run_coverage(bdd_path, module)

    missing_lines = legacy_lines - bdd_lines
    new_lines = bdd_lines - legacy_lines

    if missing_lines:
        print(f"❌ Coverage loss: {len(missing_lines)} lines")
        print(f"   Missing: {sorted(missing_lines)}")
        return False

    print(f"✅ Coverage preserved: {len(bdd_lines)} lines")
    if new_lines:
        print(f"   Bonus: {len(new_lines)} new lines covered")

    return True

# Example usage
if __name__ == "__main__":
    domains = [
        ("circuit_breaker", "tests/domain/circuit_breaker",
         "tests/features/trade/circuit_breaker.feature",
         "src/operations/multileg_trader"),
    ]

    all_passed = True
    for domain, legacy, bdd, module in domains:
        passed = compare_coverage(domain, legacy, bdd, module)
        all_passed = all_passed and passed

    sys.exit(0 if all_passed else 1)
```

---

### Phase 1 Analysis Checklist (Enhanced)

During pilot domain analysis, add overlap detection:

- [ ] List all test functions
- [ ] Identify test patterns (setup, assertions, mocks)
- [ ] **Create overlap matrix**
- [ ] **Apply consolidation rules**
- [ ] **Document merge/keep decisions**
- [ ] Map to business scenarios
- [ ] Group related scenarios
- [ ] **Verify no coverage loss after consolidation**

---

## Test Migration Mapping

### Circuit Breaker Domain (Phase 1 Pilot)

| Legacy Test File | Legacy Function | New Feature File | New Scenario | Step File |
|-----------------|-----------------|------------------|--------------|-----------|
| `test_circuit_breaker_smart_skip.py` | `regression_dependency_detection_cargo_dependency()` | `trade/circuit_breaker.feature` | "Detect cargo dependency between segments" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_smart_skip.py` | `regression_dependency_detection_credit_dependency()` | `trade/circuit_breaker.feature` | "Detect credit dependency between segments" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_smart_skip.py` | `regression_should_skip_segment_with_independents_remaining()` | `trade/circuit_breaker.feature` | "Skip failed segment when independent segments remain" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_smart_skip.py` | `regression_should_not_skip_when_all_depend_on_failed()` | `trade/circuit_breaker.feature` | "Abort when all segments depend on failed segment" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_smart_skip.py` | `regression_cargo_blocks_future_segments()` | `trade/circuit_breaker.feature` | "Detect cargo blocking future segments" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_profitability.py` | `regression_circuit_breaker_profitability()` | `trade/circuit_breaker.feature` | "Validate profitability calculations" | `circuit_breaker_steps.py` |
| `test_circuit_breaker_buy_spike_simple.py` | `regression_buy_price_spike_detection()` | `trade/circuit_breaker.feature` | "Detect buy price spike" | `circuit_breaker_steps.py` |

### Routing Domain (Phase 2)

| Legacy Test File | Legacy Function | New Feature File | New Scenario | Step File |
|-----------------|-----------------|------------------|--------------|-----------|
| `test_ortools_mining_steps.py` | *(already BDD)* | `routing/ortools_optimization.feature` | *(migrate as-is)* | `routing_steps.py` |
| `test_ortools_crossing_edges_bug.py` | `regression_ortools_crossing_edges()` | `routing/ortools_optimization.feature` | "Prevent crossing edges in VRP solutions" | `routing_steps.py` |
| `test_ortools_duplicate_waypoint_bug.py` | `regression_duplicate_waypoint()` | `routing/ortools_optimization.feature` | "Handle duplicate waypoint visits" | `routing_steps.py` |
| `test_ortools_router_fast_fuel_aware_routing.py` | `regression_fuel_aware_routing()` | `routing/fuel_aware_routing.feature` | "Calculate fuel-optimal routes" | `routing_steps.py` |

### Trade Domain (Phase 2)

| Legacy Test File | Legacy Function | New Feature File | New Scenario | Step File |
|-----------------|-----------------|------------------|--------------|-----------|
| `test_multileg_trader_action_placement.py` | `regression_action_placement()` | `trade/multileg_trading.feature` | "Place trade actions at correct waypoints" | `trade_steps.py` |
| `test_fleet_trade_optimizer.py` | `regression_fleet_optimizer()` | `trade/fleet_optimization.feature` | "Optimize fleet assignments" | `trade_steps.py` |
| `test_price_validation_circuit_breaker.py` | `regression_price_validation()` | `trade/price_validation.feature` | "Validate market prices before execution" | `trade_steps.py` |

---

## Performance Benchmarks

### Current State (With Bridge)

```
Test Suite Execution:
  Total tests: 117
  Execution time: ~50-60 seconds
  Subprocess overhead: ~19 seconds (200ms × 94 files)
  Per-test average: ~512ms
```

### Target State (Pure BDD)

```
Test Suite Execution:
  Total tests: 117 (same)
  Execution time: <30 seconds
  Subprocess overhead: 0 seconds
  Per-test average: <256ms
  Improvement: ~40% faster
```

### Phase Benchmarks

| Phase | Tests Migrated | Target Execution Time |
|-------|----------------|-----------------------|
| Phase 1 | 10 (circuit_breaker) | <500ms |
| Phase 2 | 42 (routing, trade, operations) | <8s |
| Phase 3 | 52 (remaining domains) | <12s |
| Phase 4 | 23 (unit tests) | <5s |
| **Total** | **117** | **<30s** |

---

## Success Criteria

### Functional Criteria

- [ ] ✅ All 94 domain tests expressed as Gherkin scenarios
- [ ] ✅ All 23 unit tests migrated or justified
- [ ] ✅ Zero subprocess execution
- [ ] ✅ Bridge mechanism deleted
- [ ] ✅ Feature files organized by domain
- [ ] ✅ Step library complete (common + domain-specific)
- [ ] ✅ Fixture library consolidated

### Non-Functional Criteria

- [ ] ✅ 100% coverage preservation
- [ ] ✅ Test suite execution <30s
- [ ] ✅ Business-readable feature files
- [ ] ✅ Clear step patterns documented
- [ ] ✅ Contributor-friendly documentation

### Quality Gates

- [ ] ✅ Test coverage ≥85%
- [ ] ✅ 100% test pass rate
- [ ] ✅ `TESTING_GUIDE.md` complete
- [ ] ✅ Code review approved
- [ ] ✅ Coverage verification script passing

---

## Risk Management

### High Risks

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| **Coverage loss during migration** | High | Medium | Pilot domain validates approach; verification script confirms 100% assertion migration |
| **Step definition duplication** | Medium | High | Establish common step library early (Phase 1); enforce DRY in code reviews |
| **Performance regression** | Medium | Low | Benchmark after each phase; optimize fixtures proactively |
| **Contributor confusion** | Medium | Medium | Clear documentation; pilot domain as reference; gradual rollout |

### Medium Risks

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| **Gherkin scenarios too technical** | Medium | Medium | Code review focus on readability; non-technical stakeholder review |
| **Fixture complexity** | Medium | Medium | Start simple; refactor incrementally; document patterns |
| **Test flakiness** | Medium | Low | Maintain strict fixture isolation; avoid shared state |

### Contingency Plans

**If Phase 1 fails:**
- Reassess approach with team
- Consider hybrid model (keep some legacy tests)
- Extend timeline

**If performance targets not met:**
- Enable pytest-xdist parallel execution
- Optimize slow fixtures
- Consider pytest-pytest-bdd performance plugins

**If coverage gaps identified:**
- Add missing step definitions
- Document uncovered edge cases
- Prioritize based on risk

---

## Documentation Deliverables

### Files to Create

1. **`TESTING_GUIDE.md`** (PRIMARY)
   ```markdown
   # Testing Guide

   ## BDD Testing with pytest-bdd

   ### Philosophy
   ### Step Definition Patterns
   ### Fixture Library
   ### Domain-Specific Examples
   ### Contribution Guidelines
   ### Running Tests
   ### Troubleshooting
   ```

2. **`docs/TEST_MIGRATION_MAP.md`**
   ```markdown
   # Test Migration Mapping

   ## Overview
   ## Domain-by-Domain Mapping
   ## Coverage Verification
   ## Phase Status
   ```

3. **`tests/bdd/steps/README.md`**
   ```markdown
   # Step Library Organization

   ## Directory Structure
   ## Common Steps
   ## Domain-Specific Steps
   ## Fixture Library
   ## Step Reuse Patterns
   ```

### Files to Update

1. **`CLAUDE.md`** (lines 329-339)
   - Update testing section with BDD patterns
   - Add pytest-bdd examples
   - Document step library organization

2. **`README.md`**
   - Add BDD testing section
   - Include example Gherkin scenarios
   - Link to `TESTING_GUIDE.md`

3. **`pytest.ini`**
   - Remove `norecursedirs` exclusion
   - Add BDD-specific configuration
   - Configure pytest markers

4. **`tests/conftest.py`**
   - Enhance with BDD fixtures
   - Add step loading configuration
   - Document fixture scope

---

## Timeline & Milestones

### Overview

```
Week 1: Phase 1 - Foundation (pilot domain)
Week 2-3: Phase 2 - Core domains
Week 4-5: Phase 3 - Remaining domains
Week 6: Phase 4 - Unit tests
Week 7: Phase 5 - Bridge removal & cleanup
Week 8: Phase 6 - Optimization & documentation
```

### Milestones

| Milestone | Target Date | Deliverables |
|-----------|-------------|--------------|
| **M1: Phase 1 Complete** | End of Week 1 | Pilot domain migrated, patterns established |
| **M2: Phase 2 Complete** | End of Week 3 | 42 core tests migrated |
| **M3: Phase 3 Complete** | End of Week 5 | All 94 domain tests migrated |
| **M4: Phase 4 Complete** | End of Week 6 | Unit tests migrated |
| **M5: Phase 5 Complete** | End of Week 7 | Bridge removed, cleanup done |
| **M6: Phase 6 Complete** | End of Week 8 | Optimized, documented, shipped |

### Progress Tracking

Create GitHub project board with columns:
- **Backlog** - All domains to migrate
- **In Progress** - Current domain being worked
- **Review** - Awaiting code review
- **Done** - Migrated and verified

Track per domain:
- [ ] Analysis complete
- [ ] Feature files written
- [ ] Step definitions implemented
- [ ] Tests passing
- [ ] Coverage verified
- [ ] Legacy tests deleted
- [ ] Documentation updated

---

## Team Roles & Responsibilities

### Recommended Team Structure

**Option A: Single Developer**
- Duration: 8 weeks full-time
- Phases executed sequentially
- Best for small team or solo developer

**Option B: Small Team (2-3 developers)**
- Duration: 4-5 weeks
- Parallel domain migration in Phase 2-3
- One developer focuses on documentation

**Option C: Full Team (4+ developers)**
- Duration: 3-4 weeks
- Maximum parallelization
- Dedicated roles (migration, review, documentation)

### Roles

**Migration Lead**
- Owns overall migration plan
- Establishes patterns in Phase 1
- Reviews all pull requests
- Resolves blockers

**Domain Migrators** (1-3 people)
- Execute domain migrations
- Write feature files and step definitions
- Verify coverage
- Delete legacy tests

**Documentation Lead**
- Creates `TESTING_GUIDE.md`
- Documents patterns and examples
- Updates `CLAUDE.md` and `README.md`
- Creates verification script

**Reviewer**
- Reviews all BDD pull requests
- Validates business readability of scenarios
- Ensures step reuse and DRY principles
- Approves migrations before legacy deletion

---

## Next Steps

### Immediate Actions

1. **Review and approve this plan**
   - [ ] Team review meeting
   - [ ] Adjust timeline if needed
   - [ ] Assign roles

2. **Set up tracking**
   - [ ] Create GitHub project board
   - [ ] Set up milestone tracking
   - [ ] Schedule weekly check-ins

3. **Prepare environment**
   - [ ] Create feature branch: `refactor/bdd-migration`
   - [ ] Set up CI/CD for feature branch
   - [ ] Document rollback plan

4. **Start Phase 1**
   - [ ] Assign Phase 1 lead
   - [ ] Schedule Phase 1 kickoff
   - [ ] Begin pilot domain migration

### Phase 1 Kickoff Checklist

- [ ] Team understands BDD principles
- [ ] pytest-bdd documentation reviewed
- [ ] Reference test reviewed (`test_ortools_mining_steps.py`)
- [ ] Step library structure agreed
- [ ] Feature file format agreed
- [ ] Code review process established

---

## Appendix

### Reference Links

**Internal:**
- Bridge mechanism: `tests/bdd/steps/test_domain_features.py:19-35`
- Reference BDD test: `tests/domain/routing/test_ortools_mining_steps.py`
- Mock infrastructure: `tests/mock_api.py`
- pytest config: `pytest.ini`, `tests/conftest.py`

**External:**
- pytest-bdd docs: https://pytest-bdd.readthedocs.io/
- Gherkin reference: https://cucumber.io/docs/gherkin/reference/
- BDD best practices: https://automationpanda.com/bdd/
- pytest fixtures: https://docs.pytest.org/en/stable/fixture.html

### Glossary

- **BDD**: Behavior-Driven Development
- **Gherkin**: Language for writing BDD scenarios
- **Step Definition**: Python function implementing a Gherkin step
- **Fixture**: pytest mechanism for test setup/teardown
- **Scenario**: Single test case in Gherkin
- **Feature**: Collection of related scenarios
- **Background**: Common setup for all scenarios in a feature
- **Scenario Outline**: Parameterized scenario with example table

### FAQ

**Q: Why not keep the bridge mechanism?**
A: Subprocess overhead (~19s), maintenance burden of two systems, prevents fixture sharing, not idiomatic BDD.

**Q: Can we migrate domains in parallel?**
A: Yes! Domains are independent. Multiple team members can work on different domains simultaneously.

**Q: What if we find coverage gaps during migration?**
A: Document them in `TEST_MIGRATION_MAP.md`, prioritize by risk, add missing scenarios before deleting legacy tests.

**Q: Should all unit tests become BDD scenarios?**
A: No. Pure unit tests (e.g., utility functions) can stay as pytest. Only behavioral tests benefit from Gherkin.

**Q: How do we handle flaky tests?**
A: Use strict fixture isolation, avoid shared state, mock time-dependent operations, document known issues.

**Q: What if Phase 1 takes longer than expected?**
A: Extend timeline proportionally. Better to get patterns right in Phase 1 than rush and create technical debt.

---

**Document Version:** 1.0
**Last Updated:** 2025-10-15
**Status:** Draft - Pending Approval

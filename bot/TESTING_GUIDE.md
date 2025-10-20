# Testing Guide

**SpaceTraders Bot - BDD Testing with pytest-bdd**

This guide documents our comprehensive Behavior-Driven Development (BDD) test suite using pytest-bdd and Gherkin scenarios.

## Table of Contents

1. [Philosophy](#philosophy)
2. [Test Structure](#test-structure)
3. [Running Tests](#running-tests)
4. [Writing BDD Tests](#writing-bdd-tests)
5. [Step Definition Patterns](#step-definition-patterns)
6. [Fixture Library](#fixture-library)
7. [Domain Examples](#domain-examples)
8. [Troubleshooting](#troubleshooting)

---

## Philosophy

### Why BDD?

**Every test can and should be BDD.** Our testing philosophy embraces Gherkin scenarios for ALL tests, from unit tests to complex integration tests:

1. **Readability:** Gherkin's Given/When/Then structure makes tests understandable by anyone
2. **Living Documentation:** Feature files serve as executable specifications
3. **Consistency:** Single testing framework across entire codebase
4. **Discoverability:** All tests discoverable through pytest-bdd
5. **Maintainability:** Unified step patterns reduce duplication

### 100% BDD Migration

As of Phase 5 (2025-10-19), **all 117 test files** have been migrated to BDD format:
- ✅ 94 domain tests → BDD scenarios in `tests/bdd/features/`
- ✅ 23 unit tests → BDD scenarios in `tests/bdd/features/unit/`
- ❌ Legacy subprocess bridge mechanism → **DELETED**
- ❌ Legacy `tests/domain/` directory → **DELETED**
- ❌ Legacy `tests/unit/` directory → **DELETED**

---

## Test Structure

### Directory Organization

```
tests/
├── bdd/
│   ├── features/                    # Gherkin feature files (business-readable)
│   │   ├── unit/                    # Unit-level tests
│   │   │   ├── cli.feature
│   │   │   ├── core.feature
│   │   │   └── operations.feature
│   │   ├── trading/                 # Trading domain
│   │   ├── routing/                 # Routing & optimization
│   │   ├── scouting/                # Market scouting
│   │   ├── navigation/              # Ship navigation
│   │   ├── contracts/               # Contract management
│   │   ├── refueling/               # Refueling logic
│   │   ├── touring/                 # Tour optimization
│   │   └── ...                      # Other domains
│   │
│   └── steps/                       # Step definitions
│       ├── fixtures/                # Shared fixtures
│       │   ├── mock_api.py
│       │   └── __init__.py
│       ├── unit/                    # Unit test steps
│       │   ├── test_cli_steps.py
│       │   ├── test_core_steps.py
│       │   └── test_operations_steps.py
│       ├── trading/                 # Trading steps
│       ├── routing/                 # Routing steps
│       ├── scouting/                # Scouting steps
│       └── ...                      # Other domain steps
│
├── conftest.py                      # pytest-bdd configuration
├── bdd_table_utils.py               # Table parsing utilities
└── mock_daemon.py                   # Mock daemon manager
```

### File Naming Conventions

**Feature Files:** `tests/bdd/features/<domain>/<feature_name>.feature`
- Use descriptive names: `circuit_breaker.feature`, `fuel_aware_routing.feature`
- Group related scenarios in single feature file

**Step Definitions:** `tests/bdd/steps/<domain>/test_<feature>_steps.py`
- Match feature file name: `test_circuit_breaker_steps.py`
- Use `test_` prefix for pytest discovery

---

## Running Tests

### Basic Commands

```bash
# Run all BDD tests
pytest tests/

# Run specific domain
pytest tests/bdd/features/trading/

# Run specific feature
pytest tests/bdd/features/routing/fuel_aware_routing.feature

# Run with verbose output
pytest tests/ -v

# Run with markers
pytest -m unit tests/              # Unit tests only
pytest -m domain tests/            # Domain tests only
pytest -m regression tests/        # Regression tests only
```

### Coverage

```bash
# Run with coverage
pytest tests/ --cov=src --cov-report=html

# Open coverage report
open htmlcov/index.html

# Coverage for specific module
pytest tests/bdd/features/trading/ --cov=src/spacetraders_bot/operations/multileg_trader
```

### Performance

```bash
# Show slowest tests
pytest tests/ --durations=10

# Measure total execution time
time pytest tests/
```

**Target:** <30 seconds for full suite (~170 scenarios)

---

## Writing BDD Tests

### Feature File Structure

```gherkin
# tests/bdd/features/trading/circuit_breaker.feature
Feature: Circuit Breaker Price Validation
  As a trading bot operator
  I want automatic price spike detection
  So that unprofitable segments are skipped

  Background:
    Given a mock API client
    And a mock database

  Scenario: Skip segment when price spike makes trade unprofitable
    Given a 3-segment multileg trade route
    And segment 2 expects CLOTHING buy price of 1000 credits
    When actual market price spikes to 1360 credits
    Then segment 2 should be skipped
    And profitability should be recalculated
    And remaining segments should execute if profitable

  Scenario Outline: Detect price validation failures at any segment
    Given a <segment_count>-segment trade route
    When <failure_type> occurs at segment <segment_index>
    Then circuit breaker should trigger
    And segment should be <action>

    Examples:
      | segment_count | failure_type      | segment_index | action  |
      | 3             | buy price spike   | 1             | skipped |
      | 5             | stale sell price  | 3             | skipped |
      | 4             | cargo overflow    | 2             | aborted |
```

### Best Practices

**✅ DO:**
- Use business-readable language (avoid technical jargon)
- Focus on behavior, not implementation
- Use Scenario Outline for data-driven tests
- Tag regression tests with `@regression`
- Add clear, concise scenario descriptions

**❌ DON'T:**
- Include implementation details in scenarios
- Test multiple unrelated behaviors in one scenario
- Use overly technical variable names
- Create scenarios longer than 10 steps

---

## Step Definition Patterns

### Basic Step Structure

```python
# tests/bdd/steps/trading/test_circuit_breaker_steps.py
"""Step definitions for circuit breaker scenarios."""

import pytest
from pytest_bdd import given, when, then, parsers, scenarios
from tests.bdd.steps.fixtures.mock_api import MockAPIClient

# Load all scenarios from feature file
scenarios('../../features/trading/circuit_breaker.feature')


@pytest.fixture
def circuit_breaker_context():
    """Shared context for circuit breaker scenarios."""
    return {
        'route': None,
        'ship': None,
        'api': None,
        'result': None,
    }


@given(parsers.parse('a {segment_count:d}-segment multileg trade route'))
def setup_route(circuit_breaker_context, segment_count):
    """Create multileg route with specified segments."""
    from spacetraders_bot.operations.multileg_trader import MultiLegRoute, RouteSegment

    segments = []
    for i in range(segment_count):
        segment = RouteSegment(
            waypoint=f"X1-TEST-{i}",
            action="BUY" if i % 2 == 0 else "SELL",
            good="CLOTHING",
            units=10,
            expected_price=1000,
        )
        segments.append(segment)

    circuit_breaker_context['route'] = MultiLegRoute(segments=segments)


@when(parsers.parse('actual market price spikes to {price:d} credits'))
def simulate_price_spike(circuit_breaker_context, price):
    """Simulate price spike in market data."""
    circuit_breaker_context['api'].set_market_price("CLOTHING", price)
    # Execute route with circuit breaker logic
    result = execute_route_with_circuit_breaker(circuit_breaker_context['route'])
    circuit_breaker_context['result'] = result


@then(parsers.parse('segment {index:d} should be skipped'))
def verify_segment_skipped(circuit_breaker_context, index):
    """Verify segment was skipped."""
    result = circuit_breaker_context['result']
    assert index in result.skipped_segments, \
        f"Segment {index} was not skipped"
```

### Parsing Patterns

```python
from pytest_bdd import parsers

# Integer parsing
@given(parsers.parse('ship has {fuel:d} fuel'))
def ship_fuel(context, fuel: int):
    context['fuel'] = fuel

# String parsing
@given(parsers.parse('ship "{ship_symbol}" is docked'))
def ship_docked(context, ship_symbol: str):
    context['ships'][ship_symbol]['status'] = 'DOCKED'

# Multiple parameters
@when(parsers.parse('ship navigates from "{origin}" to "{destination}"'))
def ship_navigates(context, origin: str, destination: str):
    context['navigator'].navigate(origin, destination)

# Float parsing
@then(parsers.parse('profit should be approximately {amount:f} credits'))
def verify_profit(context, amount: float):
    assert abs(context['profit'] - amount) < 100
```

### Context Management

```python
@pytest.fixture
def domain_context():
    """Shared context fixture."""
    return {
        'api': None,
        'ships': {},
        'navigator': None,
        'result': None,
        'errors': [],
    }


# Access in steps
@given('a mock API client')
def setup_api(domain_context):
    domain_context['api'] = MockAPIClient()


@when('I execute operation')
def execute_operation(domain_context):
    result = some_operation(domain_context['api'])
    domain_context['result'] = result
```

---

## Fixture Library

### Mock API Client

```python
# tests/bdd/steps/fixtures/mock_api.py
from tests.bdd.steps.fixtures.mock_api import MockAPIClient

@pytest.fixture
def mock_api():
    """Mock SpaceTraders API client."""
    return MockAPIClient()


# Usage in steps
@given('a mock API client')
def setup_mock_api(context, mock_api):
    context['api'] = mock_api
    # Configure mock responses
    mock_api.set_ship_status("SHIP-1", {
        'nav': {'status': 'DOCKED'},
        'fuel': {'current': 400, 'capacity': 400},
    })
```

### Mock Database

```python
@pytest.fixture
def mock_db():
    """In-memory SQLite database for testing."""
    import sqlite3
    conn = sqlite3.connect(':memory:')
    # Initialize schema
    yield conn
    conn.close()
```

### Test Utilities

```python
# tests/bdd_table_utils.py - Gherkin table parsing
from tests.bdd_table_utils import table_to_rows

@given(parsers.parse('the following ships:\\n{table}'))
def setup_ships(context, table):
    rows = table_to_rows(table)
    for row in rows:
        ship_symbol, fuel, cargo = row
        context['ships'][ship_symbol] = {
            'fuel': int(fuel),
            'cargo': int(cargo),
        }
```

---

## Domain Examples

### Unit Tests (CLI)

```gherkin
# tests/bdd/features/unit/cli.feature
Feature: CLI Command Routing
  Tests for CLI argument parsing and command dispatch

  Scenario: Route graph-build command to operation handler
    Given the CLI is ready to process commands
    When I run "graph-build --player-id 7 --system X1-TEST"
    Then the graph_build_operation should be called
    And the operation should receive system "X1-TEST"
    And the command should succeed with exit code 0
```

### Unit Tests (Core Infrastructure)

```gherkin
# tests/bdd/features/unit/core.feature
Feature: Core Library Components
  Tests for API client, smart navigator, routing

  Scenario: APIResult helper creates success result
    Given an APIResult with success data
    When I create a success result with data and status 200
    Then the result should be ok
    And the result data should match the input
    And the status code should be 200
```

### Domain Tests (Trading)

```gherkin
# tests/bdd/features/trading/circuit_breaker.feature
Feature: Circuit Breaker Price Validation

  Scenario: Skip failed segment when independent segments remain profitable
    Given a 5-segment multileg trade route
    And segments 4 and 5 are independent of segment 3
    When segment 3 fails due to price spike
    And remaining profit exceeds 5000 credits
    Then the circuit breaker should skip segment 3
    And segments 4 and 5 should execute successfully
```

### Domain Tests (Routing)

```gherkin
# tests/bdd/features/routing/fuel_aware_routing.feature
Feature: Fuel-Aware Route Planning

  Scenario: Insert refuel stop when fuel insufficient
    Given a ship at X1-HU87-A1 with 50 fuel
    And destination X1-HU87-Z9 requires 200 fuel
    And fuel station X1-HU87-M5 is 80 units away
    When I plan route to destination
    Then route should insert refuel stop at X1-HU87-M5
    And ship should refuel before continuing to destination
```

---

## Troubleshooting

### Common Issues

**Issue: `ModuleNotFoundError: No module named 'spacetraders_bot'`**
```bash
# Solution: Ensure src directory is in PYTHONPATH
export PYTHONPATH="${PYTHONPATH}:$(pwd)/src"
# Or run from project root
cd /path/to/bot && pytest tests/
```

**Issue: `fixture 'context' not found`**
```python
# Solution: Create context fixture in conftest.py or step file
@pytest.fixture
def domain_context():
    return {}
```

**Issue: `StepDefinitionNotFoundError`**
```bash
# Solution: Ensure step definition exists and scenarios() is called
# In test_feature_steps.py:
scenarios('../../features/domain/feature.feature')
```

**Issue: Tests pass individually but fail when run together**
```python
# Solution: Ensure proper fixture isolation
@pytest.fixture
def isolated_context():
    """Create fresh context for each test."""
    return {}  # Don't reuse objects between tests
```

### Debugging Tips

```python
# Add debugging output in steps
@when('I execute operation')
def execute_op(context):
    print(f"DEBUG: API state = {context['api']}")
    result = operation()
    print(f"DEBUG: Result = {result}")
    context['result'] = result

# Use pytest debugging flags
pytest tests/ -v --tb=short  # Short traceback
pytest tests/ -v --tb=long   # Detailed traceback
pytest tests/ -v -s          # Show print statements
pytest tests/ -v --pdb       # Drop into debugger on failure
```

---

## Contributing

### Adding New Tests

1. **Create feature file** in appropriate domain directory
2. **Write Gherkin scenarios** using business-readable language
3. **Create step definitions** in matching step file
4. **Import scenarios** using `scenarios('../../features/...')`
5. **Run tests** to verify: `pytest tests/bdd/features/your_domain/`
6. **Add to documentation** if introducing new patterns

### Code Review Checklist

- [ ] Scenarios are business-readable (no technical jargon)
- [ ] Step definitions follow established patterns
- [ ] Context fixtures properly isolated
- [ ] All scenarios pass: `pytest tests/ -v`
- [ ] No coverage loss: `pytest --cov=src`
- [ ] Documentation updated if needed

---

## Additional Resources

**Internal Documentation:**
- `BDD_MIGRATION_PLAN.md` - Complete migration strategy
- `PHASE_4_COMPLETE_ALL_MIGRATED.md` - Phase 4 completion details
- `CLAUDE.md` - Codebase guide (testing section)

**External Resources:**
- [pytest-bdd Documentation](https://pytest-bdd.readthedocs.io/)
- [Gherkin Reference](https://cucumber.io/docs/gherkin/reference/)
- [BDD Best Practices](https://automationpanda.com/bdd/)

---

**Last Updated:** 2025-10-19 (Phase 5: Bridge Removal & Cleanup)
**Test Count:** 170+ scenarios across all domains
**Migration Status:** ✅ 100% Complete

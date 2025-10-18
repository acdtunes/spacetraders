# pytest-bdd Best Practices: Complete Migration and Implementation Guide

This guide provides comprehensive best practices for converting Python test suites to BDD using pytest-bdd and Gherkin, based on official documentation and authoritative sources.

## Table of Contents

1. [pytest-bdd Organization Best Practices](#1-pytest-bdd-organization-best-practices)
2. [Gherkin Scenario Design Patterns](#2-gherkin-scenario-design-patterns)
3. [Domain-Driven Step Library Organization](#3-domain-driven-step-library-organization)
4. [Fixture and Mock Encapsulation](#4-fixture-and-mock-encapsulation)
5. [Performance Optimization](#5-performance-optimization)
6. [Migration Strategies](#6-migration-strategies)
7. [Documentation and Maintainability](#7-documentation-and-maintainability)
8. [Example Tables and Scenario Outlines](#8-example-tables-and-scenario-outlines)

---

## 1. pytest-bdd Organization Best Practices

### 1.1 Project Structure

**Recommended directory layout** (from [official pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/)):

```
project/
├── features/                    # Feature files organized by domain
│   ├── frontend/
│   │   └── auth/
│   │       └── login.feature
│   ├── backend/
│   │   └── auth/
│   │       └── login.feature
│   ├── navigation/
│   │   ├── routing.feature
│   │   └── refueling.feature
│   └── operations/
│       ├── mining.feature
│       └── trading.feature
│
├── tests/                       # Test implementations
│   ├── conftest.py             # Shared fixtures, hooks, common steps
│   ├── domain/                 # Domain-specific test modules
│   │   ├── __init__.py
│   │   ├── test_navigation.py
│   │   └── test_operations.py
│   └── bdd/
│       └── steps/              # Step definition modules
│           ├── __init__.py
│           ├── common/         # Shared across domains
│           │   ├── given.py
│           │   ├── when.py
│           │   └── then.py
│           ├── navigation/     # Domain-specific
│           │   ├── given.py
│           │   ├── when.py
│           │   └── then.py
│           └── operations/
│               └── ...
│
└── pytest.ini                   # pytest configuration
```

### 1.2 File Organization Principles

**Official recommendation** ([pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/)):

> "As a best practice, put commonly shared steps in `conftest.py` and feature-specific steps in the test module."

**Key principles:**

1. **Semantic grouping**: Organize features by business domain, not by technical layer
2. **Step independence**: Step definition module names don't need to match feature file names
3. **Shared steps location**: `conftest.py` for cross-domain, domain-specific folders for domain logic
4. **Discovery**: Any step definition can be used by any feature file within the same project

### 1.3 Feature File Naming and Organization

**Best practices** (from [Cucumber docs](https://cucumber.io/docs/bdd/better-gherkin/)):

- Use `.feature` extension for all feature files
- One feature per file
- Name files after the feature they describe (e.g., `smart_navigation.feature`)
- Organize in semantic folders representing business domains
- Use tags for cross-cutting concerns (e.g., `@smoke`, `@integration`, `@slow`)

**Example:**
```gherkin
# features/navigation/smart_navigation.feature
@navigation @fuel-aware
Feature: Smart Navigation
  As a ship operator
  I want the system to plan fuel-efficient routes
  So that I can minimize operational costs

  Background:
    Given a valid API client is configured
    And the system has X1-HU87 graph data loaded
```

### 1.4 Test Selection with Tags

Use pytest markers derived from Gherkin tags for selective test execution:

```bash
# Run all backend auth tests that are successful scenarios
pytest -m 'backend and auth and successful'

# Run smoke tests only
pytest -m 'smoke'

# Exclude slow integration tests
pytest -m 'not slow'
```

---

## 2. Gherkin Scenario Design Patterns

### 2.1 The Golden Rules

**From [Automation Panda](https://automationpanda.com/2017/01/30/bdd-101-writing-good-gherkin/):**

1. **The Golden Rule**: "Write Gherkin so that people who don't know the feature will understand it"
2. **The Cardinal Rule**: "One scenario should cover exactly one behavior"
3. **The One-to-One Rule**: One When-Then pair = one behavior

### 2.2 Declarative vs. Imperative Style

**ALWAYS prefer declarative** (from [Cucumber docs](https://cucumber.io/docs/bdd/better-gherkin/)):

**BAD (Imperative - describes HOW):**
```gherkin
When I type "freeFrieda@example.com" in the email field
And I type "MySecretPassword123" in the password field
And I click the "Sign In" button
And I wait for the page to load
Then I should see my dashboard
```

**GOOD (Declarative - describes WHAT):**
```gherkin
When Frieda logs in with valid credentials
Then she should see her dashboard
```

**Why declarative is better:**
- Survives UI changes (voice interface, biometrics, etc.)
- Shorter and easier to follow
- Focuses on business value, not keystrokes
- Reads as living documentation
- Less brittle and maintenance-heavy

### 2.3 Scenario Structure Best Practices

**From [BrowserStack Guide](https://www.browserstack.com/guide/gherkin-and-its-role-bdd-scenarios):**

```gherkin
Feature: Feature name that describes the capability
  As a [role]
  I want [feature]
  So that [benefit]

  Scenario: Descriptive scenario name
    Given [precondition/context]      # State setup
    And [additional context]
    When [action/trigger]             # The behavior under test
    Then [expected outcome]           # Verification
    And [additional verification]
```

**Key principles:**

1. **Use third-person perspective consistently**
2. **Write steps as subject-predicate phrases**
3. **Use present tense for all step types**
4. **Keep scenarios under 10 steps** (ideally 3-5)
5. **Limit "And" statements to 2-3 per Given/When/Then**
6. **Each scenario = single, testable behavior**

### 2.4 Scenario Independence

**Critical requirement** ([Gherkin Best Practices](https://github.com/andredesousa/gherkin-best-practices)):

> "Each scenario should be independent and isolated from another scenario, enabling any scenario to run at any given time and produce the same results."

**Example:**

```gherkin
# BAD: Depends on previous scenario
Scenario: User views their shopping cart
  When the user navigates to the cart page
  Then the cart should contain the items from the previous purchase

# GOOD: Self-contained
Scenario: User views shopping cart with items
  Given the user has 3 items in their cart
  When the user navigates to the cart page
  Then the cart should display 3 items
```

### 2.5 Anti-Patterns to Avoid

**From [Automation Panda](https://automationpanda.com/2017/01/30/bdd-101-writing-good-gherkin/):**

1. **Multiple When-Then pairs** - Indicates testing multiple behaviors
2. **Vague scenarios** - Lacking concrete values or assertions
3. **UI-heavy testing** - Slow, brittle, doesn't express business intent
4. **Conjunctive steps** - Combining multiple actions in one step
5. **Procedure-driven thinking** - Just translating test procedures to Gherkin
6. **Style violations** - Inconsistent capitalization, mixing perspectives

---

## 3. Domain-Driven Step Library Organization

### 3.1 Recommended Structure

**Based on [pytest-bdd community patterns](https://automationpanda.com/2018/10/22/python-testing-101-pytest-bdd/):**

```
tests/
├── conftest.py                 # Shared fixtures + common steps
├── bdd/
│   └── steps/
│       ├── __init__.py
│       ├── common/             # Cross-domain shared steps
│       │   ├── __init__.py
│       │   ├── given.py        # Common setup steps
│       │   ├── when.py         # Common action steps
│       │   └── then.py         # Common assertion steps
│       │
│       ├── navigation/         # Navigation domain
│       │   ├── __init__.py
│       │   ├── given.py
│       │   ├── when.py
│       │   └── then.py
│       │
│       ├── mining/             # Mining domain
│       │   ├── __init__.py
│       │   └── steps.py        # Alternative: all in one file
│       │
│       └── trading/
│           └── ...
```

### 3.2 Organizing Steps by Type vs. Domain

**Two main approaches:**

**Approach 1: By Step Type (Given/When/Then)**
```python
# steps/navigation/given.py
from pytest_bdd import given, parsers

@given(parsers.parse('the ship "{ship}" is at waypoint "{waypoint}"'))
def ship_at_waypoint(ship, waypoint, mock_api):
    mock_api.set_ship_location(ship, waypoint)

# steps/navigation/when.py
from pytest_bdd import when, parsers

@when(parsers.parse('the ship navigates to "{destination}"'))
def navigate_ship(ship, destination, ship_controller):
    ship_controller.navigate(destination)

# steps/navigation/then.py
from pytest_bdd import then, parsers

@then(parsers.parse('the ship should arrive at "{waypoint}"'))
def verify_arrival(ship, waypoint, ship_controller):
    assert ship_controller.get_status()['nav']['waypointSymbol'] == waypoint
```

**Approach 2: All Domain Steps Together**
```python
# steps/navigation/steps.py
from pytest_bdd import given, when, then, parsers

@given(parsers.parse('the ship "{ship}" is at waypoint "{waypoint}"'))
def ship_at_waypoint(ship, waypoint, mock_api):
    mock_api.set_ship_location(ship, waypoint)

@when(parsers.parse('the ship navigates to "{destination}"'))
def navigate_ship(ship, destination, ship_controller):
    ship_controller.navigate(destination)

@then(parsers.parse('the ship should arrive at "{waypoint}"'))
def verify_arrival(ship, waypoint, ship_controller):
    assert ship_controller.get_status()['nav']['waypointSymbol'] == waypoint
```

**Recommendation:** Use Approach 2 (all together) for smaller domains, Approach 1 (separated) for large domains with many steps.

### 3.3 Importing Steps in Test Modules

**Method 1: Direct import in test file**
```python
# tests/domain/test_navigation.py
from pytest_bdd import scenarios
from tests.bdd.steps.navigation import steps
from tests.bdd.steps.common import given, when, then

scenarios('../../features/navigation/')
```

**Method 2: Use conftest.py with pytest_plugins**
```python
# tests/conftest.py
pytest_plugins = [
    'tests.bdd.steps.common.given',
    'tests.bdd.steps.common.when',
    'tests.bdd.steps.common.then',
]

# tests/domain/conftest.py
pytest_plugins = [
    'tests.bdd.steps.navigation.steps',
    'tests.bdd.steps.mining.steps',
]
```

**Note:** Imported modules must be packages with `__init__.py` files.

### 3.4 Step Reusability Patterns

**Official recommendation** ([pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/)):

> "Keep step definitions modular and reusable. If multiple scenarios have similar steps, you can reuse the same function in different tests to prevent duplication and ensure consistency."

**Example of reusable step:**

```python
# steps/common/given.py
from pytest_bdd import given

@given('a valid API client is configured', target_fixture='api_client')
def api_client(mock_api_token):
    """Shared across ALL domains"""
    from lib.api_client import APIClient
    return APIClient(token=mock_api_token)

# Used in navigation, mining, trading, contracts, etc.
```

---

## 4. Fixture and Mock Encapsulation

### 4.1 Fixture Organization in conftest.py

**From [Test Automation University](https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/chapter9.html):**

```python
# tests/conftest.py
import pytest
from pytest_bdd import given

# Shared fixtures available to all tests
@pytest.fixture
def mock_api():
    """Mock API client for all BDD tests"""
    from tests.mock_api import MockSpaceTradersAPI
    api = MockSpaceTradersAPI()
    yield api
    api.cleanup()

@pytest.fixture
def ship_controller(mock_api):
    """Fixture with dependency injection"""
    from lib.ship_controller import ShipController
    return ShipController(mock_api, "TEST-SHIP-1")

# Shared step that provides a fixture
@given('a valid API client is configured', target_fixture='api_client')
def api_client(mock_api):
    return mock_api
```

### 4.2 Mock Encapsulation Best Practices

**From [pytest mocking best practices](https://pytest-with-eric.com/mocking/pytest-common-mocking-problems/):**

**Principle 1: Test through interfaces, not implementation**

```python
# BAD: Testing internal implementation
def test_extract_calls_internal_method(mocker):
    mock_method = mocker.patch('ship_controller._calculate_yield')
    # Tests implementation, not behavior

# GOOD: Testing public interface behavior
def test_extract_adds_cargo_to_ship(ship_controller, mock_api):
    initial_cargo = ship_controller.get_cargo_units()
    ship_controller.extract()
    assert ship_controller.get_cargo_units() > initial_cargo
```

**Principle 2: Centralize mock setup in fixtures**

```python
# conftest.py
@pytest.fixture
def mock_navigation_response():
    """Centralized mock for navigation responses"""
    return {
        'data': {
            'nav': {
                'waypointSymbol': 'X1-HU87-B9',
                'status': 'IN_TRANSIT',
                'route': {
                    'arrival': '2025-01-15T12:30:00Z'
                }
            },
            'fuel': {
                'current': 850,
                'capacity': 1000
            }
        }
    }

@pytest.fixture
def mock_api(mock_navigation_response, mocker):
    """Group related mocks together"""
    api = MockSpaceTradersAPI()
    api.set_navigation_response(mock_navigation_response)
    return api
```

**Principle 3: Use fixture scope appropriately**

```python
# Session scope for expensive setup
@pytest.fixture(scope='session')
def system_graph_data():
    """Load once for all tests"""
    return load_graph('X1-HU87')

# Function scope (default) for test isolation
@pytest.fixture
def ship_controller(mock_api):
    """New instance per test"""
    return ShipController(mock_api, "TEST-SHIP")
```

### 4.3 Fixture Dependency Injection in Steps

**From [pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/):**

> "In pytest-bdd you just declare an argument of the step function that it depends on and pytest will make sure to provide it."

```python
from pytest_bdd import given, when, then, parsers

@given(parsers.parse('the ship "{ship}" has {fuel:d} units of fuel'))
def ship_with_fuel(ship, fuel, mock_api):
    """Uses mock_api fixture via dependency injection"""
    mock_api.set_ship_fuel(ship, fuel)

@when('the ship attempts to navigate')
def navigate_ship(ship_controller):
    """Uses ship_controller fixture"""
    ship_controller.navigate("X1-HU87-B9")

@then('the navigation should succeed')
def verify_navigation(ship_controller):
    """Fixture persists across steps in same scenario"""
    status = ship_controller.get_status()
    assert status['nav']['status'] in ['IN_TRANSIT', 'DOCKED']
```

### 4.4 Using target_fixture for Step-Generated Fixtures

**Pattern for steps that create test state:**

```python
@given('a mining ship at an asteroid', target_fixture='mining_ship')
def create_mining_ship(mock_api):
    """Step creates fixture for subsequent steps"""
    ship = ShipController(mock_api, "MINER-1")
    ship.navigate("X1-HU87-B9")  # Asteroid
    ship.orbit()
    return ship

@when('the ship extracts resources')
def extract_resources(mining_ship):
    """Uses fixture created by previous step"""
    mining_ship.extract()

@then('the cargo should contain ore')
def verify_ore_in_cargo(mining_ship):
    """Same fixture available throughout scenario"""
    cargo = mining_ship.get_cargo()
    assert any(item['symbol'].endswith('_ORE') for item in cargo)
```

---

## 5. Performance Optimization

### 5.1 Large-Scale BDD Test Suite Challenges

**From [BDD performance research](https://www.accelq.com/blog/bdd-in-testing/):**

Key bottlenecks in large BDD suites:
1. Sequential execution of scenarios
2. Expensive setup/teardown per scenario
3. Slow UI interactions (if applicable)
4. Large test data loading
5. Parser overhead

### 5.2 Parallel Execution

**pytest-xdist for parallel test execution:**

```bash
# Install pytest-xdist
pip install pytest-xdist

# Run tests in parallel (auto-detect CPUs)
pytest -n auto

# Run with specific worker count
pytest -n 4

# Distribute by file
pytest --dist loadfile

# Distribute by test
pytest --dist loadscope
```

**Example: Reducing 11 hours to <4 hours** (from production BDD suite):
- Sequential: ~11 hours
- With 4 workers: ~4 hours
- With 8 workers: ~2.5 hours

### 5.3 Fixture Scope Optimization

**Use appropriate scopes to minimize overhead:**

```python
# Session scope - once per test session
@pytest.fixture(scope='session')
def system_graphs():
    """Expensive: load all system graphs once"""
    return {
        'X1-HU87': load_graph('X1-HU87'),
        'X1-NF92': load_graph('X1-NF92'),
    }

# Module scope - once per test module
@pytest.fixture(scope='module')
def database_connection():
    """Database connection shared by module"""
    conn = create_connection()
    yield conn
    conn.close()

# Function scope - per test (default, safest for isolation)
@pytest.fixture
def ship_controller(mock_api):
    """New instance per test - ensures isolation"""
    return ShipController(mock_api, "TEST-SHIP")
```

### 5.4 Parser Performance Improvements

**From [pytest-bdd changelog](https://pytest-bdd.readthedocs.io/en/latest/):**

> "Version 7.0.0: 15% parser performance improvement"

The latest pytest-bdd (8.1.0) uses the official gherkin-official parser, providing:
- Better compatibility
- Improved parsing speed
- Lower memory footprint

**To benefit:**
```bash
pip install --upgrade pytest-bdd
```

### 5.5 Selective Test Execution

**Use tags to run only relevant tests:**

```bash
# Smoke tests only (fast feedback)
pytest -m smoke

# Skip slow integration tests during development
pytest -m 'not slow'

# Run unit-level BDD tests only
pytest -m unit

# Run specific domain
pytest -m navigation
```

**Tag your scenarios:**
```gherkin
@smoke @unit
Scenario: Calculate fuel requirement for short trip
  Given a ship with 100 fuel capacity
  When calculating fuel for a 50 unit distance
  Then the required fuel should be approximately 50 units

@integration @slow
Scenario: Complete mining operation with real API
  Given a real SpaceTraders API connection
  # ... full integration test
```

### 5.6 Test Data Management

**From [BDD scale optimization patterns](https://blogs.halodoc.io/optimising-the-jenkins-android-regression-suite-to-improve-performance-3/):**

```python
# BAD: Load large dataset per test
@pytest.fixture
def market_data():
    return load_all_market_data()  # Loads 10MB JSON every test

# GOOD: Load once, use efficiently
@pytest.fixture(scope='session')
def market_data_cache():
    """Load once for entire session"""
    return load_all_market_data()

@pytest.fixture
def market_data(market_data_cache):
    """Return copy for test isolation"""
    return market_data_cache.copy()

# BETTER: Load only what's needed
@pytest.fixture
def market_data(request):
    """Load on-demand based on test needs"""
    system = request.param if hasattr(request, 'param') else 'X1-HU87'
    return load_market_data_for_system(system)
```

### 5.7 The scenarios() Helper

**From [pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/):**

Use `scenarios()` to automatically bind all scenarios in a feature file:

```python
# Instead of this (manual binding):
@scenario('navigation/routing.feature', 'Plan simple route')
def test_plan_simple_route():
    pass

@scenario('navigation/routing.feature', 'Plan route with refuel stop')
def test_plan_route_with_refuel():
    pass

# Use this (automatic):
from pytest_bdd import scenarios

scenarios('navigation/routing.feature')  # Binds ALL scenarios
```

---

## 6. Migration Strategies

### 6.1 Incremental Migration Approach

**Recommended strategy** (from pytest community experience):

**Phase 1: Prepare Infrastructure**
1. Install pytest-bdd: `pip install pytest-bdd`
2. Create directory structure (features/, tests/bdd/steps/)
3. Set up conftest.py with existing fixtures
4. Configure pytest.ini

**Phase 2: Create BDD Infrastructure**
1. Start with shared fixtures in conftest.py
2. Create common step libraries
3. Set up mock/test data infrastructure

**Phase 3: Migrate by Domain**
1. Pick a bounded domain (e.g., navigation)
2. Write feature files for existing test cases
3. Create step definitions reusing existing test code
4. Run both old and new tests in parallel
5. Verify equivalent coverage
6. Deprecate old tests

**Phase 4: Expand Coverage**
1. Migrate next domain
2. Identify and extract common patterns
3. Refactor step libraries
4. Build domain-specific step modules

### 6.2 Migration Example: Traditional Test to BDD

**Before (traditional pytest):**

```python
# tests/test_navigation.py
def test_navigate_with_sufficient_fuel(mock_api):
    # Setup
    ship = ShipController(mock_api, "TEST-SHIP")
    mock_api.set_ship_location("TEST-SHIP", "X1-HU87-A1")
    mock_api.set_ship_fuel("TEST-SHIP", 500)

    # Execute
    result = ship.navigate("X1-HU87-B9")

    # Verify
    assert result is True
    status = ship.get_status()
    assert status['nav']['status'] == 'IN_TRANSIT'
    assert status['nav']['route']['destination']['symbol'] == 'X1-HU87-B9'
```

**After (pytest-bdd):**

```gherkin
# features/navigation/navigation.feature
Feature: Ship Navigation
  As a ship operator
  I want to navigate between waypoints
  So that I can position ships for operations

  Scenario: Navigate with sufficient fuel
    Given the ship "TEST-SHIP" is at waypoint "X1-HU87-A1"
    And the ship has 500 units of fuel
    When the ship navigates to "X1-HU87-B9"
    Then the ship should be in transit
    And the destination should be "X1-HU87-B9"
```

```python
# tests/bdd/steps/navigation/steps.py
from pytest_bdd import given, when, then, parsers

@given(parsers.parse('the ship "{ship}" is at waypoint "{waypoint}"'))
def ship_at_waypoint(ship, waypoint, mock_api):
    mock_api.set_ship_location(ship, waypoint)

@given(parsers.parse('the ship has {fuel:d} units of fuel'))
def ship_has_fuel(fuel, mock_api):
    mock_api.set_ship_fuel("TEST-SHIP", fuel)

@when(parsers.parse('the ship navigates to "{destination}"'))
def navigate_ship(destination, ship_controller):
    ship_controller.navigate(destination)

@then('the ship should be in transit')
def verify_in_transit(ship_controller):
    status = ship_controller.get_status()
    assert status['nav']['status'] == 'IN_TRANSIT'

@then(parsers.parse('the destination should be "{waypoint}"'))
def verify_destination(waypoint, ship_controller):
    status = ship_controller.get_status()
    assert status['nav']['route']['destination']['symbol'] == waypoint

# tests/domain/test_navigation.py
from pytest_bdd import scenarios

scenarios('../../features/navigation/navigation.feature')
```

### 6.3 Reusing Existing Test Infrastructure

**Key advantage of pytest-bdd** ([official docs](https://pytest-bdd.readthedocs.io/en/latest/)):

> "Pytest fixtures written for unit tests can be reused for setup and actions mentioned in feature steps with dependency injection."

**Example:**

```python
# Existing pytest fixtures (no changes needed!)
@pytest.fixture
def mock_api():
    return MockSpaceTradersAPI()

@pytest.fixture
def ship_controller(mock_api):
    return ShipController(mock_api, "TEST-SHIP")

# BDD steps can use them directly via dependency injection
@when('the ship extracts resources')
def extract_resources(ship_controller):  # ← Uses existing fixture
    ship_controller.extract()
```

### 6.4 Version-Specific Migration Guidance

**From [pytest-bdd documentation](https://pytest-bdd.readthedocs.io/en/latest/):**

Use the migration helper:
```bash
pytest-bdd migrate <your test folder>
```

**Key breaking changes:**

**From 2.x to 3.x:**
- Removed `pytestbdd_feature_base_dir` fixture
- Removed `pytestbdd_strict_gherkin` fixture

**From 3.x to 4.x:**
- Removed `strict_gherkin` parameter from `@scenario()`

**From 4.x to 5.x:**
- Example substitution now during parsing (major improvement!)
- No longer need double step definitions like:
  ```python
  # OLD (4.x): needed both
  @given("there are <start> cucumbers")
  @given(parsers.parse("there are {start} cucumbers"))

  # NEW (5.x+): just one
  @given(parsers.parse("there are {start} cucumbers"))
  ```
- Removed `example_converters` parameter - use `converters` instead

**From 7.x to 8.x (latest):**
- Now uses official gherkin-official parser
- Multiline steps MUST use triple-quotes
- All feature files MUST use `Feature:` keyword
- Vertical example tables NO LONGER SUPPORTED (use horizontal)

### 6.5 Code Generation Tool

**From [pytest-bdd docs](https://pytest-bdd.readthedocs.io/en/latest/):**

pytest-bdd can generate missing step definitions:

```bash
# Generate code for missing steps
pytest --generate-missing --feature features/ tests/

# Example output:
"""
@given('a ship at an asteroid')
def a_ship_at_an_asteroid():
    pass
"""
```

This helps with migration by creating step stubs for existing feature files.

---

## 7. Documentation and Maintainability

### 7.1 Feature File Documentation Standards

**From [Gherkin best practices](https://github.com/andredesousa/gherkin-best-practices):**

```gherkin
# GOOD: Complete, business-focused documentation
Feature: Smart Navigation with Fuel Management
  As a fleet manager
  I want ships to automatically plan fuel-efficient routes
  So that I can minimize operational costs and prevent fuel starvation

  The smart navigation system uses OR-Tools to plan routes that:
  - Minimize total flight time
  - Automatically insert refuel stops when needed
  - Prefer CRUISE mode when fuel is plentiful
  - Fall back to DRIFT mode when fuel is limited

  Background:
    Given a valid API client is configured
    And the system has X1-HU87 graph data loaded

  Scenario: Plan route with sufficient fuel for CRUISE mode
    Given the ship "TRADER-1" is at waypoint "X1-HU87-A1"
    And the ship has 800 units of fuel
    And the fuel capacity is 1000 units
    When the navigator plans a route to "X1-HU87-B9"
    Then the route should use CRUISE mode
    And no refuel stops should be required
```

**Key principles:**

1. **Feature description explains WHY and WHAT**
2. **User story format (As/I want/So that) provides context**
3. **Additional details clarify business rules**
4. **Scenarios are concise and focused**
5. **Background eliminates repeated setup steps**

### 7.2 Naming Conventions

**From [Cucumber documentation](https://cucumber.io/docs/bdd/better-gherkin/):**

> "Both scenario and feature names are used in the reports, making descriptive titles essential for clarity and debugging."

**Best practices:**

```gherkin
# BAD: Vague, technical
Scenario: Test 1
Scenario: Navigation works
Scenario: API call succeeds

# GOOD: Specific, business-focused
Scenario: Navigate with sufficient fuel for CRUISE mode
Scenario: Automatically insert refuel stop when fuel insufficient
Scenario: Fall back to DRIFT mode when fuel below 75% capacity
```

**Consistency rules:**
- Use consistent terminology across all scenarios
- Match domain language (ubiquitous language from DDD)
- Third-person perspective
- Present tense
- Subject-predicate structure

### 7.3 Step Definition Documentation

**Document complex step logic:**

```python
from pytest_bdd import given, parsers

@given(parsers.parse('the ship "{ship}" has {fuel:d} units of fuel'))
def ship_has_fuel(ship, fuel, mock_api):
    """
    Set the ship's current fuel level.

    This step configures the mock API to return the specified fuel level
    when querying the ship's status. The fuel level affects navigation
    decisions like flight mode selection and refuel stop insertion.

    Args:
        ship: Ship identifier (e.g., "TRADER-1")
        fuel: Current fuel units (integer)
        mock_api: Mock SpaceTraders API fixture
    """
    mock_api.set_ship_fuel(ship, fuel)
```

### 7.4 Maintaining Step Definition Libraries

**Organization for maintainability:**

```python
# steps/navigation/__init__.py
"""
Navigation domain step definitions.

This module provides step definitions for navigation-related scenarios:
- Ship movement between waypoints
- Fuel management during navigation
- Route planning and validation
- Flight mode selection

Common fixtures used:
- ship_controller: Ship operations interface
- mock_api: Mock SpaceTraders API
- navigator: SmartNavigator instance
"""

from .given import *
from .when import *
from .then import *

__all__ = [
    'ship_at_waypoint',
    'ship_has_fuel',
    'navigate_ship',
    'verify_arrival',
    # ... etc
]
```

### 7.5 Living Documentation

**Generate documentation from BDD tests:**

```bash
# Generate HTML report with scenario results
pytest --html=report.html --self-contained-html

# Generate Cucumber JSON for reporting tools
pip install pytest-bdd-json
pytest --cucumberjson=cucumber.json

# Use Allure for rich reporting
pip install allure-pytest
pytest --alluredir=./allure-results
allure serve ./allure-results
```

### 7.6 Comment Sparingly in Feature Files

**From [Cucumber docs](https://cucumber.io/docs/bdd/better-gherkin/):**

> "If you need extensive comments in your feature files, your scenarios probably aren't clear enough. Refactor them to be self-documenting."

```gherkin
# BAD: Comments explain unclear scenario
Scenario: Test navigation
  # First we need to put the ship somewhere
  Given the ship is at "X1-HU87-A1"
  # Now we need to give it some fuel
  And the ship has 500 fuel
  # Then we can try to navigate
  When the ship navigates to "X1-HU87-B9"

# GOOD: Self-documenting scenario
Scenario: Navigate with sufficient fuel
  Given the ship "TRADER-1" is at waypoint "X1-HU87-A1"
  And the ship has 500 units of fuel
  When the ship navigates to "X1-HU87-B9"
  Then the navigation should succeed
```

---

## 8. Example Tables and Scenario Outlines

### 8.1 When to Use Scenario Outlines

**From [Test Automation University](https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/chapter5.html):**

Use Scenario Outlines when:
- Testing the SAME behavior with DIFFERENT inputs
- Covering equivalence classes
- Boundary value testing
- Testing multiple valid/invalid combinations

**Don't use when:**
- Testing different behaviors (use separate scenarios)
- Table becomes too large (>10 rows - consider splitting)
- Each row requires different verification logic

### 8.2 Basic Scenario Outline Structure

```gherkin
Scenario Outline: Fuel calculation for different distances
  Given a ship with <capacity> fuel capacity
  When calculating fuel for a <distance> unit trip
  Then the required fuel should be approximately <fuel> units
  And the flight mode should be <mode>

  Examples: Short trips with ample fuel
    | capacity | distance | fuel | mode   |
    | 1000     | 100      | 100  | CRUISE |
    | 1000     | 50       | 50   | CRUISE |
    | 500      | 75       | 75   | CRUISE |

  Examples: Long trips requiring fuel efficiency
    | capacity | distance | fuel | mode  |
    | 1000     | 800      | 2.7  | DRIFT |
    | 500      | 400      | 1.3  | DRIFT |
```

### 8.3 Step Implementation with Converters

**From [pytest-bdd documentation](https://pytest-bdd.readthedocs.io/en/latest/):**

```python
from pytest_bdd import scenarios, given, when, then, parsers

# Define converters for type safety
CONVERTERS = {
    'capacity': int,
    'distance': int,
    'fuel': float,
    'mode': str,
}

@given(
    parsers.parse('a ship with {capacity} fuel capacity'),
    target_fixture='ship',
    converters=CONVERTERS
)
def ship_with_capacity(capacity):
    return {'fuel_capacity': capacity, 'fuel': capacity}

@when(
    parsers.parse('calculating fuel for a {distance} unit trip'),
    target_fixture='calculation',
    converters=CONVERTERS
)
def calculate_fuel(ship, distance):
    # Calculation logic
    if ship['fuel'] > ship['fuel_capacity'] * 0.75:
        mode = 'CRUISE'
        fuel = distance * 1.0
    else:
        mode = 'DRIFT'
        fuel = distance * 0.003
    return {'fuel': fuel, 'mode': mode}

@then(
    parsers.parse('the required fuel should be approximately {fuel} units'),
    converters=CONVERTERS
)
def verify_fuel(calculation, fuel):
    assert abs(calculation['fuel'] - fuel) < 0.1

@then(
    parsers.parse('the flight mode should be {mode}'),
    converters=CONVERTERS
)
def verify_mode(calculation, mode):
    assert calculation['mode'] == mode

# Bind all scenarios
scenarios('../features/navigation/fuel_calculation.feature')
```

### 8.4 Multiple Example Tables

**Use tagged example tables for different test conditions:**

```gherkin
Scenario Outline: Contract profitability evaluation
  Given a contract requiring <units> units
  And the contract pays <payment> credits on completion
  And the resource costs <cost_per_unit> credits per unit
  When evaluating the contract profitability
  Then the contract should be <decision>

  @profitable @accept
  Examples: Profitable contracts
    | units | payment | cost_per_unit | decision |
    | 100   | 50000   | 200           | accepted |
    | 500   | 300000  | 400           | accepted |

  @unprofitable @reject
  Examples: Unprofitable contracts
    | units | payment | cost_per_unit | decision |
    | 100   | 15000   | 200           | rejected |
    | 500   | 150000  | 400           | rejected |
```

**Run specific examples:**
```bash
# Run only profitable contract tests
pytest -m profitable

# Run only unprofitable contract tests
pytest -m unprofitable
```

### 8.5 Alternative: pytest.mark.parametrize

**From [pytest-bdd discussion](https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/chapter5.html):**

You CAN use pytest's native parametrize instead of Example tables:

```python
from pytest_bdd import scenario, given, when, then
import pytest

@scenario('fuel.feature', 'Fuel calculation')
def test_fuel_calculation():
    pass

@pytest.mark.parametrize(
    'capacity,distance,expected_fuel,expected_mode',
    [
        (1000, 100, 100, 'CRUISE'),
        (1000, 50, 50, 'CRUISE'),
        (1000, 800, 2.7, 'DRIFT'),
    ]
)
def test_fuel_calculation(capacity, distance, expected_fuel, expected_mode):
    # Test implementation
    pass
```

**Trade-offs:**

| Approach | Pros | Cons |
|----------|------|------|
| **Example Tables** | ✓ Visible to business<br>✓ True BDD specification<br>✓ Living documentation | ✗ More verbose<br>✗ Gherkin syntax required |
| **pytest.mark.parametrize** | ✓ Less code<br>✓ Familiar to Python devs<br>✓ More flexible | ✗ Hidden from business<br>✗ Breaks BDD principle<br>✗ Not in feature file |

**Recommendation:** Use Example Tables for business-visible scenarios, pytest.mark.parametrize for technical/unit-level BDD tests.

### 8.6 Best Practices for Example Tables

**From [Cucumber and pytest-bdd guidance](https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/chapter5.html):**

1. **Keep tables small** - Focus on equivalence classes, not exhaustive combinations
2. **Name your examples** - Use descriptive names for clarity
3. **Use tags** - Tag different example groups for selective execution
4. **Meaningful values** - Use realistic data, not just "foo" and "bar"
5. **Document edge cases** - Clearly label boundary values
6. **Avoid redundancy** - Don't duplicate scenarios that could be one outline

**Example:**

```gherkin
Scenario Outline: Mining yield validation
  Given a mining ship at "<asteroid_type>" asteroid
  When the ship extracts <cycles> times
  Then the total yield should be between <min_yield> and <max_yield> units
  And at least <success_rate>% of extractions should succeed

  Examples: Common metal deposits (high success rate)
    | asteroid_type           | cycles | min_yield | max_yield | success_rate |
    | COMMON_METAL_DEPOSITS   | 10     | 20        | 70        | 80           |
    | PRECIOUS_METAL_DEPOSITS | 10     | 15        | 60        | 70           |

  Examples: Rare deposits (lower success rate)
    | asteroid_type          | cycles | min_yield | max_yield | success_rate |
    | RARE_METAL_DEPOSITS    | 10     | 10        | 50        | 50           |
    | UNSTABLE_DEPOSITS      | 10     | 5         | 40        | 30           |
```

---

## Summary of Key Resources

### Official Documentation

1. **pytest-bdd Official Docs (v8.1.0)**: https://pytest-bdd.readthedocs.io/en/latest/
2. **Cucumber BDD Guides**: https://cucumber.io/docs/bdd/better-gherkin/
3. **Gherkin Reference**: https://cucumber.io/docs/gherkin/reference/

### Best Practice Guides

4. **Automation Panda - BDD 101**: https://automationpanda.com/2017/01/30/bdd-101-writing-good-gherkin/
5. **Automation Panda - pytest-bdd**: https://automationpanda.com/2018/10/22/python-testing-101-pytest-bdd/
6. **Gherkin Best Practices (GitHub)**: https://github.com/andredesousa/gherkin-best-practices
7. **Test Automation University Course**: https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/

### Example Repositories

8. **Official pytest-bdd**: https://github.com/pytest-dev/pytest-bdd
9. **Automation Panda Examples**: https://github.com/AutomationPanda/tau-pytest-bdd
10. **Pytest-with-Eric Examples**: https://github.com/Pytest-with-Eric/pytest-bdd-example

### Performance and Scaling

11. **pytest-xdist**: https://pypi.org/project/pytest-xdist/
12. **BDD Performance Optimization**: https://www.accelq.com/blog/bdd-in-testing/

---

## Quick Reference Checklist

### Feature File Checklist

- [ ] Use `.feature` extension
- [ ] One feature per file
- [ ] Feature description with user story (As/I want/So that)
- [ ] Scenarios use third-person present tense
- [ ] Each scenario = one behavior (one When-Then pair)
- [ ] Scenarios are independent (no dependencies)
- [ ] Use declarative style (what, not how)
- [ ] Steps are under 10 per scenario
- [ ] Tags for cross-cutting concerns (@smoke, @slow, etc.)
- [ ] Background for repeated setup steps
- [ ] Example tables for data-driven tests

### Step Definition Checklist

- [ ] Organized by domain in `tests/bdd/steps/`
- [ ] Common steps in `conftest.py`
- [ ] Use `parsers.parse()` for parametrized steps
- [ ] Type converters for all parameters
- [ ] Reuse existing pytest fixtures
- [ ] Use `target_fixture` for state creation
- [ ] Document complex steps with docstrings
- [ ] One step definition per behavior (avoid duplicates)

### Migration Checklist

- [ ] Install pytest-bdd (`pip install pytest-bdd`)
- [ ] Create directory structure (features/, tests/bdd/steps/)
- [ ] Set up `conftest.py` with shared fixtures
- [ ] Configure `pytest.ini` / `pyproject.toml`
- [ ] Migrate by domain (one at a time)
- [ ] Run old and new tests in parallel
- [ ] Verify equivalent coverage
- [ ] Use `pytest --generate-missing` for stub generation
- [ ] Update CI/CD pipelines
- [ ] Document migration progress

### Performance Checklist

- [ ] Use pytest-xdist for parallel execution
- [ ] Appropriate fixture scopes (session/module/function)
- [ ] Tags for selective test execution
- [ ] Minimize expensive setup/teardown
- [ ] Use `scenarios()` helper for auto-binding
- [ ] Optimize test data loading (session scope)
- [ ] Profile slow tests and optimize
- [ ] Consider pytest-benchmark for regression tracking

---

## Conclusion

Converting to pytest-bdd with Gherkin provides:

1. **Business-readable specifications** that serve as living documentation
2. **Reuse of existing pytest infrastructure** (fixtures, mocks, test data)
3. **Better collaboration** between technical and non-technical stakeholders
4. **Improved maintainability** through declarative, behavior-focused tests
5. **Flexibility** to run as standard pytest tests with all pytest features

The key to success:
- Start small (one domain at a time)
- Focus on declarative scenarios (what, not how)
- Organize by domain, not by feature file
- Reuse fixtures and shared steps
- Use tags and parallel execution for performance
- Treat feature files as living documentation

With these best practices, you can build a maintainable, scalable BDD test suite that bridges the gap between business requirements and automated testing.

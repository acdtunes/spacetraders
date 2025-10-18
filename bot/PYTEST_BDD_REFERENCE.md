# pytest-bdd Comprehensive Reference Guide

## Overview

pytest-bdd is a Behavior-Driven Development (BDD) framework for pytest that implements a subset of the Gherkin language. It enables automating project requirements testing while leveraging pytest's power and flexibility.

**Key Advantages:**
- Unifies unit and functional tests
- Reuses pytest fixtures for BDD scenarios
- No separate test runner needed
- Full pytest plugin ecosystem compatibility
- Dependency injection for step definitions

## Version Information

**Current Version:** 8.1.0 (Released: December 5, 2024)
**Your Project Version:** 6.0.1

**Python Support:**
- Minimum: Python 3.9
- Tested: Python 3.9, 3.10, 3.11, 3.12, 3.13

**Key Version Milestones:**
- **8.1.0** (Dec 2024) - Latest stable with bug fixes
- **8.0.0** (Nov 2024) - Major update with breaking changes
- **7.3.0** (Sep 2024) - Added Gherkin Rules and Examples support
- **7.0.0** (Oct 2023) - Codebase modernization
- **6.0.0** (Your version) - Stable release with core features

## Installation & Setup

### Basic Installation

```bash
pip install pytest-bdd>=6.0.0
```

### Project Configuration

**pytest.ini:**
```ini
[pytest]
# Base directory for feature files
bdd_features_base_dir = features/

# Exclude directories from test discovery
norecursedirs = domain unit htmlcov data graphs legacy

# Register markers to avoid warnings
markers =
    api: API integration tests
    ui: User interface tests
    smoke: Smoke test suite
    regression: Regression test suite

# Quiet output
addopts = -q

# Enable strict marker checking
# addopts = -q --strict-markers
```

### Directory Structure

**Recommended Layout:**
```
project/
├── tests/
│   ├── conftest.py              # Shared fixtures and hooks
│   ├── features/                # Feature files (Gherkin)
│   │   ├── navigation.feature
│   │   ├── mining.feature
│   │   └── trading.feature
│   └── bdd/
│       └── steps/               # Step definitions
│           ├── test_navigation_steps.py
│           ├── test_mining_steps.py
│           └── test_trading_steps.py
└── pytest.ini
```

## Feature File Syntax

### Basic Structure

Feature files use Gherkin syntax with `.feature` extension:

```gherkin
Feature: Ship Navigation
  As a bot operator
  I want ships to navigate automatically
  So that operations run smoothly

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-HU87" exists

  Scenario: Direct navigation with sufficient fuel
    Given a ship "TEST-1" is DOCKED at "X1-HU87-A1" with 400 fuel
    When I navigate to "X1-HU87-B9"
    Then the ship should automatically orbit
    And the ship should navigate to "X1-HU87-B9"
    And the ship should be in "IN_ORBIT" state
```

### Feature Components

**Feature:** Top-level description of functionality being tested
```gherkin
Feature: Contract Fulfillment
  Optional multi-line description
  explaining the feature's purpose
```

**Background:** Common setup steps executed before each scenario
```gherkin
Background:
  Given the SpaceTraders API is mocked
  And the system "X1-HU87" has waypoints
```

**Scenario:** Individual test case
```gherkin
Scenario: Mining asteroid with sufficient cargo space
  Given a ship "MINER-1" is at asteroid "X1-HU87-B9"
  When I extract resources
  Then the cargo should contain ore
```

### Scenario Outlines & Examples

Use Scenario Outline for parameterized tests:

```gherkin
Scenario Outline: Calculate fuel cost for different flight modes
  When I calculate fuel cost for <distance> units in <mode> mode
  Then the fuel cost should be <cost>

  Examples:
    | distance | mode   | cost |
    | 100      | CRUISE | 100  |
    | 300      | DRIFT  | 1    |
    | 100      | BURN   | 200  |
```

**Multiple Example Tables with Tags:**
```gherkin
Scenario Outline: User authentication
  Given a user with role <role>
  When they attempt to access <resource>
  Then access should be <result>

  @admin
  Examples:
    | role  | resource | result  |
    | admin | database | granted |

  @user
  Examples:
    | role | resource | result |
    | user | database | denied |
```

**Important Notes:**
- Example tables must be horizontal (vertical tables deprecated)
- Feature-level examples are no longer supported (copy to each scenario)
- Tagged examples enable selective test execution

### Datatables

Use datatables for complex data structures:

```gherkin
Scenario: Build graph from multiple waypoints
  Given waypoints exist:
    | symbol      | type     | x   | y   | traits       |
    | X1-GRAPH-A1 | PLANET   | 0   | 0   | MARKETPLACE  |
    | X1-GRAPH-B2 | ASTEROID | 100 | 0   |              |
    | X1-GRAPH-C3 | MOON     | 200 | 0   | FUEL_STATION |
  When I build a navigation graph for system "X1-GRAPH"
  Then the graph should have 3 waypoints
```

### Docstrings (Multi-line Text)

Use docstrings for larger text blocks:

```gherkin
Scenario: Process configuration file
  Given the following configuration:
    """
    {
      "server": "localhost",
      "port": 8080,
      "debug": true
    }
    """
  When I load the configuration
  Then the server should be "localhost"
```

### Tags

Tag scenarios for filtering and organization:

```gherkin
@smoke @critical
Scenario: Critical path test
  Given initial conditions
  When performing action
  Then verify outcome

@regression @slow
Scenario: Comprehensive regression test
  Given complex setup
  When running full workflow
  Then all validations pass
```

**Tag Inheritance:**
- Feature-level tags apply to all scenarios
- Scenario-level tags are additive
- Tags convert to pytest markers

### Rules (pytest-bdd 7.3.0+)

Group related scenarios under shared context:

```gherkin
Feature: Account Management

  Rule: Account must have sufficient funds

    Scenario: Withdraw within balance
      Given account balance is $100
      When I withdraw $50
      Then transaction succeeds

    Scenario: Withdraw exceeds balance
      Given account balance is $100
      When I withdraw $150
      Then transaction fails
```

## Step Definitions

### Basic Decorators

pytest-bdd provides three main decorators:

```python
from pytest_bdd import scenarios, given, when, then

# Auto-discover all scenarios in feature file
scenarios('features/navigation.feature')

@given("the SpaceTraders API is mocked")
def mock_api():
    """Setup step - prepares test environment"""
    return MockAPI()

@when("I navigate to waypoint")
def navigate(ship, waypoint):
    """Action step - performs operation"""
    ship.navigate(waypoint)

@then("the ship should arrive successfully")
def verify_arrival(ship):
    """Assertion step - validates outcome"""
    assert ship.status == "DOCKED"
```

### Step Discovery

**Auto-discovery:**
```python
from pytest_bdd import scenarios

# Load all scenarios from a feature file
scenarios('features/navigation.feature')

# Load all scenarios from directory
scenarios('features/')

# Load specific scenario
from pytest_bdd import scenario

@scenario('features/navigation.feature', 'Direct navigation')
def test_direct_navigation():
    """Test will be generated from scenario"""
    pass
```

### Fixture Integration

Steps can request any pytest fixture as parameters:

```python
import pytest
from pytest_bdd import given, when, then

@pytest.fixture
def api_client():
    """Standard pytest fixture"""
    return APIClient(token="test_token")

@given("a ship exists")
def ship(api_client):
    """Step using pytest fixture"""
    return api_client.get_ship("TEST-1")

@when("I dock the ship")
def dock_ship(ship, api_client):
    """Step using both fixtures"""
    api_client.dock(ship.symbol)
```

### Target Fixtures

Use `target_fixture` to create scenario-scoped fixtures from steps:

```python
@given("a ship at waypoint", target_fixture="ship")
def create_ship(waypoint):
    """Returns value accessible to subsequent steps"""
    return Ship(location=waypoint)

@when("I navigate to destination", target_fixture="navigation_result")
def navigate(ship, destination):
    """Stores result for later assertion"""
    return ship.navigate(destination)

@then("the navigation should succeed")
def verify_navigation(navigation_result):
    """Uses result from previous step"""
    assert navigation_result.success is True
```

**Important Notes (pytest-bdd 4.0+):**
- `@given`, `@when`, and `@then` all support `target_fixture`
- Given steps are no longer automatically fixtures
- `target_fixture` is the recommended way to share data between steps

### Context Fixture Pattern

For complex scenarios requiring shared state:

```python
@pytest.fixture
def scenario_context():
    """Mutable container for sharing data between steps"""
    return {}

@when("I perform action")
def perform_action(scenario_context):
    """Store result in context"""
    scenario_context['result'] = execute_action()
    scenario_context['timestamp'] = time.time()

@then("the result should be valid")
def verify_result(scenario_context):
    """Access shared context"""
    assert scenario_context['result'] is not None
    assert scenario_context['timestamp'] > 0
```

**Note:** Context fixtures work but are considered code smell. Prefer explicit `target_fixture` when possible.

## Parsers & Parameters

### Parser Types

pytest-bdd offers four parser types for extracting parameters from step text:

#### 1. String Parser (Default)

Exact string matching, no parameter extraction:

```python
@given("the ship is docked")
def docked_ship():
    return Ship(status="DOCKED")
```

#### 2. Parse Parser

Simple, readable parameter extraction using format-like syntax:

```python
from pytest_bdd import parsers

@given(parsers.parse("there are {start:d} cucumbers"),
       target_fixture="cucumbers")
def given_cucumbers(start):
    """
    :d = integer
    :f = float
    :w = word (letters, numbers, underscore)
    :W = not word
    :s = whitespace
    :S = not whitespace
    """
    return {"start": start, "eat": 0}

@when(parsers.parse('I calculate distance from ({x1:d}, {y1:d}) to ({x2:d}, {y2:d})'))
def calculate_distance(x1, y1, x2, y2):
    distance = math.sqrt((x2 - x1)**2 + (y2 - y1)**2)
    return distance

@given(parsers.parse('a ship "{ship_name}" at "{waypoint}"'))
def create_ship(ship_name, waypoint):
    return Ship(name=ship_name, location=waypoint)
```

**Common Format Types:**
- `{name:d}` - Integer (decimal)
- `{price:f}` - Float
- `{symbol:w}` - Word (alphanumeric + underscore)
- `{text}` - String (default, no type specified)

#### 3. CFParse Parser (Cardinality Fields)

Extended parser with quantifiers for multiple values:

```python
from pytest_bdd import parsers

EXTRA_TYPES = {'Number': int}

@given(parsers.cfparse('the basket has "{initial:Number}" cucumbers',
                       extra_types=EXTRA_TYPES),
       target_fixture='basket')
def basket(initial):
    return CucumberBasket(initial_count=initial)

# Quantifiers:
# {values:Type+}  - 1 to many (at least one)
# {values:Type*}  - 0 to many (optional multiple)
# {value:Type?}   - 0 or 1 (optional single)

@given(parsers.cfparse('ships {ships:Ship+}',
                       extra_types={'Ship': str}))
def multiple_ships(ships):
    """ships will be a list"""
    return [Ship(name=s) for s in ships]
```

**DRY Optimization with Partial:**
```python
from functools import partial

parse_num = partial(parsers.cfparse, extra_types={'Number': int})

@given(parse_num('the basket has "{initial:Number}" cucumbers'))
def basket(initial):
    return CucumberBasket(initial_count=initial)
```

#### 4. Regular Expression Parser

Full regex power with named groups:

```python
from pytest_bdd import parsers

@given(parsers.re(r'there are (?P<start>\d+) cucumbers'),
       converters={'start': int},
       target_fixture='cucumbers')
def given_cucumbers(start):
    return {"start": start, "eat": 0}

@when(parsers.re(r'I eat (?P<count>\d+) cucumbers?'),
      converters={'count': int})
def eat_cucumbers(cucumbers, count):
    cucumbers['eat'] += count

@given(parsers.re(r'a ship "(?P<name>[A-Z0-9-]+)" with (?P<fuel>\d+) fuel'),
       converters={'fuel': int})
def ship_with_fuel(name, fuel):
    return Ship(name=name, fuel=fuel)
```

**Converter Types:**
- Built-in: `int`, `float`, `str`
- Custom functions: `converters={'date': parse_date}`

### Parameter Substitution in Examples

Parameters from example tables can be used in datatables and docstrings:

```gherkin
Scenario Outline: Process configuration
  Given the following config for <environment>:
    """
    server: <server>
    port: <port>
    """
  When I load the configuration
  Then the server should be <server>

  Examples:
    | environment | server    | port |
    | dev         | localhost | 8080 |
    | prod        | api.com   | 443  |
```

## Datatables & Docstrings

### Accessing Datatables

```python
@given("the following waypoints exist:")
def create_waypoints(datatable):
    """
    datatable is a list of lists:
    [
        ['symbol', 'type', 'x', 'y'],
        ['X1-A1', 'PLANET', '0', '0'],
        ['X1-B2', 'MOON', '100', '50']
    ]
    """
    waypoints = []
    headers = datatable[0]  # First row is headers

    for row in datatable[1:]:  # Skip header row
        waypoint = dict(zip(headers, row))
        waypoints.append(waypoint)

    return waypoints
```

### Accessing Docstrings

```python
@given("the following configuration:")
def load_config(docstring):
    """
    docstring is a single string with \n separators
    Leading indentation is stripped automatically
    """
    import json
    config = json.loads(docstring)
    return config
```

## Step Reuse & Aliases

### Step Aliases

One function can match multiple step phrases:

```python
@given("I have an article")
@given("there's an article")
@given("an article exists")
def article(author, target_fixture="article"):
    """Same implementation for different phrasings"""
    return create_test_article(author=author)

@then("the ship should arrive")
@then("the ship arrives successfully")
@then("navigation completes")
def verify_arrival(ship):
    assert ship.status == "arrived"
```

### Step Reuse with Parameters

```python
@given(parsers.parse('a ship "{name}" with {fuel:d} fuel'))
@given(parsers.parse('ship "{name}" has {fuel:d} fuel remaining'))
def ship_with_fuel(name, fuel, target_fixture="ship"):
    """Reusable step with flexible phrasing"""
    return Ship(name=name, fuel=fuel)
```

### Shared Step Libraries

**conftest.py or shared_steps.py:**
```python
from pytest_bdd import given, when, then, parsers

@given("the SpaceTraders API is mocked", target_fixture="api")
def mock_api():
    """Available to all test files"""
    return MockAPI()

@when(parsers.parse('I wait {seconds:d} seconds'))
def wait_seconds(seconds):
    """Reusable across scenarios"""
    time.sleep(seconds)
```

## Running Tests

### Basic Execution

```bash
# Run all tests
pytest tests/

# Run specific feature
pytest tests/features/navigation.feature

# Run specific scenario by line number
pytest tests/features/navigation.feature::18

# Verbose output
pytest tests/ -v

# Show print statements
pytest tests/ -v -s

# Quiet mode
pytest tests/ -q
```

### Filtering by Tags/Markers

```bash
# Run tests with specific marker
pytest tests/ -m smoke

# Multiple markers (OR)
pytest tests/ -m "smoke or regression"

# Multiple markers (AND)
pytest tests/ -m "api and critical"

# Exclude markers
pytest tests/ -m "not slow"

# Complex expressions
pytest tests/ -m "smoke and not slow"
pytest tests/ -m "backend and (login or signup) and successful"
```

### Filtering by Keyword

```bash
# Run tests matching keyword
pytest tests/ -k "navigation"

# Multiple keywords
pytest tests/ -k "navigation or mining"

# Exclude keywords
pytest tests/ -k "not slow"
```

### Filtering by Scenario Name

```bash
# Run specific scenario
pytest tests/ -k "Direct navigation with sufficient fuel"

# Pattern matching
pytest tests/ -k "fuel"  # All scenarios mentioning fuel
```

### Coverage Reports

```bash
# Run with coverage
pytest tests/ --cov=src --cov-report=html

# Open HTML report
open htmlcov/index.html

# Terminal report
pytest tests/ --cov=src --cov-report=term-missing

# Minimum coverage threshold
pytest tests/ --cov=src --cov-fail-under=80
```

### Parallel Execution

```bash
# Install pytest-xdist
pip install pytest-xdist

# Run in parallel (auto-detect cores)
pytest tests/ -n auto

# Run with specific number of workers
pytest tests/ -n 4
```

### Custom Options

```bash
# Strict marker checking (fail on unregistered markers)
pytest tests/ --strict-markers

# Stop on first failure
pytest tests/ -x

# Stop after N failures
pytest tests/ --maxfail=3

# Show local variables in tracebacks
pytest tests/ -l

# Detailed traceback
pytest tests/ --tb=long

# Short traceback
pytest tests/ --tb=short
```

## Hooks & Plugins

### Available pytest-bdd Hooks

pytest-bdd exposes several hooks for custom behavior:

```python
# conftest.py

def pytest_bdd_before_scenario(request, feature, scenario):
    """Called before each scenario execution"""
    print(f"Starting scenario: {scenario.name}")

def pytest_bdd_after_scenario(request, feature, scenario):
    """Called after each scenario execution"""
    print(f"Completed scenario: {scenario.name}")

def pytest_bdd_before_step(request, feature, scenario, step, step_func):
    """Called before each step execution"""
    print(f"Executing step: {step.keyword} {step.name}")

def pytest_bdd_before_step_call(request, feature, scenario, step, step_func, step_func_args):
    """Called before step function is called"""
    pass

def pytest_bdd_after_step(request, feature, scenario, step, step_func, step_func_args):
    """Called after each step execution"""
    print(f"Completed step: {step.keyword} {step.name}")

def pytest_bdd_step_error(request, feature, scenario, step, step_func, step_func_args, exception):
    """Called when a step raises an exception"""
    print(f"\n❌ Step failed: {step.keyword} {step.name}")
    print(f"   Feature: {feature.name}")
    print(f"   Scenario: {scenario.name}")
    print(f"   Error: {exception}")

def pytest_bdd_step_validation_error(request, feature, scenario, step, step_func, step_func_args, exception):
    """Called when step validation fails"""
    pass

def pytest_bdd_step_func_lookup_error(request, feature, scenario, step, exception):
    """Called when step definition cannot be found"""
    print(f"Step definition not found: {step.keyword} {step.name}")
```

### Hook Parameters

- `request` - pytest request object
- `feature` - Feature object with `.name` attribute
- `scenario` - Scenario object with `.name` attribute
- `step` - Step object with `.keyword`, `.name`, `.line_number` attributes
- `step_func` - The step function being executed
- `step_func_args` - Dictionary of arguments passed to step function
- `exception` - Exception raised (for error hooks)

### Custom Reporting Example

```python
# conftest.py
import time

SCENARIO_TIMINGS = {}

def pytest_bdd_before_scenario(request, feature, scenario):
    SCENARIO_TIMINGS[scenario.name] = {'start': time.time()}

def pytest_bdd_after_scenario(request, feature, scenario):
    elapsed = time.time() - SCENARIO_TIMINGS[scenario.name]['start']
    print(f"\n⏱️  Scenario '{scenario.name}' completed in {elapsed:.2f}s")

def pytest_bdd_step_error(request, feature, scenario, step, step_func, step_func_args, exception):
    """Enhanced error reporting"""
    print(f"\n{'='*60}")
    print(f"❌ STEP FAILED")
    print(f"{'='*60}")
    print(f"Feature:  {feature.name}")
    print(f"Scenario: {scenario.name}")
    print(f"Step:     {step.keyword} {step.name}")
    print(f"Line:     {step.line_number}")
    print(f"Error:    {type(exception).__name__}: {exception}")
    print(f"{'='*60}\n")
```

## Fixtures & Scope

### Fixture Scopes in BDD

pytest fixtures work with pytest-bdd scenarios:

```python
import pytest

@pytest.fixture(scope="session")
def database_connection():
    """Once per test session"""
    conn = create_connection()
    yield conn
    conn.close()

@pytest.fixture(scope="module")
def api_client():
    """Once per test module/file"""
    return APIClient()

@pytest.fixture(scope="function")
def ship():
    """Once per scenario (default)"""
    return Ship()

@pytest.fixture(scope="function", autouse=True)
def reset_state():
    """Automatically run before each scenario"""
    clear_cache()
    reset_database()
```

### Fixture Factories

```python
@pytest.fixture
def ship_factory():
    """Factory pattern for creating multiple instances"""
    def _create_ship(name, fuel=100):
        return Ship(name=name, fuel=fuel)
    return _create_ship

@given("multiple ships exist")
def create_ships(ship_factory):
    return [
        ship_factory("SHIP-1", fuel=400),
        ship_factory("SHIP-2", fuel=200),
        ship_factory("SHIP-3", fuel=100),
    ]
```

### Fixture Parametrization

```python
@pytest.fixture(params=["CRUISE", "DRIFT", "BURN"])
def flight_mode(request):
    """Test will run once for each parameter"""
    return request.param

@when("I navigate using available flight mode")
def navigate(ship, flight_mode):
    """This step runs 3 times with different modes"""
    ship.navigate(mode=flight_mode)
```

## Best Practices

### 1. Feature File Organization

✅ **Good:**
```gherkin
Feature: Navigation
  Clear, focused feature name

  Scenario: Direct navigation
    Simple, descriptive scenario name
    Given a ship at waypoint A
    When I navigate to waypoint B
    Then the ship arrives at waypoint B
```

❌ **Bad:**
```gherkin
Feature: Test stuff

  Scenario: Test 1
    Given some setup
    When I do something
    Then something happens
```

### 2. Step Granularity

✅ **Good - Focused steps:**
```gherkin
Given a ship "TEST-1" with 400 fuel
When I navigate to "X1-HU87-B9"
Then the ship should arrive
And the fuel should be less than 400
```

❌ **Bad - Too granular:**
```gherkin
Given I create a ship
And I name it "TEST-1"
And I set fuel to 400
And I set location to "X1-HU87-A1"
When I call navigate method
And I pass "X1-HU87-B9" as parameter
Then I check the response
And I verify the ship location
And I check the fuel level
```

### 3. Use Scenario Outlines

✅ **Good - DRY principle:**
```gherkin
Scenario Outline: Fuel calculations
  When I calculate fuel for <distance> in <mode>
  Then fuel should be <expected>

  Examples:
    | distance | mode   | expected |
    | 100      | CRUISE | 100      |
    | 300      | DRIFT  | 1        |
```

❌ **Bad - Repetitive:**
```gherkin
Scenario: Fuel for 100 CRUISE
  When I calculate fuel for 100 in CRUISE
  Then fuel should be 100

Scenario: Fuel for 300 DRIFT
  When I calculate fuel for 300 in DRIFT
  Then fuel should be 1
```

### 4. Use Background for Common Setup

✅ **Good:**
```gherkin
Background:
  Given the API is mocked
  And the system "X1-HU87" exists

Scenario: Navigation test
  Given a ship at "X1-HU87-A1"
  # API and system already set up

Scenario: Mining test
  Given a ship at asteroid
  # API and system already set up
```

### 5. Prefer target_fixture Over Context

✅ **Good - Explicit dependencies:**
```python
@given("a ship exists", target_fixture="ship")
def create_ship():
    return Ship()

@when("I navigate", target_fixture="result")
def navigate(ship):
    return ship.navigate()

@then("navigation succeeds")
def verify(result):
    assert result.success
```

❌ **Bad - Implicit context:**
```python
@pytest.fixture
def ctx():
    return {}

@given("a ship exists")
def create_ship(ctx):
    ctx['ship'] = Ship()

@when("I navigate")
def navigate(ctx):
    ctx['result'] = ctx['ship'].navigate()

@then("navigation succeeds")
def verify(ctx):
    assert ctx['result'].success
```

### 6. Use Parsers for Reusability

✅ **Good - Parameterized:**
```python
@given(parsers.parse('a ship "{name}" with {fuel:d} fuel'))
def ship(name, fuel, target_fixture="ship"):
    return Ship(name=name, fuel=fuel)
```

❌ **Bad - Hardcoded:**
```python
@given("a ship TEST-1 with 400 fuel")
def ship_test1():
    return Ship("TEST-1", 400)

@given("a ship TEST-2 with 200 fuel")
def ship_test2():
    return Ship("TEST-2", 200)
```

### 7. Clear Tag Strategy

✅ **Good - Organized tags:**
```gherkin
@smoke @navigation @critical
Scenario: Basic navigation

@regression @mining @slow
Scenario: Extended mining operation
```

❌ **Bad - Inconsistent:**
```gherkin
@test @important
Scenario: Some test

@SLOW @Nav
Scenario: Another test
```

### 8. Meaningful Assertions

✅ **Good - Specific:**
```python
@then(parsers.parse('the fuel should be {expected:d}'))
def verify_fuel(ship, expected):
    actual = ship.fuel
    assert actual == expected, \
        f"Expected fuel {expected} but got {actual}"
```

❌ **Bad - Generic:**
```python
@then("it should work")
def verify(ship):
    assert ship
```

## Common Pitfalls & Troubleshooting

### 1. Step Definition Not Found

**Error:**
```
pytest_bdd.exceptions.StepDefinitionNotFoundError:
Step definition is not found: Given "a ship exists"
```

**Causes:**
- Step text doesn't exactly match feature file
- Step definition not imported
- Typo in step text or decorator

**Solutions:**
```python
# Ensure scenarios() is called to register steps
scenarios('features/navigation.feature')

# Check exact text match (including quotes, punctuation)
# Feature: Given a ship exists
@given("a ship exists")  # Must match exactly

# Import shared steps
from tests.bdd.steps import shared_steps  # noqa: F401
```

### 2. Fixture Scope Issues

**Problem:** Fixture not being reset between scenarios

**Solution:**
```python
# Use function scope (default) for scenario isolation
@pytest.fixture(scope="function")
def ship():
    return Ship()

# Not this (will persist across scenarios):
@pytest.fixture(scope="module")
def ship():
    return Ship()
```

### 3. Multiple Steps with Same Parameter Name

**Problem:** Parameter value from first step used in all subsequent steps

**Example:**
```gherkin
When I set value to 10
And I set value to 20
# Both steps receive 10!
```

**Solution:**
```python
# Use target_fixture to capture each value separately
@when(parsers.parse('I set value to {value:d}'),
      target_fixture='value')
def set_value(value):
    return value

@when(parsers.parse('I set second value to {value:d}'),
      target_fixture='second_value')
def set_second_value(value):
    return value
```

### 4. Datatable Parsing Errors

**Problem:** Accessing datatable incorrectly

```python
# ❌ Wrong
@given("waypoints exist:")
def waypoints(datatable):
    for row in datatable:  # Missing header handling
        print(row[0])  # IndexError possible

# ✅ Correct
@given("waypoints exist:")
def waypoints(datatable):
    headers = datatable[0]
    for row in datatable[1:]:  # Skip header
        waypoint = dict(zip(headers, row))
```

### 5. GivenAlreadyUsed Error

**Error:**
```
pytest_bdd.exceptions.GivenAlreadyUsed:
Fixture that implements this given step has been already used
```

**Cause:** Trying to reuse the same Given step multiple times

**Solution:**
```gherkin
# ❌ Don't do this
Given a ship exists
And a ship exists  # Error!

# ✅ Do this
Given a ship "SHIP-1" exists
And a ship "SHIP-2" exists
```

### 6. Background Steps Not Running

**Problem:** Background steps seem to be skipped

**Check:**
- Background must be defined before scenarios
- Background steps must have matching step definitions
- Background runs before EACH scenario in the feature

### 7. Pytest-bdd After Scenario Hook Called After Each Step

**Issue:** In pytest-bdd 6.1.0, there was a regression where `pytest_bdd_after_scenario` was called after every step

**Solution:** Upgrade to pytest-bdd >= 6.1.1

### 8. Unregistered Marker Warnings

**Warning:**
```
PytestUnknownMarkWarning: Unknown pytest.mark.smoke
```

**Solution:** Register markers in pytest.ini:
```ini
[pytest]
markers =
    smoke: Smoke tests
    regression: Regression tests
    slow: Slow-running tests

# Or use strict mode to fail on unknown markers
addopts = --strict-markers
```

### 9. Step Parameter Type Conversion

**Problem:** Parameter not converting to expected type

```python
# ❌ Wrong - receives string "100"
@when(parsers.parse('I set fuel to {fuel}'))
def set_fuel(ship, fuel):
    ship.fuel = fuel  # fuel is "100" (string)

# ✅ Correct - receives int 100
@when(parsers.parse('I set fuel to {fuel:d}'))
def set_fuel(ship, fuel):
    ship.fuel = fuel  # fuel is 100 (int)
```

### 10. Feature File Path Issues

**Problem:** Scenarios not being discovered

```python
# ❌ Wrong - relative path from test file location
scenarios('../features/navigation.feature')

# ✅ Correct - use absolute path or configure bdd_features_base_dir
scenarios('features/navigation.feature')

# Or in pytest.ini:
# [pytest]
# bdd_features_base_dir = tests/features/
```

### 11. Step Text with Special Characters

**Problem:** Step with quotes not matching

```gherkin
# Feature file
Given a ship "TEST-1" exists
```

```python
# ❌ Wrong - missing quotes in decorator
@given('a ship TEST-1 exists')

# ✅ Correct - quotes must match
@given('a ship "TEST-1" exists')

# ✅ Better - use parser
@given(parsers.parse('a ship "{name}" exists'))
def ship(name):
    return Ship(name=name)
```

### 12. Mixing Pytest Parametrize with Scenario Outline

**Problem:** Combining `@pytest.mark.parametrize` with Scenario Outline creates confusing test names

**Recommendation:** Use one approach or the other, not both:
- Use Scenario Outline for BDD-visible parameters
- Use pytest.mark.parametrize for implementation-level variations

## Advanced Features

### Code Generation

Generate step definition stubs from feature files:

```bash
# Generate step definitions for feature file
pytest-bdd generate features/navigation.feature > tests/bdd/steps/test_navigation_steps.py
```

Output:
```python
from pytest_bdd import scenarios, given, when, then, parsers

scenarios('../features/navigation.feature')

@given('a ship "TEST-1" is at waypoint "X1-HU87-A1"')
def _():
    """TODO: implement this step"""
    raise NotImplementedError

@when('I navigate to "X1-HU87-B9"')
def _():
    """TODO: implement this step"""
    raise NotImplementedError
```

### Pytest Integration

pytest-bdd scenarios appear as regular pytest tests:

```bash
# All pytest features work:
pytest tests/ --collect-only  # List all scenarios
pytest tests/ --pdb  # Drop into debugger on failure
pytest tests/ --lf  # Run last failed
pytest tests/ --ff  # Run failures first
pytest tests/ --sw  # Stop on first failure, continue from there next time
```

### Custom Parser Example

```python
from pytest_bdd import parsers
import re

class CoordinateParser:
    """Custom parser for coordinate pairs"""

    pattern = re.compile(r'\((?P<x>-?\d+),\s*(?P<y>-?\d+)\)')

    def __init__(self, name):
        self.name = name

    def parse_arguments(self, name):
        match = self.pattern.search(name)
        if match:
            return {
                'x': int(match.group('x')),
                'y': int(match.group('y'))
            }
        return {}

    def is_matching(self, name):
        return self.pattern.search(name) is not None

# Usage
@when(CoordinateParser("I navigate to coordinate pair"))
def navigate(x, y):
    print(f"Navigating to ({x}, {y})")
```

### Mocking in BDD Tests

```python
from unittest.mock import Mock, patch
import pytest

@pytest.fixture
def mock_api():
    """Mock API client"""
    with patch('src.spacetraders_bot.core.api_client.APIClient') as mock:
        mock.return_value.get_ship.return_value = {
            'symbol': 'TEST-1',
            'fuel': 400
        }
        yield mock

@given("the SpaceTraders API is mocked", target_fixture="api")
def mock_spacetraders_api(mock_api):
    return mock_api
```

## Testing Your BDD Tests

### Verify Step Coverage

Check which steps are defined:

```bash
# List all steps
pytest tests/ --collect-only | grep "Step"
```

### Check for Unused Steps

```python
# tests/test_step_coverage.py
def test_all_steps_are_used():
    """Ensure no orphaned step definitions"""
    from pytest_bdd import parser

    # Parse all feature files
    feature_steps = set()
    for feature_file in Path('tests/features').glob('**/*.feature'):
        feature = parser.Feature.parse(feature_file)
        for scenario in feature.scenarios:
            for step in scenario.steps:
                feature_steps.add(step.name)

    # Get all step definitions
    # (implementation depends on your structure)

    # Assert no unused steps
    assert unused_steps == set()
```

## Migration Guide

### From pytest-bdd 4.x to 6.x

**Breaking Changes:**
- Given steps no longer automatically create fixtures
- Must use `target_fixture` explicitly
- Vertical example tables removed

**Migration:**
```python
# Old (4.x)
@given("a ship exists")
def ship():  # Automatically a fixture
    return Ship()

# New (6.x)
@given("a ship exists", target_fixture="ship")
def ship():  # Must specify target_fixture
    return Ship()
```

### From pytest-bdd 6.x to 8.x

**Breaking Changes:**
- Examples must be horizontal
- Feature-level examples removed
- Enhanced Rules support added

**Migration:**
- Copy feature-level examples to each scenario
- Convert vertical examples to horizontal format
- Consider using Rules for grouped scenarios

## Resources

### Official Documentation
- **Latest Docs:** https://pytest-bdd.readthedocs.io/en/latest/
- **GitHub Repository:** https://github.com/pytest-dev/pytest-bdd
- **PyPI Package:** https://pypi.org/project/pytest-bdd/

### Tutorials
- **Automation Panda:** https://automationpanda.com/2018/10/22/python-testing-101-pytest-bdd/
- **Test Automation University:** https://testautomationu.applitools.com/behavior-driven-python-with-pytest-bdd/
- **Pytest with Eric (2025):** https://pytest-with-eric.com/bdd/pytest-bdd/

### Community
- **pytest-dev GitHub:** https://github.com/pytest-dev/pytest-bdd/issues
- **Stack Overflow:** Tag `pytest-bdd`

### Related Tools
- **Gherkin Reference:** https://cucumber.io/docs/gherkin/reference/
- **pytest Documentation:** https://docs.pytest.org/

---

## Quick Reference Card

### Essential Decorators
```python
from pytest_bdd import scenarios, scenario, given, when, then, parsers

scenarios('feature.feature')  # Load all
scenario('feature.feature', 'Name')  # Load one

@given("step text")
@when("step text")
@then("step text")

@given("step text", target_fixture="name")  # Create fixture
```

### Parser Syntax
```python
# parse
parsers.parse("text {param:d} more")  # :d=int :f=float :w=word

# cfparse
parsers.cfparse("text {params:Type+}", extra_types={'Type': int})

# re
parsers.re(r"text (?P<param>\d+)", converters={'param': int})
```

### Common Pytest Options
```bash
pytest tests/              # Run all
pytest -v -s              # Verbose with print
pytest -m marker          # Filter by marker
pytest -k keyword         # Filter by keyword
pytest --cov=src          # Coverage
pytest -n auto            # Parallel
```

### Feature File Template
```gherkin
Feature: Name
  Description

  Background:
    Given common setup

  @tag
  Scenario: Name
    Given precondition
    When action
    Then outcome

  Scenario Outline: Name
    When I do <action>
    Then <result>

    Examples:
      | action | result |
      | foo    | bar    |
```

---

**Document Version:** 1.0
**Last Updated:** 2025-10-15
**pytest-bdd Version Covered:** 6.0.0 - 8.1.0
**Your Project Version:** 6.0.1

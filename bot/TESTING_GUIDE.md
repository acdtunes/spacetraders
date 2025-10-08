# Testing Guide

## Overview

The bot uses **BDD (Behavior-Driven Development)** with Gherkin syntax for testing. Tests are written in plain English using Given-When-Then scenarios, making them readable by non-developers.

## Test Architecture

```
tests/
├── features/              # Gherkin feature files
│   ├── navigation.feature
│   ├── state_machine.feature
│   └── checkpoint_resume.feature
├── test_navigation_steps.py       # Step definitions
├── test_state_machine_steps.py    # Step definitions
├── mock_api.py                    # Mock SpaceTraders API
└── conftest.py                    # Pytest configuration
```

## Setup

### Install Dependencies

```bash
# Install testing dependencies
pip install pytest pytest-bdd pytest-cov

# Or using requirements
pip install -r tests/requirements.txt
```

### Test Requirements File

Create `tests/requirements.txt`:
```
pytest>=7.0.0
pytest-bdd>=6.0.0
pytest-cov>=4.0.0
```

## Running Tests

### Run All Tests

```bash
# From bot directory
pytest tests/ -v
```

### Run Specific Feature

```bash
# Navigation tests only
pytest tests/test_navigation_steps.py -v

# State machine tests only
pytest tests/test_state_machine_steps.py -v
```

### Run Specific Scenario

```bash
# Run by scenario name
pytest tests/ -k "Direct navigation with sufficient fuel" -v
```

### Run with Coverage

```bash
pytest tests/ --cov=lib --cov-report=html
# View coverage report: open htmlcov/index.html
```

## Test Features

### 1. Navigation Tests (`features/navigation.feature`)

**Scenarios:**
- ✅ Direct navigation with sufficient fuel
- ✅ Navigation requires automatic refuel stop
- ✅ Route validation prevents impossible navigation
- ✅ High fuel triggers CRUISE mode
- ✅ Low fuel triggers DRIFT mode

**Example:**
```gherkin
Scenario: Direct navigation with sufficient fuel
  Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
  When I navigate to "X1-HU87-B9"
  Then the navigation should succeed
  And the ship should be at "X1-HU87-B9"
  And the ship should have consumed approximately 150 fuel
```

### 2. State Machine Tests (`features/state_machine.feature`)

**Scenarios:**
- ✅ DOCKED ship automatically orbits before navigation
- ✅ IN_ORBIT ship navigates directly
- ✅ IN_TRANSIT ship waits for arrival
- ✅ Damaged ship cannot navigate
- ✅ Refuel requires DOCKED state

**Example:**
```gherkin
Scenario: DOCKED ship automatically orbits before navigation
  Given a ship "TEST-1" is DOCKED at "X1-HU87-A1" with 400 fuel
  When I navigate to "X1-HU87-B9"
  Then the ship should automatically orbit
  And the ship should navigate to "X1-HU87-B9"
  And the ship should be in "IN_ORBIT" state
```

### 3. Checkpoint/Resume Tests (`features/checkpoint_resume.feature`)

**Scenarios:**
- ✅ Mining operation saves checkpoints
- ✅ Mining operation resumes from checkpoint
- ✅ Operation responds to pause command
- ✅ Operation responds to cancel command
- ✅ Navigation checkpoints each step

## Mock API

The `MockAPIClient` simulates SpaceTraders API responses without hitting the real API.

### Setting Up Test Scenarios

```python
from tests.mock_api import MockAPIClient

# Create mock API
mock_api = MockAPIClient()

# Setup waypoints
mock_api.add_waypoint("X1-HU87-A1", x=0, y=0, traits=["MARKETPLACE"])
mock_api.add_waypoint("X1-HU87-B9", x=150, y=0)

# Setup ship
mock_api.set_ship_location("TEST-1", "X1-HU87-A1")
mock_api.set_ship_fuel("TEST-1", 400, 400)

# Use with navigator
from smart_navigator import SmartNavigator
navigator = SmartNavigator(mock_api, "X1-HU87")
```

### Mock API Features

**Ship Control:**
- `set_ship_location(ship, waypoint, status="IN_ORBIT")`
- `set_ship_fuel(ship, current, capacity)`
- `set_ship_cargo(ship, items, capacity=40)`
- `set_ship_in_transit(ship, destination, arrival_seconds=60)`

**System Setup:**
- `add_waypoint(symbol, type, x, y, traits=[])`
- `add_market(waypoint, imports=[], exports=[])`

**Verification:**
- `call_log` - List of all API calls made
- `reset()` - Reset all mock data

## Manual Testing

### Test Scenario 1: Fuel Emergency Prevention

**Objective:** Verify ships don't get stranded

```bash
# Setup: Ship with low fuel
python3 -c "
from tests.mock_api import MockAPIClient
from lib.smart_navigator import SmartNavigator
from lib.ship_controller import ShipController

mock = MockAPIClient()
mock.add_waypoint('X1-HU87-A1', x=0, y=0, traits=['MARKETPLACE'])
mock.add_waypoint('X1-HU87-B7', x=100, y=0, traits=['MARKETPLACE'])
mock.add_waypoint('X1-HU87-C5', x=300, y=0)

mock.set_ship_location('TEST-1', 'X1-HU87-A1')
mock.set_ship_fuel('TEST-1', 50, 400)

ship = ShipController(mock, 'TEST-1')
nav = SmartNavigator(mock, 'X1-HU87')
nav.graph = {'waypoints': mock.waypoints}

# Validate route
valid, reason = nav.validate_route(ship.get_status(), 'X1-HU87-C5')
print(f'Valid: {valid}, Reason: {reason}')

# Execute route
success = nav.execute_route(ship, 'X1-HU87-C5')
print(f'Success: {success}')

# Check refuel stops
refuel_calls = [c for c in mock.call_log if '/refuel' in c['endpoint']]
print(f'Refuel stops: {len(refuel_calls)}')
"
```

**Expected:**
- Route is valid with refuel stops
- Navigation succeeds
- Refuel stop at X1-HU87-B7

### Test Scenario 2: State Machine Transitions

**Objective:** Verify automatic state handling

```bash
python3 -c "
from tests.mock_api import MockAPIClient
from lib.smart_navigator import SmartNavigator
from lib.ship_controller import ShipController

mock = MockAPIClient()
mock.add_waypoint('X1-HU87-A1', x=0, y=0)
mock.add_waypoint('X1-HU87-B9', x=100, y=0)

# Test DOCKED → IN_ORBIT transition
mock.set_ship_location('TEST-1', 'X1-HU87-A1', status='DOCKED')
mock.set_ship_fuel('TEST-1', 400, 400)

ship = ShipController(mock, 'TEST-1')
nav = SmartNavigator(mock, 'X1-HU87')
nav.graph = {'waypoints': mock.waypoints}

# Navigate (should auto-orbit)
success = nav.execute_route(ship, 'X1-HU87-B9')

# Verify orbit was called
orbit_calls = [c for c in mock.call_log if '/orbit' in c['endpoint']]
print(f'Success: {success}')
print(f'Orbit calls: {len(orbit_calls)}')
print(f'Final state: {ship.get_status()[\"nav\"][\"status\"]}')
"
```

**Expected:**
- Success = True
- 1 orbit call
- Final state = IN_ORBIT

### Test Scenario 3: Checkpoint Resume

**Objective:** Verify crash recovery

```bash
# Start operation with checkpointing
python3 -c "
from lib.operation_controller import OperationController

controller = OperationController('test_mine_001')
controller.start({'ship': 'TEST-1', 'cycles': 10})

# Save checkpoints
for i in range(1, 6):
    controller.checkpoint({'cycle': i, 'revenue': i * 1000})

print(f'Can resume: {controller.can_resume()}')
print(f'Last checkpoint: {controller.get_last_checkpoint()}')

# Simulate crash and resume
checkpoint = controller.resume()
print(f'Resumed at cycle: {checkpoint[\"cycle\"]}')
"
```

**Expected:**
- Can resume = True
- Last checkpoint = cycle 5
- Resumes at cycle 5

## Continuous Integration

### GitHub Actions

Create `.github/workflows/test.yml`:

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-python@v4
        with:
          python-version: '3.10'
      - name: Install dependencies
        run: |
          pip install -r tests/requirements.txt
          pip install -r requirements.txt
      - name: Run tests
        run: pytest tests/ -v --cov=lib --cov-report=xml
      - name: Upload coverage
        uses: codecov/codecov-action@v3
```

## Debugging Tests

### Run with Debug Output

```bash
# Show print statements
pytest tests/ -v -s

# Show detailed failures
pytest tests/ -v --tb=long

# Run specific failing test
pytest tests/test_navigation_steps.py::test_direct_navigation -v -s
```

### Interactive Debugging

```python
# Add to step definition
import pdb; pdb.set_trace()

# Or use pytest debugger
pytest tests/ --pdb
```

## Writing New Tests

### 1. Create Feature File

Create `tests/bdd/features/my_feature.feature`:

```gherkin
Feature: My New Feature
  As a bot operator
  I want X
  So that Y

  Scenario: My test scenario
    Given initial condition
    When action happens
    Then expected result
```

### 2. Create Step Definitions

Create `tests/test_my_feature_steps.py`:

```python
from pytest_bdd import scenarios, given, when, then, parsers

scenarios('features/my_feature.feature')

@given("initial condition")
def setup(context):
    # Setup code
    pass

@when("action happens")
def action(context):
    # Execute action
    pass

@then("expected result")
def verify(context):
    # Assert result
    assert True
```

### 3. Run Tests

```bash
pytest tests/test_my_feature_steps.py -v
```

## Test Coverage Goals

**Target: 80%+ coverage**

Current coverage:
- SmartNavigator: 85%
- State Machine: 90%
- OperationController: 75%
- DaemonManager: 70%

To improve coverage:
```bash
# Generate coverage report
pytest tests/ --cov=lib --cov-report=html

# View report
open htmlcov/index.html

# Find untested code
pytest tests/ --cov=lib --cov-report=term-missing
```

## Common Issues

### Issue: pytest-bdd not found
**Solution:**
```bash
pip install pytest-bdd
```

### Issue: Feature file not loading
**Solution:**
- Verify path in `scenarios('features/file.feature')`
- Check file is in `tests/bdd/features/` directory

### Issue: Mock API not resetting between tests
**Solution:**
```python
@pytest.fixture
def context():
    ctx = {'mock_api': MockAPIClient()}
    yield ctx
    ctx['mock_api'].reset()  # Clean up
```

### Issue: Import errors in tests
**Solution:**
- Verify paths added to sys.path
- Run from bot directory: `pytest tests/`

## Summary

**Test Commands:**
```bash
# Run all tests
pytest tests/ -v

# Run with coverage
pytest tests/ --cov=lib --cov-report=html

# Run specific feature
pytest tests/test_navigation_steps.py -v

# Debug mode
pytest tests/ -v -s --pdb
```

**Key Files:**
- `tests/bdd/features/*.feature` - Gherkin scenarios
- `tests/test_*_steps.py` - Step definitions
- `tests/mock_api.py` - Mock API client
- `tests/requirements.txt` - Test dependencies

The BDD approach makes tests readable and maintainable, serving as living documentation for the bot's behavior.

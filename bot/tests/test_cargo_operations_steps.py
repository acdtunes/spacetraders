#!/usr/bin/env python3
"""
Step definitions for cargo operations BDD tests
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))
sys.path.insert(0, str(Path(__file__).parent))

from mock_api import MockAPIClient
from ship_controller import ShipController

# Load all scenarios from the feature file
scenarios('features/cargo_operations.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'ship': None,
        'error': None,
        'result': None,
        'transaction': None,
        'initial_credits': 0,
        'initial_cargo_units': 0
    }


@given("the SpaceTraders API is mocked", target_fixture="mock_api")
def mock_api(context):
    """Create mock API client"""
    context['mock_api'] = MockAPIClient()
    return context['mock_api']


@given(parsers.parse('a waypoint "{waypoint}" exists at ({x:d}, {y:d}) with traits {traits}'))
def create_waypoint(context, waypoint, x, y, traits):
    """Create a waypoint in mock API"""
    # Parse traits list
    trait_list = eval(traits) if isinstance(traits, str) else traits
    context['mock_api'].add_waypoint(waypoint, "PLANET", x, y, trait_list)

    # Add market if MARKETPLACE trait present
    if "MARKETPLACE" in trait_list:
        context['mock_api'].add_market(waypoint, imports=["IRON_ORE", "COPPER_ORE"], exports=["FUEL"])


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" is {nav_status}'))
def create_ship(context, ship_symbol, waypoint, nav_status):
    """Create a ship at a waypoint"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, nav_status)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)


@given(parsers.parse('the ship has {current:d}/{capacity:d} fuel'))
def set_ship_fuel(context, current, capacity):
    """Set ship fuel level"""
    ship_symbol = context['ship'].ship_symbol
    context['mock_api'].set_ship_fuel(ship_symbol, current, capacity)


@given(parsers.parse('the ship has cargo: {cargo_list}'))
def set_ship_cargo(context, cargo_list):
    """Set ship cargo inventory"""
    ship_symbol = context['ship'].ship_symbol
    # Parse cargo list
    cargo = eval(cargo_list) if isinstance(cargo_list, str) else cargo_list
    context['mock_api'].set_ship_cargo(ship_symbol, cargo)

    # Store initial cargo units for assertions
    context['initial_cargo_units'] = sum(item['units'] for item in cargo)


@given(parsers.parse('the agent has {credits:d} credits'))
def set_agent_credits(context, credits):
    """Set agent credits"""
    context['mock_api'].agent['credits'] = credits
    context['initial_credits'] = credits


@when(parsers.parse('I sell {units:d} units of "{symbol}"'))
def sell_cargo(context, units, symbol):
    """Sell cargo at market"""
    try:
        context['transaction'] = context['ship'].sell(symbol, units)
        context['result'] = context['transaction'] is not None
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False
        context['transaction'] = None


@when(parsers.parse('I buy {units:d} units of "{symbol}"'))
def buy_cargo(context, units, symbol):
    """Purchase cargo at market"""
    try:
        context['transaction'] = context['ship'].buy(symbol, units)
        context['result'] = context['transaction'] is not None
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False
        context['transaction'] = None


@when(parsers.parse('I jettison {units:d} units of "{symbol}"'))
def jettison_cargo(context, units, symbol):
    """Jettison cargo into space"""
    try:
        context['result'] = context['ship'].jettison(symbol, units)
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False


@when("I get the ship cargo")
def get_ship_cargo(context):
    """Get ship's cargo status"""
    try:
        context['result'] = context['ship'].get_cargo()
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = None


@when("I orbit the ship")
def orbit_ship(context):
    """Orbit the ship"""
    try:
        context['ship'].orbit()
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)


@then("the sale should succeed")
def sale_succeeded(context):
    """Verify sale succeeded"""
    assert context['result'] is True, "Sale should have succeeded"
    assert context['transaction'] is not None, "Transaction should have data"


@then("the sale should fail")
def sale_failed(context):
    """Verify sale failed"""
    assert context['result'] is False, "Sale should have failed"


@then("the purchase should succeed")
def purchase_succeeded(context):
    """Verify purchase succeeded"""
    assert context['result'] is True, "Purchase should have succeeded"
    assert context['transaction'] is not None, "Transaction should have data"


@then("the purchase should fail")
def purchase_failed(context):
    """Verify purchase failed"""
    assert context['result'] is False, "Purchase should have failed"


@then("the jettison should succeed")
def jettison_succeeded(context):
    """Verify jettison succeeded"""
    assert context['result'] is True, "Jettison should have succeeded"


@then("the jettison should fail")
def jettison_failed(context):
    """Verify jettison failed"""
    assert context['result'] is False, "Jettison should have failed"


@then(parsers.parse('the ship cargo should have {expected_units:d} units'))
def cargo_units_is(context, expected_units):
    """Verify cargo units"""
    cargo = context['ship'].get_cargo()
    actual_units = cargo['units']
    assert actual_units == expected_units, \
        f"Expected {expected_units} cargo units, got {actual_units}"


@then("the agent credits should increase")
def credits_increased(context):
    """Verify agent credits increased"""
    current_credits = context['mock_api'].agent['credits']
    assert current_credits > context['initial_credits'], \
        f"Credits should increase from {context['initial_credits']}, got {current_credits}"


@then("the agent credits should decrease")
def credits_decreased(context):
    """Verify agent credits decreased"""
    current_credits = context['mock_api'].agent['credits']
    assert current_credits < context['initial_credits'], \
        f"Credits should decrease from {context['initial_credits']}, got {current_credits}"


@then(parsers.parse('the cargo should show {units:d}/{capacity:d} units'))
def cargo_capacity_is(context, units, capacity):
    """Verify cargo capacity"""
    assert context['result'] is not None, "Cargo result should not be None"
    assert context['result']['units'] == units, \
        f"Expected {units} cargo units, got {context['result']['units']}"
    assert context['result']['capacity'] == capacity, \
        f"Expected {capacity} cargo capacity, got {context['result']['capacity']}"


@then(parsers.parse('the cargo should have {expected_items:d} items'))
def cargo_items_count(context, expected_items):
    """Verify number of items in cargo"""
    assert context['result'] is not None, "Cargo result should not be None"
    actual_items = len(context['result']['inventory'])
    assert actual_items == expected_items, \
        f"Expected {expected_items} items in cargo, got {actual_items}"

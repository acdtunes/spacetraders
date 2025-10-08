#!/usr/bin/env python3
"""
Step definitions for ship operations BDD tests
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
scenarios('features/ship_operations.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'ship': None,
        'error': None,
        'result': None,
        'initial_credits': 0,
        'initial_fuel': 0
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
        context['mock_api'].add_market(waypoint, imports=[], exports=["FUEL"])


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
    context['initial_fuel'] = current


@given(parsers.parse('the agent has {credits:d} credits'))
def set_agent_credits(context, credits):
    """Set agent credits"""
    context['mock_api'].agent['credits'] = credits
    context['initial_credits'] = credits


@when("I orbit the ship")
def orbit_ship(context):
    """Orbit the ship"""
    try:
        context['result'] = context['ship'].orbit()
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False


@when("I dock the ship")
def dock_ship(context):
    """Dock the ship"""
    try:
        context['result'] = context['ship'].dock()
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False


@when(parsers.parse('I navigate the ship to "{destination}"'))
def navigate_ship(context, destination):
    """Navigate ship to destination"""
    try:
        # Must be in orbit to navigate
        if context['ship'].get_nav_status() != "IN_ORBIT":
            context['ship'].orbit()

        context['result'] = context['ship'].navigate(destination)
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False


@when("I refuel the ship")
def refuel_ship(context):
    """Refuel the ship"""
    try:
        context['result'] = context['ship'].refuel()
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False


@then(parsers.parse('the ship should be {expected_status}'))
def ship_status_is(context, expected_status):
    """Verify ship navigation status"""
    if expected_status.startswith('at "') and expected_status.endswith('"'):
        expected_location = expected_status[4:-1]
        actual_location = context['ship'].get_location()
        assert actual_location == expected_location, \
            f"Expected {expected_location}, got {actual_location}"
        return

    actual_status = context['ship'].get_nav_status()
    assert actual_status == expected_status, f"Expected {expected_status}, got {actual_status}"


@then(parsers.parse('the ship should be at "{expected_location}"'))
def ship_location_is(context, expected_location):
    """Verify ship location"""
    actual_location = context['ship'].get_location()
    assert actual_location == expected_location, f"Expected {expected_location}, got {actual_location}"


@then("no error should occur")
def no_error(context):
    """Verify no error occurred"""
    assert context['error'] is None, f"Unexpected error: {context['error']}"


@then("the ship fuel should be less than 400")
def fuel_decreased(context):
    """Verify fuel was consumed"""
    fuel = context['ship'].get_fuel()
    assert fuel['current'] < 400, f"Fuel should be less than 400, got {fuel['current']}"


@then("navigation should fail")
def navigation_failed(context):
    """Verify navigation failed"""
    assert context['result'] is False, "Navigation should have failed"


@then(parsers.parse('the ship should have {current:d}/{capacity:d} fuel'))
def fuel_level_is(context, current, capacity):
    """Verify exact fuel level"""
    fuel = context['ship'].get_fuel()
    assert fuel['current'] == current, f"Expected {current} fuel, got {fuel['current']}"
    assert fuel['capacity'] == capacity, f"Expected {capacity} capacity, got {fuel['capacity']}"


@then("the agent credits should decrease")
def credits_decreased(context):
    """Verify agent credits decreased"""
    current_credits = context['mock_api'].agent['credits']
    assert current_credits < context['initial_credits'], \
        f"Credits should decrease from {context['initial_credits']}, got {current_credits}"

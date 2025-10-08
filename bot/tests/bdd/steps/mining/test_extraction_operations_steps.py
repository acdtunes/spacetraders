#!/usr/bin/env python3
"""
Step definitions for resource extraction operations BDD tests
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))
sys.path.insert(0, str(Path(__file__).resolve().parents[4]))

from mock_api import MockAPIClient
from ship_controller import ShipController

# Load all scenarios from the feature file
scenarios('../../features/mining/extraction_operations.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'ship': None,
        'error': None,
        'result': None,
        'extraction_data': None,
        'cooldown_data': None
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


@given(parsers.parse('the ship has {current:d}/{capacity:d} cargo units'))
def set_ship_cargo_empty(context, current, capacity):
    """Set ship cargo to empty with capacity"""
    ship_symbol = context['ship'].ship_symbol
    context['mock_api'].set_ship_cargo(ship_symbol, [], capacity)


@given(parsers.parse('the ship has {current:d}/{capacity:d} cargo units with items {items}'))
def set_ship_cargo_with_items(context, current, capacity, items):
    """Set ship cargo with specific items"""
    ship_symbol = context['ship'].ship_symbol
    item_list = eval(items) if isinstance(items, str) else items
    context['mock_api'].set_ship_cargo(ship_symbol, item_list, capacity)


@given(parsers.parse('the ship has an active cooldown of {seconds:d} seconds'))
def set_ship_cooldown(context, seconds):
    """Set ship cooldown"""
    ship_symbol = context['ship'].ship_symbol
    context['mock_api'].set_ship_cooldown(ship_symbol, seconds)


@when("I extract resources")
def extract_resources(context):
    """Extract resources using ship controller"""
    try:
        context['extraction_data'] = context['ship'].extract()
        context['result'] = True if context['extraction_data'] else False
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['result'] = False
        context['extraction_data'] = None


@when("I check the cooldown status")
def check_cooldown(context):
    """Check ship cooldown status"""
    try:
        ship = context['ship'].get_status()
        context['cooldown_data'] = ship['cooldown']
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)
        context['cooldown_data'] = None


@then("the extraction should succeed")
def extraction_succeeded(context):
    """Verify extraction succeeded"""
    assert context['result'] is True, f"Extraction should succeed, got {context['result']}"
    assert context['extraction_data'] is not None, "Extraction data should not be None"


@then("the extraction should fail")
def extraction_failed(context):
    """Verify extraction failed"""
    assert context['result'] is False, f"Extraction should fail, got {context['result']}"
    assert context['extraction_data'] is None, "Extraction data should be None on failure"


@then(parsers.parse('the extracted resource should be "{expected_symbol}"'))
def extracted_resource_is(context, expected_symbol):
    """Verify extracted resource type"""
    assert context['extraction_data'] is not None, "No extraction data"
    actual_symbol = context['extraction_data']['symbol']
    assert actual_symbol == expected_symbol, \
        f"Expected {expected_symbol}, got {actual_symbol}"


@then("the cargo should contain extracted resources")
def cargo_contains_resources(context):
    """Verify cargo was updated with extracted resources"""
    assert context['extraction_data'] is not None, "No extraction data"
    cargo_units = context['extraction_data']['cargo_units']
    extracted_units = context['extraction_data']['units']
    assert cargo_units >= extracted_units, \
        f"Cargo should contain at least {extracted_units} units, got {cargo_units}"


@then("a cooldown should be active")
def cooldown_active(context):
    """Verify cooldown is active after extraction"""
    assert context['extraction_data'] is not None, "No extraction data"
    cooldown = context['extraction_data']['cooldown']
    assert cooldown > 0, f"Cooldown should be active, got {cooldown}s"


@then("the error should indicate cooldown active")
def error_indicates_cooldown(context):
    """Verify error is due to cooldown"""
    # In our mock, extraction fails (returns None) when cooldown is active
    assert context['result'] is False, "Should fail due to cooldown"


@then("the error should indicate cargo full")
def error_indicates_cargo_full(context):
    """Verify error is due to full cargo"""
    # In our mock, extraction fails (returns None) when cargo is full
    assert context['result'] is False, "Should fail due to full cargo"


@then("the error should indicate invalid location")
def error_indicates_invalid_location(context):
    """Verify error is due to invalid location"""
    # In our mock, extraction fails (returns None) at invalid locations
    assert context['result'] is False, "Should fail at invalid location"


@then("the error should indicate must be in orbit")
def error_indicates_must_orbit(context):
    """Verify error is due to not being in orbit"""
    # In our mock, extraction fails (returns None) when not in orbit
    assert context['result'] is False, "Should fail when not in orbit"


@then(parsers.parse('the cooldown remaining should be {seconds:d} seconds'))
def cooldown_remaining_is(context, seconds):
    """Verify cooldown remaining time"""
    assert context['cooldown_data'] is not None, "No cooldown data"
    remaining = context['cooldown_data']['remainingSeconds']
    assert remaining == seconds, \
        f"Expected {seconds}s remaining, got {remaining}s"


@then(parsers.parse('the extracted units should be between {min_units:d} and {max_units:d}'))
def extracted_units_in_range(context, min_units, max_units):
    """Verify extracted units within expected range"""
    assert context['extraction_data'] is not None, "No extraction data"
    units = context['extraction_data']['units']
    assert min_units <= units <= max_units, \
        f"Expected {min_units}-{max_units} units, got {units}"


@then(parsers.parse('the extracted units should be {expected_units:d}'))
def extracted_units_exact(context, expected_units):
    """Verify exact extracted units"""
    assert context['extraction_data'] is not None, "No extraction data"
    units = context['extraction_data']['units']
    assert units == expected_units, \
        f"Expected {expected_units} units, got {units}"

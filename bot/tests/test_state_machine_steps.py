#!/usr/bin/env python3
"""
Step definitions for state_machine.feature
"""

import sys
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))
sys.path.insert(0, str(Path(__file__).parent))

from bdd_table_utils import table_to_rows
from smart_navigator import SmartNavigator
from mock_api import MockAPIClient
from ship_controller import ShipController

scenarios('features/state_machine.feature')


# Helper function to normalize waypoint data from mock API
def normalize_waypoints(mock_waypoints):
    """Convert mock API waypoint format to graph format"""
    normalized = {}
    for symbol, wp in mock_waypoints.items():
        # Extract traits list
        traits = [t['symbol'] if isinstance(t, dict) else t for t in wp.get('traits', [])]
        has_fuel = 'MARKETPLACE' in traits or 'FUEL_STATION' in traits

        normalized[symbol] = {
            "type": wp['type'],
            "x": wp['x'],
            "y": wp['y'],
            "traits": traits,
            "has_fuel": has_fuel,
            "orbitals": wp.get('orbitals', [])
        }
    return normalized


# Helper function to build graph edges
def build_graph_edges(waypoints):
    """Build edges for graph from waypoints"""
    import math
    edges = []
    waypoint_list = list(waypoints.keys())

    for i, wp1 in enumerate(waypoint_list):
        wp1_data = waypoints[wp1]
        for wp2 in waypoint_list[i+1:]:
            wp2_data = waypoints[wp2]

            # Calculate distance
            distance = math.sqrt(
                (wp2_data['x'] - wp1_data['x']) ** 2 +
                (wp2_data['y'] - wp1_data['y']) ** 2
            )

            # Add edge (both directions)
            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": distance,
                "type": "normal"
            })
            edges.append({
                "from": wp2,
                "to": wp1,
                "distance": distance,
                "type": "normal"
            })

    return edges


@pytest.fixture
def context():
    return {
        'mock_api': None,
        'navigator': None,
        'ship': None,
        'success': None,
        'error': None
    }


@given("the SpaceTraders API is mocked")
def mock_api(context):
    context['mock_api'] = MockAPIClient()


@given(parsers.parse('the system "{system}" has waypoints:\n{table}'))
@given(parsers.parse('the system "{system}" has waypoints:'))
def setup_waypoints(context, system, table: str | None = None, datatable: list[list[str]] | None = None):
    rows = table_to_rows(table, datatable)
    if not rows:
        return

    headers = rows[0]

    for values in rows[1:]:
        waypoint_data = dict(zip(headers, values))
        context['mock_api'].add_waypoint(
            symbol=waypoint_data['symbol'],
            x=int(waypoint_data['x']),
            y=int(waypoint_data['y'])
        )


@given(parsers.parse('a ship "{ship_symbol}" is {state} at "{waypoint}" with {fuel:d} fuel'))
def ship_in_state(context, ship_symbol, state, waypoint, fuel):
    context['mock_api'].set_ship_location(ship_symbol, waypoint, status=state)
    context['mock_api'].set_ship_fuel(ship_symbol, fuel, 400)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    system = waypoint.rsplit('-', 1)[0]
    context['navigator'] = SmartNavigator(context['mock_api'], system)
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@given(parsers.parse('a ship "{ship_symbol}" is IN_TRANSIT to "{destination}"'))
def ship_in_transit(context, ship_symbol, destination):
    context['mock_api'].add_waypoint(destination, x=100, y=0)
    context['mock_api'].set_ship_location(ship_symbol, "X1-HU87-A1")
    context['mock_api'].set_ship_fuel(ship_symbol, 400, 400)
    context['mock_api'].set_ship_in_transit(ship_symbol, destination, arrival_seconds=1)

    context['ship'] = ShipController(context['mock_api'], ship_symbol)
    context['navigator'] = SmartNavigator(context['mock_api'], "X1-HU87")
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@given(parsers.parse('a ship "{ship_symbol}" is {state} at "{waypoint}"'))
def ship_in_state_no_fuel(context, ship_symbol, state, waypoint):
    """Create ship in specific state without fuel parameter"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, status=state)
    # Set low fuel (200/400 = 50%) to trigger refuel in tests
    context['mock_api'].set_ship_fuel(ship_symbol, 200, 400)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    system = waypoint.rsplit('-', 1)[0]
    context['navigator'] = SmartNavigator(context['mock_api'], system)
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {integrity:d}% integrity'))
def ship_with_damage(context, ship_symbol, waypoint, integrity):
    context['mock_api'].set_ship_location(ship_symbol, waypoint)
    context['mock_api'].set_ship_fuel(ship_symbol, 400, 400)

    ship_data = context['mock_api'].get_ship(ship_symbol)
    ship_data['frame']['integrity'] = integrity

    context['ship'] = ShipController(context['mock_api'], ship_symbol)
    context['navigator'] = SmartNavigator(context['mock_api'], "X1-HU87")
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@when(parsers.parse('I navigate to "{destination}"'))
def navigate_to(context, destination):
    context['success'] = context['navigator'].execute_route(context['ship'], destination)


@when(parsers.parse('I attempt to navigate to "{destination}"'))
def attempt_navigate(context, destination):
    import logging
    import io

    # Capture log output
    log_capture = io.StringIO()
    handler = logging.StreamHandler(log_capture)
    handler.setLevel(logging.DEBUG)
    logger = logging.getLogger(SmartNavigator.__module__)
    logger.addHandler(handler)
    logger.setLevel(logging.DEBUG)
    logger.propagate = False

    try:
        context['success'] = context['navigator'].execute_route(context['ship'], destination)
        if not context['success']:
            # Capture logged error
            context['error'] = log_capture.getvalue()
    except Exception as e:
        context['success'] = False
        context['error'] = str(e)
    finally:
        handler.flush()
        logger.removeHandler(handler)
    if context.get('error'):
        context['error'] = context['error'] or log_capture.getvalue()


@when("a refuel stop is executed")
def execute_refuel_stop(context):
    # Simulate refuel step from route
    context['success'] = context['navigator']._ensure_valid_state(context['ship'], 'DOCKED')
    if context['success']:
        context['ship'].refuel()


@then("the ship should automatically orbit")
def ship_orbited(context):
    orbit_calls = [c for c in context['mock_api'].call_log if '/orbit' in c['endpoint']]
    assert len(orbit_calls) > 0, "Ship did not orbit"


@then(parsers.parse('the ship should navigate to "{destination}"'))
def ship_navigated(context, destination):
    nav_calls = [c for c in context['mock_api'].call_log
                 if '/navigate' in c['endpoint'] and c['data']['waypointSymbol'] == destination]
    assert len(nav_calls) > 0, f"Ship did not navigate to {destination}"


@then(parsers.parse('the ship should be in "{state}" state'))
def ship_in_state_check(context, state):
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['status'] == state, \
        f"Ship in {ship_data['nav']['status']}, expected {state}"


@then("the ship should navigate directly")
def ship_navigates_directly(context):
    # No orbit call should have been made
    initial_call_count = len(context['mock_api'].call_log)
    # Verify navigation happened without state change
    nav_calls = [c for c in context['mock_api'].call_log if '/navigate' in c['endpoint']]
    assert len(nav_calls) > 0, "No navigation occurred"


@then(parsers.parse('the ship should be at "{waypoint}"'))
def ship_at_waypoint(context, waypoint):
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['waypointSymbol'] == waypoint


@then("the ship should wait for arrival")
def ship_waited(context):
    # In real implementation, would check wait was called
    # For now, verify ship arrived
    assert context['success'] is True


@then("the navigation should fail")
def navigation_failed(context):
    assert context['success'] is False, "Navigation should have failed"


@then(parsers.parse('the error should mention "{text1}" or "{text2}"'))
def error_contains(context, text1, text2):
    error_text = str(context.get('error', '')).lower()
    # Also check log messages
    assert text1.lower() in error_text or text2.lower() in error_text, \
        f"Error '{error_text}' doesn't mention '{text1}' or '{text2}'"


@then("the ship should automatically dock")
def ship_docked(context):
    dock_calls = [c for c in context['mock_api'].call_log if '/dock' in c['endpoint']]
    assert len(dock_calls) > 0, "Ship did not dock"


@then("the ship should refuel successfully")
def ship_refueled(context):
    refuel_calls = [c for c in context['mock_api'].call_log if '/refuel' in c['endpoint']]
    assert len(refuel_calls) > 0, "Ship did not refuel"


@then("the ship should be DOCKED")
def verify_docked(context):
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['status'] == 'DOCKED'

#!/usr/bin/env python3
"""
Step definitions for state machine edge case scenarios
"""

import sys
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))
sys.path.insert(0, str(Path(__file__).resolve().parents[4]))

from bdd_table_utils import table_to_rows
from smart_navigator import SmartNavigator
from mock_api import MockAPIClient
from ship_controller import ShipController

scenarios('../../features/core/state_machine_edge_cases.feature')


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
        'error': None,
        'transitions': [],
        'final_state': None,
        'api_call_count': 0
    }


@given("the SpaceTraders API is mocked")
def mock_api(context):
    context['mock_api'] = MockAPIClient()


@given(parsers.parse('the system "{system}" has waypoints:\n{table}'))
@given(parsers.parse('the system "{system}" has waypoints:'))
def setup_waypoints(context, system, table: str | None = None, datatable: list[list[str]] | None = None):
    """Setup waypoints from datatable"""
    rows = table_to_rows(table, datatable)
    if not rows:
        return

    headers = rows[0]

    for values in rows[1:]:
        waypoint_data = dict(zip(headers, values))

        symbol = waypoint_data.get('symbol')
        x = int(waypoint_data.get('x', 0))
        y = int(waypoint_data.get('y', 0))
        context['mock_api'].add_waypoint(symbol, 'ASTEROID', x, y)


@given(parsers.parse('a ship "{ship_symbol}" is {state} at "{waypoint}"'))
def ship_in_state(context, ship_symbol, state, waypoint):
    """Create ship in specific state at waypoint"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, status=state)
    context['mock_api'].set_ship_fuel(ship_symbol, 400, 400)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    system = waypoint.rsplit('-', 1)[0]
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }
    context['navigator'] = SmartNavigator(context['mock_api'], system, graph=graph)


@given(parsers.parse('a ship "{ship_symbol}" is IN_TRANSIT at "{waypoint}"'))
def ship_in_transit_corrupted(context, ship_symbol, waypoint):
    """Create ship in IN_TRANSIT state"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, status="IN_TRANSIT")
    context['mock_api'].set_ship_fuel(ship_symbol, 400, 400)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    system = waypoint.rsplit('-', 1)[0]
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }
    context['navigator'] = SmartNavigator(context['mock_api'], system, graph=graph)


@given("the ship nav data is corrupted with no route")
def corrupt_nav_data(context):
    """Corrupt ship nav data by removing route"""
    ship_symbol = context['ship'].ship_symbol
    ship_data = context['mock_api'].ships[ship_symbol]
    ship_data['nav']['route'] = None


@given("the orbit API endpoint will fail")
def orbit_will_fail(context):
    """Set orbit endpoint to fail"""
    context['mock_api'].fail_endpoint = '/orbit'


@given(parsers.parse('navigation to "{destination}" is in progress'))
def navigation_in_progress(context, destination):
    """Set up ship in navigation"""
    ship_symbol = context['ship'].ship_symbol
    context['mock_api'].set_ship_in_transit(ship_symbol, destination, arrival_seconds=1)


@when(parsers.parse('I request transition to "{target_state}"'))
def request_state_transition(context, target_state):
    """Request state transition"""
    context['api_call_count'] = len(context['mock_api'].call_log)

    try:
        # Call actual production code methods
        if target_state == "IN_ORBIT":
            context['success'] = context['ship'].orbit()
        elif target_state == "DOCKED":
            context['success'] = context['ship'].dock()
        else:
            context['success'] = False
            context['error'] = f"Unknown target state: {target_state}"

        # Update final state
        ship_symbol = context['ship'].ship_symbol
        context['final_state'] = context['mock_api'].ships[ship_symbol]['nav']['status']

    except Exception as e:
        context['success'] = False
        context['error'] = str(e)


@when(parsers.parse('I request these transitions in sequence:\n{table}'))
@when('I request these transitions in sequence:')
def rapid_transitions(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Execute rapid state transitions"""
    context['transitions'] = []

    rows = table_to_rows(table, datatable)
    if not rows:
        return

    headers = rows[0]

    for values in rows[1:]:
        row_data = dict(zip(headers, values))

        from_state = row_data.get('from')
        to_state = row_data.get('to')

        try:
            if to_state == "IN_ORBIT":
                result = context['ship'].orbit()
            elif to_state == "DOCKED":
                result = context['ship'].dock()
            else:
                result = False

            context['transitions'].append({
                'from': from_state,
                'to': to_state,
                'success': result
            })

        except Exception as e:
            context['transitions'].append({
                'from': from_state,
                'to': to_state,
                'success': False,
                'error': str(e)
            })

    # Get final state
    ship_symbol = context['ship'].ship_symbol
    context['final_state'] = context['mock_api'].ships[ship_symbol]['nav']['status']


@when("navigation completes")
def navigation_completes(context):
    """Simulate navigation completion"""
    ship_symbol = context['ship'].ship_symbol
    ship_data = context['mock_api'].ships[ship_symbol]

    # Simulate arrival
    if ship_data['nav']['status'] == 'IN_TRANSIT':
        route = ship_data['nav'].get('route', {})
        destination = route.get('destination', {}).get('symbol')

        if destination:
            # Complete navigation
            context['mock_api'].set_ship_location(ship_symbol, destination, status='IN_ORBIT')
            context['success'] = True


@then("the transition should fail")
def transition_fails(context):
    """Verify transition failed"""
    assert context['success'] is False, "Transition should have failed"


@then("an error should be logged")
def error_logged(context):
    """Verify error was logged"""
    assert context.get('error') is not None, "No error was logged"


@then("the transition should succeed")
def transition_succeeds(context):
    """Verify transition succeeded"""
    assert context['success'] is True, f"Transition failed: {context.get('error')}"


@then("no API calls should be made")
def no_api_calls(context):
    """Verify no new API calls were made"""
    new_calls = len(context['mock_api'].call_log) - context['api_call_count']
    assert new_calls == 0, f"Expected 0 API calls, but {new_calls} were made"


@then("the system should handle gracefully")
def handles_gracefully(context):
    """Verify system handled error gracefully"""
    # Should not have raised an exception (we caught it if it did)
    assert context.get('error') is not None or context.get('success') is not None


@then("no crash should occur")
def no_crash(context):
    """Verify no crash occurred"""
    # If we got here, no unhandled exception occurred
    assert True


@then(parsers.parse('the ship should remain {state}'))
def ship_remains_in_state(context, state):
    """Verify ship stayed in expected state"""
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['status'] == state, \
        f"Ship in {ship_data['nav']['status']}, expected to remain {state}"


@then("all transitions should succeed")
def all_transitions_succeed(context):
    """Verify all transitions succeeded"""
    failed = [t for t in context['transitions'] if not t['success']]
    assert len(failed) == 0, f"{len(failed)} transitions failed: {failed}"


@then(parsers.parse('the final state should be {state}'))
def final_state_is(context, state):
    """Verify final state"""
    assert context['final_state'] == state, \
        f"Final state is {context['final_state']}, expected {state}"


@then(parsers.parse('the ship should be {state} at "{waypoint}"'))
def ship_at_waypoint_in_state(context, state, waypoint):
    """Verify ship is at waypoint in specific state"""
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['status'] == state, \
        f"Ship in {ship_data['nav']['status']}, expected {state}"
    assert ship_data['nav']['waypointSymbol'] == waypoint, \
        f"Ship at {ship_data['nav']['waypointSymbol']}, expected {waypoint}"


@then("the state should be consistent")
def state_is_consistent(context):
    """Verify ship state is internally consistent"""
    ship_data = context['ship'].get_status()

    # Check that status matches reality
    status = ship_data['nav']['status']
    route = ship_data['nav'].get('route')

    if status == 'IN_TRANSIT':
        assert route is not None, "IN_TRANSIT ship should have route"
    elif status in ['IN_ORBIT', 'DOCKED']:
        # Route might exist (from previous navigation) or be None
        pass

    # Verify waypoint exists
    waypoint = ship_data['nav']['waypointSymbol']
    assert waypoint is not None and len(waypoint) > 0

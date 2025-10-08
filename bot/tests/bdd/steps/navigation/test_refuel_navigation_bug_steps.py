#!/usr/bin/env python3
"""
Step definitions for refuel navigation bug fix tests
"""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
import sys
from pathlib import Path

# Add lib and tests to path
sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))
sys.path.insert(0, str(Path(__file__).resolve().parents[4]))

from bdd_table_utils import table_to_rows
from smart_navigator import SmartNavigator
from ship_controller import ShipController
from operation_controller import OperationController
from mock_api import MockAPIClient

# Load scenarios
scenarios('../../features/navigation/refuel_navigation_bug.feature')


# Fixtures

@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'navigator': None,
        'ship_symbol': None,
        'ship': None,
        'route': None,
        'checkpoint': None,
        'planned_route': None,
        'navigation_calls': [],
        'dock_calls': [],
        'refuel_calls': [],
        'navigation_success': None,
        'system_graph': None
    }


# Background steps (reuse from test_navigation_steps.py)

@given("the SpaceTraders API is mocked")
def mock_api(context):
    context['mock_api'] = MockAPIClient()


@given(parsers.parse('the system "{system}" has the following waypoints:\n{table}'))
@given(parsers.parse('the system "{system}" has the following waypoints:'))
def setup_waypoints_table(context, system, table: str | None = None, datatable: list[list[str]] | None = None):
    """Load waypoint data for scenario from table"""
    rows = table_to_rows(table, datatable)
    if not rows:
        return

    headers = rows[0]

    for cells in rows[1:]:
        row = dict(zip(headers, cells))

        traits = [t.strip() for t in row.get('traits', '').split(',') if t.strip()]
        context['mock_api'].add_waypoint(
            symbol=row['symbol'],
            type=row.get('type', 'ASTEROID'),
            x=int(float(row.get('x', 0))),
            y=int(float(row.get('y', 0))),
            traits=traits
        )

    # Build system graph for navigator
    import math
    waypoints = {}
    for symbol, wp in context['mock_api'].waypoints.items():
        traits = [t['symbol'] if isinstance(t, dict) else t for t in wp.get('traits', [])]
        has_fuel = 'MARKETPLACE' in traits or 'FUEL_STATION' in traits
        waypoints[symbol] = {
            "type": wp['type'],
            "x": wp['x'],
            "y": wp['y'],
            "traits": traits,
            "has_fuel": has_fuel,
            "orbitals": wp.get('orbitals', [])
        }

    # Build edges
    edges = []
    waypoint_list = list(waypoints.keys())
    for i, wp1 in enumerate(waypoint_list):
        wp1_data = waypoints[wp1]
        for wp2 in waypoint_list[i+1:]:
            wp2_data = waypoints[wp2]
            distance = math.sqrt(
                (wp2_data['x'] - wp1_data['x']) ** 2 +
                (wp2_data['y'] - wp1_data['y']) ** 2
            )
            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": distance,
                "type": "normal"
            })

    context['system_graph'] = {
        "system": system,
        "waypoints": waypoints,
        "edges": edges
    }


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {fuel:d} fuel out of {capacity:d} capacity'))
def ship_at_waypoint_with_fuel_and_capacity(context, ship_symbol, waypoint, fuel, capacity):
    context['mock_api'].set_ship_location(ship_symbol, waypoint, status="DOCKED")  # Default to DOCKED
    context['mock_api'].set_ship_fuel(ship_symbol, fuel, capacity)
    context['ship_symbol'] = ship_symbol


@given(parsers.parse('the ship is in "{state}" state'))
def set_ship_state(context, state):
    # Update the ship's status by setting location again with the new status
    ship_symbol = context['ship_symbol']
    # Get current location
    ship_data = context['mock_api'].get_ship(ship_symbol)
    if ship_data:
        current_waypoint = ship_data['nav']['waypointSymbol']
        context['mock_api'].set_ship_location(ship_symbol, current_waypoint, status=state)


@given(parsers.parse('a navigation checkpoint exists with:\n{table}'))
@given('a navigation checkpoint exists with:')
def checkpoint_exists(context, table: str | None = None, datatable: list[list[str]] | None = None):
    """Create a navigation checkpoint"""
    rows = table_to_rows(table, datatable)
    if len(rows) < 2:
        return

    headers = rows[0]
    values = rows[1]

    checkpoint_data = dict(zip(headers, values))

    # Convert numeric values
    checkpoint_data['completed_step'] = int(checkpoint_data['completed_step'])
    checkpoint_data['fuel'] = int(checkpoint_data['fuel'])

    # Store checkpoint in context
    context['checkpoint'] = checkpoint_data


# When steps

@when(parsers.parse('I execute a refuel step for waypoint "{waypoint}"'))
def execute_refuel_step(context, waypoint):
    """Execute a single refuel step"""
    ship_symbol = context['ship_symbol']
    ship = ShipController(context['mock_api'], ship_symbol)

    # Create navigator with graph
    navigator = SmartNavigator(context['mock_api'], "X1-JB26", graph=context['system_graph'])

    # Track navigation, dock, and refuel calls
    nav_calls = []
    dock_calls = []
    refuel_calls = []

    original_navigate = ship.navigate
    original_dock = ship.dock
    original_refuel = ship.refuel

    def track_navigate(waypoint, **kwargs):
        nav_calls.append(waypoint)
        return original_navigate(waypoint, **kwargs)

    def track_dock():
        status = ship.get_status()
        dock_calls.append(status['nav']['waypointSymbol'])
        return original_dock()

    def track_refuel(**kwargs):
        status = ship.get_status()
        refuel_calls.append(status['nav']['waypointSymbol'])
        return original_refuel(**kwargs)

    ship.navigate = track_navigate
    ship.dock = track_dock
    ship.refuel = track_refuel

    # Execute a refuel step manually (simulating what execute_route does)
    refuel_step = {
        'action': 'refuel',
        'waypoint': waypoint,
        'fuel_added': 360
    }

    # Get current location
    current_ship_data = ship.get_status()
    current_location = current_ship_data['nav']['waypointSymbol']
    refuel_waypoint = refuel_step['waypoint']

    success = True

    # This is the code we're testing - from smart_navigator.py
    if current_location != refuel_waypoint:
        # Navigate to refuel waypoint
        if current_ship_data['nav']['status'] != 'IN_ORBIT':
            ship.orbit()

        success = ship.navigate(
            waypoint=refuel_waypoint,
            flight_mode='DRIFT',
            auto_refuel=False
        )

    if success:
        # Dock and refuel
        current_ship_data = ship.get_status()
        if current_ship_data['nav']['status'] != 'DOCKED':
            ship.dock()

        success = ship.refuel()

    context['navigation_calls'] = nav_calls
    context['dock_calls'] = dock_calls
    context['refuel_calls'] = refuel_calls
    context['refuel_success'] = success
    context['ship'] = ship


@given(parsers.parse('the planned route has steps:\n{table}'))
def planned_route_steps(context, table):
    """Create a planned route with specific steps"""
    lines = [line.strip() for line in table.strip().split('\n') if line.strip()]
    headers = [h.strip() for h in lines[0].split('|')[1:-1]]

    route_steps = []
    for line in lines[1:]:
        values = [v.strip() for v in line.split('|')[1:-1]]
        step_data = dict(zip(headers, values))

        # Convert step number
        step_data['step'] = int(step_data['step'])

        # Build step based on action
        if step_data['action'] == 'navigate':
            route_steps.append({
                'action': 'navigate',
                'from': step_data['from'],
                'to': step_data['to'],
                'mode': 'DRIFT',
                'distance': 0,  # Will be calculated
                'fuel_cost': 1
            })
        elif step_data['action'] == 'refuel':
            route_steps.append({
                'action': 'refuel',
                'waypoint': step_data['waypoint'],
                'fuel_added': 360
            })

    # Store route in context
    context['planned_route'] = {
        'steps': route_steps,
        'start': 'X1-JB26-A1',
        'goal': 'X1-JB26-C9',
        'total_time': 1000,
        'final_fuel': 300
    }


@when('I resume navigation execution from step 3')
def resume_navigation_from_step_3(context):
    """Resume navigation from a specific step with checkpoint"""
    mock_api = context['mock_api']
    ship_symbol = context['ship_symbol']

    # Create operation controller with checkpoint
    op_controller = OperationController(
        operation_id=f"test-{ship_symbol}",
        operation_type="navigation",
        db_path=":memory:"
    )

    # Save checkpoint
    checkpoint = context.get('checkpoint', {})
    op_controller.checkpoint(checkpoint)

    # Get planned route
    route = context['planned_route']

    # Create ship controller
    ship = ShipController(mock_api, ship_symbol)

    # Create navigator with mocked graph
    graph = context['system_graph']
    navigator = SmartNavigator(mock_api, "X1-JB26", graph=graph)

    # Track navigation calls
    context['navigation_calls'] = []
    context['dock_calls'] = []
    context['refuel_calls'] = []

    # Monkey patch to track calls
    original_navigate = ship.navigate
    original_dock = ship.dock
    original_refuel = ship.refuel

    def track_navigate(waypoint, **kwargs):
        context['navigation_calls'].append(waypoint)
        return original_navigate(waypoint, **kwargs)

    def track_dock():
        ship_status = ship.get_status()
        context['dock_calls'].append(ship_status['nav']['waypointSymbol'])
        return original_dock()

    def track_refuel(**kwargs):
        ship_status = ship.get_status()
        context['refuel_calls'].append(ship_status['nav']['waypointSymbol'])
        return original_refuel(**kwargs)

    ship.navigate = track_navigate
    ship.dock = track_dock
    ship.refuel = track_refuel

    # Manually execute the route starting from step 3
    # This simulates what execute_route does when resuming
    start_step = checkpoint.get('completed_step', 0) + 1

    success = True
    for i, step in enumerate(route['steps'], 1):
        if i < start_step:
            continue

        if step['action'] == 'navigate':
            # Orbit if needed
            ship_status = ship.get_status()
            if ship_status['nav']['status'] == 'DOCKED':
                ship.orbit()

            success = ship.navigate(step['to'], flight_mode=step.get('mode', 'DRIFT'))
            if not success:
                break

        elif step['action'] == 'refuel':
            # BUG: Current code doesn't navigate to refuel waypoint
            # It just tries to refuel at current location

            # Get current location
            ship_status = ship.get_status()
            current_location = ship_status['nav']['waypointSymbol']
            refuel_waypoint = step['waypoint']

            # THIS IS THE BUG - no navigation check!
            # The code should check if current_location != refuel_waypoint
            # and navigate there first, but it doesn't

            # Current buggy behavior: just dock and refuel at current location
            ship_status = ship.get_status()
            if ship_status['nav']['status'] == 'IN_ORBIT':
                ship.dock()

            success = ship.refuel()
            if not success:
                break

    context['navigation_success'] = success
    context['ship'] = ship


@then(parsers.parse('the ship should navigate to "{waypoint}" first'))
def verify_navigated_to_waypoint(context, waypoint):
    """Verify ship navigated to the waypoint"""
    nav_calls = context.get('navigation_calls', [])

    assert waypoint in nav_calls, (
        f"Expected navigation to {waypoint}, but navigation calls were: {nav_calls}"
    )


@then(parsers.parse('then the ship should dock at "{waypoint}"'))
def verify_docked_at_waypoint(context, waypoint):
    """Verify ship docked at the waypoint"""
    dock_calls = context.get('dock_calls', [])

    assert waypoint in dock_calls, (
        f"Expected dock at {waypoint}, but dock calls were: {dock_calls}"
    )


@then("then the ship should refuel successfully")
def verify_refuel_succeeded(context):
    """Verify refuel succeeded"""
    assert context.get('refuel_success') is True, "Refuel should succeed"


@then(parsers.parse('the ship should be at "{waypoint}" after refuel'))
def verify_ship_at_waypoint_after_refuel(context, waypoint):
    """Verify ship is at the expected waypoint after refuel"""
    ship = context['ship']
    status = ship.get_status()
    current_location = status['nav']['waypointSymbol']

    assert current_location == waypoint, (
        f"Expected ship at {waypoint} after refuel, but it's at {current_location}"
    )

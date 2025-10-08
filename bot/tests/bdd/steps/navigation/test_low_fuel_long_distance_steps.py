#!/usr/bin/env python3
"""
Step definitions for low_fuel_long_distance.feature
Tests for bug where SmartNavigator doesn't plan refuel stops correctly
"""

import sys
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
import pytest
import math

# Add project source and tests to path
sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'src'))
sys.path.insert(0, str(Path(__file__).resolve().parents[4]))

import spacetraders_bot  # noqa: F401 - register compat aliases
from bdd_table_utils import table_to_rows
from spacetraders_bot.core.smart_navigator import SmartNavigator
from mock_api import MockAPIClient
from ship_controller import ShipController

# Load scenarios
scenarios('../../features/navigation/low_fuel_long_distance.feature')


# Helper functions
def normalize_waypoints(mock_waypoints):
    """Convert mock API waypoint format to graph format"""
    normalized = {}
    for symbol, wp in mock_waypoints.items():
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


def build_graph_edges(waypoints):
    """Build edges for graph from waypoints"""
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
            edges.append({
                "from": wp2,
                "to": wp1,
                "distance": distance,
                "type": "normal"
            })

    return edges


# Fixtures

@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'navigator': None,
        'ship': None,
        'route': None,
        'valid': None,
        'reason': None,
        'success': None
    }


# Background steps

@given("the SpaceTraders API is mocked")
def mock_api(context):
    context['mock_api'] = MockAPIClient()


@given(parsers.parse('the system "{system}" has the following waypoints:\n{table}'))
@given(parsers.parse('the system "{system}" has the following waypoints:'))
def setup_waypoints_table(context, system, table: str | None = None, datatable: list[list[str]] | None = None):
    """Load waypoints for the system from a Gherkin table"""
    rows = table_to_rows(table, datatable)
    if not rows:
        return

    headers = rows[0]

    for cells in rows[1:]:
        row = dict(zip(headers, cells))

        symbol = row['symbol']
        wp_type = row.get('type', 'PLANET')
        x = int(float(row.get('x', 0)))
        y = int(float(row.get('y', 0)))
        traits_field = row.get('traits', '')
        traits = [t.strip() for t in traits_field.split(',') if t.strip()]

        context['mock_api'].add_waypoint(symbol, wp_type, x, y, traits)


@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" with {fuel:d} fuel out of {capacity:d} capacity'))
def ship_with_fuel_capacity(context, ship_symbol, waypoint, fuel, capacity):
    context['mock_api'].set_ship_location(ship_symbol, waypoint)
    context['mock_api'].set_ship_fuel(ship_symbol, fuel, capacity)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    system = waypoint.rsplit('-', 1)[0]
    context['navigator'] = SmartNavigator(context['mock_api'], system)
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@given(parsers.parse('the ship is in "{state}" state'))
def set_ship_state(context, state):
    """Set ship state"""
    ship_data = context['ship'].get_status()
    if ship_data:
        ship_data['nav']['status'] = state


# When steps

@when(parsers.parse('I navigate to "{destination}"'))
def navigate_to(context, destination):
    context['success'] = context['navigator'].execute_route(
        context['ship'],
        destination,
        prefer_cruise=True
    )


@when(parsers.parse('I plan a route to "{destination}" with cruise preferred'))
def plan_route_cruise(context, destination):
    ship_data = context['ship'].get_status()
    context['route'] = context['navigator'].plan_route(ship_data, destination, prefer_cruise=True)


# Then steps

@then("the navigation should succeed")
def navigation_succeeds(context):
    assert context['success'] is True, "Navigation failed"


@then(parsers.parse('the ship should be at "{waypoint}"'))
def ship_at_location(context, waypoint):
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['waypointSymbol'] == waypoint, \
        f"Ship at {ship_data['nav']['waypointSymbol']}, expected {waypoint}"


@then(parsers.parse('the route should have a refuel stop at "{waypoint}"'))
def route_has_refuel_stop(context, waypoint):
    """Verify route includes refuel stop at specified waypoint"""
    assert context['route'] is not None, "No route planned"

    refuel_steps = [s for s in context['route']['steps'] if s['action'] == 'refuel']
    assert len(refuel_steps) > 0, f"Route should include refuel stop(s), but has none. Steps: {context['route']['steps']}"

    refuel_waypoints = [s['waypoint'] for s in refuel_steps]
    assert waypoint in refuel_waypoints, \
        f"Route should refuel at {waypoint}, but refuel stops are at: {refuel_waypoints}"


@then("the route should have navigation steps")
def route_has_navigation_steps(context):
    """Verify route has navigation steps"""
    assert context['route'] is not None, "No route planned"

    nav_steps = [s for s in context['route']['steps'] if s['action'] == 'navigate']
    assert len(nav_steps) > 0, f"Route should have navigation steps, but has: {context['route']['steps']}"

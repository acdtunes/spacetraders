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

# Add lib and tests to path
sys.path.insert(0, str(Path(__file__).parent.parent / "src/spacetraders_bot/core"))
sys.path.insert(0, str(Path(__file__).parent))

from smart_navigator import SmartNavigator
from mock_api import MockAPIClient
from ship_controller import ShipController

# Load scenarios
scenarios('features/low_fuel_long_distance.feature')


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
def setup_waypoints_table(context, system, table):
    """Parse waypoint table from Gherkin"""
    lines = table.strip().split('\n')
    headers = [h.strip() for h in lines[0].split('|')[1:-1]]

    for line in lines[1:]:
        values = [v.strip() for v in line.split('|')[1:-1]]
        waypoint_data = dict(zip(headers, values))

        traits = waypoint_data.get('traits', '').split(',') if waypoint_data.get('traits') else []
        traits = [t.strip() for t in traits if t.strip()]

        context['mock_api'].add_waypoint(
            symbol=waypoint_data['symbol'],
            type=waypoint_data['type'],
            x=int(waypoint_data['x']),
            y=int(waypoint_data['y']),
            traits=traits
        )


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

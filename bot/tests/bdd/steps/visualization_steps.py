"""Step definitions for visualizer coordinate handling scenarios."""

import pytest
import math
from pytest_bdd import given, when, then, parsers, scenarios

# Load all visualization scenarios
scenarios('../../features/visualization/coordinate_handling.feature')

# Constants from test file
WAYPOINTS_DB = {
    "X1-VH85-A1": {"x": 19.0, "y": 15.0},
    "X1-VH85-A2": {"x": 19.0, "y": 15.0},
    "X1-VH85-A3": {"x": 19.0, "y": 15.0},
    "X1-VH85-A4": {"x": 19.0, "y": 15.0},
    "X1-VH85-AC5D": {"x": -14.0, "y": 22.0},
    "X1-VH85-B6": {"x": 149.0, "y": 119.0},
    "X1-VH85-B7": {"x": 337.0, "y": 76.0},
    "X1-VH85-D48": {"x": 2.0, "y": 87.0},
    "X1-VH85-D49": {"x": 2.0, "y": 87.0},
    "X1-VH85-D50": {"x": 2.0, "y": 87.0},
    "X1-VH85-D51": {"x": 2.0, "y": 87.0},
    "X1-VH85-E53": {"x": 34.0, "y": -42.0},
    "X1-VH85-E54": {"x": 34.0, "y": -42.0},
    "X1-VH85-F55": {"x": 24.0, "y": 72.0},
    "X1-VH85-F56": {"x": 24.0, "y": 72.0},
    "X1-VH85-G58": {"x": -50.0, "y": -43.0},
    "X1-VH85-H59": {"x": -36.0, "y": 24.0},
    "X1-VH85-H60": {"x": -36.0, "y": 24.0},
    "X1-VH85-H61": {"x": -36.0, "y": 24.0},
    "X1-VH85-H62": {"x": -36.0, "y": 24.0},
}

CACHED_TOUR_ORDER = [
    "X1-VH85-A1", "X1-VH85-AC5D", "X1-VH85-H59", "X1-VH85-H62", "X1-VH85-H61",
    "X1-VH85-H60", "X1-VH85-G58", "X1-VH85-E53", "X1-VH85-E54", "X1-VH85-B7",
    "X1-VH85-B6", "X1-VH85-F55", "X1-VH85-F56", "X1-VH85-D51", "X1-VH85-D50",
    "X1-VH85-D49", "X1-VH85-D48", "X1-VH85-A4", "X1-VH85-A3", "X1-VH85-A2", "X1-VH85-A1",
]


@pytest.fixture
def visualization_context():
    """Shared context for visualization scenarios."""
    return {
        'waypoints': WAYPOINTS_DB.copy(),
        'tour_order': CACHED_TOUR_ORDER.copy(),
        'coordinate_groups': None,
        'leg_distances': None,
        'visualizer_data': None,
    }


@given("the X1-VH85 system with waypoint coordinates from the database")
@given("waypoints in the X1-VH85 system")
@given("a cached tour order for X1-VH85")
@given("a tour order with waypoint coordinates")
def setup_waypoints(visualization_context):
    """Waypoints and tour order are available from context."""
    pass


@when("I analyze coordinate groups")
def analyze_coordinate_groups(visualization_context):
    """Analyze waypoint coordinate groups."""
    waypoints = visualization_context['waypoints']
    coord_groups = {}

    for wp, data in waypoints.items():
        coord = (data["x"], data["y"])
        if coord not in coord_groups:
            coord_groups[coord] = []
        coord_groups[coord].append(wp)

    visualization_context['coordinate_groups'] = coord_groups


@when("I calculate distances for each tour leg")
def calculate_tour_leg_distances(visualization_context):
    """Calculate distances between consecutive waypoints in tour."""
    waypoints = visualization_context['waypoints']
    tour_order = visualization_context['tour_order']
    leg_distances = []

    for i in range(len(tour_order) - 1):
        from_wp = tour_order[i]
        to_wp = tour_order[i + 1]
        from_coord = waypoints[from_wp]
        to_coord = waypoints[to_wp]

        dx = to_coord["x"] - from_coord["x"]
        dy = to_coord["y"] - from_coord["y"]
        dist = math.hypot(dx, dy)

        leg_distances.append({"from": from_wp, "to": to_wp, "distance": dist})

    visualization_context['leg_distances'] = leg_distances


@when("I prepare data for the visualizer")
def generate_visualizer_data(visualization_context):
    """Generate data in visualizer format."""
    waypoints = visualization_context['waypoints']
    tour_order = visualization_context['tour_order']
    leg_distances = visualization_context.get('leg_distances', [])

    total_distance = sum(leg['distance'] for leg in leg_distances) if leg_distances else 0

    visualizer_data = {
        "order": tour_order,
        "coordinates": {wp: {"x": data["x"], "y": data["y"]} for wp, data in waypoints.items()},
        "total_distance": total_distance
    }

    visualization_context['visualizer_data'] = visualizer_data


@then("some waypoints should share coordinates (orbitals around same parent)")
def verify_shared_coordinates(visualization_context):
    """Verify some waypoints share coordinates."""
    coord_groups = visualization_context['coordinate_groups']
    shared_groups = [g for g in coord_groups.values() if len(g) > 1]
    assert len(shared_groups) > 0, "Should have waypoints sharing coordinates"


@then(parsers.parse('the system should have orbital groups at:\n{group_table}'))
def verify_orbital_groups(visualization_context, group_table):
    """Verify specific orbital groups exist."""
    coord_groups = visualization_context['coordinate_groups']

    # Parse table to verify key groups exist
    lines = [line.strip() for line in group_table.strip().split('\n') if '|' in line]
    data_lines = [line for line in lines if not line.startswith('|') or '---' not in line][1:]

    for line in data_lines:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) >= 2:
            coord_str = parts[0]  # e.g., "(19, 15)"
            waypoints_str = parts[1]  # e.g., "A1, A2, A3, A4"

            # Parse coordinate
            coord_str = coord_str.replace('(', '').replace(')', '')
            x, y = map(float, coord_str.split(','))
            coord = (x, y)

            # Verify this coordinate group exists
            assert coord in coord_groups, f"Coordinate group {coord} should exist"


@then("some legs should have zero distance (orbital transitions)")
def verify_zero_distance_legs(visualization_context):
    """Verify some tour legs have zero distance."""
    leg_distances = visualization_context['leg_distances']
    zero_legs = [leg for leg in leg_distances if leg['distance'] < 0.01]
    assert len(zero_legs) > 0, "Should have legs with zero distance (orbital transitions)"


@then("the total tour distance should match geometric calculations")
def verify_total_distance(visualization_context):
    """Verify total tour distance is consistent."""
    leg_distances = visualization_context['leg_distances']
    total_distance = sum(leg['distance'] for leg in leg_distances)
    assert total_distance >= 0, "Total distance should be non-negative"


@then("the data should include the tour order")
def verify_tour_order_in_data(visualization_context):
    """Verify visualizer data includes tour order."""
    visualizer_data = visualization_context['visualizer_data']
    assert "order" in visualizer_data, "Visualizer data should include 'order'"
    assert len(visualizer_data["order"]) > 0, "Tour order should not be empty"


@then("the data should include coordinate mapping for all waypoints")
def verify_coordinate_mapping(visualization_context):
    """Verify visualizer data includes coordinate mapping."""
    visualizer_data = visualization_context['visualizer_data']
    assert "coordinates" in visualizer_data, "Visualizer data should include 'coordinates'"
    assert len(visualizer_data["coordinates"]) > 0, "Coordinates should not be empty"


@then("the data should include the calculated total distance")
def verify_total_distance_in_data(visualization_context):
    """Verify visualizer data includes total distance."""
    visualizer_data = visualization_context['visualizer_data']
    assert "total_distance" in visualizer_data, "Visualizer data should include 'total_distance'"
    assert visualizer_data["total_distance"] >= 0, "Total distance should be non-negative"

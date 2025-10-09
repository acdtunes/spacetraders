#!/usr/bin/env python3
"""
Test for Bug #1 fix: Scout-markets with --markets-list should start from
first assigned market (partition centroid), not ship's current location.

This ensures disjoint tours when multiple scouts are coordinated.
"""

import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.core.routing import TourOptimizer


@pytest.fixture
def mock_graph():
    """Create a mock system graph"""
    waypoints = {
        'X1-TEST-A2': {'x': 100, 'y': 100, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-B6': {'x': 300, 'y': 200, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-F50': {'x': 500, 'y': 400, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-G53': {'x': 600, 'y': 450, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
        'X1-TEST-H55': {'x': 700, 'y': 500, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []},
    }

    # Generate fully connected edges
    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i+1:]:
            wp1_data = waypoints[wp1]
            wp2_data = waypoints[wp2]
            distance = ((wp2_data['x'] - wp1_data['x'])**2 + (wp2_data['y'] - wp1_data['y'])**2)**0.5
            edges.append({
                'from': wp1,
                'to': wp2,
                'distance': round(distance, 2),
                'type': 'normal'
            })

    return {
        'system': 'X1-TEST',
        'waypoints': waypoints,
        'edges': edges
    }


@pytest.fixture
def mock_ship_data_at_a2():
    """Mock ship data - ship starts at A2 (common starting location)"""
    return {
        'symbol': 'SHIP-1',
        'nav': {
            'waypointSymbol': 'X1-TEST-A2',
            'systemSymbol': 'X1-TEST'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'engine': {
            'speed': 9
        }
    }


def test_tour_starts_from_first_assigned_market(mock_graph, mock_ship_data_at_a2):
    """
    Test that when provided with specific markets list, tour starts from
    first assigned market (partition centroid), NOT ship's current location.

    SCENARIO:
    - Ship is at A2
    - Assigned markets (partition): [B6, F50]  # B6 is centroid
    - Expected tour: B6 → F50 → B6
    - Should NOT visit A2 (assigned to different ship)
    """
    optimizer = TourOptimizer(mock_graph, mock_ship_data_at_a2)

    # Simulate coordinator passing --markets-list B6,F50
    assigned_markets = ['X1-TEST-B6', 'X1-TEST-F50']

    # FIX: Scout-markets should start from first assigned market (B6)
    # NOT from ship's current location (A2)
    tour_start = assigned_markets[0]  # B6 (partition centroid)

    # Plan tour starting from partition centroid with return_to_start=True
    tour = optimizer.solve_nearest_neighbor(
        start=tour_start,           # Start from B6 (first assigned market)
        stops=assigned_markets,      # Visit all assigned markets
        current_fuel=400,
        return_to_start=True         # Return to B6, not A2
    )

    assert tour is not None, "Tour should be generated"

    # Extract all waypoints visited
    visited_waypoints = set()
    visited_waypoints.add(tour_start)

    for leg in tour['legs']:
        if 'goal' in leg:
            visited_waypoints.add(leg['goal'])

    print(f"\nAssigned markets: {assigned_markets}")
    print(f"Tour visits: {visited_waypoints}")
    print(f"Ship starts at: X1-TEST-A2")

    # CRITICAL CHECK: Tour should ONLY visit assigned markets
    assigned_set = set(assigned_markets)
    assert visited_waypoints == assigned_set, \
        f"Tour should only visit assigned markets {assigned_set}, but visited: {visited_waypoints}"

    # CRITICAL CHECK: Tour should NOT visit A2 (ship's current location)
    assert 'X1-TEST-A2' not in visited_waypoints, \
        "Tour should NOT visit A2 (ship's starting location) - it's assigned to another ship!"

    # CRITICAL CHECK: Tour should return to first assigned market (B6)
    if tour.get('return_to_start'):
        last_leg = tour['legs'][-1]
        last_dest = last_leg.get('goal')
        assert last_dest == tour_start, \
            f"Tour should return to first assigned market {tour_start}, but returned to {last_dest}"


def test_disjoint_tours_with_multiple_ships(mock_graph, mock_ship_data_at_a2):
    """
    Test that multiple ships all starting at A2 can have truly disjoint tours
    when they're assigned different market partitions.

    SCENARIO:
    - SHIP-1 at A2, assigned [B6, F50]
    - SHIP-2 at A2, assigned [G53, H55]
    - Both should tour their assigned markets WITHOUT visiting A2
    """
    # Ship 1 partition
    ship1_assigned = ['X1-TEST-B6', 'X1-TEST-F50']
    ship1_start = ship1_assigned[0]

    optimizer1 = TourOptimizer(mock_graph, mock_ship_data_at_a2)
    tour1 = optimizer1.solve_nearest_neighbor(
        start=ship1_start,
        stops=ship1_assigned,
        current_fuel=400,
        return_to_start=True
    )

    # Ship 2 partition (update ship data for different symbol)
    ship2_data = mock_ship_data_at_a2.copy()
    ship2_data['symbol'] = 'SHIP-2'
    ship2_data['nav'] = ship2_data['nav'].copy()
    # Ship 2 also starts at A2

    ship2_assigned = ['X1-TEST-G53', 'X1-TEST-H55']
    ship2_start = ship2_assigned[0]

    optimizer2 = TourOptimizer(mock_graph, ship2_data)
    tour2 = optimizer2.solve_nearest_neighbor(
        start=ship2_start,
        stops=ship2_assigned,
        current_fuel=400,
        return_to_start=True
    )

    assert tour1 is not None and tour2 is not None, "Both tours should be generated"

    # Extract visited waypoints for each ship
    def extract_visited(tour, start):
        visited = {start}
        for leg in tour['legs']:
            if 'goal' in leg:
                visited.add(leg['goal'])
        return visited

    ship1_visited = extract_visited(tour1, ship1_start)
    ship2_visited = extract_visited(tour2, ship2_start)

    print(f"\nShip 1 assigned: {ship1_assigned}")
    print(f"Ship 1 visits: {ship1_visited}")
    print(f"\nShip 2 assigned: {ship2_assigned}")
    print(f"Ship 2 visits: {ship2_visited}")

    # CRITICAL CHECK: Tours should be completely disjoint
    overlap = ship1_visited & ship2_visited
    assert len(overlap) == 0, \
        f"Tours should be disjoint, but found overlap: {overlap}"

    # CRITICAL CHECK: Neither tour should visit A2
    assert 'X1-TEST-A2' not in ship1_visited, "Ship 1 should not visit A2"
    assert 'X1-TEST-A2' not in ship2_visited, "Ship 2 should not visit A2"


def test_tour_plan_integration_with_markets_list(mock_graph, mock_ship_data_at_a2):
    """
    Test TourOptimizer.plan_tour() works correctly with specific markets list.
    This is what scout-markets actually uses.
    """
    optimizer = TourOptimizer(mock_graph, mock_ship_data_at_a2)

    assigned_markets = ['X1-TEST-B6', 'X1-TEST-F50']
    tour_start = assigned_markets[0]

    # Use plan_tour (higher-level API used by scout-markets)
    tour = optimizer.plan_tour(
        start=tour_start,
        stops=assigned_markets,
        current_fuel=400,
        return_to_start=True,
        algorithm='greedy',
        use_cache=False  # Disable cache for test predictability
    )

    assert tour is not None, "Tour should be generated"

    # Extract visited waypoints
    visited = {tour_start}
    for leg in tour['legs']:
        if 'goal' in leg:
            visited.add(leg['goal'])

    # Verify disjoint property
    assigned_set = set(assigned_markets)
    assert visited == assigned_set, \
        f"Tour should only visit assigned markets {assigned_set}, visited: {visited}"

    # Verify no visit to A2
    assert 'X1-TEST-A2' not in visited, "Tour should not visit A2 (starting location)"

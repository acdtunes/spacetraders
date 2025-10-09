#!/usr/bin/env python3
"""
Test for fixed-route trading coordinate bug

BUG: create_fixed_route() passes waypoint symbols (strings) to calculate_distance()
     which expects coordinate dictionaries with 'x' and 'y' keys.

Error: TypeError: string indices must be integers at core/utils.py:23
"""

import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.operations.multileg_trader import create_fixed_route


def test_create_fixed_route_coordinate_lookup():
    """
    GIVEN a fixed trading route with waypoint symbols
    WHEN create_fixed_route() is called with valid parameters
    THEN it should look up waypoint coordinates from database
    AND calculate distances using coordinate dictionaries
    AND not crash with TypeError
    """
    # Mock API
    mock_api = Mock()

    # Mock database with waypoint coordinates
    mock_db = Mock()
    mock_conn = MagicMock()

    # Setup database to return market data
    buy_market_data = [{
        'good_symbol': 'MEDICINE',
        'sell_price': 1000,  # What we pay
        'purchase_price': None,
        'trade_volume': 50
    }]

    sell_market_data = [{
        'good_symbol': 'MEDICINE',
        'sell_price': None,
        'purchase_price': 1500,  # What we receive
        'trade_volume': 50
    }]

    # Setup context manager for database transaction
    mock_db.transaction.return_value.__enter__ = Mock(return_value=mock_conn)
    mock_db.transaction.return_value.__exit__ = Mock(return_value=False)

    # Setup context manager for database connection (for coordinate lookup)
    mock_db.connection.return_value.__enter__ = Mock(return_value=mock_conn)
    mock_db.connection.return_value.__exit__ = Mock(return_value=False)

    # Mock get_market_data to return the market data
    mock_db.get_market_data = Mock(side_effect=[buy_market_data, sell_market_data])

    # Setup database connection to return waypoint coordinates when queried
    # We need to return different coordinates for each query in sequence
    coordinate_responses = [
        (100, 200),  # Current waypoint X1-GH18-J62
        (150, 250),  # Buy waypoint X1-GH18-D45
        (100, 200),  # Sell waypoint X1-GH18-J62
    ]

    mock_cursor = Mock()
    mock_conn.cursor.return_value = mock_cursor
    mock_cursor.fetchone.side_effect = coordinate_responses

    # Test parameters
    player_id = 6
    current_waypoint = "X1-GH18-J62"
    buy_waypoint = "X1-GH18-D45"
    sell_waypoint = "X1-GH18-J62"
    good = "MEDICINE"
    cargo_capacity = 40
    starting_credits = 100000
    ship_speed = 10
    fuel_capacity = 400
    current_fuel = 350

    # This should NOT crash with TypeError
    route = create_fixed_route(
        mock_api, mock_db, player_id,
        current_waypoint,
        buy_waypoint,
        sell_waypoint,
        good,
        cargo_capacity,
        starting_credits,
        ship_speed,
        fuel_capacity,
        current_fuel
    )

    # Verify route was created successfully
    assert route is not None, "Route should be created successfully"
    assert len(route.segments) >= 1, "Route should have at least one segment"
    assert route.total_distance > 0, "Route should have calculated distance"


def test_calculate_distance_with_waypoint_symbols_fails():
    """
    GIVEN waypoint symbols (strings)
    WHEN calculate_distance() is called with strings instead of coordinate dicts
    THEN it should raise TypeError

    This test validates the bug exists before the fix.
    """
    from spacetraders_bot.core.utils import calculate_distance

    # These are strings, not coordinate dictionaries
    waypoint1 = "X1-GH18-D45"
    waypoint2 = "X1-GH18-J62"

    # This should raise TypeError: string indices must be integers
    with pytest.raises(TypeError, match="string indices must be integers"):
        calculate_distance(waypoint1, waypoint2)


def test_calculate_distance_with_coordinate_dicts_succeeds():
    """
    GIVEN coordinate dictionaries with 'x' and 'y' keys
    WHEN calculate_distance() is called
    THEN it should return the Euclidean distance
    """
    from spacetraders_bot.core.utils import calculate_distance

    coord1 = {'x': 100, 'y': 200}
    coord2 = {'x': 150, 'y': 250}

    distance = calculate_distance(coord1, coord2)

    # Distance = sqrt((150-100)^2 + (250-200)^2) = sqrt(2500 + 2500) = sqrt(5000) ≈ 70.71
    assert distance > 0
    assert abs(distance - 70.71) < 0.1

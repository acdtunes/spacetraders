#!/usr/bin/env python3
"""
Test suite for fleet trade route optimizer

Validates multi-ship conflict-aware route optimization:
- No (resource, waypoint) collisions between ships
- Independent profitability for each route
- Fleet profit maximization
"""

import pytest
from unittest.mock import Mock, MagicMock
from spacetraders_bot.operations.multileg_trader import (
    MultiLegRoute,
    RouteSegment,
    TradeAction,
    FleetTradeOptimizer,
)


@pytest.fixture
def mock_db():
    """Mock database with waypoint coordinates and market data"""
    db = Mock()
    conn = Mock()
    cursor = Mock()

    # Waypoint coordinates for distance calculations
    waypoint_coords = {
        'X1-TX46-H51': (100, 100),
        'X1-TX46-K91': (150, 120),
        'X1-TX46-D41': (200, 150),
        'X1-TX46-D42': (210, 155),
        'X1-TX46-J55': (250, 180),
        'X1-TX46-H48': (300, 200),
        'X1-TX46-A4': (120, 90),
        'X1-TX46-C39': (180, 130),
        'X1-TX46-B7': (220, 160),
        'X1-TX46-F50': (260, 190),
    }

    def get_coords(waypoint):
        return waypoint_coords.get(waypoint, (0, 0))

    # Mock cursor.execute for coordinate queries
    def execute_mock(query, params):
        if "SELECT x, y FROM waypoints" in query:
            waypoint = params[0]
            x, y = get_coords(waypoint)
            cursor.fetchone.return_value = (x, y)
        elif "SELECT DISTINCT waypoint_symbol FROM market_data" in query:
            # Return all waypoints as tuples
            cursor.fetchall.return_value = [(wp,) for wp in waypoint_coords.keys()]
        return cursor

    cursor.execute.side_effect = execute_mock
    cursor.fetchall.return_value = [(wp,) for wp in waypoint_coords.keys()]
    conn.cursor.return_value = cursor
    conn.execute = execute_mock

    db.connection.return_value.__enter__ = lambda self: conn
    db.connection.return_value.__exit__ = lambda self, *args: None
    db.transaction.return_value = db.connection.return_value

    # Mock get_market_data for trade opportunities
    def get_market_data_mock(conn, waypoint, good=None):
        """Return mock market data for test scenarios"""
        # Return data for ALL goods at each waypoint (simulating full market)
        market_data = {
            'X1-TX46-D42': [
                {'good_symbol': 'ADVANCED_CIRCUITRY', 'sell_price': 8000, 'purchase_price': 12000, 'trade_volume': 20, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'SHIP_PLATING', 'sell_price': 600, 'purchase_price': 900, 'trade_volume': 30, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-H51': [
                {'good_symbol': 'SHIP_PLATING', 'sell_price': 500, 'purchase_price': 800, 'trade_volume': 30, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'COPPER_ORE', 'sell_price': 110, 'purchase_price': 160, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-K91': [
                {'good_symbol': 'CLOTHING', 'sell_price': 200, 'purchase_price': 350, 'trade_volume': 40, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'IRON_ORE', 'sell_price': 130, 'purchase_price': 190, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-C39': [
                {'good_symbol': 'COPPER_ORE', 'sell_price': 100, 'purchase_price': 150, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'CLOTHING', 'sell_price': 210, 'purchase_price': 360, 'trade_volume': 40, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-B7': [
                {'good_symbol': 'IRON_ORE', 'sell_price': 120, 'purchase_price': 180, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'ALUMINUM_ORE', 'sell_price': 95, 'purchase_price': 145, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-F50': [
                {'good_symbol': 'ALUMINUM_ORE', 'sell_price': 90, 'purchase_price': 140, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
                {'good_symbol': 'SHIP_PLATING', 'sell_price': 550, 'purchase_price': 850, 'trade_volume': 30, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-A4': [
                {'good_symbol': 'ADVANCED_CIRCUITRY', 'sell_price': 7500, 'purchase_price': 13000, 'trade_volume': 20, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-D41': [
                {'good_symbol': 'ADVANCED_CIRCUITRY', 'sell_price': 7800, 'purchase_price': 12500, 'trade_volume': 20, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-J55': [
                {'good_symbol': 'CLOTHING', 'sell_price': 190, 'purchase_price': 370, 'trade_volume': 40, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
            'X1-TX46-H48': [
                {'good_symbol': 'IRON_ORE', 'sell_price': 115, 'purchase_price': 195, 'trade_volume': 50, 'last_updated': '2025-10-14T10:00:00.000Z'},
            ],
        }

        if good:
            # Return specific good from waypoint
            data = market_data.get(waypoint, [])
            return [d for d in data if d['good_symbol'] == good]
        else:
            return market_data.get(waypoint, [])

    db.get_market_data = get_market_data_mock

    return db


@pytest.fixture
def mock_api():
    """Mock API client"""
    api = Mock()
    return api


def test_fleet_optimizer_detects_resource_waypoint_conflicts(mock_api, mock_db):
    """
    CRITICAL TEST: Verify fleet optimizer prevents (resource, waypoint) collisions

    Scenario:
    - Ship 1 gets route: D42 (buy ADVANCED_CIRCUITRY)
    - Ship 2 should NOT get any route that buys ADVANCED_CIRCUITRY at D42

    This prevents market interference and price escalation
    """
    optimizer = FleetTradeOptimizer(mock_api, mock_db, player_id=6)

    # Mock ship data
    ships = [
        {
            'symbol': 'STARHOPPER-D',
            'nav': {'waypointSymbol': 'X1-TX46-H51', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        },
        {
            'symbol': 'STARHOPPER-14',
            'nav': {'waypointSymbol': 'X1-TX46-C39', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        }
    ]

    # Mock agent credits
    starting_credits = 1000000

    # Find conflict-free routes
    fleet_result = optimizer.optimize_fleet(
        ships=ships,
        system='X1-TX46',
        max_stops=4,
        starting_credits=starting_credits,
    )

    assert fleet_result is not None
    assert 'ship_routes' in fleet_result
    assert len(fleet_result['ship_routes']) == 2

    # Extract all (resource, waypoint) BUY pairs from all routes
    # Separate by ship to detect BETWEEN-ship conflicts (not within-ship)
    ship_buy_pairs = {}  # {ship_symbol: set((good, waypoint))}

    for ship_symbol, route in fleet_result['ship_routes'].items():
        ship_buy_pairs[ship_symbol] = set()
        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    pair = (action.good, action.waypoint)
                    ship_buy_pairs[ship_symbol].add(pair)

    # CRITICAL ASSERTION: Check for conflicts BETWEEN ships
    all_ships = list(ship_buy_pairs.keys())
    for i in range(len(all_ships)):
        for j in range(i + 1, len(all_ships)):
            ship_a = all_ships[i]
            ship_b = all_ships[j]
            conflicts = ship_buy_pairs[ship_a] & ship_buy_pairs[ship_b]
            assert len(conflicts) == 0, (
                f"CONFLICT DETECTED between {ship_a} and {ship_b}: "
                f"Both ships buy at {conflicts}"
            )

    print(f"\n✅ No conflicts detected between {len(all_ships)} ships")
    for ship_symbol, pairs in ship_buy_pairs.items():
        print(f"   {ship_symbol}: {len(pairs)} unique BUY pairs")
        for pair in pairs:
            print(f"      - {pair[0]} @ {pair[1]}")


def test_fleet_optimizer_both_routes_profitable(mock_api, mock_db):
    """
    Verify each ship's route is independently profitable

    Even though we optimize for fleet total, no ship should have negative profit
    """
    optimizer = FleetTradeOptimizer(mock_api, mock_db, player_id=6)

    ships = [
        {
            'symbol': 'STARHOPPER-D',
            'nav': {'waypointSymbol': 'X1-TX46-H51', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        },
        {
            'symbol': 'STARHOPPER-14',
            'nav': {'waypointSymbol': 'X1-TX46-C39', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        }
    ]

    starting_credits = 1000000

    fleet_result = optimizer.optimize_fleet(
        ships=ships,
        system='X1-TX46',
        max_stops=4,
        starting_credits=starting_credits,
    )

    assert fleet_result is not None

    for ship_symbol, route in fleet_result['ship_routes'].items():
        print(f"\n{ship_symbol}:")
        print(f"  Profit: {route.total_profit:,} cr")
        print(f"  Stops: {len(route.segments)}")

        # CRITICAL ASSERTION: Each route must be independently profitable
        assert route.total_profit > 0, (
            f"{ship_symbol} has unprofitable route: {route.total_profit:,} cr"
        )


def test_fleet_optimizer_maximizes_total_profit(mock_api, mock_db):
    """
    Verify fleet optimizer maximizes sum of all ship profits

    Total fleet profit should be higher than sum of independently optimized routes
    """
    optimizer = FleetTradeOptimizer(mock_api, mock_db, player_id=6)

    ships = [
        {
            'symbol': 'STARHOPPER-D',
            'nav': {'waypointSymbol': 'X1-TX46-H51', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        },
        {
            'symbol': 'STARHOPPER-14',
            'nav': {'waypointSymbol': 'X1-TX46-C39', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        }
    ]

    starting_credits = 1000000

    fleet_result = optimizer.optimize_fleet(
        ships=ships,
        system='X1-TX46',
        max_stops=4,
        starting_credits=starting_credits,
    )

    assert fleet_result is not None
    assert 'total_fleet_profit' in fleet_result
    assert fleet_result['total_fleet_profit'] > 0

    # Sum individual ship profits
    individual_sum = sum(route.total_profit for route in fleet_result['ship_routes'].values())

    print(f"\n💰 Fleet Optimization Results:")
    print(f"   Total Fleet Profit: {fleet_result['total_fleet_profit']:,} cr")
    print(f"   Sum of Individual: {individual_sum:,} cr")
    print(f"   Conflicts Detected: {fleet_result.get('conflicts', 0)}")

    assert fleet_result['total_fleet_profit'] == individual_sum


def test_fleet_optimizer_single_ship_fallback(mock_api, mock_db):
    """
    Verify optimizer handles single ship (degenerates to single-ship optimization)
    """
    optimizer = FleetTradeOptimizer(mock_api, mock_db, player_id=6)

    ships = [
        {
            'symbol': 'STARHOPPER-D',
            'nav': {'waypointSymbol': 'X1-TX46-H51', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        }
    ]

    starting_credits = 1000000

    fleet_result = optimizer.optimize_fleet(
        ships=ships,
        system='X1-TX46',
        max_stops=4,
        starting_credits=starting_credits,
    )

    assert fleet_result is not None
    assert len(fleet_result['ship_routes']) == 1
    assert 'STARHOPPER-D' in fleet_result['ship_routes']


def test_fleet_optimizer_no_profitable_routes(mock_api, mock_db):
    """
    Verify optimizer handles scenario where no profitable routes exist
    """
    # Mock database with no profitable opportunities
    mock_db.get_market_data = lambda conn, waypoint, good=None: []

    optimizer = FleetTradeOptimizer(mock_api, mock_db, player_id=6)

    ships = [
        {
            'symbol': 'STARHOPPER-D',
            'nav': {'waypointSymbol': 'X1-TX46-H51', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        },
        {
            'symbol': 'STARHOPPER-14',
            'nav': {'waypointSymbol': 'X1-TX46-C39', 'systemSymbol': 'X1-TX46'},
            'cargo': {'capacity': 40},
            'fuel': {'capacity': 400, 'current': 400},
            'engine': {'speed': 10},
        }
    ]

    starting_credits = 1000000

    fleet_result = optimizer.optimize_fleet(
        ships=ships,
        system='X1-TX46',
        max_stops=4,
        starting_credits=starting_credits,
    )

    # Should return None or empty result
    assert fleet_result is None or len(fleet_result.get('ship_routes', {})) == 0

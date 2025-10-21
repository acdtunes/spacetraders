"""
BDD Step Definitions for Market Repository

Tests for database access layer functionality.
"""

import math
from unittest.mock import Mock

import pytest
from pytest_bdd import given, parsers, scenarios, then, when

from spacetraders_bot.operations._trading import (
    MarketRepository,
    NearbyMarketBuyer,
)

# Load feature file
scenarios('../../../../bdd/features/trading/_trading_module/market_repository.feature')


# ===========================
# Given Steps
# ===========================

@given('a test database')
def create_test_database(context, mock_database):
    """Create test database fixture"""
    context['database'] = mock_database


@given(parsers.parse('the following waypoints with coordinates:'))
def setup_waypoint_coordinates(context, datatable):
    """Setup waypoint coordinates in mock database"""
    db = context['database']
    waypoint_data = {}

    # Parse datatable - it's a list of lists: [['waypoint', 'x', 'y'], ['X1-TEST-A', '0', '0'], ...]
    # First row is the header
    headers = datatable[0]
    for row in datatable[1:]:
        waypoint = row[0]
        x = int(row[1])
        y = int(row[2])
        waypoint_data[waypoint] = (x, y)

    # Store waypoint data for mock lookups
    context['waypoint_data'] = waypoint_data

    # Configure mock database to return waypoint coordinates
    def mock_cursor_execute(query, params=None):
        """Mock cursor execute for waypoint lookups"""
        cursor = context['database'].connection().cursor()

        if params and "SELECT x, y FROM waypoints" in query:
            waypoint_symbol = params[0]
            if waypoint_symbol in waypoint_data:
                coords = waypoint_data[waypoint_symbol]
                cursor.fetchone = Mock(return_value=coords)
            else:
                cursor.fetchone = Mock(return_value=None)

        return cursor

    # Create a cursor mock with the execute method
    cursor_mock = Mock()
    cursor_mock.execute = mock_cursor_execute
    cursor_mock.fetchone = Mock(return_value=None)
    cursor_mock.fetchall = Mock(return_value=[])

    # Setup connection context manager
    connection_mock = Mock()
    connection_mock.__enter__ = Mock(return_value=connection_mock)
    connection_mock.__exit__ = Mock(return_value=False)
    connection_mock.cursor = Mock(return_value=cursor_mock)

    db.connection = Mock(return_value=connection_mock)


@given(parsers.parse('the following market data:'))
def setup_market_data(context, datatable):
    """Setup market data in mock database"""
    db = context['database']
    market_data = []
    waypoint_data = context.get('waypoint_data', {})

    # Parse datatable - it's a list of lists: [['waypoint', 'good', 'purchase_price', 'sell_price'], ...]
    # First row is the header
    headers = datatable[0]
    for row in datatable[1:]:
        waypoint = row[0]
        good = row[1]
        purchase_price = int(row[2])
        sell_price = int(row[3])

        market_data.append({
            'waypoint': waypoint,
            'good': good,
            'purchase_price': purchase_price,
            'sell_price': sell_price
        })

    # Store market data for mock lookups
    context['market_data'] = market_data

    # Configure mock database cursor for market queries
    def mock_cursor_factory():
        """Create a mock cursor that handles both waypoint and market queries"""
        cursor_mock = Mock()

        def mock_execute(query, params=None):
            """Handle different query types"""
            # Waypoint coordinate lookups
            if params and "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?" in query:
                waypoint_symbol = params[0]
                if waypoint_symbol in waypoint_data:
                    coords = waypoint_data[waypoint_symbol]
                    cursor_mock.fetchone = Mock(return_value=coords)
                else:
                    cursor_mock.fetchone = Mock(return_value=None)

            # Market buyer lookups (find_nearby_buyers)
            elif params and "SELECT" in query and "market_data m" in query and "JOIN waypoints w" in query:
                origin_x = params[0]
                origin_y = params[2]
                good_symbol = params[4]
                system_pattern = params[5]  # e.g., "X1-TEST-%"
                limit = params[6]

                # Extract system prefix from pattern
                system_prefix = system_pattern.rstrip('-%')

                # Find matching markets
                results = []
                for market in market_data:
                    if market['good'] != good_symbol:
                        continue
                    if market['purchase_price'] <= 0:
                        continue
                    if not market['waypoint'].startswith(system_prefix):
                        continue

                    waypoint_symbol = market['waypoint']
                    if waypoint_symbol not in waypoint_data:
                        continue

                    coords = waypoint_data[waypoint_symbol]
                    dx = coords[0] - origin_x
                    dy = coords[1] - origin_y
                    distance_squared = dx**2 + dy**2

                    results.append((
                        waypoint_symbol,
                        market['purchase_price'],
                        coords[0],
                        coords[1],
                        distance_squared
                    ))

                # Sort by distance and limit
                results.sort(key=lambda r: r[4])
                results = results[:limit]

                cursor_mock.fetchall = Mock(return_value=results)

            else:
                cursor_mock.fetchone = Mock(return_value=None)
                cursor_mock.fetchall = Mock(return_value=[])

        cursor_mock.execute = mock_execute
        cursor_mock.fetchone = Mock(return_value=None)
        cursor_mock.fetchall = Mock(return_value=[])

        return cursor_mock

    # Setup connection context manager
    connection_mock = Mock()
    connection_mock.__enter__ = Mock(return_value=connection_mock)
    connection_mock.__exit__ = Mock(return_value=False)
    connection_mock.cursor = Mock(side_effect=mock_cursor_factory)

    db.connection = Mock(return_value=connection_mock)

    # Configure get_market_data for check_market_accepts_good
    def mock_get_market_data(conn, waypoint, good):
        """Mock get_market_data method"""
        for market in market_data:
            if market['waypoint'] == waypoint and market['good'] == good:
                return [{'purchase_price': market['purchase_price']}]
        return []

    db.get_market_data = Mock(side_effect=mock_get_market_data)


@given('a MarketRepository')
def create_market_repository(context):
    """Create MarketRepository instance"""
    context['market_repo'] = MarketRepository(context['database'])


@given(parsers.parse('waypoint "{waypoint}" exists in different system'))
def add_waypoint_different_system(context, waypoint):
    """Add waypoint in different system"""
    # Add to waypoint data
    waypoint_data = context.get('waypoint_data', {})
    waypoint_data[waypoint] = (10, 20)  # Some coordinates
    context['waypoint_data'] = waypoint_data


@given(parsers.parse('"{waypoint}" buys "{good}" for {price:d}'))
def add_market_buyer(context, waypoint, good, price):
    """Add market buyer for a good"""
    market_data = context.get('market_data', [])
    market_data.append({
        'waypoint': waypoint,
        'good': good,
        'purchase_price': price,
        'sell_price': 0
    })
    context['market_data'] = market_data


# ===========================
# When Steps
# ===========================

@when('I get coordinates for "X1-TEST-A" and "X1-TEST-B"')
def get_specific_test_coordinates(context):
    """Get coordinates for X1-TEST-A and X1-TEST-B specifically - UNUSED

    Note: This step definition exists to match the feature file syntax, but the
    actual test implementation doesn't require storing the coordinates since the
    Then step queries them directly."""
    # This step is a no-op; the Then step will query coordinates directly
    pass


@when(parsers.parse('I get coordinates for "{waypoint}"'))
def get_coordinates(context, waypoint):
    """Get coordinates for a waypoint"""
    repo = context['market_repo']
    context['coordinates'] = repo.get_waypoint_coordinates(waypoint)


@when(parsers.parse('I calculate distance from "{from_wp}" to "{to_wp}"'))
def calculate_distance(context, from_wp, to_wp):
    """Calculate distance between waypoints"""
    repo = context['market_repo']
    context['distance'] = repo.calculate_distance(from_wp, to_wp)


@when(parsers.parse('I find buyers for "{good}" near "{origin}" in system "{system}" within {distance:d} units'))
def find_nearby_buyers(context, good, origin, system, distance):
    """Find nearby buyers for a good"""
    repo = context['market_repo']
    context['nearby_buyers'] = repo.find_nearby_buyers(good, origin, system, distance)


@when(parsers.parse('I find buyers for "{good}" near "{origin}" in system "{system}" within {distance:d} units with limit {limit:d}'))
def find_nearby_buyers_with_limit(context, good, origin, system, distance, limit):
    """Find nearby buyers with limit"""
    repo = context['market_repo']
    context['nearby_buyers'] = repo.find_nearby_buyers(good, origin, system, distance, limit)


@when(parsers.parse('I check if "{waypoint}" accepts "{good}"'))
def check_market_accepts_good(context, waypoint, good):
    """Check if market accepts a good"""
    repo = context['market_repo']
    context['market_accepts'] = repo.check_market_accepts_good(waypoint, good)


@when('I perform multiple sequential queries')
def perform_multiple_queries(context):
    """Perform multiple sequential queries"""
    repo = context['market_repo']

    # Execute multiple queries
    context['query_results'] = []
    try:
        # Query 1: Get coordinates
        coords = repo.get_waypoint_coordinates('X1-TEST-A')
        context['query_results'].append(('coords', coords))

        # Query 2: Calculate distance
        distance = repo.calculate_distance('X1-TEST-A', 'X1-TEST-B')
        context['query_results'].append(('distance', distance))

        # Query 3: Find buyers
        buyers = repo.find_nearby_buyers('IRON_ORE', 'X1-TEST-A', 'X1-TEST', 200)
        context['query_results'].append(('buyers', buyers))

        # Query 4: Check market accepts
        accepts = repo.check_market_accepts_good('X1-TEST-B', 'IRON_ORE')
        context['query_results'].append(('accepts', accepts))

        context['queries_succeeded'] = True
    except Exception as e:
        context['queries_succeeded'] = False
        context['error_message'] = str(e)


@when(parsers.parse('I get coordinates for "{from_wp}" and "{to_wp}"'))
def get_coordinates_for_both_waypoints(context, from_wp, to_wp):
    """Get coordinates for two waypoints - store for later verification"""
    repo = context['market_repo']
    context['coords1'] = repo.get_waypoint_coordinates(from_wp)
    context['coords2'] = repo.get_waypoint_coordinates(to_wp)


# ===========================
# Then Steps
# ===========================

@then(parsers.parse('coordinates should be ({x:d}, {y:d})'))
def check_coordinates(context, x, y):
    """Verify coordinates match expected values"""
    assert context['coordinates'] == (x, y), \
        f"Expected ({x}, {y}), got {context['coordinates']}"


@then('coordinates should be None')
def check_coordinates_none(context):
    """Verify coordinates are None"""
    assert context['coordinates'] is None, \
        f"Expected None, got {context['coordinates']}"


@then(parsers.parse('distance should be {dist:f}'))
def check_distance(context, dist):
    """Verify distance matches expected value"""
    assert abs(context['distance'] - dist) < 0.01, \
        f"Expected {dist}, got {context['distance']}"


@then(parsers.parse('I should find {count:d} buyer'))
@then(parsers.parse('I should find {count:d} buyers'))
def check_buyer_count(context, count):
    """Verify number of buyers found"""
    buyers = context['nearby_buyers']
    assert len(buyers) == count, \
        f"Expected {count} buyers, got {len(buyers)}"


@then(parsers.parse('I should find exactly {count:d} buyers'))
def check_exact_buyer_count(context, count):
    """Verify exact number of buyers found"""
    buyers = context['nearby_buyers']
    assert len(buyers) == count, \
        f"Expected exactly {count} buyers, got {len(buyers)}"


@then(parsers.parse('buyers should include "{waypoint}" at distance {dist:f}'))
def check_buyer_included(context, waypoint, dist):
    """Verify specific buyer is included at expected distance"""
    buyers = context['nearby_buyers']

    found = None
    for buyer in buyers:
        if buyer.waypoint_symbol == waypoint:
            found = buyer
            break

    assert found is not None, \
        f"Expected buyer {waypoint} not found in results"

    assert abs(found.distance - dist) < 0.01, \
        f"Expected distance {dist} for {waypoint}, got {found.distance}"


@then(parsers.parse('buyers should not include "{waypoint}"'))
def check_buyer_not_included(context, waypoint):
    """Verify specific buyer is not included"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert buyer.waypoint_symbol != waypoint, \
            f"Buyer {waypoint} should not be in results but was found"


@then('buyers should be sorted by distance ascending')
def check_buyers_sorted(context):
    """Verify buyers are sorted by distance"""
    buyers = context['nearby_buyers']

    if len(buyers) <= 1:
        return  # Already sorted

    for i in range(len(buyers) - 1):
        assert buyers[i].distance <= buyers[i+1].distance, \
            f"Buyers not sorted: {buyers[i].distance} > {buyers[i+1].distance}"


@then('first buyer should be closest')
def check_first_buyer_closest(context):
    """Verify first buyer is closest"""
    buyers = context['nearby_buyers']

    if len(buyers) == 0:
        pytest.skip("No buyers to check")

    # Already checked by sorting test, but validate again
    if len(buyers) > 1:
        assert buyers[0].distance <= buyers[1].distance, \
            "First buyer is not closest"


@then('second buyer should be second closest')
def check_second_buyer_second_closest(context):
    """Verify second buyer is second closest"""
    buyers = context['nearby_buyers']

    if len(buyers) < 2:
        pytest.skip("Less than 2 buyers to check")

    # Already checked by sorting test, but validate again
    if len(buyers) > 2:
        assert buyers[1].distance <= buyers[2].distance, \
            "Second buyer is not second closest"


@then('each buyer should have waypoint_symbol')
def check_buyer_has_waypoint_symbol(context):
    """Verify each buyer has waypoint_symbol"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert hasattr(buyer, 'waypoint_symbol'), \
            "Buyer missing waypoint_symbol attribute"
        assert buyer.waypoint_symbol is not None, \
            "Buyer waypoint_symbol is None"


@then('each buyer should have purchase_price')
def check_buyer_has_purchase_price(context):
    """Verify each buyer has purchase_price"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert hasattr(buyer, 'purchase_price'), \
            "Buyer missing purchase_price attribute"
        assert buyer.purchase_price > 0, \
            f"Buyer {buyer.waypoint_symbol} has invalid purchase_price: {buyer.purchase_price}"


@then('each buyer should have x coordinate')
def check_buyer_has_x_coordinate(context):
    """Verify each buyer has x coordinate"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert hasattr(buyer, 'x'), \
            "Buyer missing x attribute"
        assert isinstance(buyer.x, (int, float)), \
            f"Buyer {buyer.waypoint_symbol} has invalid x coordinate"


@then('each buyer should have y coordinate')
def check_buyer_has_y_coordinate(context):
    """Verify each buyer has y coordinate"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert hasattr(buyer, 'y'), \
            "Buyer missing y attribute"
        assert isinstance(buyer.y, (int, float)), \
            f"Buyer {buyer.waypoint_symbol} has invalid y coordinate"


@then('each buyer should have distance')
def check_buyer_has_distance(context):
    """Verify each buyer has distance"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        assert hasattr(buyer, 'distance'), \
            "Buyer missing distance attribute"
        assert buyer.distance >= 0, \
            f"Buyer {buyer.waypoint_symbol} has negative distance: {buyer.distance}"


@then(parsers.parse('buyer should be "{waypoint}"'))
def check_single_buyer(context, waypoint):
    """Verify single buyer is expected waypoint"""
    buyers = context['nearby_buyers']

    assert len(buyers) == 1, \
        f"Expected 1 buyer, got {len(buyers)}"

    assert buyers[0].waypoint_symbol == waypoint, \
        f"Expected buyer {waypoint}, got {buyers[0].waypoint_symbol}"


@then(parsers.parse('buyer purchase price should be {price:d}'))
def check_buyer_purchase_price(context, price):
    """Verify buyer purchase price"""
    buyers = context['nearby_buyers']

    assert len(buyers) > 0, \
        "No buyers to check price"

    assert buyers[0].purchase_price == price, \
        f"Expected purchase price {price}, got {buyers[0].purchase_price}"


@then(parsers.parse('all buyers should match system "{system}"'))
def check_buyers_match_system(context, system):
    """Verify all buyers are from expected system"""
    buyers = context['nearby_buyers']

    for buyer in buyers:
        # Extract system from waypoint symbol (e.g., X1-TEST-A -> X1-TEST)
        waypoint_parts = buyer.waypoint_symbol.rsplit('-', 1)
        buyer_system = waypoint_parts[0] if waypoint_parts else ''

        assert buyer_system == system, \
            f"Buyer {buyer.waypoint_symbol} is from system {buyer_system}, expected {system}"


@then('market should accept the good')
def check_market_accepts(context):
    """Verify market accepts the good"""
    assert context['market_accepts'] is True, \
        "Expected market to accept the good"


@then('market should not accept the good')
def check_market_not_accepts(context):
    """Verify market does not accept the good"""
    assert context['market_accepts'] is False, \
        "Expected market to not accept the good"


@then('all queries should succeed')
def check_queries_succeeded(context):
    """Verify all queries succeeded"""
    assert context.get('queries_succeeded', False) is True, \
        f"Queries failed: {context.get('error_message', 'Unknown error')}"


@then('database connections should be properly managed')
def check_database_connections(context):
    """Verify database connections are properly managed"""
    # This is a placeholder - in real implementation, we'd check connection pool state
    # For mock testing, we verify the connection was used as context manager
    db = context['database']

    # Verify connection() was called
    assert db.connection.called, \
        "Database connection() was not called"

    # Verify context manager was used (check __enter__ and __exit__ were called)
    connection_instance = db.connection.return_value
    assert connection_instance.__enter__.called, \
        "Database connection __enter__ was not called (not used as context manager)"
    assert connection_instance.__exit__.called, \
        "Database connection __exit__ was not called (not properly closed)"


@then('distance should match manual calculation from coordinates')
def check_distance_matches_manual_calculation(context):
    """Verify distance matches manual calculation from retrieved coordinates"""
    # For this test, we calculate the expected distance by querying coordinates directly
    # This verifies that calculate_distance() produces the same result as manually
    # calculating from the coordinates
    repo = context['market_repo']

    # Get calculated distance from the When step
    calculated_distance = context.get('distance')
    assert calculated_distance is not None, "Distance was not calculated"

    # Query coordinates directly to get the "truth"
    coords1 = repo.get_waypoint_coordinates('X1-TEST-A')
    coords2 = repo.get_waypoint_coordinates('X1-TEST-B')

    assert coords1 is not None, "Failed to get coordinates for X1-TEST-A"
    assert coords2 is not None, "Failed to get coordinates for X1-TEST-B"

    # Manual calculation using the queried coordinates
    dx = coords2[0] - coords1[0]
    dy = coords2[1] - coords1[1]
    expected_distance = math.sqrt(dx**2 + dy**2)

    # Verify they match
    assert abs(calculated_distance - expected_distance) < 0.01, \
        f"Calculated distance {calculated_distance} doesn't match manual calculation {expected_distance}"

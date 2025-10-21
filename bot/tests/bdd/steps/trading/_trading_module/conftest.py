"""
Shared fixtures and common step definitions for _trading module BDD tests

Common fixtures and Background steps used across all trading module step definitions.
"""

import logging
from unittest.mock import Mock, MagicMock
import pytest
from pytest_bdd import given, parsers


@pytest.fixture
def context():
    """Shared test context dictionary for storing state between steps"""
    return {
        # Fleet optimizer
        'fleet_optimizer': None,
        'ships': [],
        'fleet_result': None,
        'ship_routes': {},
        'reserved_pairs': set(),

        # Evaluation strategies
        'strategy': None,
        'evaluation': None,
        'market_opportunities': {},
        'cargo': {},
        'credits': 0,
        'cargo_capacity': 0,

        # Route planner
        'route_planner': None,
        'route': None,
        'trade_opportunities': [],
        'markets': [],
        'optimizer': None,

        # Market repository
        'market_repo': None,
        'coordinates': {},
        'distance': None,
        'nearby_buyers': {},
        'market_accepts': {},

        # Cargo salvage
        'salvage_service': None,
        'salvage_result': None,
        'salvage_revenue': 0,
        'tier_used': None,

        # Common
        'database': None,
        'api': None,
        'ship': None,
        'logger': None,
        'player_id': 1,
        'system': 'X1-TEST',
        'waypoint': None,
        'error_message': None,
    }


@pytest.fixture
def mock_database():
    """Mock database with basic structure"""
    db = Mock()

    # Mock connection as context manager
    connection_mock = Mock()
    connection_mock.__enter__ = Mock(return_value=connection_mock)
    connection_mock.__exit__ = Mock(return_value=False)

    cursor_mock = Mock()
    cursor_mock.fetchone = Mock(return_value=None)
    cursor_mock.fetchall = Mock(return_value=[])
    cursor_mock.execute = Mock()

    connection_mock.cursor = Mock(return_value=cursor_mock)

    db.connection = Mock(return_value=connection_mock)
    db.transaction = Mock(return_value=connection_mock)
    db.get_market_data = Mock(return_value=[])

    return db


@pytest.fixture
def mock_api_client():
    """Mock API client for testing"""
    api = Mock()
    api.get = Mock(return_value={'data': {}})
    api.post = Mock(return_value={'data': {}})
    api.get_agent = Mock(return_value={'credits': 10000})
    return api


@pytest.fixture
def mock_ship_controller():
    """Mock ship controller with common methods"""
    ship = Mock()

    # Default ship status
    ship.get_status = Mock(return_value={
        'symbol': 'TEST-SHIP',
        'nav': {
            'waypointSymbol': 'X1-TEST-A',
            'systemSymbol': 'X1-TEST',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 50,
            'units': 0,
            'inventory': []
        },
        'fuel': {
            'capacity': 1000,
            'current': 1000
        },
        'engine': {
            'speed': 30
        }
    })

    # Mock methods
    ship.navigate = Mock(return_value=True)
    ship.dock = Mock(return_value=True)
    ship.orbit = Mock(return_value=True)
    ship.refuel = Mock(return_value=True)
    ship.sell = Mock(return_value={'totalPrice': 0})
    ship.buy = Mock(return_value={'totalPrice': 0})

    return ship


@pytest.fixture
def mock_logger():
    """Mock logger for testing"""
    logger = Mock(spec=logging.Logger)
    logger.info = Mock()
    logger.warning = Mock()
    logger.error = Mock()
    logger.debug = Mock()
    return logger


@pytest.fixture
def sample_trade_opportunities():
    """Sample trade opportunities for testing"""
    return [
        {
            'good': 'IRON_ORE',
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-B',
            'buy_price': 100,
            'sell_price': 200,
            'spread': 100,
            'volume': 50
        },
        {
            'good': 'COPPER_ORE',
            'buy_waypoint': 'X1-TEST-B',
            'sell_waypoint': 'X1-TEST-C',
            'buy_price': 150,
            'sell_price': 300,
            'spread': 150,
            'volume': 40
        }
    ]


@pytest.fixture
def sample_markets():
    """Sample market waypoints for testing"""
    return ['X1-TEST-A', 'X1-TEST-B', 'X1-TEST-C', 'X1-TEST-D']


@pytest.fixture
def sample_waypoint_coordinates():
    """Sample waypoint coordinates for testing"""
    return {
        'X1-TEST-A': (0, 0),
        'X1-TEST-B': (100, 0),
        'X1-TEST-C': (200, 0),
        'X1-TEST-D': (300, 0)
    }
"""
Common BDD step definitions shared across _trading module tests

These steps are used in Background sections and are common to multiple feature files.
"""

from unittest.mock import Mock
from pytest_bdd import given, parsers


# ============================================================================
# Background Setup Steps - Shared across all feature files
# ============================================================================

@given('a test database with market data')
def setup_test_database(context, mock_database):
    """Setup mock database for testing"""
    context['database'] = mock_database


@given('a mock API client')
def setup_mock_api(context, mock_api_client):
    """Setup mock API client for testing"""
    context['api'] = mock_api_client


@given('a mock ship controller')
def setup_mock_ship(context, mock_ship_controller):
    """Setup mock ship controller for testing"""
    context['ship'] = mock_ship_controller


@given(parsers.parse('the following markets in system "{system}":'))
def setup_markets_with_coordinates(context, system, datatable):
    """
    Setup markets with coordinates from datatable

    Expected format:
    | waypoint  | x   | y   |
    | X1-TEST-A | 0   | 0   |
    | X1-TEST-B | 100 | 0   |
    """
    context['system'] = system
    context['markets'] = []
    context['waypoint_coords'] = {}

    # Parse datatable (pytest-bdd provides datatable as list of lists)
    # First row is header
    headers = datatable[0] if datatable else []

    for row in datatable[1:]:  # Skip header row
        waypoint = row[0]
        x = int(row[1])
        y = int(row[2])

        context['markets'].append(waypoint)
        context['waypoint_coords'][waypoint] = (x, y)

    # Configure mock database cursor to return market waypoints
    connection_mock = context['database'].connection.return_value
    cursor_mock = connection_mock.cursor.return_value

    # Setup fetchall to return list of tuples (waypoint_symbol,) for _get_markets_in_system
    market_rows = [(waypoint,) for waypoint in context['markets']]
    cursor_mock.fetchall.return_value = market_rows

    # Setup fetchone to return coordinates when queried
    def fetchone_coords(*args, **kwargs):
        # This will be called for coordinate lookups
        # Return (x, y) tuple for waypoint coordinate queries
        return None  # Default, will be overridden per query

    cursor_mock.fetchone.side_effect = fetchone_coords


@given('the following trade opportunities:')
def setup_trade_opportunities(context, datatable):
    """
    Setup trade opportunities from datatable

    Expected format:
    | buy_waypoint | sell_waypoint | good       | buy_price | sell_price | spread | volume |
    | X1-TEST-A    | X1-TEST-B     | IRON_ORE   | 100       | 200        | 100    | 50     |
    """
    context['trade_opportunities'] = []
    market_data_rows = []

    # Parse datatable (first row is header)
    headers = datatable[0] if datatable else []

    for row in datatable[1:]:  # Skip header
        opportunity = {
            'buy_waypoint': row[0],
            'sell_waypoint': row[1],
            'good': row[2],
            'buy_price': int(row[3]),
            'sell_price': int(row[4]),
            'spread': int(row[5]),
            'trade_volume': int(row[6]) if len(row) > 6 else 50
        }
        context['trade_opportunities'].append(opportunity)

        # Add market data rows for _get_trade_opportunities query
        # Format: waypoint_symbol, good_symbol, sell_price (what we pay), purchase_price (what they pay us), trade_volume
        buy_waypoint = row[0]
        sell_waypoint = row[1]
        good = row[2]
        buy_price = int(row[3])  # What we pay to buy
        sell_price = int(row[4])  # What they pay us to sell
        volume = int(row[6]) if len(row) > 6 else 50

        # Market that sells the good (where we buy)
        market_data_rows.append((buy_waypoint, good, buy_price, 0, volume))

        # Market that buys the good (where we sell)
        market_data_rows.append((sell_waypoint, good, 0, sell_price, volume))

    # Configure mock database to return market data for trade opportunities
    connection_mock = context['database'].connection.return_value
    cursor_mock = connection_mock.cursor.return_value

    # The _get_trade_opportunities method queries for markets that sell goods
    # and markets that buy goods separately, then combines them
    # We need to configure cursor.fetchall() to return appropriate data
    # For now, store market_data_rows in context for step implementations to use
    context['market_data_rows'] = market_data_rows


@given(parsers.parse('the following markets exist:'))
def setup_markets_simple(context, datatable):
    """
    Setup simple market list from datatable

    Expected format:
    | waypoint  | x   | y   |
    | X1-TEST-A | 0   | 0   |
    """
    context['markets'] = []
    context['waypoint_coords'] = {}

    # Parse datatable
    for row in datatable[1:]:  # Skip header
        waypoint = row[0]
        x = int(row[1])
        y = int(row[2])

        context['markets'].append(waypoint)
        context['waypoint_coords'][waypoint] = (x, y)


@given(parsers.parse('the following waypoints with coordinates:'))
def setup_waypoints_with_coordinates(context, datatable):
    """
    Setup waypoint coordinates for repository tests

    Expected format:
    | waypoint  | x    | y    |
    | X1-TEST-A | 0    | 0    |
    """
    context['waypoint_coords'] = {}

    # Configure mock database to return coordinates
    coords_map = {}

    for row in datatable[1:]:  # Skip header
        waypoint = row[0]
        x = int(row[1])
        y = int(row[2])

        coords_map[waypoint] = (x, y)
        context['waypoint_coords'][waypoint] = (x, y)

    # Setup mock database cursor to return coordinates
    def get_coords_side_effect(sql, params):
        waypoint = params[0]
        return coords_map.get(waypoint)

    connection_mock = context['database'].connection.return_value
    cursor_mock = connection_mock.cursor.return_value

    # Store original execute to chain calls
    original_execute = cursor_mock.execute

    def execute_with_coords(sql, params=None):
        if params and 'waypoints' in sql and 'waypoint_symbol' in sql:
            waypoint = params[0]
            cursor_mock.fetchone.return_value = coords_map.get(waypoint)
        return original_execute(sql, params) if hasattr(original_execute, '__call__') else None

    cursor_mock.execute.side_effect = execute_with_coords


@given(parsers.parse('the following market data:'))
def setup_market_data(context, datatable):
    """
    Setup market data for repository tests

    Expected format:
    | waypoint  | good       | purchase_price | sell_price |
    | X1-TEST-A | IRON_ORE   | 0              | 100        |
    """
    context['market_data'] = []

    for row in datatable[1:]:  # Skip header
        data = {
            'waypoint': row[0],
            'good': row[1],
            'purchase_price': int(row[2]),
            'sell_price': int(row[3])
        }
        context['market_data'].append(data)

    # Configure mock database to return market data
    connection_mock = context['database'].connection.return_value
    cursor_mock = connection_mock.cursor.return_value

    # Create fetchall responses for market queries
    def create_market_rows(good, waypoint=None):
        matching = [
            (d['waypoint'], d['purchase_price'], d['sell_price'])
            for d in context['market_data']
            if d['good'] == good and (waypoint is None or d['waypoint'] == waypoint)
        ]
        return matching

    cursor_mock.fetchall.side_effect = lambda: create_market_rows('IRON_ORE')


# ============================================================================
# Common Assertion Helpers
# ============================================================================

@given('ship is DOCKED')
def ship_is_docked(context, mock_ship_controller):
    """Set ship status to DOCKED"""
    status = mock_ship_controller.get_status.return_value
    status['nav']['status'] = 'DOCKED'


@given('ship is IN_ORBIT')
def ship_is_in_orbit(context, mock_ship_controller):
    """Set ship status to IN_ORBIT"""
    status = mock_ship_controller.get_status.return_value
    status['nav']['status'] = 'IN_ORBIT'


@given(parsers.parse('ship is currently at "{waypoint}"'))
def ship_at_waypoint(context, waypoint, mock_ship_controller):
    """Set ship's current location"""
    status = mock_ship_controller.get_status.return_value
    status['nav']['waypointSymbol'] = waypoint
    context['current_waypoint'] = waypoint

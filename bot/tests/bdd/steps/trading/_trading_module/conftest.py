"""
Shared fixtures for _trading module BDD tests

Contains ONLY fixtures and Background steps used by MULTIPLE features.
Feature-specific steps are in their respective test_*_steps.py files.
"""

import logging
import logging.handlers
from datetime import datetime, timedelta, timezone
from unittest.mock import Mock, MagicMock, patch
import pytest
from pytest_bdd import given, parsers

from spacetraders_bot.operations._trading import (
    TradeAction,
    RouteSegment,
    MultiLegRoute,
    MarketEvaluation,
    SegmentDependency,
)

# Import ALL shared steps so pytest-bdd can discover them
# Using wildcard import as recommended by pytest-bdd documentation
from tests.bdd.steps.trading._trading_module.test_shared_steps import *  # noqa: F403, F401


# ===========================
# Shared Fixtures
# ===========================

@pytest.fixture
def context():
    """Shared test context dictionary for storing state between steps"""
    return {
        # Market service
        'base_price': None,
        'units': None,
        'effective_price': None,
        'degradation_pct': None,
        'route': None,
        'good': None,
        'segment_index': 0,
        'planned_sell_price': None,
        'planned_sell_destination': None,

        # Database and API
        'database': None,
        'waypoint': None,
        'transaction_type': None,
        'transaction_price': None,
        'logger': None,
        'api': None,

        # Circuit breaker
        'validator': None,
        'trade_action': None,
        'system': 'X1-TEST',
        'is_profitable': None,
        'error_message': None,
        'profit_margin': None,
        'price_change': None,
        'batch_size': None,
        'batch_rationale': None,

        # Trade executor
        'ship': None,
        'trade_executor': None,
        'buy_result': None,
        'sell_result': None,

        # Route executor
        'route_executor': None,
        'execution_result': None,
        'player_id': 6,

        # Dependency analyzer
        'dependencies': None,
        'skip_decision': None,
        'skip_reason': None,
        'affected_segments': None,
        'cargo': None,
        'blocks_future': None,

        # Validation
        'freshness_valid': None,
        'stale_markets': [],
        'aging_markets': [],
        'missing_markets': [],
        'warnings': [],
    }


@pytest.fixture
def mock_database():
    """Mock database with basic structure - used by multiple features"""
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
    db.transaction = Mock(return_value=connection_mock)  # Returns context manager
    db.get_market_data = Mock(return_value=[])
    db.update_market_data = Mock()

    # Store market data for verification
    db._market_data = {}

    def update_market_data_impl(waypoint=None, good=None, purchase_price=None, sell_price=None, last_updated=None, waypoint_symbol=None, **kwargs):
        # Support both waypoint and waypoint_symbol parameter names
        wp = waypoint or waypoint_symbol
        key = (wp, good)
        if key not in db._market_data:
            db._market_data[key] = {}
        if purchase_price is not None:
            db._market_data[key]['purchase_price'] = purchase_price
        if sell_price is not None:
            db._market_data[key]['sell_price'] = sell_price
        if last_updated is not None:
            db._market_data[key]['last_updated'] = last_updated
        else:
            db._market_data[key]['last_updated'] = datetime.now(timezone.utc)

    db.update_market_data = Mock(side_effect=update_market_data_impl)

    return db


@pytest.fixture
def mock_api_client():
    """Mock API client - used by multiple features"""
    api = Mock()
    api.get_agent = Mock(return_value={'credits': 100000})

    # Configure get_market to return profitable defaults
    def default_market_response(system, waypoint):
        response = {
            'tradeGoods': [
                {'symbol': 'COPPER', 'sellPrice': 100, 'purchasePrice': 500},
                {'symbol': 'IRON', 'sellPrice': 150, 'purchasePrice': 300},
                {'symbol': 'GOLD', 'sellPrice': 1500, 'purchasePrice': 2000},
                {'symbol': 'ALUMINUM', 'sellPrice': 68, 'purchasePrice': 558},
                {'symbol': 'ALUMINUM_ORE', 'sellPrice': 68, 'purchasePrice': 558},
                {'symbol': 'SHIP_PLATING', 'sellPrice': 2000, 'purchasePrice': 8000},
                {'symbol': 'ASSAULT_RIFLES', 'sellPrice': 3000, 'purchasePrice': 6000},
                {'symbol': 'ADVANCED_CIRCUITRY', 'sellPrice': 4000, 'purchasePrice': 8000},
                {'symbol': 'CLOTHING', 'sellPrice': 2892, 'purchasePrice': 5000},
                {'symbol': 'IRON_ORE', 'sellPrice': 150, 'purchasePrice': 300},
                {'symbol': 'ICE_WATER', 'sellPrice': 30, 'purchasePrice': 50},
                {'symbol': 'PLATINUM', 'sellPrice': 3000, 'purchasePrice': 5000},
                {'symbol': 'RARE_METAL', 'sellPrice': 5000, 'purchasePrice': 8000},
            ]
        }
        return response

    api.get_market = Mock(side_effect=default_market_response)
    return api


@pytest.fixture
def mock_ship_controller():
    """Mock ship controller - used by multiple features"""
    ship = Mock()
    ship.ship_symbol = "TRADER-1"
    ship.get_status = Mock(return_value={
        'nav': {
            'systemSymbol': 'X1-TEST',
            'waypointSymbol': 'X1-TEST-A1',
            'status': 'DOCKED'
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        },
        'fuel': {
            'current': 100,
            'capacity': 100
        },
        'engine': {
            'speed': 10
        }
    })

    # Track cargo state (mutable for step functions)
    cargo_state = {'units': 0, 'capacity': 40, 'inventory': []}
    ship._cargo_state = cargo_state

    def get_cargo_mock():
        return cargo_state.copy()

    ship.get_cargo = Mock(side_effect=get_cargo_mock)
    ship.dock = Mock(return_value=True)
    ship.orbit = Mock(return_value=True)

    # Track current location
    current_location = {'waypoint': 'X1-TEST-A1'}

    def mock_navigate(waypoint, flight_mode='CRUISE', auto_refuel=True):
        current_location['waypoint'] = waypoint
        ship.get_status.return_value['nav']['waypointSymbol'] = waypoint
        ship.get_status.return_value['nav']['status'] = 'IN_ORBIT'
        ship.get_status.return_value['nav']['route'] = {
            'destination': {'symbol': waypoint},
            'arrival': '2024-01-01T00:00:00Z'
        }
        return {
            'nav': {
                'route': {'arrival': '2024-01-01T00:00:00Z', 'destination': {'symbol': waypoint}},
                'waypointSymbol': waypoint,
                'status': 'IN_ORBIT'
            }
        }

    ship.navigate = Mock(side_effect=mock_navigate)

    # Mock buy function with cargo tracking
    # Use price table matching old fixture format
    price_table = {
        'COPPER': {'buy': 100, 'sell': 500},
        'IRON': {'buy': 150, 'sell': 300},
        'GOLD': {'buy': 1500, 'sell': 2000},
        'ALUMINUM': {'buy': 68, 'sell': 558},
        'ICE_WATER': {'buy': 30, 'sell': 50},
        'IRON_ORE': {'buy': 150, 'sell': 200},
        'CLOTHING': {'buy': 2892, 'sell': 3000},
        'SHIP_PLATING': {'buy': 2000, 'sell': 8000},
        'ASSAULT_RIFLES': {'buy': 3000, 'sell': 6000},
        'ADVANCED_CIRCUITRY': {'buy': 4000, 'sell': 8000}
    }

    def mock_buy(good, units, check_market_prices=True):
        # Check available cargo space
        available_space = cargo_state['capacity'] - cargo_state['units']
        units_to_buy = min(units, available_space)

        if units_to_buy > 0:
            cargo_state['units'] += units_to_buy
            # Check if good already exists in inventory
            existing_good = next((item for item in cargo_state['inventory'] if item['symbol'] == good), None)
            if existing_good:
                existing_good['units'] += units_to_buy
            else:
                cargo_state['inventory'].append({'symbol': good, 'units': units_to_buy})

        # Update get_status return value
        ship.get_status.return_value['cargo']['units'] = cargo_state['units']
        ship.get_status.return_value['cargo']['inventory'] = cargo_state['inventory'].copy()

        # Use price table to get realistic prices
        buy_price = price_table.get(good, {}).get('buy', 100)
        # Return flat structure that TradeExecutor expects
        return {
            'units': units_to_buy,
            'tradeSymbol': good,
            'totalPrice': units_to_buy * buy_price,
            'pricePerUnit': buy_price
        }

    ship.buy = Mock(side_effect=mock_buy)

    # Expose price table for step functions to modify
    ship._price_table = price_table

    # Mock sell function with cargo tracking
    def mock_sell(good, units, **kwargs):
        # Check if ship has the good in cargo
        good_in_cargo = next((item for item in cargo_state['inventory'] if item['symbol'] == good), None)
        # DEBUG: Print cargo state when selling fails
        if not good_in_cargo:
            print(f">>> DEBUG mock_sell: Good '{good}' not found in cargo. Inventory: {cargo_state['inventory']}")
        elif good_in_cargo['units'] < units:
            print(f">>> DEBUG mock_sell: Not enough cargo. Has {good_in_cargo['units']}, needs {units}")

        if not good_in_cargo or good_in_cargo['units'] < units:
            # Not enough cargo to sell
            return None

        # Update cargo state
        cargo_state['units'] = max(0, cargo_state['units'] - units)
        good_in_cargo['units'] -= units
        if good_in_cargo['units'] == 0:
            cargo_state['inventory'].remove(good_in_cargo)

        # Update get_status return value
        ship.get_status.return_value['cargo']['units'] = cargo_state['units']
        ship.get_status.return_value['cargo']['inventory'] = cargo_state['inventory'].copy()

        # Use price table to get realistic prices
        sell_price = price_table.get(good, {}).get('sell', 500)
        # Return flat structure that TradeExecutor expects
        return {
            'units': units,
            'tradeSymbol': good,
            'totalPrice': units * sell_price,
            'pricePerUnit': sell_price
        }

    ship.sell = Mock(side_effect=mock_sell)

    return ship


@pytest.fixture
def logger_instance():
    """Mock logger instance - used by multiple features"""
    logger = logging.getLogger('test_logger')
    logger.setLevel(logging.DEBUG)
    # Add memory handler to capture logs
    handler = logging.handlers.MemoryHandler(capacity=1000, target=None)
    logger.addHandler(handler)
    return logger


@pytest.fixture
def mock_logger():
    """Alias for logger_instance - used by trade_executor and route_executor"""
    logger = logging.getLogger('test_logger')
    logger.setLevel(logging.DEBUG)
    # Add memory handler to capture logs
    handler = logging.handlers.MemoryHandler(capacity=1000, target=None)
    logger.addHandler(handler)
    return logger


@pytest.fixture
def mock_ship(mock_ship_controller):
    """Alias for mock_ship_controller - used by trade_executor steps"""
    # Add price table for trade executor tests
    price_table = {
        'COPPER': {'buy': 100, 'sell': 500},
        'IRON': {'buy': 150, 'sell': 300},
        'GOLD': {'buy': 1500, 'sell': 2000},
        'ALUMINUM': {'buy': 68, 'sell': 558},
        'ICE_WATER': {'buy': 30, 'sell': 50},
        'IRON_ORE': {'buy': 150, 'sell': 200},
        'CLOTHING': {'buy': 2892, 'sell': 3000},
        'SHIP_PLATING': {'buy': 2000, 'sell': 8000},
        'ASSAULT_RIFLES': {'buy': 3000, 'sell': 6000},
        'ADVANCED_CIRCUITRY': {'buy': 4000, 'sell': 8000}
    }
    mock_ship_controller._price_table = price_table
    return mock_ship_controller


@pytest.fixture
def mock_api(mock_api_client):
    """Alias for mock_api_client - used by trade_executor steps"""
    return mock_api_client


# ===========================
# Common Background Steps
# ===========================

@given('a database with market data')
def setup_database(context, mock_database):
    """Setup database fixture for market_service Background"""
    context['database'] = mock_database


@given('a logger instance')
def setup_logger(context, logger_instance):
    """Setup logger fixture for market_service Background"""
    context['logger'] = logger_instance


@given('a mock API client')
def setup_mock_api(context, mock_api_client):
    """Setup API client for circuit_breaker Background"""
    context['api'] = mock_api_client


@given('a mock database')
def setup_mock_db(context, mock_database):
    """Setup database for trade_executor and route_executor Background"""
    context['database'] = mock_database


@given(parsers.parse('a mock ship controller for "{ship_symbol}"'))
def setup_mock_ship(context, mock_ship_controller, ship_symbol):
    """Setup ship controller for trade_executor and route_executor Background"""
    mock_ship_controller.ship_symbol = ship_symbol
    context['ship'] = mock_ship_controller

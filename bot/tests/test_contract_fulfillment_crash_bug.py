#!/usr/bin/env python3
"""
Test for contract fulfillment crash bug (navigator not passed to _acquire_initial_resources)

BUG REPORT:
The contract_operation daemon crashes silently during the purchase phase with no error logs.

ROOT CAUSE:
The navigator parameter is initialized in contract_operation() but never passed to
_acquire_initial_resources(), which calls _purchase_initial_cargo() that tries to use
navigator.execute_route(). This causes an AttributeError when navigator is None.

REPRODUCTION:
1. Start contract fulfillment operation
2. Market discovery succeeds after retries
3. Operation prints "💰 Purchasing X units from market..."
4. Crashes on navigator.execute_route(ship, market) because navigator is None
"""

import sys
from pathlib import Path
import pytest
from unittest.mock import MagicMock, Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))

from spacetraders_bot.operations.contracts import contract_operation


class TestContractFulfillmentNavigatorBug:
    """Test that navigator is properly passed to purchase operations"""

    @pytest.fixture
    def mock_api(self):
        """Mock API client"""
        api = MagicMock()

        # Mock contract details
        api.get_contract = MagicMock(return_value={
            'id': 'test-contract',
            'accepted': True,
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 100000,
                },
                'deliver': [{
                    'tradeSymbol': 'CLOTHING',
                    'unitsRequired': 26,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-JB26-A1',
                }]
            }
        })

        # Mock deliver and fulfill API calls
        api.post = MagicMock(return_value={
            'data': {
                'contract': {
                    'terms': {
                        'payment': {
                            'onFulfilled': 100000,
                        }
                    }
                }
            }
        })

        return api

    @pytest.fixture
    def mock_ship(self):
        """Mock ship controller"""
        ship = MagicMock()
        ship.get_status = MagicMock(return_value={
            'symbol': 'STARGAZER-1',
            'cargo': {
                'capacity': 40,
                'units': 0,
                'inventory': []
            },
            'nav': {
                'systemSymbol': 'X1-JB26',
                'waypointSymbol': 'X1-JB26-A2',
                'status': 'DOCKED',
            },
            'fuel': {
                'current': 400,
                'capacity': 400,
            },
            'registration': {
                'role': 'HAULER',
            },
            'frame': {
                'integrity': 1.0,
            },
        })

        # Mock buy operation
        ship.buy = MagicMock(return_value={
            'units': 26,
            'totalPrice': 31850,
        })

        # Mock dock operation
        ship.dock = MagicMock(return_value=True)

        return ship

    @pytest.fixture
    def mock_navigator(self):
        """Mock smart navigator"""
        navigator = MagicMock()
        navigator.execute_route = MagicMock(return_value=True)
        return navigator

    @pytest.fixture
    def mock_db(self):
        """Mock database with market data"""
        db = MagicMock()

        # Mock connection context manager
        conn_mock = MagicMock()
        cursor_mock = MagicMock()

        # Return market data for CLOTHING at X1-JB26-K88
        cursor_mock.fetchone = MagicMock(return_value=('X1-JB26-K88', 1225, 'ABUNDANT'))
        cursor_mock.fetchall = MagicMock(return_value=[('X1-JB26-K88', 1225, 'ABUNDANT')])

        conn_mock.execute = MagicMock(return_value=cursor_mock)
        conn_mock.__enter__ = MagicMock(return_value=conn_mock)
        conn_mock.__exit__ = MagicMock(return_value=None)

        db.connection = MagicMock(return_value=conn_mock)
        db.get_system_graph = MagicMock(return_value={
            'waypoints': {
                'X1-JB26-A1': {'x': 0, 'y': 0},
                'X1-JB26-K88': {'x': 100, 'y': 100},
            }
        })

        return db

    @pytest.fixture
    def args(self):
        """Args for contract operation"""
        return type('obj', (object,), {
            'player_id': 2,
            'ship': 'STARGAZER-1',
            'contract_id': 'cmglil35wfwwuri73t9iltrvz',
            'buy_from': None,  # Will auto-discover market
            'log_level': 'ERROR',
        })()

    def test_navigator_passed_to_purchase_operation(
        self, mock_api, mock_ship, mock_navigator, mock_db, args
    ):
        """
        Test that navigator is properly passed to _acquire_initial_resources

        BUG: navigator was initialized in contract_operation() but never passed to
        _acquire_initial_resources(), causing AttributeError when trying to navigate
        to the market for purchasing.
        """
        with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
            with patch('spacetraders_bot.operations.contracts.SmartNavigator', return_value=mock_navigator):
                with patch('spacetraders_bot.operations.contracts.Database', return_value=mock_db):
                    with patch('spacetraders_bot.operations.contracts.time.sleep', return_value=None):
                        # This should NOT crash - navigator should be used for navigation
                        result = contract_operation(
                            args,
                            api=mock_api,
                            ship=mock_ship,
                            navigator=mock_navigator,
                            db=mock_db,
                            sleep_fn=lambda x: None,  # Skip sleep
                        )

        # Should succeed
        assert result == 0

        # Verify navigator.execute_route was called to navigate to market
        mock_navigator.execute_route.assert_called()

        # Verify purchase was made
        mock_ship.buy.assert_called_with('CLOTHING', 26)

    def test_crash_without_navigator_parameter(
        self, mock_api, mock_ship, mock_db, args
    ):
        """
        Test that reproduces the crash when navigator is not passed properly

        This test demonstrates the bug: when navigator is None in _purchase_initial_cargo,
        calling navigator.execute_route() raises AttributeError.
        """
        # Create a mock navigator that will be initialized in contract_operation
        real_navigator = MagicMock()
        real_navigator.execute_route = MagicMock(return_value=True)

        with patch('spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
            with patch('spacetraders_bot.operations.contracts.SmartNavigator', return_value=real_navigator):
                with patch('spacetraders_bot.operations.contracts.Database', return_value=mock_db):
                    with patch('spacetraders_bot.operations.contracts.time.sleep', return_value=None):
                        # Call without passing navigator - should work now after fix
                        result = contract_operation(
                            args,
                            api=mock_api,
                            ship=mock_ship,
                            navigator=None,  # Not passed - will be initialized inside
                            db=mock_db,
                            sleep_fn=lambda x: None,
                        )

        # Should succeed after fix (navigator initialized internally)
        assert result == 0

        # Verify navigator was used
        real_navigator.execute_route.assert_called()


if __name__ == '__main__':
    pytest.main([__file__, '-v'])

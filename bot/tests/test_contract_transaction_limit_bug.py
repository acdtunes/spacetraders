#!/usr/bin/env python3
"""
Test for contract fulfillment bug: transaction limits not handled in purchase_units_for_trip

BUG REPORT:
- Contract requires 22 units of POLYNUCLEOTIDES
- Market has 20 unit per-transaction limit
- Code tries to buy all 22 in one transaction
- Transaction fails with error 4604

EXPECTED:
- Detect transaction limit from error or market data
- Split purchases into multiple transactions (20 + 2)
- Successfully acquire all required units
"""

import sys
from pathlib import Path

# Add lib to path
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src' / 'spacetraders_bot'))

import pytest
from unittest.mock import Mock, patch
from operations.contracts import contract_operation


def regression_purchase_splits_when_exceeding_transaction_limit():
    """
    Test that contract fulfillment splits purchases when units exceed transaction limit

    Scenario:
    - Contract requires 22 units POLYNUCLEOTIDES
    - Market has 20 unit transaction limit
    - Ship has 40 cargo capacity (can hold all 22)
    - First purchase: 22 units (should fail with 4604)
    - Fallback: Should split into 20 + 2 units
    """
    # Mock API client
    mock_api = Mock()

    # Track purchase calls
    purchase_calls = []

    def mock_post(endpoint, data=None):
        """Mock API post - simulate transaction limit error"""
        # Track calls
        if '/purchase' in endpoint:
            purchase_calls.append({
                'endpoint': endpoint,
                'symbol': data.get('symbol'),
                'units': data.get('units')
            })

            # Simulate transaction limit error on first attempt
            if data.get('units') > 20:
                return {
                    'error': {
                        'code': 4604,
                        'message': 'Trade good POLYNUCLEOTIDES has a limit of 20 units per transaction'
                    }
                }

            # Simulate successful purchase for <=20 units
            units = data.get('units')
            ship_status['cargo']['units'] += units
            return {
                'data': {
                    'transaction': {
                        'units': units,
                        'tradeSymbol': data.get('symbol'),
                        'totalPrice': units * 100,
                        'pricePerUnit': 100
                    }
                }
            }

        # Mock delivery endpoint
        if '/deliver' in endpoint:
            delivered_units = data.get('units', 0) if data else 0
            contract_data['terms']['deliver'][0]['unitsFulfilled'] += delivered_units
            return {
                'data': {
                    'contract': contract_data
                }
            }

        # Mock fulfill endpoint
        if '/fulfill' in endpoint:
            contract_data['fulfilled'] = True
            return {
                'data': {
                    'contract': contract_data
                }
            }

        # Other endpoints (navigate, dock, etc.)
        return {'data': {}}

    mock_api.post = mock_post
    mock_api.request = Mock(return_value={'data': {}})

    # Mock ship API responses
    ship_status = {
        'nav': {
            'waypointSymbol': 'X1-GH18-E48',
            'systemSymbol': 'X1-GH18',
            'status': 'DOCKED'
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        }
    }

    # Mock get_ship to return ship status
    mock_api.get_ship = Mock(return_value=ship_status)

    # Use REAL ShipController (not mock) so it uses the fixed buy() logic
    from src.spacetraders_bot.core.ship_controller import ShipController
    real_ship = ShipController(mock_api, 'SILMARETH-1')

    # Override the log method to suppress output
    real_ship.log = lambda msg, level='INFO': None

    # Mock navigator
    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(return_value=True)

    # Mock contract
    contract_data = {
        'id': 'test-contract',
        'accepted': True,
        'terms': {
            'deliver': [{
                'tradeSymbol': 'POLYNUCLEOTIDES',
                'destinationSymbol': 'X1-GH18-A1',
                'unitsRequired': 22,
                'unitsFulfilled': 0
            }],
            'payment': {
                'onAccepted': 10000,
                'onFulfilled': 50000
            }
        }
    }

    mock_api.get_contract = Mock(return_value=contract_data)

    # Mock args
    args = Mock()
    args.ship = 'SILMARETH-1'
    args.contract_id = 'test-contract'
    args.buy_from = 'X1-GH18-E48'
    args.player_id = 1
    args.log_level = 'WARNING'

    # Create mock database with proper connection mock
    mock_db = Mock()

    # Mock the database connection context manager
    class MockConnection:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            pass

        def execute(self, *args):
            # Return a mock cursor with fetchone
            mock_cursor = Mock()
            # Return market data for resource acquisition strategy
            mock_cursor.fetchone.return_value = None  # No market data (use buy_from arg)
            return mock_cursor

    mock_db.connection.return_value = MockConnection()

    # Run contract operation
    with patch('operations.contracts.Database', return_value=mock_db):
        with patch('operations.contracts.setup_logging', return_value='test.log'):
            with patch('operations.contracts.get_api_client', return_value=mock_api):
                with patch('operations.contracts.ShipController', return_value=real_ship):
                    with patch('operations.contracts.SmartNavigator', return_value=mock_navigator):
                        with patch('operations.contracts.get_captain_logger', return_value=Mock()):
                            # This should NOW PASS with the fix - buy() handles 4604 automatically
                            result = contract_operation(
                                args,
                                api=mock_api,
                                ship=real_ship,
                                navigator=mock_navigator,
                                db=mock_db,
                                sleep_fn=lambda x: None  # Skip sleep in tests
                            )

    # ASSERTIONS

    # Current behavior: Only 1 purchase call (22 units, which fails)
    # Expected behavior: 2 purchase calls (20 units + 2 units)
    print(f"\nPurchase calls made: {len(purchase_calls)}")
    for i, call in enumerate(purchase_calls):
        print(f"  Call {i+1}: {call['units']} units of {call['symbol']}")

    # EXPECTED: Should have made multiple purchase calls
    # Pattern should be: 22 (fails) → 20 (succeeds) → 2 (succeeds) for each acquisition
    assert len(purchase_calls) >= 3, \
        f"Expected at least 3 purchase calls (22 fail, 20 success, 2 success), got {len(purchase_calls)}"

    # Find the successful batched purchase calls (not the failed 22-unit attempts)
    successful_calls = [call for call in purchase_calls if call['units'] <= 20]

    # EXPECTED: Should have at least 2 successful batched calls (20 and 2)
    assert len(successful_calls) >= 2, \
        f"Expected at least 2 successful batched calls, got {len(successful_calls)}"

    # EXPECTED: Should have 20-unit batch and 2-unit batch
    batch_sizes = [call['units'] for call in successful_calls]
    assert 20 in batch_sizes, f"Expected 20-unit batch in {batch_sizes}"
    assert 2 in batch_sizes, f"Expected 2-unit batch in {batch_sizes}"

    print(f"\n✅ Transaction limit handling PASSED:")
    print(f"   - Initial 22-unit purchase failed correctly")
    print(f"   - Automatic retry with 20-unit limit succeeded")
    print(f"   - Remaining 2 units purchased in second batch")


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])

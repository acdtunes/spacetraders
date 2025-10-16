"""
Tests for contract price polling feature.

Tests the new wait_for_profitable_price() functionality that polls market data
waiting for contract profitability criteria to be met before resource acquisition.
"""

import pytest
from unittest.mock import Mock, patch
from spacetraders_bot.operations.contracts import ResourceAcquisitionStrategy, evaluate_contract_profitability
from spacetraders_bot.core.database import Database


@pytest.fixture
def mock_database():
    """Create a mock database for testing."""
    return Mock(spec=Database)


@pytest.fixture
def mock_contract():
    """Create a mock contract for testing."""
    return {
        'id': 'contract-123',
        'terms': {
            'payment': {
                'onAccepted': 10000,
                'onFulfilled': 50000
            },
            'deliver': [{
                'tradeSymbol': 'IRON_ORE',
                'unitsRequired': 100,
                'unitsFulfilled': 0,
                'destinationSymbol': 'X1-TEST-HQ'
            }]
        }
    }


@pytest.fixture
def strategy_args(mock_database):
    """Common args for ResourceAcquisitionStrategy."""
    return {
        'trade_symbol': 'IRON_ORE',
        'system': 'X1-TEST',
        'database': mock_database,
        'log_error': Mock(),
        'sleep_fn': Mock(),
        'print_fn': Mock(),
        'max_retries': 12,
        'retry_interval_seconds': 300,
    }


class TestWaitForProfitablePrice:
    """Tests for wait_for_profitable_price() method."""

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_immediately_profitable_no_polling(self, mock_find_markets, strategy_args, mock_contract):
        """
        Test that when contract is immediately profitable, no polling occurs.

        Scenario:
        - Contract requires IRON_ORE at low price (profitable)
        - Should return immediately without polling
        """
        strategy = ResourceAcquisitionStrategy(**strategy_args)

        # Mock market data showing profitable price (100 cr/unit)
        mock_find_markets.return_value = [{
            'waypoint_symbol': 'X1-TEST-M1',
            'good_symbol': 'IRON_ORE',
            'purchase_price': 100,  # What we pay to buy
            'sell_price': 150,
            'supply': 'HIGH',
            'activity': 'STRONG',
            'trade_volume': 20,
            'last_updated': '2025-10-14T12:00:00Z'
        }]

        # Check profitability: payment=60,000, cost=100*100+300=10,300, profit=49,700 (profitable)
        result = strategy.wait_for_profitable_price(
            contract=mock_contract,
            cargo_capacity=40,
            system='X1-TEST'
        )

        # Should succeed without polling
        assert result is True
        strategy.sleep_fn.assert_not_called()  # No wait
        strategy.print_fn.assert_any_call("  ✅ Price is profitable! Proceeding with acquisition...")

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_unprofitable_becomes_profitable_after_polling(self, mock_find_markets, strategy_args, mock_contract):
        """
        Test that unprofitable contract waits and succeeds when price drops.

        Scenario:
        - Initial price too high (unprofitable)
        - After 1 retry, price drops (profitable)
        - Should poll once, then succeed
        """
        strategy = ResourceAcquisitionStrategy(**strategy_args)

        # First call: expensive (5000 cr/unit - unprofitable)
        # Second call: cheap (100 cr/unit - profitable)
        mock_find_markets.side_effect = [
            [{'purchase_price': 5000, 'good_symbol': 'IRON_ORE'}],  # Expensive
            [{'purchase_price': 100, 'good_symbol': 'IRON_ORE'}],   # Cheap
        ]

        result = strategy.wait_for_profitable_price(
            contract=mock_contract,
            cargo_capacity=40,
            system='X1-TEST'
        )

        # Should succeed after polling
        assert result is True
        assert strategy.sleep_fn.call_count == 1  # Polled once
        strategy.sleep_fn.assert_called_with(300)  # 5 minute intervals

        # Check logging
        strategy.print_fn.assert_any_call("  ⏳ Current price unprofitable, waiting for price drop...")
        strategy.print_fn.assert_any_call("  ✅ Price now profitable! Proceeding with acquisition...")

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_timeout_after_max_retries(self, mock_find_markets, strategy_args, mock_contract):
        """
        Test that polling times out after max_retries and executes anyway.

        Scenario:
        - Price stays high (unprofitable) for all retries
        - After 12 retries (1 hour), timeout and execute anyway
        """
        strategy = ResourceAcquisitionStrategy(**strategy_args)

        # Always return expensive price
        mock_find_markets.return_value = [{'purchase_price': 5000, 'good_symbol': 'IRON_ORE'}]

        result = strategy.wait_for_profitable_price(
            contract=mock_contract,
            cargo_capacity=40,
            system='X1-TEST'
        )

        # Should timeout and execute anyway
        assert result is True  # Still executes
        assert strategy.sleep_fn.call_count == 12  # All retries exhausted

        # Check timeout logging (check for the first line of the timeout message)
        strategy.print_fn.assert_any_call(
            "\n  ⚠️  Timeout - executing anyway to avoid contract expiration"
        )

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_profitability_criteria_min_profit(self, mock_find_markets, strategy_args):
        """
        Test that min_profit threshold is correctly enforced.

        Scenario:
        - Contract with net profit = 4,000 cr (below 5,000 minimum)
        - Should be unprofitable
        """
        strategy = ResourceAcquisitionStrategy(**strategy_args)

        # Contract with low payment (profitable=False due to min_profit)
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 1000,
                    'onFulfilled': 8000  # Total: 9,000
                },
                'deliver': [{
                    'tradeSymbol': 'IRON_ORE',
                    'unitsRequired': 100,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-TEST-HQ'
                }]
            }
        }

        # Price: 50 cr/unit = 5,000 total cost + 300 fuel = 5,300
        # Net profit: 9,000 - 5,300 = 3,700 (below 5,000 minimum)
        mock_find_markets.return_value = [{'purchase_price': 50, 'good_symbol': 'IRON_ORE'}]

        result = strategy.wait_for_profitable_price(
            contract=contract,
            cargo_capacity=40,
            system='X1-TEST'
        )

        # Should timeout (unprofitable on first check, never becomes profitable)
        assert result is True  # Executes anyway after timeout
        assert strategy.sleep_fn.call_count == 12

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_no_market_data_uses_conservative_estimate(self, mock_find_markets, strategy_args, mock_contract):
        """
        Test that when no market data available, uses conservative 5000 cr/unit estimate.

        Scenario:
        - Market database returns empty list (no price data)
        - Should use 5000 cr/unit conservative estimate
        - Likely unprofitable (5000 * 100 = 500,000 cost)
        """
        strategy = ResourceAcquisitionStrategy(**strategy_args)

        # No market data
        mock_find_markets.return_value = []

        result = strategy.wait_for_profitable_price(
            contract=mock_contract,
            cargo_capacity=40,
            system='X1-TEST'
        )

        # Should timeout (conservative estimate likely unprofitable)
        assert result is True
        assert strategy.sleep_fn.call_count == 12

        # Check that conservative estimate was used
        strategy.print_fn.assert_any_call("  ⏳ Current price unprofitable, waiting for price drop...")


class TestEvaluateContractProfitability:
    """Tests for evaluate_contract_profitability helper function."""

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_profitable_contract(self, mock_find_markets, mock_database):
        """Test contract that meets profitability criteria."""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 10000,
                    'onFulfilled': 50000
                },
                'deliver': [{
                    'tradeSymbol': 'IRON_ORE',
                    'unitsRequired': 100,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-TEST-HQ'
                }]
            }
        }

        # Mock market data: 100 cr/unit
        mock_find_markets.return_value = [{'purchase_price': 100, 'good_symbol': 'IRON_ORE'}]

        is_profitable, reason, metrics = evaluate_contract_profitability(
            contract=contract,
            cargo_capacity=40,
            system='X1-TEST',
            db=mock_database
        )

        # Payment: 60,000
        # Cost: 100 * 100 = 10,000 (purchase) + 300 (fuel) = 10,300
        # Profit: 49,700 (meets >5,000)
        # ROI: 482% (meets >5%)
        assert is_profitable is True
        assert metrics['net_profit'] >= 5000
        assert metrics['roi'] >= 5.0

    @patch('spacetraders_bot.operations.contracts.find_markets_selling')
    def test_unprofitable_contract_low_profit(self, mock_find_markets, mock_database):
        """Test contract that fails min_profit threshold."""
        contract = {
            'terms': {
                'payment': {
                    'onAccepted': 1000,
                    'onFulfilled': 5000
                },
                'deliver': [{
                    'tradeSymbol': 'IRON_ORE',
                    'unitsRequired': 100,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-TEST-HQ'
                }]
            }
        }

        # Mock market data: 20 cr/unit
        mock_find_markets.return_value = [{'purchase_price': 20, 'good_symbol': 'IRON_ORE'}]

        is_profitable, reason, metrics = evaluate_contract_profitability(
            contract=contract,
            cargo_capacity=40,
            system='X1-TEST',
            db=mock_database
        )

        # Payment: 6,000
        # Cost: 20 * 100 = 2,000 + 300 = 2,300
        # Profit: 3,700 (fails <5,000)
        assert is_profitable is False
        assert "Net profit" in reason
        assert metrics['net_profit'] < 5000

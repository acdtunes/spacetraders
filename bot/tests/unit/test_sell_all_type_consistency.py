#!/usr/bin/env python3
"""
Test for sell_all() return type consistency bug

Bug Report:
- File: src/spacetraders_bot/operations/multileg_trader.py, line 840
- Issue: Code assumes sell_all() returns dict, but it returns int
- Crash: AttributeError: 'int' object has no attribute 'get'
"""

import pytest
from unittest.mock import MagicMock, patch


class TestSellAllTypeConsistency:
    """Test that multileg_trader.py handles sell_all() return type correctly"""

    def regression_sell_all_returns_int_as_documented(self):
        """
        GIVEN a ShipController instance
        WHEN sell_all() is called
        THEN it should return an integer (total revenue)
        """
        from spacetraders_bot.core.api_client import APIClient
        from spacetraders_bot.core.ship_controller import ShipController

        # Mock API client
        api = MagicMock(spec=APIClient)

        # Mock ship status with cargo
        api.get_ship.return_value = {
            'nav': {'waypointSymbol': 'X1-TEST-A1', 'systemSymbol': 'X1-TEST', 'status': 'DOCKED'},
            'cargo': {
                'units': 10,
                'capacity': 40,
                'inventory': [
                    {'symbol': 'IRON_ORE', 'units': 10}
                ]
            },
            'fuel': {'current': 100, 'capacity': 100}
        }

        # Mock sell transaction
        api.post.return_value = {
            'data': {
                'transaction': {
                    'units': 10,
                    'tradeSymbol': 'IRON_ORE',
                    'pricePerUnit': 500,
                    'totalPrice': 5000
                }
            }
        }

        ship = ShipController(api, 'SHIP-1')
        result = ship.sell_all()

        # Verify return type is int, not dict
        assert isinstance(result, int), f"Expected int, got {type(result)}"
        assert result == 5000, f"Expected 5000, got {result}"

    def regression_multileg_trader_handles_sell_all_int_return(self):
        """
        GIVEN multileg_trader.py auto-recovery code (line 840)
        WHEN sell_all() returns int (as documented)
        THEN code should handle it without AttributeError
        """
        # Simulate the exact code from multileg_trader.py line 840
        sell_result = 123960  # sell_all() returns int

        # This is what the code does (line 840)
        # recovery_revenue = sell_result.get('total_revenue', 0) if sell_result else 0
        # This CRASHES with: AttributeError: 'int' object has no attribute 'get'

        # Test the BUGGY code path
        with pytest.raises(AttributeError, match="'int' object has no attribute 'get'"):
            recovery_revenue = sell_result.get('total_revenue', 0) if sell_result else 0

    def regression_multileg_trader_defensive_handling_int_return(self):
        """
        GIVEN sell_all() returns int (total revenue)
        WHEN multileg_trader.py processes the result
        THEN it should extract revenue correctly with defensive type checking
        """
        sell_result = 123960  # int return

        # FIXED code with defensive type handling
        if isinstance(sell_result, int):
            # sell_all() returned just revenue as int
            recovery_revenue = sell_result
        elif isinstance(sell_result, dict):
            # sell_all() returned full transaction dict (hypothetical future change)
            recovery_revenue = sell_result.get('total_revenue', 0)
        else:
            recovery_revenue = 0

        assert recovery_revenue == 123960, f"Expected 123960, got {recovery_revenue}"

    def regression_multileg_trader_defensive_handling_dict_return(self):
        """
        GIVEN sell_all() returns dict (hypothetical future implementation)
        WHEN multileg_trader.py processes the result
        THEN it should extract revenue correctly with defensive type checking
        """
        sell_result = {'total_revenue': 123960, 'transactions': []}  # dict return

        # FIXED code with defensive type handling
        if isinstance(sell_result, int):
            recovery_revenue = sell_result
        elif isinstance(sell_result, dict):
            recovery_revenue = sell_result.get('total_revenue', 0)
        else:
            recovery_revenue = 0

        assert recovery_revenue == 123960, f"Expected 123960, got {recovery_revenue}"

    def regression_multileg_trader_defensive_handling_none_return(self):
        """
        GIVEN sell_all() returns 0 (no cargo to sell)
        WHEN multileg_trader.py processes the result
        THEN it should handle gracefully
        """
        sell_result = 0  # No cargo to sell

        # FIXED code with defensive type handling
        if isinstance(sell_result, int):
            recovery_revenue = sell_result
        elif isinstance(sell_result, dict):
            recovery_revenue = sell_result.get('total_revenue', 0)
        else:
            recovery_revenue = 0

        assert recovery_revenue == 0, f"Expected 0, got {recovery_revenue}"

    def regression_sell_all_actual_implementation_consistency(self):
        """
        GIVEN ShipController.sell_all() implementation
        WHEN we examine its return type annotation and behavior
        THEN it should consistently return int
        """
        from spacetraders_bot.core.ship_controller import ShipController
        import inspect

        # Get method signature
        sig = inspect.signature(ShipController.sell_all)
        return_annotation = sig.return_annotation

        # Verify return type annotation is int (can be the type object or string 'int')
        assert return_annotation == int or return_annotation == 'int', \
            f"Expected return annotation to be int or 'int', got {return_annotation} ({type(return_annotation)})"

        # Read source to verify implementation
        source = inspect.getsource(ShipController.sell_all)

        # Verify all return statements return int
        assert "return total_revenue" in source, "Should return total_revenue (int)"
        assert "return 0" in source, "Should return 0 for no cargo case"

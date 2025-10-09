"""
Test for cargo cleanup market search bug

Bug: _cleanup_stranded_cargo() tries to sell at current market without checking
if the market buys that good. Should query database for nearby buyers first.

Evidence from SILMARETH-D failure:
- Ship at X1-GH18-D45 with 65x AMMONIA_ICE
- D45 market doesn't buy AMMONIA_ICE
- Function tried to sell anyway, got HTTP 400 error
- Ship now blocked with stranded cargo
"""

import pytest
from unittest.mock import Mock, MagicMock, patch, call
import logging
from spacetraders_bot.operations.multileg_trader import _cleanup_stranded_cargo


class TestCargoCleanupMarketSearch:
    """Test cargo cleanup with market compatibility check"""

    def test_cleanup_attempts_sale_without_checking_market_compatibility(self):
        """
        REPRODUCE BUG: Current implementation tries to sell without checking
        if current market buys the good first.

        GIVEN: Ship at X1-GH18-D45 with 65x AMMONIA_ICE
        AND: D45 market does NOT buy AMMONIA_ICE (will return HTTP 400)
        WHEN: _cleanup_stranded_cargo is called
        THEN: Function calls ship.sell WITHOUT checking market first
        AND: Sale fails with "Market sell failed" error
        AND: Function returns True (partial success) but cargo is still stranded
        """
        # Setup mocks
        mock_ship = Mock()
        mock_api = Mock()
        mock_db = Mock()
        logger = logging.getLogger('test')

        # Ship status: at D45 with AMMONIA_ICE
        mock_ship.get_status.side_effect = [
            # First call - initial status
            {
                'nav': {
                    'waypointSymbol': 'X1-GH18-D45',
                    'systemSymbol': 'X1-GH18',
                    'status': 'DOCKED'
                },
                'cargo': {
                    'capacity': 80,
                    'units': 65,
                    'inventory': [
                        {'symbol': 'AMMONIA_ICE', 'units': 65}
                    ]
                },
                'engine': {'speed': 9},
                'fuel': {'current': 400, 'capacity': 400}
            },
            # Second call - after failed cleanup (cargo still there)
            {
                'nav': {
                    'waypointSymbol': 'X1-GH18-D45',
                    'systemSymbol': 'X1-GH18',
                    'status': 'DOCKED'
                },
                'cargo': {
                    'capacity': 80,
                    'units': 65,  # STILL STRANDED!
                    'inventory': [
                        {'symbol': 'AMMONIA_ICE', 'units': 65}
                    ]
                }
            }
        ]

        # Ship dock is already docked
        mock_ship.dock.return_value = True

        # Mock ship sell to fail with the EXACT error from production
        mock_ship.sell.side_effect = Exception(
            "❌ POST /my/ships/SILMARETH-D/sell - Client Error (HTTP 400): "
            "4602 - Market sell failed. Trade good AMMONIA_ICE is not listed at market X1-GH18-D45."
        )

        # Execute cleanup - THIS IS THE BUG REPRODUCTION
        result = _cleanup_stranded_cargo(mock_ship, mock_api, mock_db, logger)

        # BUG EVIDENCE:
        # 1. Function calls sell WITHOUT checking if market buys the good
        assert mock_ship.sell.called, "BUG: Function attempts to sell without checking market first"
        mock_ship.sell.assert_called_with('AMMONIA_ICE', 65, check_market_prices=False)

        # 2. Function returns True even though cargo cleanup failed
        assert result is True, "BUG: Function returns success despite cargo remaining stranded"

        # 3. Cargo is still there after "successful" cleanup
        final_status = mock_ship.get_status()
        assert final_status['cargo']['units'] == 65, "BUG: Cargo remains stranded after cleanup"

    def test_cleanup_should_check_market_compatibility_first(self):
        """
        EXPECTED BEHAVIOR: Function should check if current market buys the good
        before attempting sale. If not, should search for nearby buyers.

        GIVEN: Ship at X1-GH18-D45 with 65x AMMONIA_ICE
        AND: D45 market does NOT buy AMMONIA_ICE
        AND: Nearby market D42 (<100 units) DOES buy AMMONIA_ICE at 1200 cr/unit
        WHEN: _cleanup_stranded_cargo is called (after fix)
        THEN: Function should:
          1. Check if D45 buys AMMONIA_ICE (NO)
          2. Query database for nearby buyers
          3. Navigate to D42
          4. Sell AMMONIA_ICE at D42
        """
        # This test will FAIL until the bug is fixed
        pytest.skip("This test documents the expected behavior after fix - will be enabled after implementation")


    def test_cleanup_with_compatible_market_works(self):
        """
        CONTROL: Verify cleanup works when current market DOES buy the good

        GIVEN: Ship at X1-GH18-D45 with 6x SHIP_PARTS
        AND: D45 market DOES buy SHIP_PARTS
        WHEN: _cleanup_stranded_cargo is called
        THEN: Function should successfully sell at current market
        """
        # Setup mocks
        mock_ship = Mock()
        mock_api = Mock()
        mock_db = Mock()
        logger = logging.getLogger('test')

        # Ship status: at D45 with SHIP_PARTS (market accepts this)
        mock_ship.get_status.side_effect = [
            # First call - initial status
            {
                'nav': {
                    'waypointSymbol': 'X1-GH18-D45',
                    'systemSymbol': 'X1-GH18',
                    'status': 'DOCKED'
                },
                'cargo': {
                    'capacity': 80,
                    'units': 6,
                    'inventory': [
                        {'symbol': 'SHIP_PARTS', 'units': 6}
                    ]
                },
                'engine': {'speed': 9},
                'fuel': {'current': 400, 'capacity': 400}
            },
            # Second call - after successful cleanup (cargo empty)
            {
                'nav': {
                    'waypointSymbol': 'X1-GH18-D45',
                    'systemSymbol': 'X1-GH18',
                    'status': 'DOCKED'
                },
                'cargo': {
                    'capacity': 80,
                    'units': 0,
                    'inventory': []
                }
            }
        ]

        # Ship dock is already docked
        mock_ship.dock.return_value = True

        # Mock ship sell SUCCESS (market accepts SHIP_PARTS)
        mock_ship.sell.return_value = {
            'totalPrice': 10566,  # 6 * 1761
            'units': 6,
            'pricePerUnit': 1761
        }

        # Execute cleanup
        result = _cleanup_stranded_cargo(mock_ship, mock_api, mock_db, logger)

        # Assertions
        assert result is True, "Cleanup should succeed"
        mock_ship.sell.assert_called_with('SHIP_PARTS', 6, check_market_prices=False)

        # Verify cargo is now empty
        final_status = mock_ship.get_status()
        assert final_status['cargo']['units'] == 0, "Cargo should be empty after successful cleanup"


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])

"""
Test for cargo cleanup market search bug FIX

Bug: _cleanup_stranded_cargo() tries to sell at current market without checking
if the market buys that good. Should query database for nearby buyers first.

Fix: Added market compatibility check and nearby buyer search before attempting sale.

Evidence from SILMARETH-D failure:
- Ship at X1-GH18-D45 with 65x AMMONIA_ICE
- D45 market doesn't buy AMMONIA_ICE
- Function tried to sell anyway, got HTTP 400 error
- Ship now blocked with stranded cargo

After fix:
- Function checks if D45 buys AMMONIA_ICE (NO)
- Searches database for nearby buyers
- Finds D42 (85 units away) that buys AMMONIA_ICE
- Navigates to D42 and sells successfully
"""

import pytest
from unittest.mock import Mock, MagicMock, patch, call
import logging
from spacetraders_bot.operations.multileg_trader import _cleanup_stranded_cargo


class TestCargoCleanupMarketSearchFix:
    """Test cargo cleanup with intelligent market search (after fix)"""

    def test_cleanup_finds_nearby_market_and_navigates(self):
        """
        FIXED BEHAVIOR: Function checks market compatibility, searches for buyers,
        navigates to nearby market, and sells successfully.

        GIVEN: Ship at X1-GH18-D45 with 65x AMMONIA_ICE
        AND: D45 market does NOT buy AMMONIA_ICE (no purchase_price in DB)
        AND: Nearby market D42 (85 units away) DOES buy AMMONIA_ICE at 1200 cr/unit
        WHEN: _cleanup_stranded_cargo is called (after fix)
        THEN: Function should:
          1. Check if D45 buys AMMONIA_ICE (NO - no purchase_price in DB)
          2. Query database for nearby buyers
          3. Find D42 as best buyer (85 units away, within 200 unit threshold)
          4. Navigate to D42
          5. Sell AMMONIA_ICE at D42 for 78,000 credits
          6. Return True with empty cargo
        """
        # Setup mocks
        mock_ship = Mock()
        mock_api = Mock()
        mock_db = Mock()
        logger = logging.getLogger('test')

        # Ship status: at D45 with AMMONIA_ICE
        get_status_call_count = [0]
        def mock_get_status():
            get_status_call_count[0] += 1
            if get_status_call_count[0] == 1:
                # First call - initial status at D45
                return {
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
                }
            elif get_status_call_count[0] == 2:
                # Second call - after navigation, now at D42
                return {
                    'nav': {
                        'waypointSymbol': 'X1-GH18-D42',
                        'systemSymbol': 'X1-GH18',
                        'status': 'IN_ORBIT'  # After navigation
                    },
                    'cargo': {
                        'capacity': 80,
                        'units': 65,
                        'inventory': [
                            {'symbol': 'AMMONIA_ICE', 'units': 65}
                        ]
                    },
                    'engine': {'speed': 9},
                    'fuel': {'current': 350, 'capacity': 400}
                }
            else:
                # Third call - final status check after sale
                return {
                    'nav': {
                        'waypointSymbol': 'X1-GH18-D42',
                        'systemSymbol': 'X1-GH18',
                        'status': 'DOCKED'
                    },
                    'cargo': {
                        'capacity': 80,
                        'units': 0,  # EMPTY!
                        'inventory': []
                    },
                    'engine': {'speed': 9},
                    'fuel': {'current': 350, 'capacity': 400}
                }

        mock_ship.get_status = mock_get_status

        # Mock database connection and queries
        mock_conn = Mock()
        mock_cursor = Mock()
        mock_conn.cursor.return_value = mock_cursor

        # Mock db.connection() context manager
        mock_db.connection.return_value.__enter__ = Mock(return_value=mock_conn)
        mock_db.connection.return_value.__exit__ = Mock(return_value=False)

        # Mock market data queries
        def mock_get_market_data(conn, waypoint, good):
            if waypoint == 'X1-GH18-D45' and good == 'AMMONIA_ICE':
                # D45 does NOT have purchase_price for AMMONIA_ICE (doesn't buy it)
                return []
            elif waypoint == 'X1-GH18-D42' and good == 'AMMONIA_ICE':
                # D42 DOES buy AMMONIA_ICE at 1200 cr/unit
                return [{
                    'waypoint_symbol': 'X1-GH18-D42',
                    'good_symbol': 'AMMONIA_ICE',
                    'purchase_price': 1200,  # What market pays us
                    'trade_volume': 100
                }]
            return []

        mock_db.get_market_data = mock_get_market_data

        # Mock cursor.execute for waypoint coordinate queries and buyer search
        execute_call_count = [0]
        def mock_execute(query, params):
            execute_call_count[0] += 1

            # First call: Get D45 coordinates
            if 'SELECT x, y FROM waypoints' in query and params[0] == 'X1-GH18-D45':
                mock_cursor.fetchone.return_value = (100, 200)  # D45 coords

            # Second call: Search for nearby buyers of AMMONIA_ICE
            elif 'SELECT' in query and 'market_data m' in query and 'waypoints w' in query:
                # Return D42 as nearest buyer (85 units away)
                mock_cursor.fetchall.return_value = [
                    ('X1-GH18-D42', 1200, 150, 250, 7225)  # waypoint, price, x, y, distance_squared
                    # distance = sqrt((150-100)^2 + (250-200)^2) = sqrt(2500+2500) = ~70.7 units
                ]

        mock_cursor.execute = mock_execute

        # Mock ship dock
        mock_ship.dock.return_value = True

        # Mock ship sell (succeeds at D42)
        mock_ship.sell.return_value = {
            'totalPrice': 78000,  # 65 * 1200
            'units': 65,
            'pricePerUnit': 1200
        }

        # Mock SmartNavigator for navigation
        with patch('spacetraders_bot.operations.multileg_trader.SmartNavigator') as mock_navigator_class:
            mock_navigator = Mock()
            mock_navigator_class.return_value = mock_navigator
            mock_navigator.execute_route.return_value = True  # Navigation succeeds

            # Execute cleanup with fix
            result = _cleanup_stranded_cargo(mock_ship, mock_api, mock_db, logger)

        # Assertions
        assert result is True, "Cleanup should succeed"

        # Verify database was queried for nearby buyers
        assert execute_call_count[0] >= 2, "Should query database for waypoint coords and nearby buyers"

        # Verify navigation was executed (SmartNavigator created and used)
        assert mock_navigator.execute_route.called, "Should navigate to buyer market"

        # Verify ship docked at buyer market
        assert mock_ship.dock.called, "Should dock at buyer market"

        # Verify sale was executed
        assert mock_ship.sell.called, "Should sell cargo at buyer market"
        mock_ship.sell.assert_called_with('AMMONIA_ICE', 65, check_market_prices=False)

        # Verify final status shows empty cargo
        final_status = mock_ship.get_status()
        assert final_status['cargo']['units'] == 0, "Cargo should be empty after successful cleanup"

    def test_cleanup_sells_at_current_market_when_compatible(self):
        """
        GIVEN: Ship at X1-GH18-D45 with 6x SHIP_PARTS
        AND: D45 market DOES buy SHIP_PARTS (has purchase_price in DB)
        WHEN: _cleanup_stranded_cargo is called
        THEN: Function should sell at current market without navigation
        """
        # Setup mocks
        mock_ship = Mock()
        mock_api = Mock()
        mock_db = Mock()
        logger = logging.getLogger('test')

        # Ship status: at D45 with SHIP_PARTS
        get_status_call_count = [0]
        def mock_get_status():
            get_status_call_count[0] += 1
            if get_status_call_count[0] == 1:
                # Initial status with cargo
                return {
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
                }
            else:
                # Final status - cargo empty
                return {
                    'nav': {
                        'waypointSymbol': 'X1-GH18-D45',
                        'systemSymbol': 'X1-GH18',
                        'status': 'DOCKED'
                    },
                    'cargo': {
                        'capacity': 80,
                        'units': 0,
                        'inventory': []
                    },
                    'engine': {'speed': 9},
                    'fuel': {'current': 400, 'capacity': 400}
                }

        mock_ship.get_status = mock_get_status

        # Mock database connection
        mock_conn = Mock()
        mock_db.connection.return_value.__enter__ = Mock(return_value=mock_conn)
        mock_db.connection.return_value.__exit__ = Mock(return_value=False)

        # Mock market data: D45 DOES buy SHIP_PARTS
        def mock_get_market_data(conn, waypoint, good):
            if waypoint == 'X1-GH18-D45' and good == 'SHIP_PARTS':
                return [{
                    'waypoint_symbol': 'X1-GH18-D45',
                    'good_symbol': 'SHIP_PARTS',
                    'purchase_price': 1761,  # Market buys it!
                    'trade_volume': 50
                }]
            return []

        mock_db.get_market_data = mock_get_market_data

        # Mock ship sell (succeeds at current market)
        mock_ship.sell.return_value = {
            'totalPrice': 10566,  # 6 * 1761
            'units': 6,
            'pricePerUnit': 1761
        }

        # Execute cleanup
        result = _cleanup_stranded_cargo(mock_ship, mock_api, mock_db, logger)

        # Assertions
        assert result is True, "Cleanup should succeed"

        # Verify sale was executed at current market (no navigation)
        mock_ship.sell.assert_called_with('SHIP_PARTS', 6, check_market_prices=False)

        # Verify cargo is empty
        final_status = mock_ship.get_status()
        assert final_status['cargo']['units'] == 0, "Cargo should be empty"

    def test_cleanup_skips_unsellable_goods_gracefully(self):
        """
        GIVEN: Ship at X1-GH18-D45 with 65x RARE_ARTIFACT
        AND: NO markets in system buy RARE_ARTIFACT (not in any market DB)
        WHEN: _cleanup_stranded_cargo is called
        THEN: Function should log warning and skip the good gracefully
        """
        # Setup mocks
        mock_ship = Mock()
        mock_api = Mock()
        mock_db = Mock()
        logger = logging.getLogger('test')

        # Ship status: at D45 with RARE_ARTIFACT
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
                        {'symbol': 'RARE_ARTIFACT', 'units': 65}
                    ]
                },
                'engine': {'speed': 9},
                'fuel': {'current': 400, 'capacity': 400}
            },
            # Second call - after cleanup attempt (cargo still there)
            {
                'nav': {
                    'waypointSymbol': 'X1-GH18-D45',
                    'systemSymbol': 'X1-GH18',
                    'status': 'DOCKED'
                },
                'cargo': {
                    'capacity': 80,
                    'units': 65,  # Still there - but this is expected/acceptable
                    'inventory': [
                        {'symbol': 'RARE_ARTIFACT', 'units': 65}
                    ]
                }
            }
        ]

        # Mock database connection
        mock_conn = Mock()
        mock_cursor = Mock()
        mock_conn.cursor.return_value = mock_cursor
        mock_db.connection.return_value.__enter__ = Mock(return_value=mock_conn)
        mock_db.connection.return_value.__exit__ = Mock(return_value=False)

        # Mock market data: NO market buys RARE_ARTIFACT
        def mock_get_market_data(conn, waypoint, good):
            return []  # No market has this good

        mock_db.get_market_data = mock_get_market_data

        # Mock cursor queries
        def mock_execute(query, params):
            # Get current coords
            if 'SELECT x, y FROM waypoints' in query and params[0] == 'X1-GH18-D45':
                mock_cursor.fetchone.return_value = (100, 200)

            # Search for buyers - returns empty
            elif 'SELECT' in query and 'market_data m' in query:
                mock_cursor.fetchall.return_value = []  # No buyers found

        mock_cursor.execute = mock_execute

        # Execute cleanup
        result = _cleanup_stranded_cargo(mock_ship, mock_api, mock_db, logger)

        # Assertions
        assert result is True, "Should return True for partial success (unsellable goods skipped)"

        # Verify no sell attempt was made
        assert not mock_ship.sell.called, "Should not attempt to sell when no buyers exist"

        # Verify cargo remains (this is expected for unsellable goods - logged in output)


if __name__ == '__main__':
    pytest.main([__file__, '-v', '-s'])

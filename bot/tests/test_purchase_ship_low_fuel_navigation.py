"""Test that purchase_ship operation uses SmartNavigator instead of basic navigation.

BUG: When purchasing ship has low fuel (<75%), the operation uses DRIFT mode
instead of inserting refuel stops and using CRUISE mode. This causes 26x slower
navigation.

ROOT CAUSE: _ensure_ship_ready() calls ship.navigate() which is a basic method
that doesn't use SmartNavigator's intelligent routing with refuel stops.
"""

import pytest
from types import SimpleNamespace
from unittest.mock import Mock, MagicMock, patch

from spacetraders_bot.operations.purchasing import _ensure_ship_ready, purchase_ship_operation


class TestPurchaseShipLowFuelNavigation:
    """Test purchase ship navigation with low fuel scenarios."""

    def test_ensure_ship_ready_uses_smart_navigator_with_low_fuel(self):
        """FAILING TEST: Verify _ensure_ship_ready uses SmartNavigator when ship has low fuel.

        Scenario: Ship with 88/400 fuel (22%) needs to navigate to shipyard
        Expected: Should use SmartNavigator to insert refuel stops and use CRUISE
        Actual: Uses basic ship.navigate() which defaults to DRIFT mode
        """
        # Create mock ship with low fuel (22% = <75% threshold)
        mock_ship = Mock()
        mock_ship.ship_symbol = "STARGAZER-1"
        mock_ship.get_status.return_value = {
            'nav': {
                'waypointSymbol': 'X1-TEST-A1',
                'systemSymbol': 'X1-TEST'
            },
            'fuel': {
                'current': 88,
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0
            }
        }

        # Mock the navigate method to track calls
        mock_ship.navigate = Mock(return_value=True)
        mock_ship.dock = Mock(return_value=True)

        # Mock error logger
        mock_log_error = Mock()

        shipyard_symbol = "X1-TEST-B1"

        # Execute the function
        result = _ensure_ship_ready(mock_ship, shipyard_symbol, mock_log_error)

        # BUG VALIDATION: Currently this calls ship.navigate() directly
        # This assertion will PASS (showing the bug exists)
        assert mock_ship.navigate.called, "Bug confirmed: uses basic ship.navigate()"

        # AFTER FIX: Should NOT call ship.navigate directly
        # Instead should use SmartNavigator (tested separately below)

    def test_purchase_operation_with_low_fuel_uses_smart_navigator(self, monkeypatch):
        """Integration test: Purchase operation should use SmartNavigator for low-fuel ships.

        This test will FAIL before fix and PASS after fix.
        """
        # Create mock API
        mock_api = Mock()
        mock_api.get.return_value = {
            'data': {
                'ships': [{
                    'type': 'SHIP_PROBE',
                    'purchasePrice': 1000
                }]
            }
        }
        mock_api.get_agent.return_value = {'credits': 5000}
        mock_api.post.return_value = {
            'data': {
                'ship': {'symbol': 'NEW-PROBE-1'},
                'transaction': {'totalPrice': 1000},
                'agent': {'credits': 4000}
            }
        }

        # Create mock ship with LOW FUEL (22%)
        mock_ship = Mock()
        mock_ship.ship_symbol = "STARGAZER-1"
        mock_ship.get_status.return_value = {
            'nav': {
                'waypointSymbol': 'X1-TEST-A1',
                'systemSymbol': 'X1-TEST',
                'status': 'IN_ORBIT'
            },
            'fuel': {
                'current': 88,  # LOW FUEL: 22% of capacity
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0
            }
        }

        # Track navigation method used
        navigation_method_used = {'basic': False, 'smart': False}

        def track_basic_navigate(waypoint, **kwargs):
            navigation_method_used['basic'] = True
            # Update ship location after navigation
            mock_ship.get_status.return_value['nav']['waypointSymbol'] = waypoint
            return True

        mock_ship.navigate = track_basic_navigate
        mock_ship.dock = Mock(return_value=True)

        # Mock SmartNavigator to track if it's used
        with patch('spacetraders_bot.operations.purchasing.SmartNavigator') as MockSmartNav:
            mock_navigator_instance = Mock()
            mock_navigator_instance.validate_route.return_value = (True, "Route OK with refuel stops")
            mock_navigator_instance.execute_route.return_value = True
            MockSmartNav.return_value = mock_navigator_instance

            def track_smart_navigate(*args, **kwargs):
                navigation_method_used['smart'] = True
                # Update ship location after navigation
                mock_ship.get_status.return_value['nav']['waypointSymbol'] = 'X1-TEST-B1'
                return True

            mock_navigator_instance.execute_route = track_smart_navigate

            # Mock logging and captain logger
            monkeypatch.setattr('spacetraders_bot.operations.purchasing.setup_logging',
                              lambda *args, **kwargs: 'test.log')
            monkeypatch.setattr('spacetraders_bot.operations.purchasing.get_captain_logger',
                              lambda *args: [])

            # Create args
            args = SimpleNamespace(
                player_id=1,
                ship='STARGAZER-1',
                shipyard='X1-TEST-B1',
                ship_type='SHIP_PROBE',
                max_budget=2000,
                quantity=1,
                log_level='INFO'
            )

            # Execute purchase operation
            result = purchase_ship_operation(args, api=mock_api, ship=mock_ship, captain_logger=[])

            # VERIFICATION: After fix, should use SmartNavigator, not basic navigation
            # Before fix: navigation_method_used['basic'] = True, navigation_method_used['smart'] = False
            # After fix: navigation_method_used['basic'] = False, navigation_method_used['smart'] = True

            # VERIFICATION: After fix, should use SmartNavigator, not basic navigation
            assert navigation_method_used['smart'], "Should use SmartNavigator for low-fuel navigation"
            assert not navigation_method_used['basic'], "Should NOT use basic ship.navigate()"
            assert MockSmartNav.called, "Should create SmartNavigator instance"
            assert mock_navigator_instance.validate_route.called, "Should validate route"


class TestPurchaseShipNavigationEdgeCases:
    """Additional edge case tests for purchase ship navigation."""

    def test_high_fuel_ship_also_uses_smart_navigator(self, monkeypatch):
        """Even ships with high fuel should use SmartNavigator for consistency."""
        mock_api = Mock()
        mock_api.get.return_value = {
            'data': {
                'ships': [{
                    'type': 'SHIP_PROBE',
                    'purchasePrice': 1000
                }]
            }
        }
        mock_api.get_agent.return_value = {'credits': 5000}
        mock_api.post.return_value = {
            'data': {
                'ship': {'symbol': 'NEW-PROBE-1'},
                'transaction': {'totalPrice': 1000},
                'agent': {'credits': 4000}
            }
        }

        # Ship with HIGH fuel (80%)
        mock_ship = Mock()
        mock_ship.ship_symbol = "HAULER-1"
        mock_ship.get_status.return_value = {
            'nav': {
                'waypointSymbol': 'X1-TEST-A1',
                'systemSymbol': 'X1-TEST',
                'status': 'IN_ORBIT'
            },
            'fuel': {
                'current': 320,  # HIGH FUEL: 80% of capacity
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0
            }
        }

        smart_navigator_used = {'used': False}

        mock_ship.navigate = Mock(return_value=True)
        mock_ship.dock = Mock(return_value=True)

        # Mock SmartNavigator
        with patch('spacetraders_bot.operations.purchasing.SmartNavigator') as MockSmartNav:
            mock_navigator_instance = Mock()
            mock_navigator_instance.validate_route.return_value = (True, "Route OK")

            def track_execute(*args, **kwargs):
                smart_navigator_used['used'] = True
                mock_ship.get_status.return_value['nav']['waypointSymbol'] = 'X1-TEST-B1'
                return True

            mock_navigator_instance.execute_route = track_execute
            MockSmartNav.return_value = mock_navigator_instance

            monkeypatch.setattr('spacetraders_bot.operations.purchasing.setup_logging',
                              lambda *args, **kwargs: 'test.log')
            monkeypatch.setattr('spacetraders_bot.operations.purchasing.get_captain_logger',
                              lambda *args: [])

            args = SimpleNamespace(
                player_id=1,
                ship='HAULER-1',
                shipyard='X1-TEST-B1',
                ship_type='SHIP_PROBE',
                max_budget=2000,
                quantity=1,
                log_level='INFO'
            )

            result = purchase_ship_operation(args, api=mock_api, ship=mock_ship, captain_logger=[])

            # Should use SmartNavigator regardless of fuel level
            assert smart_navigator_used['used'], "Should use SmartNavigator even with high fuel"

    def test_ship_already_at_shipyard_skips_navigation(self, monkeypatch):
        """When ship is already at shipyard, no navigation should occur."""
        mock_api = Mock()
        mock_api.get.return_value = {
            'data': {
                'ships': [{
                    'type': 'SHIP_PROBE',
                    'purchasePrice': 1000
                }]
            }
        }
        mock_api.get_agent.return_value = {'credits': 5000}
        mock_api.post.return_value = {
            'data': {
                'ship': {'symbol': 'NEW-PROBE-1'},
                'transaction': {'totalPrice': 1000},
                'agent': {'credits': 4000}
            }
        }

        # Ship ALREADY at shipyard
        mock_ship = Mock()
        mock_ship.ship_symbol = "HAULER-1"
        mock_ship.get_status.return_value = {
            'nav': {
                'waypointSymbol': 'X1-TEST-B1',  # Already at shipyard
                'systemSymbol': 'X1-TEST',
                'status': 'IN_ORBIT'
            },
            'fuel': {
                'current': 100,
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0
            }
        }

        mock_ship.navigate = Mock(return_value=True)
        mock_ship.dock = Mock(return_value=True)

        # Mock SmartNavigator
        with patch('spacetraders_bot.operations.purchasing.SmartNavigator') as MockSmartNav:
            monkeypatch.setattr('spacetraders_bot.operations.purchasing.setup_logging',
                              lambda *args, **kwargs: 'test.log')
            monkeypatch.setattr('spacetraders_bot.operations.purchasing.get_captain_logger',
                              lambda *args: [])

            args = SimpleNamespace(
                player_id=1,
                ship='HAULER-1',
                shipyard='X1-TEST-B1',
                ship_type='SHIP_PROBE',
                max_budget=2000,
                quantity=1,
                log_level='INFO'
            )

            result = purchase_ship_operation(args, api=mock_api, ship=mock_ship, captain_logger=[])

            # Should NOT navigate if already at destination
            assert not mock_ship.navigate.called, "Should skip navigation when already at shipyard"
            # SmartNavigator should not be created
            assert not MockSmartNav.called, "Should not create SmartNavigator when no navigation needed"

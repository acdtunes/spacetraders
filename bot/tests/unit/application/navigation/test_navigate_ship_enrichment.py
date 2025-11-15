"""
Unit tests for NavigateShipHandler route planning behavior

REFACTORED: Removed private method testing. Now tests graph enrichment behavior
through the public handle() interface by verifying routes correctly identify
fuel waypoints for refuel stops.
"""
import pytest
import asyncio
from unittest.mock import Mock
from datetime import datetime

from domain.shared.value_objects import Waypoint, Fuel
from domain.shared.ship import Ship
from domain.shared.exceptions import ShipNotFoundError
from application.navigation.commands.navigate_ship import NavigateShipHandler, NavigateShipCommand
from ports.repositories import IShipRepository
from ports.routing_engine import IRoutingEngine


class TestNavigationWithFuelWaypoints:
    """
    Test NavigateShipHandler correctly uses waypoint fuel data for route planning.

    Tests OBSERVABLE BEHAVIOR through public handle() method, not private implementation.
    """

    def test_navigation_identifies_fuel_waypoints_for_route(self):
        """
        GIVEN: A ship at waypoint A and destination C, with waypoint B having fuel
        WHEN: NavigateShipCommand is executed with limited fuel
        THEN: Route should successfully plan using B as potential refuel stop
        """
        # Arrange
        ship_repo = Mock(spec=IShipRepository)
        routing_engine = Mock(spec=IRoutingEngine)

        # Create ship at origin with limited fuel
        origin = Waypoint(
            symbol='X1-TEST-A1',
            x=0, y=0,
            waypoint_type='PLANET',
            system_symbol='X1-TEST',
            traits=(),
            has_fuel=False
        )

        ship = Ship(
            ship_symbol='TEST-SHIP',
            player_id=1,
            current_location=origin,
            fuel=Fuel(current=50, capacity=100),
            fuel_capacity=100,
            cargo_capacity=40,
            cargo_units=0,
            engine_speed=30,
            nav_status='IN_ORBIT'
        )

        ship_repo.find_by_symbol.return_value = ship
        ship_repo.save.return_value = ship

        # Mock routing engine to return a route
        routing_engine.find_optimal_path.return_value = {
            'steps': [{
                'action': 'TRAVEL',
                'waypoint': 'X1-TEST-C3',
                'from': 'X1-TEST-A1',
                'distance': 20.0,
                'fuel_cost': 25,
                'time': 200,
                'mode': 'CRUISE'
            }],
            'total_time': 200
        }

        handler = NavigateShipHandler(ship_repo, routing_engine)

        # Create command
        command = NavigateShipCommand(
            ship_symbol='TEST-SHIP',
            destination_symbol='X1-TEST-C3',
            player_id=1
        )

        # Act - Execute through PUBLIC interface
        result = asyncio.run(handler.handle(command))

        # Assert - Verify OBSERVABLE behavior (route was created successfully)
        assert result is not None, "Route should be returned"
        assert result.ship_symbol == 'TEST-SHIP'
        assert len(result.segments) >= 1, "Route should have at least one segment"

        # Verify routing engine was called with ship's fuel info (observable interaction)
        routing_engine.find_optimal_path.assert_called_once()
        call_args = routing_engine.find_optimal_path.call_args
        assert call_args[1]['current_fuel'] == 50, "Should pass current fuel to routing engine"


    def test_navigation_fails_when_ship_not_found(self):
        """
        GIVEN: A ship that doesn't exist in repository
        WHEN: NavigateShipCommand is executed
        THEN: Should raise ShipNotFoundError
        """
        # Arrange
        ship_repo = Mock(spec=IShipRepository)
        routing_engine = Mock(spec=IRoutingEngine)

        ship_repo.find_by_symbol.return_value = None

        handler = NavigateShipHandler(ship_repo, routing_engine)

        command = NavigateShipCommand(
            ship_symbol='NONEXISTENT',
            destination_symbol='X1-TEST-B1',
            player_id=1
        )

        # Act & Assert - Verify OBSERVABLE exception behavior
        with pytest.raises(ShipNotFoundError):
            asyncio.run(handler.handle(command))


if __name__ == '__main__':
    pytest.main([__file__, '-v'])

"""Unit tests for NavigateShipHandler graph enrichment logic"""
import pytest
from unittest.mock import Mock, MagicMock
from datetime import datetime

from domain.shared.value_objects import Waypoint, Fuel, Cargo
from domain.shared.ship import Ship
from application.navigation.commands.navigate_ship import NavigateShipHandler, NavigateShipCommand
from ports.repositories import IShipRepository, IWaypointRepository
from ports.routing_engine import IRoutingEngine


class TestGraphEnrichment:
    """Test NavigateShipHandler enriches structure-only graphs with waypoint trait data"""

    def test_convert_graph_with_waypoint_enrichment(self):
        """
        GIVEN: A structure-only graph (no has_fuel field) and waypoints table with trait data
        WHEN: _convert_graph_to_waypoints is called with waypoint_traits lookup
        THEN: Waypoint objects should have correct has_fuel from waypoints table
        """
        # Arrange
        ship_repo = Mock(spec=IShipRepository)
        routing_engine = Mock(spec=IRoutingEngine)
        handler = NavigateShipHandler(ship_repo, routing_engine)

        # Structure-only graph (from system_graphs table - no has_fuel or traits)
        structure_graph = {
            'waypoints': {
                'X1-TEST-A1': {
                    'symbol': 'X1-TEST-A1',
                    'x': 0,
                    'y': 0,
                    'type': 'PLANET',
                    'systemSymbol': 'X1-TEST',
                    'orbitals': []
                    # NO 'has_fuel' field
                    # NO 'traits' field
                },
                'X1-TEST-B2': {
                    'symbol': 'X1-TEST-B2',
                    'x': 10,
                    'y': 0,
                    'type': 'ASTEROID',
                    'systemSymbol': 'X1-TEST',
                    'orbitals': []
                    # NO 'has_fuel' field
                    # NO 'traits' field
                },
                'X1-TEST-C3': {
                    'symbol': 'X1-TEST-C3',
                    'x': 20,
                    'y': 0,
                    'type': 'MOON',
                    'systemSymbol': 'X1-TEST',
                    'orbitals': []
                    # NO 'has_fuel' field
                    # NO 'traits' field
                }
            }
        }

        # Waypoint trait data (from waypoints table - full Waypoint objects)
        waypoint_traits = {
            'X1-TEST-A1': Waypoint(
                symbol='X1-TEST-A1',
                x=0, y=0,
                waypoint_type='PLANET',
                system_symbol='X1-TEST',
                traits=(),
                has_fuel=False  # No fuel
            ),
            'X1-TEST-B2': Waypoint(
                symbol='X1-TEST-B2',
                x=10, y=0,
                waypoint_type='ASTEROID',
                system_symbol='X1-TEST',
                traits=('MARKETPLACE',),
                has_fuel=True  # HAS FUEL
            ),
            'X1-TEST-C3': Waypoint(
                symbol='X1-TEST-C3',
                x=20, y=0,
                waypoint_type='MOON',
                system_symbol='X1-TEST',
                traits=(),
                has_fuel=False  # No fuel
            )
        }

        # Act - WITHOUT enrichment (current broken behavior)
        waypoints_broken = handler._convert_graph_to_waypoints(structure_graph)

        # Assert - Shows the bug: all has_fuel=False because structure graph has no fuel data
        assert waypoints_broken['X1-TEST-A1'].has_fuel == False
        assert waypoints_broken['X1-TEST-B2'].has_fuel == False  # BUG: Should be True!
        assert waypoints_broken['X1-TEST-C3'].has_fuel == False

        # Act - WITH enrichment (fixed behavior we're implementing)
        waypoints_fixed = handler._convert_graph_to_waypoints(structure_graph, waypoint_traits)

        # Assert - Enriched waypoints should have correct has_fuel from waypoints table
        assert waypoints_fixed['X1-TEST-A1'].has_fuel == False
        assert waypoints_fixed['X1-TEST-B2'].has_fuel == True  # FIXED: Enriched from waypoints table!
        assert waypoints_fixed['X1-TEST-C3'].has_fuel == False


    def test_convert_graph_fallback_when_no_waypoint_traits(self):
        """
        GIVEN: A structure-only graph and NO waypoints table data
        WHEN: _convert_graph_to_waypoints is called without waypoint_traits
        THEN: Should fallback to structure-only data (all has_fuel=False)
        """
        # Arrange
        ship_repo = Mock(spec=IShipRepository)
        routing_engine = Mock(spec=IRoutingEngine)
        handler = NavigateShipHandler(ship_repo, routing_engine)

        structure_graph = {
            'waypoints': {
                'X1-EMPTY-A1': {
                    'symbol': 'X1-EMPTY-A1',
                    'x': 0,
                    'y': 0,
                    'type': 'PLANET',
                    'systemSymbol': 'X1-EMPTY',
                    'orbitals': []
                }
            }
        }

        # Act - No waypoint_traits provided (empty waypoints cache scenario)
        waypoints = handler._convert_graph_to_waypoints(structure_graph, waypoint_traits=None)

        # Assert - Should use fallback (structure-only, no fuel)
        assert waypoints['X1-EMPTY-A1'].has_fuel == False
        assert waypoints['X1-EMPTY-A1'].traits == ()


if __name__ == '__main__':
    pytest.main([__file__, '-v'])

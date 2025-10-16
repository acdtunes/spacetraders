"""
Test for bug: Fuel stations incorrectly included in market scout tours

BUG DESCRIPTION:
- Scout coordinator uses TourOptimizer.get_markets_from_graph() to get markets for scouting
- Function filters by "MARKETPLACE" trait, but fuel stations also have this trait
- Fuel stations only sell fuel (no trade goods), so no market intelligence value
- This wastes scout time visiting waypoints that provide no useful data

ROOT CAUSE:
- Function checks for "MARKETPLACE" trait but doesn't exclude FUEL_STATION type
- File: src/spacetraders_bot/core/routing.py:321-326

EXPECTED FIX:
- Exclude waypoints with type="FUEL_STATION" from market lists
- Keep real markets (EXCHANGE, regular marketplaces with trade goods)
"""

import pytest
from spacetraders_bot.core.routing import TourOptimizer


def test_get_markets_excludes_fuel_stations():
    """
    Test that get_markets_from_graph excludes fuel stations from market lists

    SCENARIO: System has mix of real markets and fuel stations

    Given:
    - Graph with 3 markets (EXCHANGE, MARKETPLACE)
    - Graph with 2 fuel stations (FUEL_STATION type with MARKETPLACE trait)

    When:
    - get_markets_from_graph() is called

    Then:
    - Returns only the 3 real markets
    - Excludes the 2 fuel stations (even though they have MARKETPLACE trait)
    """
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            # REAL MARKETS - should be included
            "X1-TEST-A1": {
                "type": "PLANET",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],  # Regular marketplace
                "has_fuel": True,
            },
            "X1-TEST-B7": {
                "type": "ASTEROID",
                "x": 100,
                "y": 100,
                "traits": ["MARKETPLACE", "COMMON_METAL_DEPOSITS"],  # Exchange
                "has_fuel": True,
            },
            "X1-TEST-C5": {
                "type": "MOON",
                "x": -50,
                "y": 50,
                "traits": ["MARKETPLACE", "SHIPYARD"],  # Marketplace with shipyard
                "has_fuel": True,
            },
            # FUEL STATIONS - should be excluded (only sell fuel, no trade goods)
            "X1-TEST-F1": {
                "type": "FUEL_STATION",  # Type identifies it as fuel-only
                "x": 200,
                "y": 0,
                "traits": ["MARKETPLACE"],  # Has MARKETPLACE trait but only sells fuel
                "has_fuel": True,
            },
            "X1-TEST-F2": {
                "type": "FUEL_STATION",  # Another fuel station
                "x": -200,
                "y": 0,
                "traits": ["MARKETPLACE"],  # Has MARKETPLACE trait but only sells fuel
                "has_fuel": True,
            },
            # NON-MARKETS - should be excluded
            "X1-TEST-D9": {
                "type": "ASTEROID",
                "x": 300,
                "y": 300,
                "traits": ["COMMON_METAL_DEPOSITS"],  # No marketplace
                "has_fuel": False,
            },
        },
        "edges": []
    }

    # Call function under test
    markets = TourOptimizer.get_markets_from_graph(graph)

    # VALIDATE: Only real markets returned, fuel stations excluded
    assert len(markets) == 3, f"Expected 3 real markets, got {len(markets)}: {markets}"

    # Check that real markets are included
    assert "X1-TEST-A1" in markets, "Regular marketplace should be included"
    assert "X1-TEST-B7" in markets, "Exchange should be included"
    assert "X1-TEST-C5" in markets, "Marketplace with shipyard should be included"

    # Check that fuel stations are excluded
    assert "X1-TEST-F1" not in markets, "Fuel station F1 should be excluded (only sells fuel)"
    assert "X1-TEST-F2" not in markets, "Fuel station F2 should be excluded (only sells fuel)"

    # Check that non-markets are excluded
    assert "X1-TEST-D9" not in markets, "Non-marketplace asteroid should be excluded"


def test_get_markets_handles_empty_graph():
    """Test that get_markets_from_graph handles empty/missing graph gracefully"""

    # Empty graph
    assert TourOptimizer.get_markets_from_graph({}) == []

    # None graph
    assert TourOptimizer.get_markets_from_graph(None) == []

    # Graph with no waypoints
    assert TourOptimizer.get_markets_from_graph({"system": "X1-TEST", "waypoints": {}}) == []


def test_get_markets_handles_missing_type():
    """
    Test that get_markets_from_graph handles waypoints without type field

    Some older graphs may not have the 'type' field. Function should not crash
    and should include waypoints with MARKETPLACE trait if type is missing.
    """
    graph = {
        "system": "X1-TEST",
        "waypoints": {
            "X1-TEST-A1": {
                # No 'type' field (legacy data)
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
            },
            "X1-TEST-B7": {
                "type": "PLANET",  # Has type
                "x": 100,
                "y": 100,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
            },
        },
        "edges": []
    }

    markets = TourOptimizer.get_markets_from_graph(graph)

    # Both should be included (missing type treated as potential market)
    assert len(markets) == 2
    assert "X1-TEST-A1" in markets
    assert "X1-TEST-B7" in markets


def test_scout_coordinator_uses_filtered_markets(tmp_path):
    """
    Integration test: Verify scout coordinator doesn't include fuel stations in tours

    This test ensures that when scout coordinator loads markets using get_markets_from_graph(),
    fuel stations are excluded from the partitioning and tour planning.
    """
    from spacetraders_bot.core.scout_coordinator import ScoutCoordinator
    from spacetraders_bot.core.system_graph_provider import SystemGraphProvider, GraphLoadResult
    from unittest.mock import Mock, MagicMock

    # Create mock graph with fuel stations and real markets
    mock_graph = {
        "system": "X1-TEST",
        "waypoints": {
            "X1-TEST-M1": {
                "type": "PLANET",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
            },
            "X1-TEST-M2": {
                "type": "MOON",
                "x": 100,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
            },
            "X1-TEST-F1": {
                "type": "FUEL_STATION",  # Should be excluded
                "x": 50,
                "y": 50,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
            },
        },
        "edges": []
    }

    # Mock API and graph provider
    mock_api = Mock()
    mock_graph_provider = Mock(spec=SystemGraphProvider)
    mock_graph_provider.get_graph.return_value = GraphLoadResult(
        graph=mock_graph,
        source="test",
        message="Test graph loaded"
    )

    # Create coordinator
    coordinator = ScoutCoordinator(
        system="X1-TEST",
        ships=["SCOUT-1"],
        token="test_token",
        player_id=1,
        config_file=str(tmp_path / "scout_config.json"),
        graph_provider=mock_graph_provider
    )

    # VALIDATE: Coordinator's market list excludes fuel stations
    assert len(coordinator.markets) == 2, f"Expected 2 markets, got {len(coordinator.markets)}: {coordinator.markets}"
    assert "X1-TEST-M1" in coordinator.markets, "Market M1 should be in coordinator's market list"
    assert "X1-TEST-M2" in coordinator.markets, "Market M2 should be in coordinator's market list"
    assert "X1-TEST-F1" not in coordinator.markets, "Fuel station F1 should NOT be in coordinator's market list"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])

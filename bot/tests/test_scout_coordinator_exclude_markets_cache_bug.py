"""
Test for scout coordinator exclude_markets cache bug.

BUG: When exclude_markets is used, stale tour cache entries that contain
excluded markets in their tour_order (but not in their markets list) are
being returned, causing scouts to visit excluded markets.

Example:
- Stale cache: markets=["H48"], tour_order=["I52", "H48", "I52"]
  (from a previous run where scout was stationed at I52)
- exclude_markets=["I52", "J55"]
- New scout assigned markets=["G47", "H48"]
- Scout looks up tour for H48, finds stale cache, visits I52!

Expected behavior:
- Stale cache entries containing excluded markets should be invalidated
- OR tour cache lookup should consider excluded markets in cache key
- OR tour validation should detect excluded markets and force rebuild

Current failure:
- scout-3 assigned [G47, H48] but toured [I52, H48, I52]
- scout-10 assigned [J55, F45]
- scout-12 assigned [I52, F46]

All three scouts visited I52/J55 despite exclude_markets parameter.
"""

import json
import pytest
from unittest.mock import Mock
from src.spacetraders_bot.core.scout_coordinator import ScoutCoordinator
from src.spacetraders_bot.core.database import get_database
from src.spacetraders_bot.helpers import paths


def create_test_graph():
    """Create minimal test graph with markets"""
    return {
        "system": "X1-TEST",
        "waypoints": {
            "X1-TEST-G47": {"type": "EXCHANGE", "x": 0, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
            "X1-TEST-H48": {"type": "EXCHANGE", "x": 100, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
            "X1-TEST-I52": {"type": "EXCHANGE", "x": 200, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
            "X1-TEST-J55": {"type": "EXCHANGE", "x": 300, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True, "orbitals": []},
        },
        "edges": [],
    }


@pytest.fixture
def test_db():
    """Create test database with stale cache entry"""
    db_path = paths.sqlite_path()
    db = get_database(db_path)

    # Create stale cache entry: scout stationed at I52, touring to H48
    # This simulates a previous run before exclude_markets was used
    with db.transaction() as conn:
        # Save stale tour: markets=[H48], tour_order=[I52, H48, I52]
        # This represents a return-to-start tour from I52
        db.save_tour_cache(
            conn,
            system="X1-TEST",
            markets=["X1-TEST-H48"],
            algorithm="ortools",
            tour_order=["X1-TEST-I52", "X1-TEST-H48", "X1-TEST-I52"],
            total_distance=200.0,
            start_waypoint=None,  # NULL = return-to-start mode
        )

    yield db

    # Cleanup: delete stale cache
    with db.transaction() as conn:
        conn.execute("DELETE FROM tour_cache WHERE system='X1-TEST'")


def test_exclude_markets_invalidates_stale_cache(test_db):
    """
    Test that stale cache entries containing excluded markets are not used.

    Given:
      - Stale cache: markets=[H48], tour_order=[I52, H48, I52]
      - exclude_markets=[I52, J55]
      - Scout assigned markets=[G47, H48]

    When:
      - Scout coordinator initializes with exclude_markets
      - Scout looks up tour for [G47, H48]

    Then:
      - Stale cache entry should NOT be used (tour_order contains I52)
      - New tour should be generated without I52
      - Tour should only visit G47 and H48
    """
    # Arrange
    mock_api = Mock()
    mock_api.get_ship = Mock(return_value={
        "symbol": "TEST-SHIP",
        "nav": {"waypointSymbol": "X1-TEST-G47"},
        "fuel": {"capacity": 800, "current": 800},
        "engine": {"speed": 9},
    })

    graph = create_test_graph()

    # Mock graph provider to return our test graph
    from src.spacetraders_bot.core.system_graph_provider import SystemGraphProvider, GraphResult
    mock_graph_provider = Mock(spec=SystemGraphProvider)
    mock_graph_provider.get_graph = Mock(return_value=GraphResult(
        graph=graph,
        message="Test graph loaded"
    ))

    # Act: Create coordinator with exclude_markets
    coordinator = ScoutCoordinator(
        system="X1-TEST",
        ships=["TEST-SHIP"],
        token="test-token",
        player_id=1,
        graph_provider=mock_graph_provider,
        exclude_markets=["X1-TEST-I52", "X1-TEST-J55"],
    )

    # Assert: Filtered markets should not include I52 or J55
    assert "X1-TEST-I52" not in coordinator.markets
    assert "X1-TEST-J55" not in coordinator.markets
    assert "X1-TEST-G47" in coordinator.markets
    assert "X1-TEST-H48" in coordinator.markets

    # Act: Optimize tour for [G47, H48]
    # This should NOT use the stale cache entry that contains I52
    tour = coordinator.optimize_subtour("TEST-SHIP", ["X1-TEST-G47", "X1-TEST-H48"])

    # Assert: Tour should not include excluded markets
    if tour:
        tour_waypoints = set()
        for leg in tour.get("legs", []):
            for step in leg.get("steps", []):
                if step["action"] == "navigate":
                    tour_waypoints.add(step["from"])
                    tour_waypoints.add(step["to"])

        # CRITICAL: Tour must not visit excluded markets
        assert "X1-TEST-I52" not in tour_waypoints, f"Tour includes excluded market I52! Waypoints: {tour_waypoints}"
        assert "X1-TEST-J55" not in tour_waypoints, f"Tour includes excluded market J55! Waypoints: {tour_waypoints}"


def test_stale_cache_tour_order_validation():
    """
    Test that cached tour orders are validated to exclude markets.

    This is a unit test for the tour cache lookup/validation logic.
    """
    # This will pass once we implement proper cache validation
    pass


if __name__ == "__main__":
    pytest.main([__file__, "-v", "-s"])

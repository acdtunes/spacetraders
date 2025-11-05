"""Integration test: Scout tour actually navigates ships between waypoints"""
import pytest
from unittest.mock import Mock, MagicMock
from datetime import datetime, timezone

from spacetraders.configuration.container import (
    get_database,
    get_ship_repository,
    get_market_repository,
    reset_container
)
from spacetraders.application.scouting.commands.scout_tour import (
    ScoutTourCommand,
    ScoutTourHandler
)


@pytest.fixture
def test_database():
    """Setup test database with clean state"""
    reset_container()
    db = get_database()

    # Insert test player
    with db.transaction() as conn:
        conn.execute("""
            INSERT OR REPLACE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (?, ?, ?, datetime('now'))
        """, (8888, "TEST_SCOUT", "test-token"))

        # Insert test ship at starting location X1-TEST-A1
        conn.execute("""
            INSERT OR REPLACE INTO ships
            (ship_symbol, player_id, current_location_symbol, fuel_current, fuel_capacity,
             cargo_capacity, cargo_units, engine_speed, nav_status, system_symbol, synced_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
        """, ('TEST-SCOUT-1', 8888, 'X1-TEST-A1', 100, 100, 40, 0, 10, 'DOCKED', 'X1-TEST'))

        # Insert waypoints for navigation graph
        for waypoint in ['X1-TEST-A1', 'X1-TEST-B2', 'X1-TEST-C3']:
            conn.execute("""
                INSERT OR REPLACE INTO waypoints
                (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """, (waypoint, 'X1-TEST', 'PLANET', 0, 0, '[]', 1, '[]'))

    yield db

    # Cleanup
    with db.transaction() as conn:
        conn.execute("DELETE FROM players WHERE player_id = ?", (8888,))
        conn.execute("DELETE FROM ships WHERE player_id = ?", (8888,))
        conn.execute("DELETE FROM waypoints WHERE system_symbol = ?", ('X1-TEST',))
        conn.execute("DELETE FROM market_data WHERE player_id = ?", (8888,))


@pytest.fixture
def mock_api_client():
    """Mock API client that simulates successful navigation and market data"""
    client = Mock()

    # Mock get_ship endpoint - returns full ship data
    def mock_get_ship(ship_symbol):
        # Return ship data matching the requested destination from last navigate call
        waypoint = getattr(mock_get_ship, 'last_destination', 'X1-TEST-A1')
        return {
            'data': {
                'symbol': ship_symbol,
                'nav': {
                    'waypointSymbol': waypoint,
                    'status': 'IN_ORBIT',
                    'systemSymbol': 'X1-TEST',
                    'route': {
                        'destination': {
                            'symbol': waypoint
                        }
                    }
                },
                'fuel': {'current': 100, 'capacity': 100},
                'cargo': {'units': 0, 'capacity': 40},
                'frame': {'symbol': 'FRAME_PROBE'},
                'reactor': {'symbol': 'REACTOR_SOLAR_I'},
                'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 10}
            }
        }
    client.get_ship = Mock(side_effect=mock_get_ship)

    # Mock navigation endpoint - returns arrival time
    def mock_navigate(ship_symbol, destination):
        # Store destination for get_ship to return
        mock_get_ship.last_destination = destination
        return {
            'data': {
                'nav': {
                    'status': 'IN_TRANSIT',
                    'waypointSymbol': destination,
                    'route': {
                        'arrival': datetime.now(timezone.utc).isoformat()
                    }
                },
                'fuel': {'current': 90, 'capacity': 100}
            }
        }
    client.navigate_ship = Mock(side_effect=mock_navigate)

    # Mock orbit endpoint
    def mock_orbit(ship_symbol):
        return {
            'data': {
                'nav': {
                    'status': 'IN_ORBIT',
                    'waypointSymbol': getattr(mock_get_ship, 'last_destination', 'X1-TEST-A1')
                }
            }
        }
    client.orbit_ship = Mock(side_effect=mock_orbit)

    # Mock dock endpoint
    def mock_dock(ship_symbol):
        return {
            'data': {
                'nav': {
                    'status': 'DOCKED',
                    'waypointSymbol': getattr(mock_get_ship, 'last_destination', 'X1-TEST-A1')
                }
            }
        }
    client.dock_ship = Mock(side_effect=mock_dock)

    # Mock market data endpoint - returns fake trade goods
    def mock_get_market(system, waypoint):
        return {
            'data': {
                'tradeGoods': [
                    {
                        'symbol': 'FUEL',
                        'supply': 'MODERATE',
                        'activity': 'STRONG',
                        'purchasePrice': 100,
                        'sellPrice': 90,
                        'tradeVolume': 1000
                    }
                ]
            }
        }
    client.get_market = Mock(side_effect=mock_get_market)

    # Mock get_system_waypoints for graph building
    def mock_get_waypoints(system):
        return {
            'data': [
                {'symbol': 'X1-TEST-A1', 'type': 'PLANET', 'x': 0, 'y': 0, 'traits': []},
                {'symbol': 'X1-TEST-B2', 'type': 'PLANET', 'x': 10, 'y': 10, 'traits': []},
                {'symbol': 'X1-TEST-C3', 'type': 'PLANET', 'x': 20, 'y': 20, 'traits': []},
            ]
        }
    client.get_system_waypoints = Mock(side_effect=mock_get_waypoints)

    return client


@pytest.mark.asyncio
async def test_scout_tour_actually_moves_ship_between_waypoints(test_database, mock_api_client):
    """
    Integration test: Verify scout tour ACTUALLY navigates the ship to each waypoint.

    This test ensures that when scout tour visits waypoints B and C:
    1. Ship's current_location is updated to B after visiting B
    2. Ship's current_location is updated to C after visiting C
    3. Ship is not stuck at starting location A

    This test will FAIL with the current broken "simplified navigation" code.
    """
    ship_repo = get_ship_repository()
    market_repo = get_market_repository()

    # Patch API client into container
    from spacetraders.configuration import container as container_module
    from spacetraders.domain.shared.value_objects import Waypoint
    from spacetraders.ports.outbound.graph_provider import GraphLoadResult

    original_get_api = container_module.get_api_client_for_player
    original_get_graph = container_module.get_graph_provider_for_player

    container_module.get_api_client_for_player = lambda player_id: mock_api_client

    # Mock graph provider with simple graph
    mock_graph_provider = Mock()
    mock_graph = {
        'waypoints': {
            'X1-TEST-A1': {'symbol': 'X1-TEST-A1', 'x': 0, 'y': 0, 'type': 'PLANET'},
            'X1-TEST-B2': {'symbol': 'X1-TEST-B2', 'x': 10, 'y': 10, 'type': 'PLANET'},
            'X1-TEST-C3': {'symbol': 'X1-TEST-C3', 'x': 20, 'y': 20, 'type': 'PLANET'},
        },
        'edges': []
    }
    mock_graph_provider.get_graph = Mock(return_value=GraphLoadResult(
        graph=mock_graph,
        source='test',
        message='Test graph'
    ))
    container_module.get_graph_provider_for_player = lambda player_id: mock_graph_provider

    try:
        # Create handler
        handler = ScoutTourHandler(ship_repo, market_repo)

        # Verify ship starts at A1
        ship_before = ship_repo.find_by_symbol('TEST-SCOUT-1', 8888)
        assert ship_before.current_location.symbol == 'X1-TEST-A1', "Ship should start at A1"

        # Execute scout tour: A1 -> B2 -> C3
        command = ScoutTourCommand(
            ship_symbol='TEST-SCOUT-1',
            player_id=8888,
            system='X1-TEST',
            markets=['X1-TEST-B2', 'X1-TEST-C3'],
            return_to_start=False
        )

        result = await handler.handle(command)

        # Verify tour completed
        assert result.markets_visited == 2, "Should have visited 2 markets"

        # CRITICAL: Verify ship actually moved to final waypoint C3
        ship_after = ship_repo.find_by_symbol('TEST-SCOUT-1', 8888)
        assert ship_after.current_location.symbol == 'X1-TEST-C3', \
            f"Ship should be at C3 after tour, but is at {ship_after.current_location.symbol}"

        # Verify market data was persisted for both waypoints
        with test_database.connection() as conn:
            market_rows = conn.execute("""
                SELECT waypoint_symbol FROM market_data
                WHERE player_id = ? AND waypoint_symbol IN ('X1-TEST-B2', 'X1-TEST-C3')
            """, (8888,)).fetchall()

        visited_waypoints = {row[0] for row in market_rows}
        assert visited_waypoints == {'X1-TEST-B2', 'X1-TEST-C3'}, \
            f"Should have market data for B2 and C3, got {visited_waypoints}"

    finally:
        # Restore originals
        container_module.get_api_client_for_player = original_get_api
        container_module.get_graph_provider_for_player = original_get_graph


@pytest.mark.asyncio
async def test_scout_tour_with_return_to_start(test_database, mock_api_client):
    """
    Integration test: Verify scout tour returns to starting location when requested.
    """
    ship_repo = get_ship_repository()
    market_repo = get_market_repository()

    # Patch API client
    from spacetraders.configuration import container as container_module
    original_get_api = container_module.get_api_client_for_player
    container_module.get_api_client_for_player = lambda player_id: mock_api_client

    try:
        handler = ScoutTourHandler(ship_repo, market_repo)

        # Verify ship starts at A1
        ship_before = ship_repo.find_by_symbol('TEST-SCOUT-1', 8888)
        assert ship_before.current_location.symbol == 'X1-TEST-A1'

        # Execute scout tour with return_to_start=True
        command = ScoutTourCommand(
            ship_symbol='TEST-SCOUT-1',
            player_id=8888,
            system='X1-TEST',
            markets=['X1-TEST-B2'],
            return_to_start=True
        )

        result = await handler.handle(command)

        # Verify ship returned to start
        ship_after = ship_repo.find_by_symbol('TEST-SCOUT-1', 8888)
        assert ship_after.current_location.symbol == 'X1-TEST-A1', \
            f"Ship should have returned to A1, but is at {ship_after.current_location.symbol}"

    finally:
        container_module.get_api_client_for_player = original_get_api

"""
Integration tests for ShipRepository with real database and graph structures.

These tests verify that the repository correctly handles production-like
graph structures where waypoint symbols are dictionary keys, not fields.
"""
import pytest
from unittest.mock import Mock
from datetime import datetime

from spacetraders.adapters.secondary.persistence.ship_repository import ShipRepository
from spacetraders.adapters.secondary.persistence.database import Database
from spacetraders.adapters.secondary.persistence.player_repository import PlayerRepository
from spacetraders.domain.shared.ship import Ship
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.value_objects import Waypoint, Fuel
from spacetraders.configuration.container import reset_container
from tests.fixtures.graph_fixtures import (
    REALISTIC_SYSTEM_GRAPH,
    create_realistic_ship_response,
    get_mock_graph_for_system,
    validate_graph_structure
)


@pytest.fixture
def database():
    """Create test database instance"""
    reset_container()
    db = Database(":memory:")
    # Database initializes schema automatically in __init__
    yield db
    reset_container()


@pytest.fixture
def graph_provider():
    """Create mock graph provider with realistic production structures"""
    mock_provider = Mock()
    mock_provider.get_graph.side_effect = get_mock_graph_for_system
    return mock_provider


@pytest.fixture
def test_player(database):
    """Create a test player in the database"""
    player_repo = PlayerRepository(database)
    player = Player(
        player_id=None,
        agent_symbol='TEST-AGENT',
        token='test-token-123',
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={}
    )
    return player_repo.create(player)


@pytest.fixture
def ship_repository(database, graph_provider, test_player):
    """Create ship repository with real database and realistic graph provider"""
    return ShipRepository(database, graph_provider)


@pytest.fixture
def test_ship():
    """Create test ship"""
    waypoint = Waypoint(
        symbol='X1-TEST-A1',
        x=-2,
        y=26,
        system_symbol='X1-TEST',
        waypoint_type='PLANET',
        traits=('ROCKY', 'OUTPOST', 'MARKETPLACE'),
        has_fuel=True,
        orbitals=('X1-TEST-A2', 'X1-TEST-A3')
    )
    return Ship(
        ship_symbol='TEST-SHIP-1',
        player_id=1,
        current_location=waypoint,
        fuel=Fuel(current=400, capacity=400),
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )


def test_graph_fixtures_are_valid():
    """Verify that our test fixtures match production structure"""
    # This should not raise any assertions
    validate_graph_structure(REALISTIC_SYSTEM_GRAPH)


def test_create_and_find_ship(ship_repository, test_ship):
    """Test creating and finding a ship"""
    # Create ship
    created = ship_repository.create(test_ship)
    assert created.ship_symbol == 'TEST-SHIP-1'

    # Find ship
    found = ship_repository.find_by_symbol('TEST-SHIP-1', 1)
    assert found is not None
    assert found.ship_symbol == 'TEST-SHIP-1'
    assert found.current_location.symbol == 'X1-TEST-A1'


def test_reconstruct_waypoint_from_realistic_graph(ship_repository, test_ship):
    """
    CRITICAL TEST: Verify waypoint reconstruction doesn't fail with KeyError.

    This test ensures that _reconstruct_waypoint() correctly handles production
    graph structures where the waypoint symbol is the dictionary KEY, not a field.
    """
    # Create ship in database
    ship_repository.create(test_ship)

    # Find ship - this triggers _reconstruct_waypoint()
    # In the buggy version, this would raise KeyError: 'symbol'
    found = ship_repository.find_by_symbol('TEST-SHIP-1', 1)

    # Verify waypoint was reconstructed correctly
    assert found is not None
    assert found.current_location.symbol == 'X1-TEST-A1'
    assert found.current_location.x == -2
    assert found.current_location.y == 26
    assert found.current_location.waypoint_type == 'PLANET'
    assert 'ROCKY' in found.current_location.traits
    assert found.current_location.has_fuel is True


def test_sync_from_api_with_realistic_graph(ship_repository, test_ship, graph_provider):
    """
    CRITICAL TEST: Verify sync_from_api doesn't fail with KeyError.

    This test replicates the production bug where fake mocks allowed KeyError
    to pass undetected in tests but failed in production.
    """
    # Create ship in database
    ship_repository.create(test_ship)

    # Create mock API client
    mock_api_client = Mock()
    mock_api_client.get_ship.return_value = create_realistic_ship_response(
        ship_symbol='TEST-SHIP-1',
        status='IN_ORBIT',
        waypoint_symbol='X1-TEST-B2',
        fuel_current=250,
        fuel_capacity=400
    )

    # Sync from API - this should NOT raise KeyError: 'symbol'
    synced_ship = ship_repository.sync_from_api(
        ship_symbol='TEST-SHIP-1',
        player_id=1,
        api_client=mock_api_client,
        graph_provider=graph_provider
    )

    # Verify ship was synced correctly
    assert synced_ship is not None
    assert synced_ship.current_location.symbol == 'X1-TEST-B2'
    assert synced_ship.fuel.current == 250
    assert synced_ship.nav_status == Ship.IN_ORBIT

    # Verify waypoint details were reconstructed from graph
    assert synced_ship.current_location.x == 50  # From REALISTIC_SYSTEM_GRAPH
    assert synced_ship.current_location.y == 100
    assert synced_ship.current_location.waypoint_type == 'PLANET'


def test_find_all_by_player_with_realistic_graph(ship_repository, test_ship):
    """Test finding all ships for a player with realistic graph reconstruction"""
    # Create multiple ships
    ship_repository.create(test_ship)

    # Create second ship at different location
    ship2_waypoint = Waypoint(
        symbol='X1-TEST-B2',
        x=50,
        y=100,
        system_symbol='X1-TEST',
        waypoint_type='PLANET',
        traits=('MARKETPLACE', 'SHIPYARD'),
        has_fuel=True,
        orbitals=()
    )
    ship2 = Ship(
        ship_symbol='TEST-SHIP-2',
        player_id=1,
        current_location=ship2_waypoint,
        fuel=Fuel(current=300, capacity=400),
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )
    ship_repository.create(ship2)

    # Find all ships - this triggers multiple waypoint reconstructions
    ships = ship_repository.find_all_by_player(1)

    assert len(ships) == 2
    assert ships[0].ship_symbol == 'TEST-SHIP-1'
    assert ships[1].ship_symbol == 'TEST-SHIP-2'

    # Verify both waypoints were reconstructed correctly
    assert ships[0].current_location.symbol == 'X1-TEST-A1'
    assert ships[1].current_location.symbol == 'X1-TEST-B2'


def test_update_ship_preserves_realistic_waypoint(ship_repository, test_ship):
    """Test updating ship preserves waypoint reconstruction"""
    # Create ship
    ship_repository.create(test_ship)

    # Update ship fuel
    test_ship.consume_fuel(100)
    ship_repository.update(test_ship)

    # Find ship - verify waypoint still reconstructs correctly
    found = ship_repository.find_by_symbol('TEST-SHIP-1', 1)
    assert found is not None
    assert found.fuel.current == 300
    assert found.current_location.symbol == 'X1-TEST-A1'
    assert found.current_location.has_fuel is True


def test_sync_from_api_with_waypoint_not_in_graph(ship_repository, test_ship):
    """Test sync_from_api handles waypoints not in graph gracefully"""
    # Create ship
    ship_repository.create(test_ship)

    # Create mock API client returning unknown waypoint
    mock_api_client = Mock()
    mock_api_client.get_ship.return_value = create_realistic_ship_response(
        ship_symbol='TEST-SHIP-1',
        status='IN_ORBIT',
        waypoint_symbol='X1-TEST-UNKNOWN',  # Not in our test graph
        fuel_current=250,
        fuel_capacity=400
    )

    # Mock graph provider
    mock_graph = Mock()
    mock_graph.get_graph.return_value = get_mock_graph_for_system('X1-TEST')

    # Sync should use fallback minimal waypoint, not crash
    synced_ship = ship_repository.sync_from_api(
        ship_symbol='TEST-SHIP-1',
        player_id=1,
        api_client=mock_api_client,
        graph_provider=mock_graph
    )

    # Verify fallback waypoint was created
    assert synced_ship is not None
    assert synced_ship.current_location.symbol == 'X1-TEST-UNKNOWN'
    assert synced_ship.current_location.x == 0.0  # Fallback coordinates
    assert synced_ship.current_location.y == 0.0


def test_no_fake_symbol_field_in_graph_data():
    """
    REGRESSION TEST: Ensure graph data doesn't have 'symbol' field.

    This test explicitly checks that we're not using fake mock structures
    with fabricated 'symbol' fields in waypoint data.
    """
    graph = REALISTIC_SYSTEM_GRAPH
    waypoints = graph['waypoints']

    for waypoint_symbol, waypoint_data in waypoints.items():
        # CRITICAL: symbol should be the KEY, not a field
        assert 'symbol' not in waypoint_data, (
            f"FAKE MOCK DETECTED: waypoint '{waypoint_symbol}' has 'symbol' field! "
            f"This doesn't match production structure where symbol is the dict key."
        )

        # Verify required fields exist
        assert 'type' in waypoint_data
        assert 'x' in waypoint_data
        assert 'y' in waypoint_data
        assert 'traits' in waypoint_data
        assert 'has_fuel' in waypoint_data
        assert 'orbitals' in waypoint_data

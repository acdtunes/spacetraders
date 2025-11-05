"""Shared fixtures for persistence integration tests"""
import pytest
import tempfile
from pathlib import Path
from datetime import datetime

from adapters.secondary.persistence.database import Database
from adapters.secondary.persistence.player_repository import PlayerRepository
from adapters.secondary.persistence.ship_repository import ShipRepository
from adapters.secondary.persistence.route_repository import RouteRepository
from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel


class MockGraphProvider:
    """Mock graph provider for testing"""

    def get_graph(self, system_symbol: str):
        """Return mock graph data"""
        class GraphResult:
            def __init__(self):
                self.graph = {
                    "waypoints": {
                        "X1-A1": {
                            "symbol": "X1-A1",
                            "x": 0.0,
                            "y": 0.0,
                            "systemSymbol": "X1",
                            "type": "PLANET",
                            "traits": ["MARKETPLACE", "SHIPYARD"],
                            "has_fuel": True,
                            "orbitals": ["X1-A1-M1"]
                        },
                        "X1-A2": {
                            "symbol": "X1-A2",
                            "x": 10.0,
                            "y": 10.0,
                            "systemSymbol": "X1",
                            "type": "MOON",
                            "traits": [],
                            "has_fuel": False,
                            "orbitals": []
                        },
                        "X1-A3": {
                            "symbol": "X1-A3",
                            "x": 20.0,
                            "y": 20.0,
                            "systemSymbol": "X1",
                            "type": "ASTEROID",
                            "traits": ["MINING"],
                            "has_fuel": False,
                            "orbitals": []
                        }
                    }
                }
        return GraphResult()


@pytest.fixture
def temp_db_path():
    """Create temporary database path"""
    with tempfile.TemporaryDirectory() as tmpdir:
        yield Path(tmpdir) / "test.db"


@pytest.fixture
def db(temp_db_path):
    """Create fresh database"""
    return Database(temp_db_path)


@pytest.fixture
def graph_provider():
    """Create mock graph provider"""
    return MockGraphProvider()


@pytest.fixture
def player_repository(db):
    """Create player repository"""
    return PlayerRepository(db)


@pytest.fixture
def ship_repository(db, graph_provider):
    """Create ship repository"""
    return ShipRepository(db, graph_provider)


@pytest.fixture
def route_repository(db):
    """Create route repository"""
    return RouteRepository(db)


@pytest.fixture
def test_player(player_repository):
    """Create a test player"""
    player = Player(
        player_id=None,
        agent_symbol="TEST_AGENT",
        token="token123",
        created_at=datetime(2025, 1, 1, 12, 0, 0),
        last_active=datetime(2025, 1, 1, 12, 0, 0)
    )
    return player_repository.create(player)


@pytest.fixture
def test_ship(ship_repository, test_player):
    """Create a test ship"""
    waypoint = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    fuel = Fuel(current=100, capacity=200)
    ship = Ship(
        ship_symbol="SHIP-1",
        player_id=test_player.player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )
    return ship_repository.create(ship)

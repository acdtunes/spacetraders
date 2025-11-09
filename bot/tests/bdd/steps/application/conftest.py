import pytest
from typing import Optional, List, Dict
from datetime import datetime, timezone
from domain.shared.ship import Ship
from domain.shared.player import Player
from ports.repositories import IShipRepository
from ports.outbound.repositories import IPlayerRepository
from ports.outbound.api_client import ISpaceTradersAPI


class MockShipRepository(IShipRepository):
    """In-memory ship repository for testing"""

    def __init__(self, mock_api=None):
        self._ships: Dict[tuple, Ship] = {}
        self._mock_api = mock_api  # Reference to mock API for auto-registration

    def create(self, ship: Ship) -> Ship:
        key = (ship.ship_symbol, ship.player_id)
        self._ships[key] = ship
        # Auto-register ship with mock API
        if self._mock_api:
            self._mock_api.register_ship(ship)
        return ship

    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        return self._ships.get((ship_symbol, player_id))

    def find_all_by_player(self, player_id: int) -> List[Ship]:
        return [s for s in self._ships.values() if s.player_id == player_id]

    def update(self, ship: Ship, from_api: bool = False) -> None:
        key = (ship.ship_symbol, ship.player_id)
        self._ships[key] = ship

    def delete(self, ship_symbol: str, player_id: int) -> None:
        key = (ship_symbol, player_id)
        if key in self._ships:
            del self._ships[key]

    def clear_all(self) -> None:
        """Clear all ships from repository (public method for testing)"""
        self._ships.clear()

    def sync_from_api(self, ship_symbol: str, player_id: int, api_client, graph_provider) -> Ship:
        """
        Sync ship state from mock API and update repository.

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID
            api_client: API client to fetch ship state
            graph_provider: Graph provider to reconstruct waypoints

        Returns:
            Ship entity with fresh state from API
        """
        from application.navigation.commands._ship_converter import convert_api_ship_to_entity
        from domain.shared.value_objects import Waypoint

        # Fetch ship from API
        ship_response = api_client.get_ship(ship_symbol)
        ship_data = ship_response.get('data')

        if not ship_data:
            raise ValueError(f"Failed to fetch ship {ship_symbol} from API")

        # Extract location from API response
        nav = ship_data.get('nav', {})
        location_symbol = nav.get('waypointSymbol')
        system_symbol = nav.get('systemSymbol', location_symbol.rsplit('-', 1)[0] if location_symbol else 'UNKNOWN')

        # For testing, create a simple waypoint
        # In real implementation, this would use graph_provider
        current_waypoint = Waypoint(
            symbol=location_symbol,
            x=0.0,
            y=0.0,
            system_symbol=system_symbol
        )

        # Convert API response to Ship entity
        ship = convert_api_ship_to_entity(
            ship_data,
            player_id,
            current_waypoint
        )

        # Update repository with synced state
        self.update(ship, from_api=True)

        return ship


class MockSpaceTradersAPI(ISpaceTradersAPI):
    """Mock API client for testing"""

    def __init__(self):
        self.ships: List[dict] = []
        self.agent_symbol: str = "TEST-AGENT"
        self.agent_fetched: bool = False
        self.agent_data: Optional[dict] = None
        # Tracking for orbit operations
        self.orbit_called: bool = False
        self.orbit_ship_symbol: Optional[str] = None
        self.orbit_calls: List[str] = []
        # Tracking for dock operations
        self.dock_called: bool = False
        self.dock_ship_symbol: Optional[str] = None
        self.dock_calls: List[str] = []
        # Tracking for refuel operations
        self.refuel_called: bool = False
        self.refuel_ship_symbol: Optional[str] = None
        self.refuel_calls: List[str] = []
        self.refuel_cost: int = 100
        # Tracking for navigate operations
        self.navigate_calls: List[tuple] = []
        # Store ships for API responses
        self._ship_registry: Dict[str, Ship] = {}
        # Track current state overrides (nav_status, location, fuel)
        self._ship_state: Dict[str, dict] = {}

    def register_ship(self, ship: Ship):
        """Register a ship so mock API can return it in responses"""
        self._ship_registry[ship.ship_symbol] = ship

    def _ship_to_api_dict(self, ship: Ship, nav_status: Optional[str] = None) -> dict:
        """Convert Ship entity to API response format, applying current state overrides"""
        # Get current state overrides if any
        state = self._ship_state.get(ship.ship_symbol, {})

        current_nav_status = nav_status or state.get("nav_status", ship.nav_status)

        nav_dict = {
            "status": current_nav_status,
            "waypointSymbol": state.get("location", ship.current_location.symbol),
            "systemSymbol": ship.current_location.system_symbol
        }

        # Include route data if ship is IN_TRANSIT
        if current_nav_status == "IN_TRANSIT":
            # Use arrival_time from state if available, otherwise use past time for instant completion
            from datetime import datetime, timezone
            if "arrival_time" in state:
                arrival_time = state["arrival_time"]
                # Use ISO format with microseconds to preserve precision
                arrival_str = arrival_time.isoformat().replace('+00:00', 'Z')
            else:
                arrival_str = "2024-01-01T00:00:00Z"  # Past arrival for instant completion in tests

            nav_dict["route"] = {
                "destination": {
                    "symbol": state.get("location", ship.current_location.symbol),
                    "x": ship.current_location.x,
                    "y": ship.current_location.y,
                    "type": ship.current_location.waypoint_type
                },
                "arrival": arrival_str
            }

        return {
            "symbol": ship.ship_symbol,
            "nav": nav_dict,
            "fuel": {
                "current": state.get("fuel_current", ship.fuel.current),
                "capacity": ship.fuel_capacity
            },
            "cargo": {
                "capacity": ship.cargo_capacity,
                "units": ship.cargo_units
            },
            "engine": {
                "speed": ship.engine_speed
            }
        }

    def get_ships(self):
        return {"data": self.ships}

    def get_agent(self):
        self.agent_fetched = True
        if self.agent_data:
            return self.agent_data
        return {"data": {"symbol": self.agent_symbol}}

    def get_ship(self, ship_symbol: str):
        """Return full ship data, simulating arrival time transitions"""
        from datetime import datetime, timezone
        # Get registered ship and return full data
        if ship_symbol in self._ship_registry:
            ship = self._ship_registry[ship_symbol]

            # Check if ship has arrival time and if it has passed
            state = self._ship_state.get(ship_symbol, {})
            if "arrival_time" in state:
                arrival_time = state["arrival_time"]
                now = datetime.now(timezone.utc)
                # Ensure both times are timezone-aware for comparison
                if arrival_time.tzinfo is None:
                    arrival_time = arrival_time.replace(tzinfo=timezone.utc)
                if now >= arrival_time:
                    # Ship has arrived - transition from IN_TRANSIT to IN_ORBIT
                    state["nav_status"] = "IN_ORBIT"
                    del state["arrival_time"]  # Remove arrival time once arrived
                    self._ship_state[ship_symbol] = state

            return {"data": self._ship_to_api_dict(ship)}
        return {"data": {}}

    def navigate_ship(self, ship_symbol: str, destination: str):
        self.navigate_calls.append((ship_symbol, destination))
        # Get registered ship and return full data
        if ship_symbol in self._ship_registry:
            ship = self._ship_registry[ship_symbol]
            # Calculate fuel consumption
            current_fuel = self._ship_state.get(ship_symbol, {}).get("fuel_current", ship.fuel.current)
            new_fuel = max(0, current_fuel - 30)

            # Update state for this ship
            self._ship_state[ship_symbol] = {
                "nav_status": "IN_TRANSIT",
                "location": destination,
                "fuel_current": new_fuel
            }

            ship_dict = self._ship_to_api_dict(ship)
            return {"data": {"ship": ship_dict}}
        return {"data": {"nav": {"waypointSymbol": destination}}}

    def orbit_ship(self, ship_symbol: str):
        self.orbit_called = True
        self.orbit_ship_symbol = ship_symbol
        self.orbit_calls.append(ship_symbol)
        # Get registered ship and return full data
        if ship_symbol in self._ship_registry:
            ship = self._ship_registry[ship_symbol]
            # Update state
            state = self._ship_state.get(ship_symbol, {})
            state["nav_status"] = "IN_ORBIT"
            self._ship_state[ship_symbol] = state
            return {"data": {"ship": self._ship_to_api_dict(ship)}}
        return {"data": {"nav": {"status": "IN_ORBIT"}}}

    def dock_ship(self, ship_symbol: str):
        self.dock_called = True
        self.dock_ship_symbol = ship_symbol
        self.dock_calls.append(ship_symbol)
        # Get registered ship and return full data
        if ship_symbol in self._ship_registry:
            ship = self._ship_registry[ship_symbol]
            # Update state
            state = self._ship_state.get(ship_symbol, {})
            state["nav_status"] = "DOCKED"
            self._ship_state[ship_symbol] = state
            return {"data": {"ship": self._ship_to_api_dict(ship)}}
        return {"data": {"nav": {"status": "DOCKED"}}}

    def refuel_ship(self, ship_symbol: str):
        self.refuel_called = True
        self.refuel_ship_symbol = ship_symbol
        self.refuel_calls.append(ship_symbol)
        # Get registered ship and return full data with fuel at capacity
        if ship_symbol in self._ship_registry:
            ship = self._ship_registry[ship_symbol]
            # Update state with full fuel
            state = self._ship_state.get(ship_symbol, {})
            state["fuel_current"] = ship.fuel_capacity
            self._ship_state[ship_symbol] = state

            # Create updated ship dict
            ship_dict = self._ship_to_api_dict(ship)
            result = {
                "data": {
                    "ship": ship_dict
                }
            }
            # Only include transaction if refuel_cost is not None
            if self.refuel_cost is not None:
                result["data"]["transaction"] = {"totalPrice": self.refuel_cost}
            return result
        # Fallback for unregistered ships
        result = {"data": {"fuel": {"current": 100, "capacity": 100}}}
        if self.refuel_cost is not None:
            result["transaction"] = {"totalPrice": self.refuel_cost}
        return result

    def set_flight_mode(self, ship_symbol: str, flight_mode: str):
        """Set ship flight mode (CRUISE, DRIFT, BURN, STEALTH)"""
        # Update state with new flight mode
        state = self._ship_state.get(ship_symbol, {})
        state["flight_mode"] = flight_mode
        self._ship_state[ship_symbol] = state

        return {
            "data": {
                "nav": {
                    "status": state.get("nav_status", "IN_ORBIT"),
                    "waypointSymbol": state.get("location", "X1-A1"),
                    "flightMode": flight_mode
                }
            }
        }

    def list_waypoints(self, system_symbol: str, page: int = 1, limit: int = 20):
        return {"data": [], "meta": {"total": 0, "page": page, "limit": limit}}

    def get_shipyard(self, system_symbol: str, waypoint_symbol: str):
        """Get shipyard details at a waypoint"""
        return {"data": {"symbol": waypoint_symbol, "shipTypes": []}}

    def purchase_ship(self, ship_type: str, waypoint_symbol: str):
        """Purchase a ship at a shipyard"""
        return {"data": {"ship": {}, "transaction": {}}}

    def get_market(self, system: str, waypoint: str):
        """Get market data for a waypoint"""
        return {"data": {"symbol": waypoint, "tradeGoods": []}}

    def get_contracts(self, page: int = 1, limit: int = 20):
        """Get all contracts with pagination (stub for testing)"""
        return {"data": [], "meta": {"total": 0, "page": page, "limit": limit}}

    def get_contract(self, contract_id: str):
        """Get specific contract details (stub for testing)"""
        return {"data": {"id": contract_id}}

    def accept_contract(self, contract_id: str):
        """Accept a contract (stub for testing)"""
        return {"data": {"contract": {"id": contract_id, "accepted": True}, "agent": {}}}

    def deliver_contract(self, contract_id: str, ship_symbol: str, trade_symbol: str, units: int):
        """Deliver goods to fulfill contract (stub for testing)"""
        return {"data": {"contract": {"id": contract_id}, "cargo": {}}}

    def fulfill_contract(self, contract_id: str):
        """Complete and fulfill a contract (stub for testing)"""
        return {"data": {"contract": {"id": contract_id, "fulfilled": True}, "agent": {}}}

    def negotiate_contract(self, ship_symbol: str):
        """Negotiate a new contract with ship (stub for testing)"""
        return {"data": {"contract": {"id": "new-contract", "shipSymbol": ship_symbol}}}

    def purchase_cargo(self, ship_symbol: str, trade_symbol: str, units: int):
        """Purchase cargo from market (stub for testing)"""
        return {
            "data": {
                "cargo": {
                    "capacity": 100,
                    "units": units,
                    "inventory": [
                        {
                            "symbol": trade_symbol,
                            "units": units,
                            "name": trade_symbol,
                            "description": f"Test {trade_symbol}"
                        }
                    ]
                },
                "transaction": {
                    "waypointSymbol": "X1-TEST-A1",
                    "shipSymbol": ship_symbol,
                    "tradeSymbol": trade_symbol,
                    "type": "PURCHASE",
                    "units": units,
                    "pricePerUnit": 10,
                    "totalPrice": units * 10
                }
            }
        }

    def jettison_cargo(self, ship_symbol: str, cargo_symbol: str, units: int):
        """Jettison cargo from ship into space (stub for testing)"""
        return {
            "data": {
                "cargo": {
                    "capacity": 100,
                    "units": 0,
                    "inventory": []
                }
            }
        }


@pytest.fixture
def mock_api():
    """Provide mock API client for tests"""
    return MockSpaceTradersAPI()


@pytest.fixture
def mock_ship_repo(mock_api):
    """Provide mock ship repository for tests"""
    return MockShipRepository(mock_api=mock_api)


class MockPlayerRepository(IPlayerRepository):
    """In-memory player repository for testing"""

    def __init__(self):
        self._players: Dict[int, Player] = {}
        self._next_id = 1
        self._agents: Dict[str, int] = {}  # agent_symbol -> player_id

    def create(self, player: Player) -> Player:
        player_id = self._next_id
        self._next_id += 1

        created_player = Player(
            player_id=player_id,
            agent_symbol=player.agent_symbol,
            token=player.token,
            created_at=player.created_at,
            last_active=player.last_active,
            metadata=player.metadata,
            credits=player.credits  # Preserve credits
        )

        self._players[player_id] = created_player
        self._agents[player.agent_symbol] = player_id
        return created_player

    def find_by_id(self, player_id: int) -> Optional[Player]:
        return self._players.get(player_id)

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        player_id = self._agents.get(agent_symbol)
        return self._players.get(player_id) if player_id else None

    def list_all(self) -> List[Player]:
        return list(self._players.values())

    def update(self, player: Player) -> None:
        if player.player_id in self._players:
            self._players[player.player_id] = player

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        return agent_symbol in self._agents


@pytest.fixture
def mock_player_repo():
    """Provide mock player repository for tests"""
    return MockPlayerRepository()


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@pytest.fixture(autouse=True)
def setup_test_environment():
    """Initialize database schema for application tests"""
    from configuration.container import reset_container, get_engine
    from adapters.secondary.persistence.models import metadata

    # Reset container to ensure clean state
    reset_container()

    # Initialize SQLAlchemy schema for in-memory database
    engine = get_engine()
    metadata.create_all(engine)

    yield

    # Cleanup after test
    reset_container()

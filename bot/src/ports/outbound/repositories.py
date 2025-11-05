from abc import ABC, abstractmethod
from typing import Optional, List
from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.navigation.route import Route
from domain.shared.contract import Contract
from domain.shared.value_objects import Waypoint


class IPlayerRepository(ABC):
    """Port for player persistence"""

    @abstractmethod
    def create(self, player: Player) -> Player:
        """Persist new player, returns player with assigned ID"""
        pass

    @abstractmethod
    def find_by_id(self, player_id: int) -> Optional[Player]:
        """Load player by ID"""
        pass

    @abstractmethod
    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        """Load player by agent symbol"""
        pass

    @abstractmethod
    def list_all(self) -> List[Player]:
        """List all registered players"""
        pass

    @abstractmethod
    def update(self, player: Player) -> None:
        """Update existing player"""
        pass

    @abstractmethod
    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        """Check if agent symbol already registered"""
        pass


class IShipRepository(ABC):
    """Port for ship persistence"""

    @abstractmethod
    def create(self, ship: Ship) -> Ship:
        """
        Persist new ship

        Args:
            ship: Ship entity to persist

        Returns:
            The persisted ship (same instance, as ship_symbol is the ID)

        Raises:
            DuplicateShipError: If ship with same symbol already exists for player
        """
        pass

    @abstractmethod
    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        """
        Find ship by symbol and player ID

        Args:
            ship_symbol: Unique ship identifier
            player_id: Owning player's ID

        Returns:
            Ship if found, None otherwise
        """
        pass

    @abstractmethod
    def find_all_by_player(self, player_id: int) -> List[Ship]:
        """
        Find all ships belonging to a player

        Args:
            player_id: Player's ID

        Returns:
            List of ships (empty if none found)
        """
        pass

    @abstractmethod
    def update(self, ship: Ship) -> None:
        """
        Update existing ship

        Args:
            ship: Ship entity with updated state

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        pass

    @abstractmethod
    def delete(self, ship_symbol: str, player_id: int) -> None:
        """
        Delete ship from persistence

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        pass

    @abstractmethod
    def sync_from_api(self, ship_symbol: str, player_id: int, api_client, graph_provider):
        """
        Sync ship state from SpaceTraders API and update database

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID
            api_client: API client to fetch ship state
            graph_provider: Graph provider to reconstruct waypoints

        Returns:
            Ship entity with fresh state from API

        Raises:
            DomainException: If API call fails or ship not found
        """
        pass


class IRouteRepository(ABC):
    """Port for route persistence"""

    @abstractmethod
    def create(self, route: Route) -> Route:
        """
        Persist new route

        Args:
            route: Route entity to persist

        Returns:
            The persisted route (same instance)

        Raises:
            DuplicateRouteError: If route with same ID already exists
        """
        pass

    @abstractmethod
    def find_by_id(self, route_id: str) -> Optional[Route]:
        """
        Find route by ID

        Args:
            route_id: Unique route identifier

        Returns:
            Route if found, None otherwise
        """
        pass

    @abstractmethod
    def find_by_ship(self, ship_symbol: str, player_id: int) -> List[Route]:
        """
        Find all routes for a ship

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID

        Returns:
            List of routes (empty if none found)
        """
        pass

    @abstractmethod
    def update(self, route: Route) -> None:
        """
        Update existing route

        Args:
            route: Route entity with updated state

        Raises:
            RouteNotFoundError: If route doesn't exist
        """
        pass

    @abstractmethod
    def delete(self, route_id: str) -> None:
        """
        Delete route from persistence

        Args:
            route_id: Route's unique identifier

        Raises:
            RouteNotFoundError: If route doesn't exist
        """
        pass


class IContractRepository(ABC):
    """Port for contract persistence"""

    @abstractmethod
    def save(self, contract: Contract, player_id: int) -> None:
        """
        Save or update contract

        Args:
            contract: Contract entity to persist
            player_id: Owning player's ID
        """
        pass

    @abstractmethod
    def find_by_id(self, contract_id: str, player_id: int) -> Optional[Contract]:
        """
        Find contract by ID

        Args:
            contract_id: Unique contract identifier
            player_id: Owning player's ID

        Returns:
            Contract if found, None otherwise
        """
        pass

    @abstractmethod
    def find_all(self, player_id: int) -> List[Contract]:
        """
        Find all contracts for a player

        Args:
            player_id: Player's ID

        Returns:
            List of contracts (empty if none found)
        """
        pass

    @abstractmethod
    def find_active(self, player_id: int) -> List[Contract]:
        """
        Find active (accepted but not fulfilled) contracts

        Args:
            player_id: Player's ID

        Returns:
            List of active contracts
        """
        pass


class IWaypointRepository(ABC):
    """Port for waypoint caching persistence"""

    @abstractmethod
    def save_waypoints(self, waypoints: List[Waypoint]) -> None:
        """
        Save or update waypoints in cache

        Args:
            waypoints: List of Waypoint value objects to cache
        """
        pass

    @abstractmethod
    def find_by_system(self, system_symbol: str) -> List[Waypoint]:
        """
        Find all waypoints in a system

        Args:
            system_symbol: System identifier (e.g., "X1-GZ7")

        Returns:
            List of cached waypoints (empty if none cached)
        """
        pass

    @abstractmethod
    def find_by_trait(self, system_symbol: str, trait: str) -> List[Waypoint]:
        """
        Find waypoints with a specific trait

        Args:
            system_symbol: System identifier
            trait: Trait symbol (e.g., "SHIPYARD", "MARKETPLACE")

        Returns:
            List of waypoints with the trait
        """
        pass

    @abstractmethod
    def find_by_fuel(self, system_symbol: str) -> List[Waypoint]:
        """
        Find waypoints with fuel stations

        Args:
            system_symbol: System identifier

        Returns:
            List of waypoints with fuel available
        """
        pass

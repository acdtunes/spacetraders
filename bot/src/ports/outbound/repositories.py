from abc import ABC, abstractmethod
from typing import Optional, List, Dict, Any
from datetime import datetime
from domain.shared.player import Player
from domain.shared.ship import Ship
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
    """
    Port for ship data retrieval (API-only, no caching).

    All ship data is fetched directly from the SpaceTraders API on each query.
    This ensures ship state (location, fuel, cargo) is always fresh and consistent.
    """

    @abstractmethod
    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        """
        Find ship by symbol and player ID from SpaceTraders API.

        Fetches live ship data including:
        - Current location (waypoint)
        - Fuel levels
        - Cargo contents
        - Navigation status (DOCKED, IN_ORBIT, IN_TRANSIT)

        Args:
            ship_symbol: Unique ship identifier
            player_id: Owning player's ID

        Returns:
            Ship entity with live data from API, or None if not found

        Raises:
            DomainException: If API call fails
        """
        pass

    @abstractmethod
    def find_all_by_player(self, player_id: int) -> List[Ship]:
        """
        Find all ships belonging to a player from SpaceTraders API.

        Fetches live fleet data for all ships owned by the player.

        Args:
            player_id: Player's ID

        Returns:
            List of ships with live data from API (empty if player has no ships)

        Raises:
            DomainException: If API call fails
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
    def save_waypoints(self, waypoints: List[Waypoint], synced_at: Optional[datetime] = None, replace_system: bool = False) -> None:
        """
        Save or update waypoints in cache with timestamp

        Args:
            waypoints: List of Waypoint value objects to cache
            synced_at: Timestamp when waypoints were synced (defaults to now)
            replace_system: If True, delete all existing waypoints for the system first (default: False)
        """
        pass

    @abstractmethod
    def find_by_system(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find all waypoints in a system with optional lazy-loading

        Args:
            system_symbol: System identifier (e.g., "X1-GZ7")
            player_id: Optional player ID for lazy-loading from API if cache is stale

        Returns:
            List of cached waypoints (empty if none cached and no player_id provided)
        """
        pass

    @abstractmethod
    def find_by_trait(self, system_symbol: str, trait: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with a specific trait with optional lazy-loading

        Args:
            system_symbol: System identifier
            trait: Trait symbol (e.g., "SHIPYARD", "MARKETPLACE")
            player_id: Optional player ID for lazy-loading from API if cache is stale

        Returns:
            List of waypoints with the trait
        """
        pass

    @abstractmethod
    def find_by_fuel(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with fuel stations with optional lazy-loading

        Args:
            system_symbol: System identifier
            player_id: Optional player ID for lazy-loading from API if cache is stale

        Returns:
            List of waypoints with fuel available
        """
        pass

    @abstractmethod
    def get_system_sync_time(self, system_symbol: str) -> Optional[datetime]:
        """
        Get the last sync time for a system

        Args:
            system_symbol: System identifier

        Returns:
            Timestamp when system was last synced, None if never synced
        """
        pass

    @abstractmethod
    def is_cache_stale(self, system_symbol: str, ttl_seconds: int = 7200) -> bool:
        """
        Check if cached data for a system is stale

        Args:
            system_symbol: System identifier
            ttl_seconds: Time-to-live in seconds (default: 7200 = 2 hours)

        Returns:
            True if cache is stale or doesn't exist, False if fresh
        """
        pass


class IShipAssignmentRepository(ABC):
    """Port for ship assignment persistence

    Manages ship-to-container assignments to prevent double-booking.
    Ensures ships are only assigned to one operation at a time.
    """

    @abstractmethod
    def assign(
        self,
        player_id: int,
        ship_symbol: str,
        container_id: str,
        operation: str
    ) -> bool:
        """
        Assign ship to container

        Args:
            player_id: Player ID
            ship_symbol: Ship to assign
            container_id: Container ID to assign to
            operation: Operation type (e.g., 'navigation', 'mining', 'scouting')

        Returns:
            True if assignment successful, False if already assigned
        """
        pass

    @abstractmethod
    def release(
        self,
        player_id: int,
        ship_symbol: str,
        reason: str = "completed"
    ) -> None:
        """
        Release ship assignment

        Args:
            player_id: Player ID
            ship_symbol: Ship to release
            reason: Reason for release (e.g., 'completed', 'stopped', 'failed')
        """
        pass

    @abstractmethod
    def check_available(
        self,
        player_id: int,
        ship_symbol: str
    ) -> bool:
        """
        Check if ship is available for assignment

        Args:
            player_id: Player ID
            ship_symbol: Ship to check

        Returns:
            True if ship is available (idle or not assigned)
        """
        pass

    @abstractmethod
    def get_assignment_info(
        self,
        player_id: int,
        ship_symbol: str
    ) -> Optional[Dict[str, Any]]:
        """
        Get assignment info for a ship

        Args:
            player_id: Player ID
            ship_symbol: Ship to check

        Returns:
            Dict with status, container_id, operation, assigned_at,
            released_at, release_reason, or None if not found
        """
        pass

    @abstractmethod
    def release_all_active_assignments(self, reason: str = "daemon_restart") -> int:
        """
        Release all active ship assignments

        Called on daemon startup to clean up zombie assignments
        from crashed or killed daemon instances.

        Args:
            reason: Reason for release (default: 'daemon_restart')

        Returns:
            Number of assignments released
        """
        pass

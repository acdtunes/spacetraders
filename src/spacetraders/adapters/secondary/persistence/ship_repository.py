import logging
import sqlite3
import time
from typing import Optional, List

from ....ports.outbound.repositories import IShipRepository
from ....ports.outbound.graph_provider import ISystemGraphProvider
from ....domain.shared.ship import Ship
from ....domain.shared.value_objects import Waypoint
from ....domain.shared.exceptions import DomainException
from .database import Database
from .mappers import ShipMapper

logger = logging.getLogger(__name__)


class ShipNotFoundError(DomainException):
    """Raised when ship is not found in repository"""
    pass


class DuplicateShipError(DomainException):
    """Raised when attempting to create duplicate ship"""
    pass


class ShipRepository(IShipRepository):
    """SQLite implementation of ship repository"""

    def __init__(self, database: Database, graph_provider: Optional[ISystemGraphProvider] = None):
        """
        Initialize ship repository

        Args:
            database: Database instance for persistence
            graph_provider: Optional provider to get system graphs for waypoint reconstruction.
                          If None, waypoints will use minimal fallback data.
        """
        self._database = database
        self._graph_provider = graph_provider
        logger.info("ShipRepository initialized")

    def create(self, ship: Ship) -> Ship:
        """
        Persist new ship

        Args:
            ship: Ship entity to persist

        Returns:
            The persisted ship (same instance)

        Raises:
            DuplicateShipError: If ship with same symbol already exists for player
        """
        logger.debug(f"Creating ship: {ship.ship_symbol} for player {ship.player_id}")

        ship_dict = ShipMapper.to_db_dict(ship)

        try:
            with self._database.transaction() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    INSERT INTO ships (
                        ship_symbol, player_id, current_location_symbol,
                        fuel_current, fuel_capacity, cargo_capacity,
                        cargo_units, engine_speed, nav_status, system_symbol
                    ) VALUES (
                        :ship_symbol, :player_id, :current_location_symbol,
                        :fuel_current, :fuel_capacity, :cargo_capacity,
                        :cargo_units, :engine_speed, :nav_status, :system_symbol
                    )
                """, ship_dict)

            logger.info(f"Ship created: {ship.ship_symbol}")
            return ship

        except sqlite3.IntegrityError as e:
            if "UNIQUE constraint failed" in str(e) or "PRIMARY KEY constraint failed" in str(e):
                logger.error(f"Duplicate ship: {ship.ship_symbol} for player {ship.player_id}")
                raise DuplicateShipError(
                    f"Ship {ship.ship_symbol} already exists for player {ship.player_id}"
                ) from e
            raise

    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        """
        Find ship by symbol and player ID

        Args:
            ship_symbol: Unique ship identifier
            player_id: Owning player's ID

        Returns:
            Ship if found, None otherwise
        """
        logger.debug(f"Finding ship: {ship_symbol} for player {player_id}")

        with self._database.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT ship_symbol, player_id, current_location_symbol,
                       fuel_current, fuel_capacity, cargo_capacity,
                       cargo_units, engine_speed, nav_status, system_symbol
                FROM ships
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            row = cursor.fetchone()

            if row is None:
                logger.debug(f"Ship not found: {ship_symbol}")
                return None

            # Reconstruct waypoint from graph
            waypoint = self._reconstruct_waypoint(
                row["current_location_symbol"],
                row["system_symbol"]
            )

            ship = ShipMapper.from_db_row(row, waypoint)
            logger.debug(f"Ship found: {ship_symbol}")
            return ship

    def find_all_by_player(self, player_id: int) -> List[Ship]:
        """
        Find all ships belonging to a player

        Args:
            player_id: Player's ID

        Returns:
            List of ships (empty if none found)
        """
        logger.debug(f"Finding all ships for player {player_id}")

        with self._database.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT ship_symbol, player_id, current_location_symbol,
                       fuel_current, fuel_capacity, cargo_capacity,
                       cargo_units, engine_speed, nav_status, system_symbol
                FROM ships
                WHERE player_id = ?
                ORDER BY ship_symbol
            """, (player_id,))

            rows = cursor.fetchall()
            ships = []

            for row in rows:
                try:
                    waypoint = self._reconstruct_waypoint(
                        row["current_location_symbol"],
                        row["system_symbol"]
                    )
                    ship = ShipMapper.from_db_row(row, waypoint)
                    ships.append(ship)
                except Exception as e:
                    logger.error(f"Failed to reconstruct ship {row['ship_symbol']}: {e}")
                    # Skip this ship and continue with others

            logger.info(f"Found {len(ships)} ships for player {player_id}")
            return ships

    def update(self, ship: Ship, from_api: bool = False) -> None:
        """
        Update existing ship

        Args:
            ship: Ship entity with updated state
            from_api: If True, data came from API and synced_at should be updated

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        logger.debug(f"Updating ship: {ship.ship_symbol} (from_api={from_api})")

        ship_dict = ShipMapper.to_db_dict(ship)

        with self._database.transaction() as conn:
            cursor = conn.cursor()

            if from_api:
                # Data from API: update synced_at timestamp
                cursor.execute("""
                    UPDATE ships
                    SET current_location_symbol = :current_location_symbol,
                        fuel_current = :fuel_current,
                        fuel_capacity = :fuel_capacity,
                        cargo_capacity = :cargo_capacity,
                        cargo_units = :cargo_units,
                        engine_speed = :engine_speed,
                        nav_status = :nav_status,
                        system_symbol = :system_symbol,
                        synced_at = CURRENT_TIMESTAMP
                    WHERE ship_symbol = :ship_symbol AND player_id = :player_id
                """, ship_dict)
            else:
                # Local update: preserve synced_at timestamp
                cursor.execute("""
                    UPDATE ships
                    SET current_location_symbol = :current_location_symbol,
                        fuel_current = :fuel_current,
                        fuel_capacity = :fuel_capacity,
                        cargo_capacity = :cargo_capacity,
                        cargo_units = :cargo_units,
                        engine_speed = :engine_speed,
                        nav_status = :nav_status,
                        system_symbol = :system_symbol
                    WHERE ship_symbol = :ship_symbol AND player_id = :player_id
                """, ship_dict)

            if cursor.rowcount == 0:
                logger.error(f"Ship not found for update: {ship.ship_symbol}")
                raise ShipNotFoundError(
                    f"Ship {ship.ship_symbol} not found for player {ship.player_id}"
                )

        logger.info(f"Ship updated: {ship.ship_symbol}")

    def delete(self, ship_symbol: str, player_id: int) -> None:
        """
        Delete ship from persistence

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID

        Raises:
            ShipNotFoundError: If ship doesn't exist
        """
        logger.debug(f"Deleting ship: {ship_symbol} for player {player_id}")

        with self._database.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                DELETE FROM ships
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            if cursor.rowcount == 0:
                logger.error(f"Ship not found for deletion: {ship_symbol}")
                raise ShipNotFoundError(
                    f"Ship {ship_symbol} not found for player {player_id}"
                )

        logger.info(f"Ship deleted: {ship_symbol}")

    def sync_from_api(self, ship_symbol: str, player_id: int, api_client, graph_provider) -> Ship:
        """
        Sync ship state from SpaceTraders API and update database.

        This encapsulates the common pattern of:
        1. Fetch ship from API
        2. Convert API response to Ship entity
        3. Update database with from_api=True

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
        logger.debug(f"Syncing ship {ship_symbol} from API for player {player_id}")

        # Import here to avoid circular dependency
        from ....application.navigation.commands._ship_converter import convert_api_ship_to_entity

        # Fetch ship from API
        ship_response = api_client.get_ship(ship_symbol)
        ship_data = ship_response.get('data')

        if not ship_data:
            raise DomainException(f"Failed to fetch ship {ship_symbol} from API")

        # Extract location from API response
        nav = ship_data.get('nav', {})
        location_symbol = nav.get('waypointSymbol')

        if not location_symbol:
            raise DomainException(f"API response missing waypointSymbol for ship {ship_symbol}")

        # Get system symbol from location
        system_symbol = location_symbol.rsplit('-', 1)[0] if '-' in location_symbol else location_symbol

        # Get graph to reconstruct waypoint with full details
        graph_result = graph_provider.get_graph(system_symbol)
        graph = graph_result.graph
        waypoints = graph.get('waypoints', {})

        # Get waypoint object for current location
        if location_symbol in waypoints:
            wp_data = waypoints[location_symbol]
            # Check if wp_data is already a Waypoint object
            if isinstance(wp_data, Waypoint):
                current_waypoint = wp_data
            else:
                # Convert dict to Waypoint
                # CRITICAL: In production graphs, the waypoint symbol is the DICTIONARY KEY,
                # not a field in the data. Use location_symbol (the key) as the symbol.
                current_waypoint = Waypoint(
                    symbol=location_symbol,  # Symbol is the dict key, not a field!
                    x=wp_data.get("x", 0.0),
                    y=wp_data.get("y", 0.0),
                    system_symbol=wp_data.get("systemSymbol", system_symbol),
                    waypoint_type=wp_data.get("type"),
                    traits=tuple(wp_data.get("traits", [])),
                    has_fuel=wp_data.get("has_fuel", False),
                    orbitals=tuple(wp_data.get("orbitals", []))
                )
        else:
            # Fallback to minimal waypoint if not in graph
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

        # Update database with synced state
        self.update(ship, from_api=True)

        logger.info(f"Ship {ship_symbol} synced from API: location={ship.current_location.symbol}, status={ship.nav_status}, fuel={ship.fuel.current}/{ship.fuel_capacity}")
        return ship

    def _reconstruct_waypoint(self, waypoint_symbol: str, system_symbol: str) -> Waypoint:
        """
        Reconstruct waypoint object from graph data

        Args:
            waypoint_symbol: Waypoint symbol to reconstruct
            system_symbol: System the waypoint belongs to

        Returns:
            Reconstructed Waypoint object

        Raises:
            ValueError: If waypoint not found in graph
        """
        # If no graph provider, use minimal fallback immediately
        if self._graph_provider is None:
            logger.debug(f"No graph provider - using minimal waypoint for {waypoint_symbol}")
            return Waypoint(
                symbol=waypoint_symbol,
                x=0.0,
                y=0.0,
                system_symbol=system_symbol
            )

        # Start timing for performance measurement
        start = time.perf_counter()

        try:
            # Get graph from provider
            graph_result = self._graph_provider.get_graph(system_symbol)
            waypoints = graph_result.graph.get("waypoints", {})

            if waypoint_symbol not in waypoints:
                logger.error(f"Waypoint {waypoint_symbol} not found in system {system_symbol}")
                raise ValueError(f"Waypoint {waypoint_symbol} not found in graph")

            wp_data = waypoints[waypoint_symbol]

            # Reconstruct waypoint value object
            # CRITICAL: In production graphs, the waypoint symbol is the DICTIONARY KEY,
            # not a field in the data. Use waypoint_symbol (the key) as the symbol.
            waypoint = Waypoint(
                symbol=waypoint_symbol,  # Symbol is the dict key, not a field!
                x=wp_data["x"],
                y=wp_data["y"],
                system_symbol=wp_data.get("systemSymbol"),
                waypoint_type=wp_data.get("type"),
                traits=tuple(wp_data.get("traits", [])),
                has_fuel=wp_data.get("has_fuel", False),
                orbitals=tuple(wp_data.get("orbitals", []))
            )

            # Log timing for performance analysis
            elapsed = time.perf_counter() - start
            logger.info(f"Waypoint reconstruction {waypoint_symbol}: {elapsed*1000:.2f}ms")

            return waypoint

        except Exception as e:
            elapsed = time.perf_counter() - start
            logger.error(f"Failed to reconstruct waypoint {waypoint_symbol} ({elapsed*1000:.2f}ms): {e}")
            # Create minimal waypoint as fallback
            return Waypoint(
                symbol=waypoint_symbol,
                x=0.0,
                y=0.0,
                system_symbol=system_symbol
            )

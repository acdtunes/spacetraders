import logging
import sqlite3
from typing import Optional, List
from datetime import datetime

from ports.outbound.repositories import IRouteRepository
from domain.navigation.route import Route
from domain.shared.exceptions import DomainException
from .database import Database
from .mappers import RouteMapper

logger = logging.getLogger(__name__)


class RouteNotFoundError(DomainException):
    """Raised when route is not found in repository"""
    pass


class DuplicateRouteError(DomainException):
    """Raised when attempting to create duplicate route"""
    pass


class RouteRepository(IRouteRepository):
    """SQLite implementation of route repository"""

    def __init__(self, database: Database):
        """
        Initialize route repository

        Args:
            database: Database instance for persistence
        """
        self._database = database
        logger.info("RouteRepository initialized")

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
        logger.debug(f"Creating route: {route.route_id} for ship {route.ship_symbol}")

        route_dict = RouteMapper.to_db_dict(route)
        route_dict["created_at"] = datetime.utcnow().isoformat()

        try:
            with self._database.transaction() as conn:
                cursor = conn.cursor()
                cursor.execute("""
                    INSERT INTO routes (
                        route_id, ship_symbol, player_id, status,
                        current_segment_index, ship_fuel_capacity,
                        segments_json, created_at
                    ) VALUES (
                        :route_id, :ship_symbol, :player_id, :status,
                        :current_segment_index, :ship_fuel_capacity,
                        :segments_json, :created_at
                    )
                """, route_dict)

            logger.info(f"Route created: {route.route_id}")
            return route

        except sqlite3.IntegrityError as e:
            if "UNIQUE constraint failed" in str(e) or "PRIMARY KEY constraint failed" in str(e):
                logger.error(f"Duplicate route: {route.route_id}")
                raise DuplicateRouteError(
                    f"Route {route.route_id} already exists"
                ) from e
            raise

    def find_by_id(self, route_id: str) -> Optional[Route]:
        """
        Find route by ID

        Args:
            route_id: Unique route identifier

        Returns:
            Route if found, None otherwise
        """
        logger.debug(f"Finding route: {route_id}")

        with self._database.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT route_id, ship_symbol, player_id, status,
                       current_segment_index, ship_fuel_capacity,
                       segments_json, created_at
                FROM routes
                WHERE route_id = ?
            """, (route_id,))

            row = cursor.fetchone()

            if row is None:
                logger.debug(f"Route not found: {route_id}")
                return None

            route = RouteMapper.from_db_row(row)
            logger.debug(f"Route found: {route_id}")
            return route

    def find_by_ship(self, ship_symbol: str, player_id: int) -> List[Route]:
        """
        Find all routes for a ship

        Args:
            ship_symbol: Ship's unique identifier
            player_id: Owning player's ID

        Returns:
            List of routes (empty if none found)
        """
        logger.debug(f"Finding routes for ship: {ship_symbol} (player {player_id})")

        with self._database.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT route_id, ship_symbol, player_id, status,
                       current_segment_index, ship_fuel_capacity,
                       segments_json, created_at
                FROM routes
                WHERE ship_symbol = ? AND player_id = ?
                ORDER BY created_at DESC
            """, (ship_symbol, player_id))

            rows = cursor.fetchall()
            routes = []

            for row in rows:
                try:
                    route = RouteMapper.from_db_row(row)
                    routes.append(route)
                except Exception as e:
                    logger.error(f"Failed to deserialize route {row['route_id']}: {e}")
                    # Skip this route and continue with others

            logger.info(f"Found {len(routes)} routes for ship {ship_symbol}")
            return routes

    def update(self, route: Route) -> None:
        """
        Update existing route

        Args:
            route: Route entity with updated state

        Raises:
            RouteNotFoundError: If route doesn't exist
        """
        logger.debug(f"Updating route: {route.route_id}")

        route_dict = RouteMapper.to_db_dict(route)

        with self._database.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE routes
                SET status = :status,
                    current_segment_index = :current_segment_index,
                    ship_fuel_capacity = :ship_fuel_capacity,
                    segments_json = :segments_json
                WHERE route_id = :route_id
            """, route_dict)

            if cursor.rowcount == 0:
                logger.error(f"Route not found for update: {route.route_id}")
                raise RouteNotFoundError(
                    f"Route {route.route_id} not found"
                )

        logger.info(f"Route updated: {route.route_id}")

    def delete(self, route_id: str) -> None:
        """
        Delete route from persistence

        Args:
            route_id: Route's unique identifier

        Raises:
            RouteNotFoundError: If route doesn't exist
        """
        logger.debug(f"Deleting route: {route_id}")

        with self._database.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                DELETE FROM routes
                WHERE route_id = ?
            """, (route_id,))

            if cursor.rowcount == 0:
                logger.error(f"Route not found for deletion: {route_id}")
                raise RouteNotFoundError(
                    f"Route {route_id} not found"
                )

        logger.info(f"Route deleted: {route_id}")

    def find_active_routes(self, player_id: int) -> List[Route]:
        """
        Find all active (PLANNED or EXECUTING) routes for a player

        This is a convenience method for operations that need to check
        for currently active navigation plans.

        Args:
            player_id: Player's ID

        Returns:
            List of active routes (empty if none found)
        """
        logger.debug(f"Finding active routes for player {player_id}")

        with self._database.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT route_id, ship_symbol, player_id, status,
                       current_segment_index, ship_fuel_capacity,
                       segments_json, created_at
                FROM routes
                WHERE player_id = ? AND status IN ('PLANNED', 'EXECUTING')
                ORDER BY created_at DESC
            """, (player_id,))

            rows = cursor.fetchall()
            routes = []

            for row in rows:
                try:
                    route = RouteMapper.from_db_row(row)
                    routes.append(route)
                except Exception as e:
                    logger.error(f"Failed to deserialize route {row['route_id']}: {e}")

            logger.info(f"Found {len(routes)} active routes for player {player_id}")
            return routes

    def cleanup_completed_routes(self, player_id: int, keep_recent: int = 10) -> int:
        """
        Clean up old completed routes, keeping only recent ones

        This is a maintenance method to prevent route table from growing too large.

        Args:
            player_id: Player's ID
            keep_recent: Number of recent completed routes to keep (default: 10)

        Returns:
            Number of routes deleted
        """
        logger.debug(f"Cleaning up completed routes for player {player_id}, keeping {keep_recent}")

        with self._database.transaction() as conn:
            cursor = conn.cursor()

            # Find route IDs to delete (completed/failed/aborted, keeping most recent)
            cursor.execute("""
                SELECT route_id
                FROM routes
                WHERE player_id = ? AND status IN ('COMPLETED', 'FAILED', 'ABORTED')
                ORDER BY created_at DESC
                LIMIT -1 OFFSET ?
            """, (player_id, keep_recent))

            route_ids = [row["route_id"] for row in cursor.fetchall()]

            if not route_ids:
                logger.info("No routes to clean up")
                return 0

            # Delete old routes
            placeholders = ",".join("?" * len(route_ids))
            cursor.execute(f"""
                DELETE FROM routes
                WHERE route_id IN ({placeholders})
            """, route_ids)

            deleted_count = cursor.rowcount

        logger.info(f"Cleaned up {deleted_count} completed routes for player {player_id}")
        return deleted_count

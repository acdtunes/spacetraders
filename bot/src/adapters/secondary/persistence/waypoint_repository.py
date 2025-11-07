"""Waypoint repository implementation for caching waypoint data"""
import json
import logging
from typing import List, Optional
from datetime import datetime

from ports.repositories import IWaypointRepository
from domain.shared.value_objects import Waypoint
from .database import Database

logger = logging.getLogger(__name__)


class WaypointRepository(IWaypointRepository):
    """SQLite implementation of waypoint caching repository"""

    TTL_SECONDS = 7200  # 2 hours

    def __init__(self, database: Database, api_client_factory=None):
        """
        Initialize WaypointRepository

        Args:
            database: Database instance for persistence
            api_client_factory: Optional factory function (player_id -> ISpaceTradersAPI)
                              for lazy-loading waypoints from API
        """
        self._db = database
        self._api_client_factory = api_client_factory

    def save_waypoints(self, waypoints: List[Waypoint], synced_at: Optional[datetime] = None, replace_system: bool = False) -> None:
        """
        Save or update waypoints in cache using UPSERT with timestamp

        Args:
            waypoints: List of Waypoint value objects to cache
            synced_at: Timestamp when waypoints were synced (defaults to now)
            replace_system: If True, delete all existing waypoints for the system first (default: False)
        """
        if not waypoints:
            return

        if synced_at is None:
            synced_at = datetime.now()

        sync_time_str = synced_at.isoformat()
        system_symbol = waypoints[0].system_symbol

        with self._db.transaction() as conn:
            # Clear old waypoints for the system if replace_system is True
            if replace_system:
                conn.execute("""
                    DELETE FROM waypoints
                    WHERE system_symbol = ?
                """, (system_symbol,))

            # Insert new waypoints
            for waypoint in waypoints:
                conn.execute("""
                    INSERT INTO waypoints
                        (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals, synced_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(waypoint_symbol)
                    DO UPDATE SET
                        system_symbol = excluded.system_symbol,
                        type = excluded.type,
                        x = excluded.x,
                        y = excluded.y,
                        traits = excluded.traits,
                        has_fuel = excluded.has_fuel,
                        orbitals = excluded.orbitals,
                        synced_at = excluded.synced_at
                """, (
                    waypoint.symbol,
                    waypoint.system_symbol,
                    waypoint.waypoint_type,
                    waypoint.x,
                    waypoint.y,
                    json.dumps(waypoint.traits) if waypoint.traits else None,
                    1 if waypoint.has_fuel else 0,
                    json.dumps(waypoint.orbitals) if waypoint.orbitals else None,
                    sync_time_str
                ))

        logger.debug(f"Saved {len(waypoints)} waypoints to cache with timestamp {sync_time_str}")

    def find_by_system(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find all waypoints in a system with automatic lazy-loading

        Checks cache freshness. If stale/empty and player_id provided,
        fetches from API and caches.

        Args:
            system_symbol: System identifier (e.g., "X1-GZ7")
            player_id: Optional player ID for lazy-loading from API

        Returns:
            List of cached waypoints (empty if none cached and no player_id)
        """
        # Check cache freshness
        is_stale = self.is_cache_stale(system_symbol, self.TTL_SECONDS)

        # Lazy-load from API if needed
        if is_stale and player_id and self._api_client_factory:
            logger.info(f"Cache stale for system {system_symbol}, fetching from API")
            self._fetch_and_cache_from_api(system_symbol, player_id)

        # Return from cache (now guaranteed fresh if API available)
        return self._query_from_database(system_symbol)

    def _query_from_database(self, system_symbol: str) -> List[Waypoint]:
        """Query waypoints from database"""
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals
                FROM waypoints
                WHERE system_symbol = ?
                ORDER BY waypoint_symbol
            """, (system_symbol,))

            rows = cursor.fetchall()
            return [self._row_to_waypoint(row) for row in rows]

    def _fetch_and_cache_from_api(self, system_symbol: str, player_id: int):
        """Fetch all waypoints from API and cache them"""
        api_client = self._api_client_factory(player_id)
        all_waypoints = []
        page = 1

        while True:
            response = api_client.list_waypoints(
                system_symbol=system_symbol,
                page=page,
                limit=20
            )

            waypoint_data = response.get("data", [])
            if not waypoint_data:
                break

            # Convert to Waypoint objects
            for wp_data in waypoint_data:
                waypoint = self._convert_api_to_waypoint(wp_data, system_symbol)
                all_waypoints.append(waypoint)

            # Check pagination
            meta = response.get("meta", {})
            if page * 20 >= meta.get("total", 0):
                break
            page += 1

        # Save to cache
        if all_waypoints:
            self.save_waypoints(all_waypoints, replace_system=True)
            logger.info(f"Cached {len(all_waypoints)} waypoints for system {system_symbol}")

    def _convert_api_to_waypoint(self, wp_data: dict, system_symbol: str) -> Waypoint:
        """Convert API response to Waypoint value object"""
        traits = tuple(t["symbol"] for t in wp_data.get("traits", []))
        orbitals = tuple(o["symbol"] for o in wp_data.get("orbitals", []))

        return Waypoint(
            symbol=wp_data["symbol"],
            x=float(wp_data["x"]),
            y=float(wp_data["y"]),
            system_symbol=wp_data.get("systemSymbol", system_symbol),
            waypoint_type=wp_data["type"],
            traits=traits,
            has_fuel=any(t == "MARKETPLACE" for t in traits),  # Simplified fuel detection
            orbitals=orbitals
        )

    def find_by_trait(self, system_symbol: str, trait: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with a specific trait with automatic lazy-loading

        Args:
            system_symbol: System identifier
            trait: Trait symbol (e.g., "SHIPYARD", "MARKETPLACE")
            player_id: Optional player ID for lazy-loading from API

        Returns:
            List of waypoints with the trait
        """
        # Lazy-load if needed (same logic as find_by_system)
        is_stale = self.is_cache_stale(system_symbol, self.TTL_SECONDS)
        if is_stale and player_id and self._api_client_factory:
            logger.info(f"Cache stale for system {system_symbol}, fetching from API before filtering")
            self._fetch_and_cache_from_api(system_symbol, player_id)

        # Query with filter
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals
                FROM waypoints
                WHERE system_symbol = ?
                  AND traits LIKE ?
                ORDER BY waypoint_symbol
            """, (system_symbol, f'%"{trait}"%'))

            rows = cursor.fetchall()
            waypoints = [self._row_to_waypoint(row) for row in rows]

            # Filter in Python to ensure exact trait match (JSON contains the trait)
            return [wp for wp in waypoints if trait in wp.traits]

    def find_by_fuel(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with fuel stations with automatic lazy-loading

        Args:
            system_symbol: System identifier
            player_id: Optional player ID for lazy-loading from API

        Returns:
            List of waypoints with fuel available
        """
        # Lazy-load if needed (same logic as find_by_system)
        is_stale = self.is_cache_stale(system_symbol, self.TTL_SECONDS)
        if is_stale and player_id and self._api_client_factory:
            logger.info(f"Cache stale for system {system_symbol}, fetching from API before fuel filter")
            self._fetch_and_cache_from_api(system_symbol, player_id)

        # Query with fuel filter
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals
                FROM waypoints
                WHERE system_symbol = ?
                  AND has_fuel = 1
                ORDER BY waypoint_symbol
            """, (system_symbol,))

            rows = cursor.fetchall()
            return [self._row_to_waypoint(row) for row in rows]

    def get_system_sync_time(self, system_symbol: str) -> Optional[datetime]:
        """
        Get the last sync time for a system

        Args:
            system_symbol: System identifier

        Returns:
            Timestamp when system was last synced, None if never synced
        """
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT MAX(synced_at) as last_sync
                FROM waypoints
                WHERE system_symbol = ?
            """, (system_symbol,))

            row = cursor.fetchone()
            if row and row['last_sync']:
                return datetime.fromisoformat(row['last_sync'])

            return None

    def is_cache_stale(self, system_symbol: str, ttl_seconds: int = 7200) -> bool:
        """
        Check if cached data for a system is stale

        Args:
            system_symbol: System identifier
            ttl_seconds: Time-to-live in seconds (default: 7200 = 2 hours)

        Returns:
            True if cache is stale or doesn't exist, False if fresh
        """
        sync_time = self.get_system_sync_time(system_symbol)
        if sync_time is None:
            return True

        age_seconds = (datetime.now() - sync_time).total_seconds()
        return age_seconds > ttl_seconds

    def _row_to_waypoint(self, row) -> Waypoint:
        """
        Convert database row to Waypoint value object

        Args:
            row: SQLite row object

        Returns:
            Waypoint value object
        """
        traits_json = row['traits']
        traits = tuple(json.loads(traits_json)) if traits_json else ()

        orbitals_json = row['orbitals']
        orbitals = tuple(json.loads(orbitals_json)) if orbitals_json else ()

        return Waypoint(
            symbol=row['waypoint_symbol'],
            x=row['x'],
            y=row['y'],
            system_symbol=row['system_symbol'],
            waypoint_type=row['type'],
            traits=traits,
            has_fuel=bool(row['has_fuel']),
            orbitals=orbitals
        )

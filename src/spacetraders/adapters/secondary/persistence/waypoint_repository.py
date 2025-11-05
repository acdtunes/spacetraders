"""Waypoint repository implementation for caching waypoint data"""
import json
import logging
from typing import List

from ....ports.repositories import IWaypointRepository
from ....domain.shared.value_objects import Waypoint
from .database import Database

logger = logging.getLogger(__name__)


class WaypointRepository(IWaypointRepository):
    """SQLite implementation of waypoint caching repository"""

    def __init__(self, database: Database):
        """
        Initialize WaypointRepository

        Args:
            database: Database instance for persistence
        """
        self._db = database

    def save_waypoints(self, waypoints: List[Waypoint]) -> None:
        """
        Save or update waypoints in cache using UPSERT

        Args:
            waypoints: List of Waypoint value objects to cache
        """
        with self._db.transaction() as conn:
            for waypoint in waypoints:
                conn.execute("""
                    INSERT INTO waypoints
                        (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(waypoint_symbol)
                    DO UPDATE SET
                        system_symbol = excluded.system_symbol,
                        type = excluded.type,
                        x = excluded.x,
                        y = excluded.y,
                        traits = excluded.traits,
                        has_fuel = excluded.has_fuel,
                        orbitals = excluded.orbitals
                """, (
                    waypoint.symbol,
                    waypoint.system_symbol,
                    waypoint.waypoint_type,
                    waypoint.x,
                    waypoint.y,
                    json.dumps(waypoint.traits) if waypoint.traits else None,
                    1 if waypoint.has_fuel else 0,
                    json.dumps(waypoint.orbitals) if waypoint.orbitals else None
                ))

        logger.debug(f"Saved {len(waypoints)} waypoints to cache")

    def find_by_system(self, system_symbol: str) -> List[Waypoint]:
        """
        Find all waypoints in a system

        Args:
            system_symbol: System identifier (e.g., "X1-GZ7")

        Returns:
            List of cached waypoints (empty if none cached)
        """
        with self._db.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals
                FROM waypoints
                WHERE system_symbol = ?
                ORDER BY waypoint_symbol
            """, (system_symbol,))

            rows = cursor.fetchall()
            return [self._row_to_waypoint(row) for row in rows]

    def find_by_trait(self, system_symbol: str, trait: str) -> List[Waypoint]:
        """
        Find waypoints with a specific trait

        Args:
            system_symbol: System identifier
            trait: Trait symbol (e.g., "SHIPYARD", "MARKETPLACE")

        Returns:
            List of waypoints with the trait
        """
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

    def find_by_fuel(self, system_symbol: str) -> List[Waypoint]:
        """
        Find waypoints with fuel stations

        Args:
            system_symbol: System identifier

        Returns:
            List of waypoints with fuel available
        """
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

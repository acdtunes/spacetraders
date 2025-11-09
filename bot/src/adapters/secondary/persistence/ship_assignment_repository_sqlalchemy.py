"""SQLAlchemy-based ShipAssignmentRepository implementation.

This replaces the manual SQL queries with SQLAlchemy Core, eliminating
the need for backend-specific UPSERT syntax.
"""

import logging
from datetime import datetime, timezone
from typing import Optional, Dict, Any
from sqlalchemy import select, update as sql_update
from sqlalchemy.engine import Engine
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.dialects.postgresql import insert as pg_insert

from ports.outbound.repositories import IShipAssignmentRepository
from .models import ship_assignments

logger = logging.getLogger(__name__)


class ShipAssignmentRepositorySQLAlchemy(IShipAssignmentRepository):
    """Repository for ship assignment persistence using SQLAlchemy

    Manages ship-to-container assignments to prevent double-booking.
    Ensures ships are only assigned to one operation at a time.
    """

    def __init__(self, engine: Engine):
        """Initialize with SQLAlchemy engine

        Args:
            engine: SQLAlchemy Engine instance for database operations
        """
        self._engine = engine
        logger.info("ShipAssignmentRepository initialized (SQLAlchemy)")

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
        with self._engine.begin() as conn:
            # Check current assignment
            stmt = select(ship_assignments.c.status, ship_assignments.c.container_id).where(
                ship_assignments.c.ship_symbol == ship_symbol,
                ship_assignments.c.player_id == player_id
            )
            result = conn.execute(stmt)
            row = result.fetchone()

            if row and row.status == "active":
                logger.warning(
                    f"Ship {ship_symbol} already assigned to {row.container_id}"
                )
                return False

            # Assign ship using dialect-specific UPSERT
            assigned_at = datetime.now(timezone.utc)

            # Detect backend type from engine
            backend = conn.engine.dialect.name

            if backend == 'postgresql':
                # PostgreSQL: Use pg_insert with on_conflict_do_update
                stmt = pg_insert(ship_assignments).values(
                    ship_symbol=ship_symbol,
                    player_id=player_id,
                    container_id=container_id,
                    operation=operation,
                    status='active',
                    assigned_at=assigned_at
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['ship_symbol', 'player_id'],
                    set_={
                        'container_id': stmt.excluded.container_id,
                        'operation': stmt.excluded.operation,
                        'status': 'active',
                        'assigned_at': stmt.excluded.assigned_at,
                        'released_at': None,
                        'release_reason': None
                    }
                )
                conn.execute(stmt)
            else:
                # SQLite: Use INSERT OR REPLACE
                stmt = sqlite_insert(ship_assignments).values(
                    ship_symbol=ship_symbol,
                    player_id=player_id,
                    container_id=container_id,
                    operation=operation,
                    status='active',
                    assigned_at=assigned_at
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['ship_symbol', 'player_id'],
                    set_={
                        'container_id': stmt.excluded.container_id,
                        'operation': stmt.excluded.operation,
                        'status': 'active',
                        'assigned_at': stmt.excluded.assigned_at,
                        'released_at': None,
                        'release_reason': None
                    }
                )
                conn.execute(stmt)
            logger.info(f"Assigned {ship_symbol} to {container_id}")
            return True

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
        with self._engine.begin() as conn:
            stmt = (
                sql_update(ship_assignments)
                .where(
                    ship_assignments.c.ship_symbol == ship_symbol,
                    ship_assignments.c.player_id == player_id
                )
                .values(
                    status='idle',
                    released_at=datetime.now(timezone.utc),
                    release_reason=reason
                )
            )
            conn.execute(stmt)
            logger.info(f"Released {ship_symbol}: {reason}")

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
        with self._engine.connect() as conn:
            stmt = select(ship_assignments.c.status).where(
                ship_assignments.c.ship_symbol == ship_symbol,
                ship_assignments.c.player_id == player_id
            )
            result = conn.execute(stmt)
            row = result.fetchone()

            return not row or row.status != "active"

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
        with self._engine.connect() as conn:
            stmt = (
                select(
                    ship_assignments.c.status,
                    ship_assignments.c.container_id,
                    ship_assignments.c.operation,
                    ship_assignments.c.assigned_at,
                    ship_assignments.c.released_at,
                    ship_assignments.c.release_reason
                )
                .where(
                    ship_assignments.c.ship_symbol == ship_symbol,
                    ship_assignments.c.player_id == player_id
                )
                .order_by(ship_assignments.c.assigned_at.desc())
                .limit(1)
            )

            result = conn.execute(stmt)
            row = result.fetchone()

            if not row:
                return None

            return {
                'status': row.status,
                'container_id': row.container_id,
                'operation': row.operation,
                'assigned_at': row.assigned_at,
                'released_at': row.released_at,
                'release_reason': row.release_reason
            }

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
        with self._engine.begin() as conn:
            # Get all active assignments
            stmt = select(
                ship_assignments.c.ship_symbol,
                ship_assignments.c.player_id
            ).where(ship_assignments.c.status == 'active')

            result = conn.execute(stmt)
            active_assignments = result.fetchall()

            # Release each one
            for assignment in active_assignments:
                update_stmt = (
                    sql_update(ship_assignments)
                    .where(
                        ship_assignments.c.ship_symbol == assignment.ship_symbol,
                        ship_assignments.c.player_id == assignment.player_id,
                        ship_assignments.c.status == 'active'
                    )
                    .values(
                        status='idle',
                        released_at=datetime.now(timezone.utc),
                        release_reason=reason
                    )
                )
                conn.execute(update_stmt)

            count = len(active_assignments)
            if count > 0:
                logger.info(f"Released {count} zombie assignment(s) on daemon startup")
            return count

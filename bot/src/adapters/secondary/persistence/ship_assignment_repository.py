"""Ship assignment repository implementation"""
import logging
from datetime import datetime
from typing import Optional, Dict, Any
from ports.outbound.repositories import IShipAssignmentRepository

logger = logging.getLogger(__name__)


class ShipAssignmentRepository(IShipAssignmentRepository):
    """Repository for ship assignment persistence

    Manages ship-to-container assignments to prevent double-booking.
    Ensures ships are only assigned to one operation at a time.
    """

    def __init__(self, database):
        """Initialize with database instance

        Args:
            database: Database instance for persistence
        """
        self._db = database
        logger.info("ShipAssignmentRepository initialized")

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
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            # Check current assignment
            cursor.execute("""
                SELECT status, container_id FROM ship_assignments
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            row = cursor.fetchone()
            if row and row["status"] == "active":
                logger.warning(
                    f"Ship {ship_symbol} already assigned to {row['container_id']}"
                )
                return False

            # Assign ship (use backend-specific upsert syntax)
            assigned_at = datetime.now().isoformat()

            if self._db.backend == 'postgresql':
                # PostgreSQL: INSERT ... ON CONFLICT ... DO UPDATE
                cursor.execute("""
                    INSERT INTO ship_assignments (
                        ship_symbol, player_id, container_id, operation,
                        status, assigned_at
                    ) VALUES (?, ?, ?, ?, 'active', ?)
                    ON CONFLICT (ship_symbol, player_id) DO UPDATE SET
                        container_id = EXCLUDED.container_id,
                        operation = EXCLUDED.operation,
                        status = 'active',
                        assigned_at = EXCLUDED.assigned_at,
                        released_at = NULL,
                        release_reason = NULL
                """, (ship_symbol, player_id, container_id, operation, assigned_at))
            else:
                # SQLite: INSERT OR REPLACE
                cursor.execute("""
                    INSERT OR REPLACE INTO ship_assignments (
                        ship_symbol, player_id, container_id, operation,
                        status, assigned_at
                    ) VALUES (?, ?, ?, ?, 'active', ?)
                """, (ship_symbol, player_id, container_id, operation, assigned_at))

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
        with self._db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE ship_assignments
                SET status = 'idle',
                    released_at = ?,
                    release_reason = ?
                WHERE ship_symbol = ? AND player_id = ?
            """, (
                datetime.now().isoformat(),
                reason,
                ship_symbol,
                player_id
            ))
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
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT status FROM ship_assignments
                WHERE ship_symbol = ? AND player_id = ?
            """, (ship_symbol, player_id))

            row = cursor.fetchone()
            return not row or row["status"] != "active"

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
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT status, container_id, operation,
                       assigned_at, released_at, release_reason
                FROM ship_assignments
                WHERE ship_symbol = ? AND player_id = ?
                ORDER BY assigned_at DESC
                LIMIT 1
            """, (ship_symbol, player_id))

            row = cursor.fetchone()
            if not row:
                return None

            return {
                'status': row['status'],
                'container_id': row['container_id'],
                'operation': row['operation'],
                'assigned_at': row['assigned_at'],
                'released_at': row['released_at'],
                'release_reason': row['release_reason']
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
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            # Get all active assignments
            cursor.execute("""
                SELECT ship_symbol, player_id FROM ship_assignments
                WHERE status = 'active'
            """)
            active_assignments = cursor.fetchall()

            # Release each one
            for assignment in active_assignments:
                cursor.execute("""
                    UPDATE ship_assignments
                    SET status = 'idle',
                        released_at = ?,
                        release_reason = ?
                    WHERE ship_symbol = ? AND player_id = ? AND status = 'active'
                """, (
                    datetime.now().isoformat(),
                    reason,
                    assignment['ship_symbol'],
                    assignment['player_id']
                ))

            count = len(active_assignments)
            if count > 0:
                logger.info(f"Released {count} zombie assignment(s) on daemon startup")
            return count

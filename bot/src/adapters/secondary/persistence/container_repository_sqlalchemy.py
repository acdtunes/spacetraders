"""SQLAlchemy-based ContainerRepository implementation.

This replaces the manual SQL queries with SQLAlchemy Core.
"""

import logging
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
from sqlalchemy import select, update as sql_update, delete as sql_delete
from sqlalchemy.engine import Engine

from .models import containers

logger = logging.getLogger(__name__)


class ContainerRepositorySQLAlchemy:
    """Repository for container persistence using SQLAlchemy

    Manages container lifecycle for the daemon system.
    """

    def __init__(self, engine: Engine):
        """Initialize with SQLAlchemy engine

        Args:
            engine: SQLAlchemy Engine instance for database operations
        """
        self._engine = engine
        logger.info("ContainerRepository initialized (SQLAlchemy)")

    def insert(
        self,
        container_id: str,
        player_id: int,
        container_type: str,
        status: str,
        restart_policy: str,
        config: str,
        started_at: str,
        command_type: Optional[str] = None
    ) -> None:
        """Insert a new container record into the database.

        Args:
            container_id: Unique container identifier
            player_id: Player identifier
            container_type: Type of container (e.g., 'command')
            status: Initial status (e.g., 'STARTING')
            restart_policy: Restart policy ('no', 'on-failure', 'always')
            config: JSON string of container configuration
            started_at: ISO format timestamp when container was started
            command_type: Optional command type (e.g., 'scout_markets', 'navigate', 'purchase_ship')
        """
        # Convert ISO string to datetime object for SQLAlchemy DateTime column
        started_dt = datetime.fromisoformat(started_at.replace('Z', '+00:00'))

        with self._engine.begin() as conn:
            stmt = containers.insert().values(
                container_id=container_id,
                player_id=player_id,
                container_type=container_type,
                command_type=command_type,
                status=status,
                restart_policy=restart_policy,
                restart_count=0,
                config=config,
                started_at=started_dt
            )
            conn.execute(stmt)
            logger.info(f"Inserted container {container_id} for player {player_id}")

    def update_status(
        self,
        container_id: str,
        player_id: int,
        status: str,
        stopped_at: Optional[str] = None,
        exit_code: Optional[int] = None,
        exit_reason: Optional[str] = None
    ) -> None:
        """Update container status in the database.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            status: New status ('RUNNING', 'STOPPED', 'FAILED', etc.)
            stopped_at: Optional ISO format timestamp when container stopped
            exit_code: Optional exit code (0 for success, non-zero for failure)
            exit_reason: Optional reason for failure
        """
        with self._engine.begin() as conn:
            values = {'status': status}

            if stopped_at is not None:
                # Convert ISO string to datetime object for SQLAlchemy DateTime column
                stopped_dt = datetime.fromisoformat(stopped_at.replace('Z', '+00:00'))
                values.update({
                    'stopped_at': stopped_dt,
                    'exit_code': exit_code,
                    'exit_reason': exit_reason
                })

            stmt = (
                sql_update(containers)
                .where(
                    containers.c.container_id == container_id,
                    containers.c.player_id == player_id
                )
                .values(**values)
            )
            conn.execute(stmt)
            logger.debug(f"Updated container {container_id} status to {status}")

    def get(self, container_id: str, player_id: int) -> Optional[Dict[str, Any]]:
        """Get a container by ID.

        Args:
            container_id: Container identifier
            player_id: Player identifier

        Returns:
            Container dictionary or None if not found
        """
        with self._engine.connect() as conn:
            stmt = select(containers).where(
                containers.c.container_id == container_id,
                containers.c.player_id == player_id
            )
            result = conn.execute(stmt)
            row = result.fetchone()

            if not row:
                return None

            return {
                'container_id': row.container_id,
                'player_id': row.player_id,
                'container_type': row.container_type,
                'command_type': row.command_type,
                'status': row.status,
                'restart_policy': row.restart_policy,
                'restart_count': row.restart_count,
                'config': row.config,
                'started_at': row.started_at,
                'stopped_at': row.stopped_at,
                'exit_code': row.exit_code,
                'exit_reason': row.exit_reason
            }

    def list_by_status(
        self,
        status: str,
        player_id: Optional[int] = None
    ) -> List[Dict[str, Any]]:
        """List all containers with a specific status.

        Args:
            status: Container status to filter by
            player_id: Optional player ID filter

        Returns:
            List of container dictionaries
        """
        with self._engine.connect() as conn:
            stmt = select(containers).where(containers.c.status == status)

            if player_id is not None:
                stmt = stmt.where(containers.c.player_id == player_id)

            result = conn.execute(stmt)
            rows = result.fetchall()

            return [
                {
                    'container_id': row.container_id,
                    'player_id': row.player_id,
                    'container_type': row.container_type,
                    'command_type': row.command_type,
                    'status': row.status,
                    'restart_policy': row.restart_policy,
                    'restart_count': row.restart_count,
                    'config': row.config,
                    'started_at': row.started_at,
                    'stopped_at': row.stopped_at,
                    'exit_code': row.exit_code,
                    'exit_reason': row.exit_reason
                }
                for row in rows
            ]

    def list_all(self, player_id: Optional[int] = None) -> List[Dict[str, Any]]:
        """List all containers, optionally filtered by player.

        Args:
            player_id: Optional player ID filter

        Returns:
            List of container dictionaries
        """
        with self._engine.connect() as conn:
            stmt = select(containers)

            if player_id is not None:
                stmt = stmt.where(containers.c.player_id == player_id)

            result = conn.execute(stmt)
            rows = result.fetchall()

            return [
                {
                    'container_id': row.container_id,
                    'player_id': row.player_id,
                    'container_type': row.container_type,
                    'command_type': row.command_type,
                    'status': row.status,
                    'restart_policy': row.restart_policy,
                    'restart_count': row.restart_count,
                    'config': row.config,
                    'started_at': row.started_at,
                    'stopped_at': row.stopped_at,
                    'exit_code': row.exit_code,
                    'exit_reason': row.exit_reason
                }
                for row in rows
            ]

    def delete(self, container_id: str, player_id: int) -> None:
        """Delete a container record.

        Args:
            container_id: Container identifier
            player_id: Player identifier
        """
        with self._engine.begin() as conn:
            stmt = sql_delete(containers).where(
                containers.c.container_id == container_id,
                containers.c.player_id == player_id
            )
            conn.execute(stmt)
            logger.info(f"Deleted container {container_id}")

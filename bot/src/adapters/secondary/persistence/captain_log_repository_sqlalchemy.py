"""SQLAlchemy-based CaptainLogRepository implementation."""

import logging
from typing import Dict, Any, List, Optional
from sqlalchemy import select, insert
from sqlalchemy.engine import Engine

from ports.outbound.captain_log_repository import ICaptainLogRepository
from .models import captain_logs

logger = logging.getLogger(__name__)


class CaptainLogRepositorySQLAlchemy(ICaptainLogRepository):
    """Repository for persisting captain narrative logs using SQLAlchemy"""

    def __init__(self, engine: Engine):
        """
        Initialize captain log repository.

        Args:
            engine: SQLAlchemy Engine instance
        """
        self._engine = engine
        logger.debug("CaptainLogRepository initialized (SQLAlchemy)")

    def insert_log(
        self,
        player_id: int,
        timestamp: str,
        entry_type: str,
        narrative: str,
        event_data: Optional[str] = None,
        tags: Optional[str] = None,
        fleet_snapshot: Optional[str] = None
    ) -> int:
        """
        Insert a captain log entry.

        Args:
            player_id: Player identifier
            timestamp: ISO format timestamp
            entry_type: Entry type (session_start, operation_started, etc.)
            narrative: The narrative prose from captain
            event_data: Optional JSON string of event data
            tags: Optional JSON string array of tags
            fleet_snapshot: Optional JSON string of fleet state

        Returns:
            log_id: ID of the inserted log
        """
        with self._engine.begin() as conn:
            result = conn.execute(
                insert(captain_logs).values(
                    player_id=player_id,
                    timestamp=timestamp,
                    entry_type=entry_type,
                    narrative=narrative,
                    event_data=event_data,
                    tags=tags,
                    fleet_snapshot=fleet_snapshot
                )
            )
            log_id = result.lastrowid
            logger.debug(f"Inserted captain log {log_id} for player {player_id}")
            return log_id

    def get_logs(
        self,
        player_id: int,
        limit: int = 100,
        entry_type: Optional[str] = None,
        since: Optional[str] = None,
        tags: Optional[List[str]] = None
    ) -> List[Dict[str, Any]]:
        """
        Get captain logs for player with optional filtering.

        Args:
            player_id: Player identifier
            limit: Maximum number of logs to return (default 100)
            entry_type: Optional filter by entry type
            since: Optional ISO timestamp - only logs after this time
            tags: Optional list of tags to filter by (all must match)

        Returns:
            List of log dictionaries
        """
        with self._engine.connect() as conn:
            # Build query
            stmt = (
                select(
                    captain_logs.c.log_id,
                    captain_logs.c.player_id,
                    captain_logs.c.timestamp,
                    captain_logs.c.entry_type,
                    captain_logs.c.narrative,
                    captain_logs.c.event_data,
                    captain_logs.c.tags,
                    captain_logs.c.fleet_snapshot
                )
                .where(captain_logs.c.player_id == player_id)
            )

            # Add optional filters
            if entry_type:
                stmt = stmt.where(captain_logs.c.entry_type == entry_type)

            if since:
                stmt = stmt.where(captain_logs.c.timestamp >= since)

            # Order and limit
            stmt = stmt.order_by(captain_logs.c.timestamp.desc()).limit(limit)

            result = conn.execute(stmt)
            rows = result.fetchall()

            results = []
            for row in rows:
                log = {
                    'log_id': row.log_id,
                    'player_id': row.player_id,
                    'timestamp': row.timestamp,
                    'entry_type': row.entry_type,
                    'narrative': row.narrative,
                    'event_data': row.event_data,
                    'tags': row.tags,
                    'fleet_snapshot': row.fleet_snapshot
                }

                # Filter by tags if specified
                if tags:
                    import json
                    log_tags = json.loads(log['tags']) if log['tags'] else []
                    if not all(tag in log_tags for tag in tags):
                        continue

                results.append(log)

            return results

    def get_recent_history(self, player_id: int, limit: int = 10) -> List[str]:
        """
        Get recent narratives for context (just the text).

        Args:
            player_id: Player identifier
            limit: Number of recent narratives to retrieve

        Returns:
            List of narrative strings in chronological order (oldest first)
        """
        logs = self.get_logs(player_id, limit=limit)
        # Reverse to get chronological order (oldest first)
        return [log['narrative'] for log in reversed(logs)]

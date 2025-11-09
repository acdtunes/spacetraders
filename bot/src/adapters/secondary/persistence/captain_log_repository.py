"""Repository for captain log persistence"""
import logging
from datetime import datetime, timezone
from typing import Dict, Any, List, Optional

from adapters.secondary.persistence.database import Database
from ports.outbound.captain_log_repository import ICaptainLogRepository

logger = logging.getLogger(__name__)


class CaptainLogRepository(ICaptainLogRepository):
    """Repository for persisting captain narrative logs"""

    def __init__(self, database: Database):
        self._db = database

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
        with self._db.transaction() as conn:
            cursor = conn.execute("""
                INSERT INTO captain_logs
                (player_id, timestamp, entry_type, narrative, event_data, tags, fleet_snapshot)
                VALUES (?, ?, ?, ?, ?, ?, ?)
            """, (player_id, timestamp, entry_type, narrative, event_data, tags, fleet_snapshot))

            log_id = cursor.lastrowid
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
        with self._db.connection() as conn:
            query = """
                SELECT log_id, player_id, timestamp, entry_type, narrative,
                       event_data, tags, fleet_snapshot
                FROM captain_logs
                WHERE player_id = ?
            """
            params = [player_id]

            if entry_type:
                query += " AND entry_type = ?"
                params.append(entry_type)

            if since:
                query += " AND timestamp >= ?"
                params.append(since)

            # Note: Tag filtering is done in Python for SQLite compatibility
            # PostgreSQL could use JSON operators for better performance

            query += " ORDER BY timestamp DESC LIMIT ?"
            params.append(limit)

            cursor = conn.execute(query, params)
            rows = cursor.fetchall()

            results = []
            for row in rows:
                log = {
                    'log_id': row['log_id'],
                    'player_id': row['player_id'],
                    'timestamp': row['timestamp'],
                    'entry_type': row['entry_type'],
                    'narrative': row['narrative'],
                    'event_data': row['event_data'],
                    'tags': row['tags'],
                    'fleet_snapshot': row['fleet_snapshot']
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

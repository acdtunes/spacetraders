"""SQLAlchemy-based ContainerLogRepository implementation.

This replaces the manual SQL queries with SQLAlchemy Core.
"""

import logging
import threading
from datetime import datetime, timedelta, timezone
from typing import Optional, List, Dict, Any, Tuple
from sqlalchemy import select
from sqlalchemy.engine import Engine

from .models import container_logs

logger = logging.getLogger(__name__)


class ContainerLogRepositorySQLAlchemy:
    """Repository for container log persistence using SQLAlchemy

    Manages container logs with time-windowed deduplication.
    """

    def __init__(self, engine: Engine):
        """Initialize with SQLAlchemy engine

        Args:
            engine: SQLAlchemy Engine instance for database operations
        """
        self._engine = engine

        # Initialize log deduplication cache
        self._log_dedup_cache: Dict[Tuple[str, str], datetime] = {}
        self._log_dedup_lock = threading.Lock()
        self._log_dedup_window = timedelta(seconds=60)  # 60-second deduplication window
        self._log_dedup_max_size = 10000  # Max cache entries before cleanup

        logger.info("ContainerLogRepository initialized (SQLAlchemy)")

    def log(
        self,
        container_id: str,
        player_id: int,
        message: str,
        level: str = "INFO"
    ) -> None:
        """Log container message to database with time-windowed deduplication.

        Suppresses duplicate messages within the deduplication window (default 60 seconds)
        to reduce log volume while preserving all unique events.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            message: Log message
            level: Log level (INFO, WARNING, ERROR, DEBUG)
        """
        now = datetime.now(timezone.utc)
        cache_key = (container_id, message)

        # Thread-safe deduplication check
        with self._log_dedup_lock:
            # Check if this message was logged recently
            if cache_key in self._log_dedup_cache:
                last_logged = self._log_dedup_cache[cache_key]
                if now - last_logged < self._log_dedup_window:
                    # Duplicate within window, skip logging
                    return

            # Clean up cache if it's getting too large
            if len(self._log_dedup_cache) >= self._log_dedup_max_size:
                self._cleanup_dedup_cache()

            # Update cache with current timestamp
            self._log_dedup_cache[cache_key] = now

        # Log to database (outside lock to minimize lock contention)
        with self._engine.begin() as conn:
            stmt = container_logs.insert().values(
                container_id=container_id,
                player_id=player_id,
                timestamp=now,
                level=level,
                message=message
            )
            conn.execute(stmt)

    def _cleanup_dedup_cache(self):
        """Clean up old entries from the deduplication cache.

        Removes entries older than the deduplication window to prevent unbounded
        memory growth. Called automatically when cache size exceeds threshold.

        Note: Must be called while holding self._log_dedup_lock
        """
        now = datetime.now(timezone.utc)
        cutoff = now - self._log_dedup_window

        # Remove entries older than the deduplication window
        keys_to_remove = [
            key for key, timestamp in self._log_dedup_cache.items()
            if timestamp < cutoff
        ]

        for key in keys_to_remove:
            del self._log_dedup_cache[key]

        logger.debug(f"Cleaned up {len(keys_to_remove)} old entries from log deduplication cache")

    def get_logs(
        self,
        container_id: str,
        player_id: int,
        limit: int = 100,
        level: Optional[str] = None,
        since: Optional[str] = None
    ) -> List[Dict[str, Any]]:
        """Get container logs from database.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            limit: Maximum number of logs to return (default 100)
            level: Filter by log level (optional)
            since: Filter by timestamp - only logs after this timestamp (optional)

        Returns:
            List of log dictionaries with keys: log_id, container_id, player_id,
            timestamp, level, message
        """
        with self._engine.connect() as conn:
            stmt = select(container_logs).where(
                container_logs.c.container_id == container_id,
                container_logs.c.player_id == player_id
            )

            if level:
                stmt = stmt.where(container_logs.c.level == level)

            if since:
                stmt = stmt.where(container_logs.c.timestamp > since)

            stmt = stmt.order_by(container_logs.c.timestamp.desc()).limit(limit)

            result = conn.execute(stmt)
            rows = result.fetchall()

            return [
                {
                    'log_id': row.log_id,
                    'container_id': row.container_id,
                    'player_id': row.player_id,
                    'timestamp': row.timestamp.isoformat() if isinstance(row.timestamp, datetime) else row.timestamp,
                    'level': row.level,
                    'message': row.message
                }
                for row in rows
            ]

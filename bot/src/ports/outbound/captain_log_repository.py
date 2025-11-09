"""Port interface for captain log repository"""
from abc import ABC, abstractmethod
from typing import Dict, Any, List, Optional


class ICaptainLogRepository(ABC):
    """Interface for captain log repository"""

    @abstractmethod
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
        pass

    @abstractmethod
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
        pass

    @abstractmethod
    def get_recent_history(self, player_id: int, limit: int = 10) -> List[str]:
        """
        Get recent narratives for context (just the text).

        Args:
            player_id: Player identifier
            limit: Number of recent narratives to retrieve

        Returns:
            List of narrative strings in chronological order (oldest first)
        """
        pass

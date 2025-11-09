"""Shared fixtures for captain BDD tests"""
import pytest
from datetime import datetime, timezone
from typing import Dict, Any, List, Optional


class MockCaptainLogRepository:
    """Mock repository for captain logs (in-memory)"""

    def __init__(self):
        self._logs: Dict[int, Dict[str, Any]] = {}
        self._next_id = 1

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
        """Insert a captain log entry"""
        log_id = self._next_id
        self._next_id += 1

        self._logs[log_id] = {
            "log_id": log_id,
            "player_id": player_id,
            "timestamp": timestamp,
            "entry_type": entry_type,
            "narrative": narrative,
            "event_data": event_data,
            "tags": tags,
            "fleet_snapshot": fleet_snapshot
        }

        return log_id

    def get_by_id(self, log_id: int) -> Optional[Dict[str, Any]]:
        """Get log by ID"""
        return self._logs.get(log_id)

    def get_logs(
        self,
        player_id: int,
        limit: int = 100,
        entry_type: Optional[str] = None,
        since: Optional[str] = None,
        tags: Optional[List[str]] = None
    ) -> List[Dict[str, Any]]:
        """Get logs for player with filtering"""
        results = []

        for log in self._logs.values():
            if log["player_id"] != player_id:
                continue

            if entry_type and log["entry_type"] != entry_type:
                continue

            if since and log["timestamp"] < since:
                continue

            if tags:
                import json
                log_tags = json.loads(log["tags"]) if log["tags"] else []
                if not all(tag in log_tags for tag in tags):
                    continue

            results.append(log)

        # Sort by timestamp descending
        results.sort(key=lambda x: x["timestamp"], reverse=True)

        return results[:limit]

    def get_recent_history(self, player_id: int, limit: int = 10) -> List[str]:
        """Get recent narratives for context"""
        logs = self.get_logs(player_id, limit=limit)
        return [log["narrative"] for log in logs]


@pytest.fixture
def mock_captain_log_repo():
    """Create mock captain log repository"""
    return MockCaptainLogRepository()

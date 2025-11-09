"""Command to log a captain narrative entry"""
from dataclasses import dataclass
from typing import Optional, Dict, Any, List
from datetime import datetime, timezone
import json

from pymediatr import Request, RequestHandler

from ports.outbound.captain_log_repository import ICaptainLogRepository
from ports.repositories import IPlayerRepository
from domain.shared.exceptions import PlayerNotFoundError


# Valid entry types for captain logs
VALID_ENTRY_TYPES = {
    "session_start",
    "operation_started",
    "operation_completed",
    "critical_error",
    "strategic_decision",
    "session_end"
}


@dataclass(frozen=True)
class LogCaptainEntryCommand(Request[int]):
    """Command to log a captain narrative entry"""
    player_id: int
    entry_type: str
    narrative: str
    event_data: Optional[Dict[str, Any]] = None
    tags: Optional[List[str]] = None
    fleet_snapshot: Optional[Dict[str, Any]] = None


class LogCaptainEntryHandler(RequestHandler[LogCaptainEntryCommand, int]):
    """Handler for logging captain entries"""

    def __init__(
        self,
        captain_log_repository: ICaptainLogRepository,
        player_repository: IPlayerRepository
    ):
        self._captain_log_repo = captain_log_repository
        self._player_repo = player_repository

    async def handle(self, request: LogCaptainEntryCommand) -> int:
        """
        Log a captain narrative entry.

        Validates:
        - Player exists
        - Entry type is valid
        - Narrative is not empty
        - JSON data is valid

        Returns:
            log_id: ID of the inserted log entry

        Raises:
            PlayerNotFoundError: If player doesn't exist
            ValueError: If validation fails
        """
        # 1. Validate player exists
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")

        # 2. Validate entry type
        if request.entry_type not in VALID_ENTRY_TYPES:
            raise ValueError(
                f"Invalid entry_type '{request.entry_type}'. "
                f"Must be one of: {', '.join(sorted(VALID_ENTRY_TYPES))}"
            )

        # 3. Validate narrative is not empty
        if not request.narrative or not request.narrative.strip():
            raise ValueError("narrative cannot be empty")

        # 4. Serialize JSON fields and validate
        event_data_json = None
        if request.event_data is not None:
            try:
                event_data_json = json.dumps(request.event_data)
            except (TypeError, ValueError) as e:
                raise ValueError(f"Invalid JSON in event_data: {e}")

        tags_json = None
        if request.tags is not None:
            try:
                tags_json = json.dumps(request.tags)
            except (TypeError, ValueError) as e:
                raise ValueError(f"Invalid JSON in tags: {e}")

        fleet_snapshot_json = None
        if request.fleet_snapshot is not None:
            try:
                fleet_snapshot_json = json.dumps(request.fleet_snapshot)
            except (TypeError, ValueError) as e:
                raise ValueError(f"Invalid JSON in fleet_snapshot: {e}")

        # 5. Generate timestamp
        timestamp = datetime.now(timezone.utc).isoformat()

        # 6. Insert log
        log_id = self._captain_log_repo.insert_log(
            player_id=request.player_id,
            timestamp=timestamp,
            entry_type=request.entry_type,
            narrative=request.narrative,
            event_data=event_data_json,
            tags=tags_json,
            fleet_snapshot=fleet_snapshot_json
        )

        return log_id

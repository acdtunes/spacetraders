"""Query to retrieve captain logs"""
from dataclasses import dataclass
from typing import Optional, List, Dict, Any

from pymediatr import Request, RequestHandler

from ports.outbound.captain_log_repository import ICaptainLogRepository
from ports.repositories import IPlayerRepository
from domain.shared.exceptions import PlayerNotFoundError


@dataclass(frozen=True)
class GetCaptainLogsQuery(Request[List[Dict[str, Any]]]):
    """Query to get captain logs with filtering"""
    player_id: int
    limit: int = 100
    entry_type: Optional[str] = None
    since: Optional[str] = None
    tags: Optional[List[str]] = None


class GetCaptainLogsHandler(RequestHandler[GetCaptainLogsQuery, List[Dict[str, Any]]]):
    """Handler for retrieving captain logs"""

    def __init__(
        self,
        captain_log_repository: ICaptainLogRepository,
        player_repository: IPlayerRepository
    ):
        self._captain_log_repo = captain_log_repository
        self._player_repo = player_repository

    async def handle(self, request: GetCaptainLogsQuery) -> List[Dict[str, Any]]:
        """
        Retrieve captain logs with optional filtering.

        Validates:
        - Player exists

        Returns:
            List of log dictionaries in reverse chronological order

        Raises:
            PlayerNotFoundError: If player doesn't exist
        """
        # 1. Validate player exists
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")

        # 2. Retrieve logs with filtering
        logs = self._captain_log_repo.get_logs(
            player_id=request.player_id,
            limit=request.limit,
            entry_type=request.entry_type,
            since=request.since,
            tags=request.tags
        )

        return logs

from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from domain.shared.player import Player
from domain.shared.exceptions import PlayerNotFoundError
from ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class GetPlayerQuery(Request[Player]):
    """Query to get player by ID"""
    player_id: int

class GetPlayerHandler(RequestHandler[GetPlayerQuery, Player]):
    """Handler for getting player by ID"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: GetPlayerQuery) -> Player:
        """Get player by ID"""
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")
        return player

@dataclass(frozen=True)
class GetPlayerByAgentQuery(Request[Player]):
    """Query to get player by agent symbol"""
    agent_symbol: str

class GetPlayerByAgentHandler(RequestHandler[GetPlayerByAgentQuery, Player]):
    """Handler for getting player by agent symbol"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: GetPlayerByAgentQuery) -> Player:
        """Get player by agent symbol"""
        player = self._player_repo.find_by_agent_symbol(request.agent_symbol)
        if not player:
            raise PlayerNotFoundError(f"Agent '{request.agent_symbol}' not found")
        return player

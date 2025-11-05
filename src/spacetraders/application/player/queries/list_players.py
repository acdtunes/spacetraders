from dataclasses import dataclass
from typing import List
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class ListPlayersQuery(Request[List[Player]]):
    """Query to list all players"""
    pass

class ListPlayersHandler(RequestHandler[ListPlayersQuery, List[Player]]):
    """Handler for listing all players"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: ListPlayersQuery) -> List[Player]:
        """List all registered players"""
        return self._player_repo.list_all()

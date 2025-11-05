from dataclasses import dataclass
from typing import Dict, Any
from pymediatr import Request, RequestHandler

from ....domain.shared.player import Player
from ....domain.shared.exceptions import PlayerNotFoundError
from ....ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class UpdatePlayerMetadataCommand(Request[Player]):
    """Command to update player metadata"""
    player_id: int
    metadata: Dict[str, Any]

class UpdatePlayerMetadataHandler(RequestHandler[UpdatePlayerMetadataCommand, Player]):
    """Handler for updating player metadata"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: UpdatePlayerMetadataCommand) -> Player:
        """Update player metadata"""
        # 1. Load player
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")

        # 2. Update domain entity
        player.update_metadata(request.metadata)

        # 3. Persist
        self._player_repo.update(player)
        return player

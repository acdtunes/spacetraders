from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from domain.shared.player import Player
from domain.shared.exceptions import PlayerNotFoundError
from ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class TouchPlayerLastActiveCommand(Request[Player]):
    """Command to touch player's last_active timestamp"""
    player_id: int

class TouchPlayerLastActiveHandler(RequestHandler[TouchPlayerLastActiveCommand, Player]):
    """Handler for touching player's last_active timestamp"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: TouchPlayerLastActiveCommand) -> Player:
        """Touch player's last_active timestamp"""
        # 1. Load player
        player = self._player_repo.find_by_id(request.player_id)
        if not player:
            raise PlayerNotFoundError(f"Player {request.player_id} not found")

        # 2. Update domain entity
        player.update_last_active()

        # 3. Persist
        self._player_repo.update(player)
        return player

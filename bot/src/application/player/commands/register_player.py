from dataclasses import dataclass
from typing import Optional, Dict, Any
from datetime import datetime, timezone
from pymediatr import Request, RequestHandler

from domain.shared.player import Player
from domain.shared.exceptions import DuplicateAgentSymbolError
from ports.repositories import IPlayerRepository

@dataclass(frozen=True)
class RegisterPlayerCommand(Request[Player]):
    """Command to register new player"""
    agent_symbol: str
    token: str
    metadata: Optional[Dict[str, Any]] = None

class RegisterPlayerHandler(RequestHandler[RegisterPlayerCommand, Player]):
    """Handler for player registration"""

    def __init__(self, player_repository: IPlayerRepository):
        self._player_repo = player_repository

    async def handle(self, request: RegisterPlayerCommand) -> Player:
        """
        Register new player

        Raises:
            DuplicateAgentSymbolError: If agent symbol already registered
        """
        # 1. Check uniqueness (domain rule enforcement)
        if self._player_repo.exists_by_agent_symbol(request.agent_symbol):
            raise DuplicateAgentSymbolError(
                f"Agent symbol '{request.agent_symbol}' already registered"
            )

        # 2. Create domain entity
        player = Player(
            player_id=None,  # Will be assigned by repository
            agent_symbol=request.agent_symbol,
            token=request.token,
            created_at=datetime.now(timezone.utc),
            metadata=request.metadata
        )

        # 3. Persist via repository
        return self._player_repo.create(player)

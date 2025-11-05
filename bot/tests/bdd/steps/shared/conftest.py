import pytest
from typing import Optional, List, Dict
from domain.shared.player import Player
from ports.outbound.repositories import IPlayerRepository
from datetime import datetime, timezone

class MockPlayerRepository(IPlayerRepository):
    """In-memory player repository for testing"""

    def __init__(self):
        self._players: Dict[int, Player] = {}
        self._next_id = 1
        self._agents: Dict[str, int] = {}  # agent_symbol -> player_id

    def create(self, player: Player) -> Player:
        player_id = self._next_id
        self._next_id += 1

        created_player = Player(
            player_id=player_id,
            agent_symbol=player.agent_symbol,
            token=player.token,
            created_at=player.created_at,
            last_active=player.last_active,
            metadata=player.metadata
        )

        self._players[player_id] = created_player
        self._agents[player.agent_symbol] = player_id
        return created_player

    def find_by_id(self, player_id: int) -> Optional[Player]:
        return self._players.get(player_id)

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        player_id = self._agents.get(agent_symbol)
        return self._players.get(player_id) if player_id else None

    def list_all(self) -> List[Player]:
        return list(self._players.values())

    def update(self, player: Player) -> None:
        if player.player_id in self._players:
            self._players[player.player_id] = player

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        return agent_symbol in self._agents

@pytest.fixture
def mock_player_repo():
    """Provide mock player repository for tests"""
    return MockPlayerRepository()

@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}

from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Optional, Dict, Any
from .exceptions import InsufficientCreditsError

class Player:
    """
    Player entity - represents a SpaceTraders agent/account

    Invariants:
    - agent_symbol must be unique
    - token must be valid Bearer token format
    - last_active updated on any operation
    - credits cannot be negative
    """

    def __init__(
        self,
        player_id: Optional[int],
        agent_symbol: str,
        token: str,
        created_at: datetime,
        last_active: Optional[datetime] = None,
        metadata: Optional[Dict[str, Any]] = None,
        credits: int = 0
    ):
        if not agent_symbol or not agent_symbol.strip():
            raise ValueError("agent_symbol cannot be empty")
        if not token or not token.strip():
            raise ValueError("token cannot be empty")
        if credits < 0:
            raise ValueError("credits cannot be negative")

        self._player_id = player_id
        self._agent_symbol = agent_symbol.strip()
        self._token = token.strip()
        self._created_at = created_at
        self._last_active = last_active or created_at
        self._metadata = metadata or {}
        self._credits = credits

    @property
    def player_id(self) -> Optional[int]:
        return self._player_id

    @property
    def agent_symbol(self) -> str:
        return self._agent_symbol

    @property
    def token(self) -> str:
        return self._token

    @property
    def created_at(self) -> datetime:
        return self._created_at

    @property
    def last_active(self) -> datetime:
        return self._last_active

    @property
    def metadata(self) -> Dict[str, Any]:
        return self._metadata.copy()

    @property
    def credits(self) -> int:
        """Get player's current credit balance"""
        return self._credits

    def update_last_active(self) -> None:
        """Touch last active timestamp"""
        self._last_active = datetime.now(timezone.utc)

    def update_metadata(self, metadata: Dict[str, Any]) -> None:
        """Update metadata dict"""
        self._metadata.update(metadata)

    def is_active_within(self, hours: int) -> bool:
        """Check if player was active within N hours"""
        delta = datetime.now(timezone.utc) - self._last_active
        return delta.total_seconds() < (hours * 3600)

    def add_credits(self, amount: int) -> None:
        """
        Add credits to player's balance

        Args:
            amount: Credits to add

        Raises:
            ValueError: If amount is negative
        """
        if amount < 0:
            raise ValueError("amount cannot be negative")
        self._credits += amount

    def spend_credits(self, amount: int) -> None:
        """
        Spend credits from player's balance

        Args:
            amount: Credits to spend

        Raises:
            ValueError: If amount is negative
            InsufficientCreditsError: If player doesn't have enough credits
        """
        if amount < 0:
            raise ValueError("amount cannot be negative")
        if self._credits < amount:
            raise InsufficientCreditsError(
                f"Insufficient credits: need {amount}, have {self._credits}"
            )
        self._credits -= amount

    def __repr__(self) -> str:
        return f"Player(id={self.player_id}, agent={self.agent_symbol})"

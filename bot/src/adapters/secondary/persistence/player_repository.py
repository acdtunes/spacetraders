import json
import logging
from typing import Optional, List
from datetime import datetime

from domain.shared.player import Player
from ports.outbound.repositories import IPlayerRepository
from .database import Database
from .mappers import PlayerMapper

logger = logging.getLogger(__name__)

class PlayerRepository(IPlayerRepository):
    """SQLite implementation of player repository"""

    def __init__(self, database: Database):
        self._db = database

    def create(self, player: Player) -> Player:
        """
        Persist new player.

        NOTE: Credits are cached in database. Use SyncPlayerCommand to synchronize with API.
        """
        with self._db.transaction() as conn:
            cursor = conn.cursor()

            cursor.execute("""
                INSERT INTO players (agent_symbol, token, created_at, last_active, metadata, credits)
                VALUES (?, ?, ?, ?, ?, ?)
            """, (
                player.agent_symbol,
                player.token,
                player.created_at.isoformat(),
                player.last_active.isoformat(),
                json.dumps(player.metadata),
                player.credits
            ))

            player_id = cursor.lastrowid
            logger.info(f"Created player {player_id}: {player.agent_symbol}")

            # Return player with assigned ID
            return Player(
                player_id=player_id,
                agent_symbol=player.agent_symbol,
                token=player.token,
                created_at=player.created_at,
                last_active=player.last_active,
                metadata=player.metadata,
                credits=player.credits
            )

    def find_by_id(self, player_id: int) -> Optional[Player]:
        """Load player by ID"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players WHERE player_id = ?", (player_id,))
            row = cursor.fetchone()
            return PlayerMapper.from_db_row(row) if row else None

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        """Load player by agent symbol"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players WHERE agent_symbol = ?", (agent_symbol,))
            row = cursor.fetchone()
            return PlayerMapper.from_db_row(row) if row else None

    def list_all(self) -> List[Player]:
        """List all players"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT * FROM players ORDER BY created_at")
            rows = cursor.fetchall()
            return [PlayerMapper.from_db_row(row) for row in rows]

    def update(self, player: Player) -> None:
        """
        Update existing player (metadata, last_active, and credits).

        NOTE: Credits are cached in database. Use SyncPlayerCommand to synchronize with API.
        """
        with self._db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE players
                SET last_active = ?, metadata = ?, credits = ?
                WHERE player_id = ?
            """, (
                player.last_active.isoformat(),
                json.dumps(player.metadata),
                player.credits,
                player.player_id
            ))
            logger.debug(f"Updated player {player.player_id}")

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        """Check if agent symbol exists"""
        with self._db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT 1 FROM players WHERE agent_symbol = ? LIMIT 1",
                (agent_symbol,)
            )
            return cursor.fetchone() is not None

"""SQLAlchemy-based PlayerRepository implementation.

This is the new implementation using SQLAlchemy Core instead of raw SQL queries.
Once validated, this will replace player_repository.py.
"""

import json
import logging
from typing import Optional, List
from datetime import datetime
from sqlalchemy import select, insert, update as sql_update, exists
from sqlalchemy.engine import Engine

from domain.shared.player import Player
from ports.outbound.repositories import IPlayerRepository
from .models import players
from .mappers import PlayerMapper

logger = logging.getLogger(__name__)


class PlayerRepositorySQLAlchemy(IPlayerRepository):
    """SQLAlchemy implementation of player repository"""

    def __init__(self, engine: Engine):
        self._engine = engine

    def create(self, player: Player) -> Player:
        """
        Persist new player.

        NOTE: Credits are cached in database. Use SyncPlayerCommand to synchronize with API.
        """
        with self._engine.begin() as conn:
            result = conn.execute(
                insert(players).values(
                    agent_symbol=player.agent_symbol,
                    token=player.token,
                    created_at=player.created_at,
                    last_active=player.last_active,
                    metadata=player.metadata,  # Auto JSON serialization
                    credits=player.credits
                )
            )

            player_id = result.lastrowid
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
        with self._engine.connect() as conn:
            stmt = select(players).where(players.c.player_id == player_id)
            result = conn.execute(stmt)
            row = result.fetchone()

            if not row:
                return None

            # Convert SQLAlchemy Row to dict-like object for mapper
            row_dict = row._mapping
            return PlayerMapper.from_db_row(row_dict)

    def find_by_agent_symbol(self, agent_symbol: str) -> Optional[Player]:
        """Load player by agent symbol"""
        with self._engine.connect() as conn:
            stmt = select(players).where(players.c.agent_symbol == agent_symbol)
            result = conn.execute(stmt)
            row = result.fetchone()

            if not row:
                return None

            row_dict = row._mapping
            return PlayerMapper.from_db_row(row_dict)

    def list_all(self) -> List[Player]:
        """List all players"""
        with self._engine.connect() as conn:
            stmt = select(players).order_by(players.c.created_at)
            result = conn.execute(stmt)
            rows = result.fetchall()

            return [PlayerMapper.from_db_row(row._mapping) for row in rows]

    def update(self, player: Player) -> None:
        """
        Update existing player (metadata, last_active, and credits).

        NOTE: Credits are cached in database. Use SyncPlayerCommand to synchronize with API.
        """
        with self._engine.begin() as conn:
            stmt = (
                sql_update(players)
                .where(players.c.player_id == player.player_id)
                .values(
                    last_active=player.last_active,
                    metadata=player.metadata,  # Auto JSON serialization
                    credits=player.credits
                )
            )
            conn.execute(stmt)
            logger.debug(f"Updated player {player.player_id}")

    def exists_by_agent_symbol(self, agent_symbol: str) -> bool:
        """Check if agent symbol exists"""
        with self._engine.connect() as conn:
            stmt = select(exists().where(players.c.agent_symbol == agent_symbol))
            result = conn.execute(stmt)
            return result.scalar()

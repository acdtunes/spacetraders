"""SQLAlchemy-based SystemGraphRepository implementation."""

import json
import logging
from typing import Optional
from datetime import datetime, timezone
from sqlalchemy import select
from sqlalchemy.engine import Engine
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.dialects.postgresql import insert as pg_insert

from .models import system_graphs

logger = logging.getLogger(__name__)


class SystemGraphRepositorySQLAlchemy:
    """Repository for system graph persistence using SQLAlchemy"""

    def __init__(self, engine: Engine):
        """Initialize with SQLAlchemy engine

        Args:
            engine: SQLAlchemy Engine instance for database operations
        """
        self._engine = engine
        logger.info("SystemGraphRepository initialized (SQLAlchemy)")

    def get(self, system_symbol: str) -> Optional[dict]:
        """Get graph for a system

        Args:
            system_symbol: System to get graph for

        Returns:
            Graph dict if found, None otherwise
        """
        with self._engine.connect() as conn:
            stmt = select(system_graphs.c.graph_data).where(
                system_graphs.c.system_symbol == system_symbol
            )
            result = conn.execute(stmt)
            row = result.fetchone()

            if row:
                return json.loads(row.graph_data)
            return None

    def save(self, system_symbol: str, graph: dict) -> None:
        """Save or update graph for a system

        Args:
            system_symbol: System symbol
            graph: Graph data to save
        """
        graph_json = json.dumps(graph)

        with self._engine.begin() as conn:
            # Detect backend type
            backend = conn.engine.dialect.name

            if backend == 'postgresql':
                # PostgreSQL: Use pg_insert with on_conflict_do_update
                stmt = pg_insert(system_graphs).values(
                    system_symbol=system_symbol,
                    graph_data=graph_json,
                    last_updated=datetime.now(timezone.utc)
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['system_symbol'],
                    set_={
                        'graph_data': stmt.excluded.graph_data,
                        'last_updated': stmt.excluded.last_updated
                    }
                )
                conn.execute(stmt)
            else:
                # SQLite: Use INSERT OR REPLACE
                stmt = sqlite_insert(system_graphs).values(
                    system_symbol=system_symbol,
                    graph_data=graph_json,
                    last_updated=datetime.now(timezone.utc)
                )
                stmt = stmt.on_conflict_do_update(
                    index_elements=['system_symbol'],
                    set_={
                        'graph_data': stmt.excluded.graph_data,
                        'last_updated': stmt.excluded.last_updated
                    }
                )
                conn.execute(stmt)

        logger.debug(f"Saved graph for {system_symbol}")

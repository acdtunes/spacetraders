"""System graph provider with database caching"""
import json
import logging
from typing import Optional

from ports.outbound.graph_provider import (
    GraphLoadResult,
    IGraphBuilder,
    ISystemGraphProvider,
)
from ..persistence.database import Database

logger = logging.getLogger(__name__)


class SystemGraphProvider(ISystemGraphProvider):
    """
    Provides system navigation graphs with database caching

    Checks database cache first, falls back to building from API if needed.
    Stores newly built graphs in database for future use.
    """

    def __init__(self, database: Database, graph_builder: IGraphBuilder):
        self.db = database
        self.builder = graph_builder

    def get_graph(self, system_symbol: str, force_refresh: bool = False) -> GraphLoadResult:
        """
        Get navigation graph for system

        Args:
            system_symbol: System to get graph for
            force_refresh: Force fetch from API even if cached

        Returns:
            GraphLoadResult with graph data and source
        """
        # Try loading from database cache first (unless force refresh)
        if not force_refresh:
            graph = self._load_from_database(system_symbol)
            if graph is not None:
                logger.info(f"Loaded graph for {system_symbol} from database cache")
                return GraphLoadResult(
                    graph=graph,
                    source="database",
                    message=f"Loaded graph for {system_symbol} from database cache",
                )

        # Build from API and cache it
        graph = self._build_from_api(system_symbol)
        return GraphLoadResult(
            graph=graph,
            source="api",
            message=f"Built graph for {system_symbol} from API",
        )

    def _load_from_database(self, system_symbol: str) -> Optional[dict]:
        """Load graph from database cache"""
        try:
            with self.db.connection() as conn:
                cursor = conn.cursor()
                cursor.execute(
                    "SELECT graph_data FROM system_graphs WHERE system_symbol = ?",
                    (system_symbol,),
                )
                row = cursor.fetchone()

                if row:
                    graph_json = row[0]
                    graph = json.loads(graph_json)
                    logger.debug(f"Cache hit for {system_symbol}")
                    return graph
                else:
                    logger.debug(f"Cache miss for {system_symbol}")
                    return None

        except Exception as e:
            logger.error(f"Error loading graph from database: {e}")
            return None

    def _build_from_api(self, system_symbol: str) -> dict:
        """Build graph from API and save to database"""
        logger.info(f"Building navigation graph for {system_symbol} from API")

        try:
            # Build the graph
            graph = self.builder.build_system_graph(system_symbol)

            # Save to database cache
            self._save_to_database(system_symbol, graph)

            logger.info(f"Graph for {system_symbol} cached in database")
            return graph

        except Exception as e:
            logger.error(f"Failed to build graph for {system_symbol}: {e}")
            raise RuntimeError(f"Failed to build graph for {system_symbol}") from e

    def _save_to_database(self, system_symbol: str, graph: dict) -> None:
        """Save graph to database cache"""
        try:
            graph_json = json.dumps(graph)

            with self.db.transaction() as conn:
                cursor = conn.cursor()
                cursor.execute(
                    """
                    INSERT INTO system_graphs (system_symbol, graph_data, last_updated)
                    VALUES (?, ?, CURRENT_TIMESTAMP)
                    ON CONFLICT(system_symbol)
                    DO UPDATE SET
                        graph_data = excluded.graph_data,
                        last_updated = CURRENT_TIMESTAMP
                    """,
                    (system_symbol, graph_json),
                )

            logger.debug(f"Saved graph for {system_symbol} to database")

        except Exception as e:
            logger.error(f"Error saving graph to database: {e}")
            # Don't raise - caching failure shouldn't break the operation

"""System graph provider with database caching"""
import logging
from typing import Optional

from ports.outbound.graph_provider import (
    GraphLoadResult,
    IGraphBuilder,
    ISystemGraphProvider,
)

logger = logging.getLogger(__name__)


class SystemGraphProvider(ISystemGraphProvider):
    """
    Provides system navigation graphs with database caching

    Checks database cache first, falls back to building from API if needed.
    Stores newly built graphs in database for future use.
    """

    def __init__(self, graph_repo, graph_builder: IGraphBuilder, player_id: int):
        """Initialize with SQLAlchemy repository

        Args:
            graph_repo: SystemGraphRepositorySQLAlchemy instance
            graph_builder: Graph builder instance
            player_id: Player ID for API client
        """
        self._graph_repo = graph_repo
        self.builder = graph_builder
        self.player_id = player_id

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
            graph = self._graph_repo.get(system_symbol)
            if graph:
                logger.debug(f"Cache hit for {system_symbol}")
            else:
                logger.debug(f"Cache miss for {system_symbol}")
            return graph
        except Exception as e:
            logger.error(f"Error loading graph from database: {e}")
            return None

    def _build_from_api(self, system_symbol: str) -> dict:
        """Build graph from API and save to database"""
        logger.info(f"Building navigation graph for {system_symbol} from API")

        try:
            # Build the graph using this player's API client
            graph = self.builder.build_system_graph(system_symbol, self.player_id)

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
            self._graph_repo.save(system_symbol, graph)
            logger.debug(f"Saved graph for {system_symbol} to database")
        except Exception as e:
            logger.error(f"Error saving graph to database: {e}")
            # Don't raise - caching failure shouldn't break the operation

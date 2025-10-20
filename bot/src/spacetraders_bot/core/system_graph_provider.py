"""Shared service for loading and caching system navigation graphs."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Dict, Optional

from .database import Database
from .route_planner import GraphBuilder

logger = logging.getLogger(__name__)


@dataclass
class GraphLoadResult:
    """Details about a system graph load operation."""

    graph: Dict
    source: str
    message: str | None = None


class SystemGraphProvider:
    """Provide system graphs with consistent caching and persistence."""

    def __init__(
        self,
        api_client,
        *,
        db: Optional[Database] = None,
        builder: Optional[GraphBuilder] = None,
    ) -> None:
        self.api = api_client
        self.db = db or Database()
        self._builder = builder or GraphBuilder(api_client, db_path=self.db.db_path)

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def get_graph(self, system_symbol: str, *, force_refresh: bool = False) -> GraphLoadResult:
        """Return the navigation graph for *system_symbol*."""
        if not force_refresh:
            graph = self._load_from_database(system_symbol)
            if graph is not None:
                return GraphLoadResult(
                    graph=graph,
                    source="database",
                    message=f"📊 Loaded graph for {system_symbol} from database",
                )

        graph = self._build_from_api(system_symbol)
        return GraphLoadResult(
            graph=graph,
            source="api",
            message=f"📊 Built graph for {system_symbol} from API",
        )

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _load_from_database(self, system_symbol: str) -> Optional[Dict]:
        with self.db.connection() as conn:
            graph = self.db.get_system_graph(conn, system_symbol)
            if graph is not None:
                logger.info("Loaded %s graph from database", system_symbol)
            return graph

    def _build_from_api(self, system_symbol: str) -> Dict:
        """Build graph from API and save to database."""
        logger.info("Building navigation graph for %s from API", system_symbol)
        graph = self._builder.build_system_graph(system_symbol)
        if not graph:
            raise RuntimeError(f"Failed to build graph for {system_symbol}")
        # Note: GraphBuilder.build_system_graph() already saves to database
        logger.info("Graph for %s saved to database", system_symbol)
        return graph


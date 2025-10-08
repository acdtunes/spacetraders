"""Shared service for loading and caching system navigation graphs."""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, Optional

from ..helpers import paths
from .database import Database
from .routing import GraphBuilder

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
        cache_dir: Optional[Path | str] = None,
        builder: Optional[GraphBuilder] = None,
    ) -> None:
        self.api = api_client
        self.db = db or Database()
        self.cache_dir = Path(cache_dir) if cache_dir else paths.DATA_DIR / "graphs"
        paths.ensure_dirs((self.cache_dir,))

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

            graph = self._load_from_disk(system_symbol)
            if graph is not None:
                self._persist_to_database(system_symbol, graph)
                return GraphLoadResult(
                    graph=graph,
                    source="file",
                    message=(
                        f"📊 Loaded graph for {system_symbol} from cache file; "
                        "synchronized with database"
                    ),
                )

        graph = self._build_and_cache(system_symbol)
        return GraphLoadResult(
            graph=graph,
            source="api",
            message=f"📊 Built graph for {system_symbol} and cached results",
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

    def _load_from_disk(self, system_symbol: str) -> Optional[Dict]:
        cache_path = self.cache_dir / f"{system_symbol}_graph.json"
        if not cache_path.exists():
            return None

        try:
            graph = json.loads(cache_path.read_text())
            logger.info("Loaded %s graph from cache file %s", system_symbol, cache_path)
            return graph
        except json.JSONDecodeError as exc:
            logger.error("Failed to read cached graph %s: %s", cache_path, exc)
            return None

    def _persist_to_database(self, system_symbol: str, graph: Dict) -> None:
        with self.db.transaction() as conn:
            self.db.save_system_graph(conn, system_symbol, graph)
        logger.info("Persisted %s graph to database", system_symbol)

    def _build_and_cache(self, system_symbol: str) -> Dict:
        logger.info("Building navigation graph for %s", system_symbol)
        graph = self._builder.build_system_graph(system_symbol)
        if not graph:
            raise RuntimeError(f"Failed to build graph for {system_symbol}")

        self._write_to_disk(system_symbol, graph)
        logger.info("Cached %s graph to disk", system_symbol)
        return graph

    def _write_to_disk(self, system_symbol: str, graph: Dict) -> None:
        cache_path = self.cache_dir / f"{system_symbol}_graph.json"
        cache_path.write_text(json.dumps(graph))


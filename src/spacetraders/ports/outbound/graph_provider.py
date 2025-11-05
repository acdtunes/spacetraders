from abc import ABC, abstractmethod
from typing import Dict, Optional
from dataclasses import dataclass

@dataclass
class GraphLoadResult:
    """Result of graph load operation"""
    graph: Dict  # {waypoints: {...}, edges: [...]}
    source: str  # "database" or "api"
    message: Optional[str] = None

class ISystemGraphProvider(ABC):
    """Port for system graph management"""

    @abstractmethod
    def get_graph(self, system_symbol: str, force_refresh: bool = False) -> GraphLoadResult:
        """
        Get navigation graph for system

        Args:
            system_symbol: System to get graph for
            force_refresh: Force fetch from API even if cached

        Returns:
            GraphLoadResult with graph data and source
        """
        pass

class IGraphBuilder(ABC):
    """Port for building graphs from API"""

    @abstractmethod
    def build_system_graph(self, system_symbol: str) -> Dict:
        """
        Fetch all waypoints for system and build graph

        Returns:
            Graph dict: {waypoints: {symbol: {...}}, edges: [{from, to, distance, type}]}
        """
        pass

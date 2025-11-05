from dataclasses import dataclass
from pymediatr import Request, RequestHandler

from ....ports.outbound.graph_provider import ISystemGraphProvider, GraphLoadResult


@dataclass(frozen=True)
class GetSystemGraphQuery(Request[GraphLoadResult]):
    """
    Query to get navigation graph for a system

    Returns graph data including waypoints and edges
    Can optionally force refresh from API instead of using cached data
    """
    system_symbol: str
    player_id: int
    force_refresh: bool = False


class GetSystemGraphHandler(RequestHandler[GetSystemGraphQuery, GraphLoadResult]):
    """
    Handler for retrieving system navigation graph

    Delegates to system graph provider which handles caching and API fetching
    """

    def __init__(self):
        pass

    async def handle(self, request: GetSystemGraphQuery) -> GraphLoadResult:
        """
        Get navigation graph for system

        Args:
            request: System graph query

        Returns:
            GraphLoadResult with graph data and source information

        Raises:
            Any exceptions from graph provider (e.g., API errors)
        """
        # Get graph provider for this player (reads token from database)
        from ....configuration.container import get_graph_provider_for_player
        graph_provider = get_graph_provider_for_player(request.player_id)

        return graph_provider.get_graph(
            system_symbol=request.system_symbol,
            force_refresh=request.force_refresh
        )

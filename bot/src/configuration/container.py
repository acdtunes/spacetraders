"""
Dependency Injection Container.

Provides singleton instances and factory methods for:
- Database connections
- Repositories
- Mediator with all handlers registered
- Pipeline behaviors (middleware)
"""
import os
from pathlib import Path
from pymediatr import Mediator

from adapters.secondary.persistence.database import Database
from adapters.secondary.persistence.player_repository import PlayerRepository
from adapters.secondary.persistence.ship_repository import ShipRepository
from adapters.secondary.persistence.route_repository import RouteRepository
from adapters.secondary.persistence.market_repository import MarketRepository
from adapters.secondary.persistence.contract_repository import ContractRepository
from adapters.secondary.persistence.waypoint_repository import WaypointRepository
from adapters.secondary.api.client import SpaceTradersAPIClient
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine
from adapters.secondary.routing.graph_builder import GraphBuilder
from adapters.secondary.routing.graph_provider import SystemGraphProvider
from application.player.commands.register_player import (
    RegisterPlayerCommand,
    RegisterPlayerHandler
)
from application.player.commands.update_player import (
    UpdatePlayerMetadataCommand,
    UpdatePlayerMetadataHandler
)
from application.player.commands.touch_last_active import (
    TouchPlayerLastActiveCommand,
    TouchPlayerLastActiveHandler
)
from application.player.queries.get_player import (
    GetPlayerQuery,
    GetPlayerHandler,
    GetPlayerByAgentQuery,
    GetPlayerByAgentHandler
)
from application.player.queries.list_players import (
    ListPlayersQuery,
    ListPlayersHandler
)
from application.navigation.commands.navigate_ship import (
    NavigateShipCommand,
    NavigateShipHandler
)
from application.navigation.commands.sync_ships import (
    SyncShipsCommand,
    SyncShipsHandler
)
from application.navigation.commands.dock_ship import (
    DockShipCommand,
    DockShipHandler
)
from application.navigation.commands.orbit_ship import (
    OrbitShipCommand,
    OrbitShipHandler
)
from application.navigation.commands.refuel_ship import (
    RefuelShipCommand,
    RefuelShipHandler
)
from application.navigation.queries.plan_route import (
    PlanRouteQuery,
    PlanRouteHandler
)
from application.navigation.queries.get_ship_location import (
    GetShipLocationQuery,
    GetShipLocationHandler
)
from application.navigation.queries.get_system_graph import (
    GetSystemGraphQuery,
    GetSystemGraphHandler
)
from application.navigation.queries.list_ships import (
    ListShipsQuery,
    ListShipsHandler
)
from application.shipyard.queries.get_shipyard_listings import (
    GetShipyardListingsQuery,
    GetShipyardListingsHandler
)
from application.shipyard.commands.purchase_ship import (
    PurchaseShipCommand,
    PurchaseShipHandler
)
from application.shipyard.commands.batch_purchase_ships import (
    BatchPurchaseShipsCommand,
    BatchPurchaseShipsHandler
)
from application.shipyard.commands.sync_waypoints import (
    SyncSystemWaypointsCommand,
    SyncSystemWaypointsHandler
)
from application.scouting.queries.get_market_data import (
    GetMarketDataQuery,
    GetMarketDataHandler
)
from application.scouting.queries.list_market_data import (
    ListMarketDataQuery,
    ListMarketDataHandler
)
from application.scouting.commands.scout_markets import (
    ScoutMarketsCommand,
    ScoutMarketsHandler
)
from application.scouting.commands.scout_tour import (
    ScoutTourCommand,
    ScoutTourHandler
)
from application.contracts.queries.get_contract import (
    GetContractQuery,
    GetContractHandler
)
from application.contracts.queries.list_contracts import (
    ListContractsQuery,
    ListContractsHandler
)
from application.contracts.queries.get_active_contracts import (
    GetActiveContractsQuery,
    GetActiveContractsHandler
)
from application.contracts.commands.accept_contract import (
    AcceptContractCommand,
    AcceptContractHandler
)
from application.contracts.commands.deliver_contract import (
    DeliverContractCommand,
    DeliverContractHandler
)
from application.contracts.commands.fulfill_contract import (
    FulfillContractCommand,
    FulfillContractHandler
)
from application.contracts.commands.negotiate_contract import (
    NegotiateContractCommand,
    NegotiateContractHandler
)
from application.trading.commands.purchase_cargo import (
    PurchaseCargoCommand,
    PurchaseCargoHandler
)
from application.trading.queries.find_cheapest_market import (
    FindCheapestMarketQuery,
    FindCheapestMarketHandler
)
from application.contracts.queries.evaluate_profitability import (
    EvaluateContractProfitabilityQuery,
    EvaluateContractProfitabilityHandler
)
from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowCommand,
    BatchContractWorkflowHandler
)
from application.common.behaviors import (
    LoggingBehavior,
    ValidationBehavior
)
from ports.outbound.api_client import ISpaceTradersAPI
from ports.outbound.graph_provider import IGraphBuilder, ISystemGraphProvider
from ports.routing_engine import IRoutingEngine
from ports.repositories import IShipRepository, IRouteRepository, IWaypointRepository
from ports.outbound.market_repository import IMarketRepository
from .settings import settings


# Singleton instances
_db = None
_player_repo = None
_ship_repo = None
_route_repo = None
_market_repo = None
_contract_repo = None
_waypoint_repo = None
_graph_provider = None
_graph_builder = None
_routing_engine = None
_mediator = None


def get_database() -> Database:
    """
    Get or create database instance.

    Returns:
        Database: Singleton database instance
    """
    global _db
    if _db is None:
        _db = Database(settings.db_path)
    return _db


def get_player_repository() -> PlayerRepository:
    """
    Get or create player repository.

    Returns:
        PlayerRepository: Singleton player repository instance
    """
    global _player_repo
    if _player_repo is None:
        _player_repo = PlayerRepository(get_database())
    return _player_repo


def get_api_client_for_player(player_id: int) -> ISpaceTradersAPI:
    """
    Create SpaceTraders API client for specific player.
    Reads token from database.

    Args:
        player_id: Player ID to get token from database

    Returns:
        ISpaceTradersAPI: API client instance with player's token

    Raises:
        ValueError: If player not found
    """
    player = get_player_repository().find_by_id(player_id)
    if not player:
        raise ValueError(f"Player {player_id} not found in database")

    return SpaceTradersAPIClient(player.token)




def get_graph_builder_for_player(player_id: int) -> IGraphBuilder:
    """
    Create graph builder for specific player.
    Uses player's token from database.

    Args:
        player_id: Player ID to get token from database

    Returns:
        IGraphBuilder: Graph builder instance with player's API client
    """
    api_client = get_api_client_for_player(player_id)
    return GraphBuilder(api_client)


def get_graph_provider_for_player(player_id: int) -> ISystemGraphProvider:
    """
    Create system graph provider for specific player.
    Uses player's token from database.

    Args:
        player_id: Player ID to get token from database

    Returns:
        ISystemGraphProvider: Graph provider instance with player's API client
    """
    graph_builder = get_graph_builder_for_player(player_id)
    return SystemGraphProvider(get_database(), graph_builder)


def get_routing_engine() -> IRoutingEngine:
    """
    Get or create routing engine.

    Returns:
        IRoutingEngine: Singleton routing engine instance
    """
    global _routing_engine
    if _routing_engine is None:
        _routing_engine = ORToolsRoutingEngine()
    return _routing_engine


def get_ship_repository() -> IShipRepository:
    """
    Get or create ship repository.

    Returns:
        IShipRepository: Singleton ship repository instance
    """
    global _ship_repo
    if _ship_repo is None:
        # Don't initialize graph_provider by default - it requires API client
        # Operations that need the graph will need to handle this separately
        _ship_repo = ShipRepository(
            get_database(),
            graph_provider=None  # Lazy-loaded only when needed
        )
    return _ship_repo


def get_route_repository() -> IRouteRepository:
    """
    Get or create route repository.

    Returns:
        IRouteRepository: Singleton route repository instance
    """
    global _route_repo
    if _route_repo is None:
        _route_repo = RouteRepository(get_database())
    return _route_repo


def get_market_repository() -> IMarketRepository:
    """
    Get or create market repository.

    Returns:
        IMarketRepository: Singleton market repository instance
    """
    global _market_repo
    if _market_repo is None:
        _market_repo = MarketRepository(get_database())
    return _market_repo


def get_contract_repository():
    """
    Get or create contract repository.

    Returns:
        ContractRepository: Singleton contract repository instance
    """
    global _contract_repo
    if _contract_repo is None:
        _contract_repo = ContractRepository(get_database())
    return _contract_repo


def get_waypoint_repository() -> IWaypointRepository:
    """
    Get or create waypoint repository.

    Returns:
        IWaypointRepository: Singleton waypoint repository instance
    """
    global _waypoint_repo
    if _waypoint_repo is None:
        _waypoint_repo = WaypointRepository(get_database())
    return _waypoint_repo

def get_mediator() -> Mediator:
    """
    Get or create configured mediator with all handlers registered.

    The mediator is configured with:
    1. Pipeline behaviors (LoggingBehavior, ValidationBehavior)
    2. All command handlers
    3. All query handlers

    Returns:
        Mediator: Fully configured mediator instance
    """
    global _mediator
    if _mediator is None:
        _mediator = Mediator()

        # Register pipeline behaviors (middleware)
        # These execute in order: Logging -> Validation -> Handler
        _mediator.register_behavior(LoggingBehavior())
        _mediator.register_behavior(ValidationBehavior())

        # Get basic dependencies (database-only, no API)
        player_repo = get_player_repository()
        ship_repo = get_ship_repository()
        route_repo = get_route_repository()

        # Lazy dependencies - only initialized when handlers that need them are called
        # Don't initialize these here as they require API client

        # ===== Player Command Handlers =====
        _mediator.register_handler(
            RegisterPlayerCommand,
            lambda: RegisterPlayerHandler(player_repo)
        )
        _mediator.register_handler(
            UpdatePlayerMetadataCommand,
            lambda: UpdatePlayerMetadataHandler(player_repo)
        )
        _mediator.register_handler(
            TouchPlayerLastActiveCommand,
            lambda: TouchPlayerLastActiveHandler(player_repo)
        )

        # ===== Player Query Handlers =====
        _mediator.register_handler(
            GetPlayerQuery,
            lambda: GetPlayerHandler(player_repo)
        )
        _mediator.register_handler(
            GetPlayerByAgentQuery,
            lambda: GetPlayerByAgentHandler(player_repo)
        )
        _mediator.register_handler(
            ListPlayersQuery,
            lambda: ListPlayersHandler(player_repo)
        )

        # ===== Navigation Command Handlers =====
        # Note: Handlers get API client and graph provider themselves using player_id from command
        _mediator.register_handler(
            NavigateShipCommand,
            lambda: NavigateShipHandler(
                ship_repo,
                get_routing_engine()
            )
        )
        _mediator.register_handler(
            DockShipCommand,
            lambda: DockShipHandler(ship_repo)
        )
        _mediator.register_handler(
            OrbitShipCommand,
            lambda: OrbitShipHandler(ship_repo)
        )
        _mediator.register_handler(
            RefuelShipCommand,
            lambda: RefuelShipHandler(ship_repo)
        )
        _mediator.register_handler(
            SyncShipsCommand,
            lambda: SyncShipsHandler(ship_repo)
        )

        # ===== Navigation Query Handlers =====
        _mediator.register_handler(
            PlanRouteQuery,
            lambda: PlanRouteHandler(
                ship_repo,
                get_routing_engine()
            )
        )
        _mediator.register_handler(
            GetShipLocationQuery,
            lambda: GetShipLocationHandler(ship_repo)
        )
        _mediator.register_handler(
            GetSystemGraphQuery,
            lambda: GetSystemGraphHandler()
        )
        _mediator.register_handler(
            ListShipsQuery,
            lambda: ListShipsHandler(ship_repo)
        )

        # ===== Shipyard Query Handlers =====
        # Note: Handler gets API client dynamically using player_id from query
        _mediator.register_handler(
            GetShipyardListingsQuery,
            lambda: GetShipyardListingsHandler(api_client_factory=get_api_client_for_player)
        )

        # ===== Shipyard Command Handlers =====
        _mediator.register_handler(
            PurchaseShipCommand,
            lambda: PurchaseShipHandler(ship_repo, player_repo, get_waypoint_repository())
        )
        _mediator.register_handler(
            BatchPurchaseShipsCommand,
            lambda: BatchPurchaseShipsHandler(ship_repo, player_repo, get_waypoint_repository())
        )
        _mediator.register_handler(
            SyncSystemWaypointsCommand,
            lambda: SyncSystemWaypointsHandler(get_waypoint_repository())
        )

        # ===== Scouting Query Handlers =====
        market_repo = get_market_repository()
        _mediator.register_handler(
            GetMarketDataQuery,
            lambda: GetMarketDataHandler(market_repo)
        )
        _mediator.register_handler(
            ListMarketDataQuery,
            lambda: ListMarketDataHandler(market_repo)
        )

        # ===== Scouting Command Handlers =====
        _mediator.register_handler(
            ScoutMarketsCommand,
            lambda: ScoutMarketsHandler(ship_repo)
        )
        _mediator.register_handler(
            ScoutTourCommand,
            lambda: ScoutTourHandler(ship_repo, market_repo)
        )

        # ===== Contract Query Handlers =====
        contract_repo = get_contract_repository()
        db = get_database()
        _mediator.register_handler(
            GetContractQuery,
            lambda: GetContractHandler(contract_repo)
        )
        _mediator.register_handler(
            ListContractsQuery,
            lambda: ListContractsHandler(contract_repo)
        )
        _mediator.register_handler(
            GetActiveContractsQuery,
            lambda: GetActiveContractsHandler(contract_repo)
        )
        _mediator.register_handler(
            EvaluateContractProfitabilityQuery,
            lambda: EvaluateContractProfitabilityHandler(
                find_market_handler=FindCheapestMarketHandler(db)
            )
        )

        # ===== Contract Command Handlers =====
        _mediator.register_handler(
            AcceptContractCommand,
            lambda: AcceptContractHandler(contract_repo, get_api_client_for_player)
        )
        _mediator.register_handler(
            DeliverContractCommand,
            lambda: DeliverContractHandler(contract_repo, get_api_client_for_player)
        )
        _mediator.register_handler(
            FulfillContractCommand,
            lambda: FulfillContractHandler(contract_repo, get_api_client_for_player)
        )
        _mediator.register_handler(
            NegotiateContractCommand,
            lambda: NegotiateContractHandler(contract_repo, get_api_client_for_player)
        )
        _mediator.register_handler(
            BatchContractWorkflowCommand,
            lambda: BatchContractWorkflowHandler(
                mediator=_mediator,
                ship_repository=ship_repo
            )
        )

        # ===== Trading Command Handlers =====
        _mediator.register_handler(
            PurchaseCargoCommand,
            lambda: PurchaseCargoHandler(get_api_client_for_player)
        )

        # ===== Trading Query Handlers =====
        _mediator.register_handler(
            FindCheapestMarketQuery,
            lambda: FindCheapestMarketHandler(db)
        )

    return _mediator


_daemon_client = None

def get_daemon_client():
    """Get daemon client singleton"""
    global _daemon_client
    if _daemon_client is None:
        from adapters.primary.daemon.daemon_client import DaemonClient
        _daemon_client = DaemonClient()
    return _daemon_client


def reset_container():
    """
    Reset all singleton instances.

    Useful for testing to ensure clean state between tests.
    """
    global _db, _player_repo, _ship_repo, _route_repo, _market_repo, _contract_repo, _waypoint_repo
    global _graph_provider, _graph_builder, _routing_engine, _mediator, _daemon_client

    # Close database connection before resetting to ensure in-memory database is properly cleaned up
    if _db is not None:
        _db.close()

    _db = None
    _player_repo = None
    _ship_repo = None
    _route_repo = None
    _market_repo = None
    _contract_repo = None
    _waypoint_repo = None
    _graph_provider = None
    _graph_builder = None
    _routing_engine = None
    _mediator = None
    _daemon_client = None

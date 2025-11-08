"""Scout markets command and handler"""
from dataclasses import dataclass
from typing import List, Dict
import logging
import uuid
from pymediatr import Request, RequestHandler

from domain.shared.exceptions import ShipNotFoundError
from ports.repositories import IShipRepository

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class ScoutMarketsResult:
    """Result of scout markets deployment"""
    container_ids: List[str]
    assignments: Dict[str, List[str]]  # ship_symbol -> assigned_markets


@dataclass(frozen=True)
class ScoutMarketsCommand(Request[ScoutMarketsResult]):
    """
    Command to partition markets across multiple ships and deploy market scouting.

    The handler orchestrates:
    1. Load ships and validate they exist
    2. Get system graph from graph provider
    3. Call routing engine's optimize_fleet_tour() to partition markets (VRP)
    4. Create N ScoutTourCommand containers via daemon client (one per ship)
    5. Return container IDs and market assignments

    Each ship gets a disjoint subset of markets optimized by the VRP solver.
    Tours always return to start for multi-market assignments (2+).
    """
    ship_symbols: List[str]
    player_id: int
    system: str
    markets: List[str]
    iterations: int = 1


class ScoutMarketsHandler(RequestHandler[ScoutMarketsCommand, ScoutMarketsResult]):
    """
    Handler for scout markets deployment.

    Partitions markets across multiple ships using VRP and launches
    individual scout tour containers for each ship.
    """

    def __init__(self, ship_repository: IShipRepository):
        """
        Initialize ScoutMarketsHandler.

        Args:
            ship_repository: Repository for ship persistence
        """
        self._ship_repo = ship_repository

    async def handle(self, request: ScoutMarketsCommand) -> ScoutMarketsResult:
        """
        Submit VRP work to background container and return immediately.

        The VRP container will:
        1. Run VRP optimization (10-30 seconds)
        2. Create scout tour containers for each ship
        """
        from configuration.container import get_container_manager, get_daemon_client
        import uuid

        container_id = f"scout-markets-vrp-{uuid.uuid4().hex[:8]}"

        # Try to use ContainerManager if inside daemon, otherwise use daemon client
        try:
            container_mgr = get_container_manager()
            # Inside daemon - use direct access
            await container_mgr.create_container(
                container_id=container_id,
                player_id=request.player_id,
                container_type='command',
                config={
                    'command_type': 'ScoutMarketsVRPCommand',
                    'params': {
                        'ship_symbols': request.ship_symbols,
                        'player_id': request.player_id,
                        'system': request.system,
                        'markets': request.markets,
                        'iterations': request.iterations
                    }
                }
            )
        except RuntimeError:
            # Outside daemon - use daemon client
            daemon = get_daemon_client()
            daemon.create_container({
                'container_id': container_id,
                'player_id': request.player_id,
                'container_type': 'command',
                'config': {
                    'command_type': 'ScoutMarketsVRPCommand',
                    'params': {
                        'ship_symbols': request.ship_symbols,
                        'player_id': request.player_id,
                        'system': request.system,
                        'markets': request.markets,
                        'iterations': request.iterations
                    }
                }
            })

        # Return immediately - VRP runs in background
        return ScoutMarketsResult(
            container_ids=[container_id],
            assignments={ship: [] for ship in request.ship_symbols}
        )

    async def handle_ORIGINAL(self, request: ScoutMarketsCommand) -> ScoutMarketsResult:
        """
        ORIGINAL VERSION - runs VRP synchronously (30+ seconds)

        Partition markets and deploy market scouting.

        Process:
        1. Load all ships from repository
        2. Get system graph from graph provider
        3. Call routing engine to partition markets (VRP)
        4. For each ship, create a ScoutTourCommand container via daemon client
        5. Return container IDs and assignments

        Args:
            request: Scout markets command

        Returns:
            ScoutMarketsResult with container IDs and market assignments

        Raises:
            ShipNotFoundError: If any ship doesn't exist
        """
        logger.info(f"Deploying scout markets: {len(request.ship_symbols)} ships, {len(request.markets)} markets")

        # 1. Load all ships and validate
        ships = []
        ship_locations = {}

        for ship_symbol in request.ship_symbols:
            ship = self._ship_repo.find_by_symbol(ship_symbol, request.player_id)
            if ship is None:
                raise ShipNotFoundError(
                    f"Ship {ship_symbol} not found for player {request.player_id}"
                )
            ships.append(ship)
            ship_locations[ship_symbol] = ship.current_location.symbol

        logger.info(f"Loaded {len(ships)} ships: {ship_locations}")

        # 2. Get system graph
        from configuration.container import get_graph_provider_for_player
        graph_provider = get_graph_provider_for_player(request.player_id)
        graph_result = graph_provider.get_graph(request.system)

        if not graph_result:
            raise Exception(f"Failed to get graph for system {request.system}")

        # Extract graph dict (handles both RealisticGraphResult and dict)
        graph_data = graph_result.graph if hasattr(graph_result, 'graph') else graph_result

        if 'waypoints' not in graph_data:
            raise Exception(f"Graph for system {request.system} has no waypoints")

        # Convert graph result to Dict[str, Waypoint]
        from domain.shared.value_objects import Waypoint
        graph = {}
        for wp_symbol, wp_data in graph_data['waypoints'].items():
            graph[wp_symbol] = Waypoint(
                symbol=wp_symbol,
                waypoint_type=wp_data.get('type', 'UNKNOWN'),
                x=wp_data.get('x', 0),
                y=wp_data.get('y', 0),
                system_symbol=request.system,
                traits=tuple(wp_data.get('traits', [])),
                has_fuel=wp_data.get('has_fuel', False)
            )

        # 3. Partition markets across ships
        # Optimization: Skip VRP for trivial cases (1 ship)
        if len(ships) == 1:
            # Single ship gets all markets - no VRP needed
            single_ship = request.ship_symbols[0]
            assignments = {single_ship: request.markets}
            logger.info(f"Single ship optimization: {single_ship} assigned all {len(request.markets)} markets")
        else:
            # Multiple ships - use VRP to partition markets (run in executor to avoid blocking)
            from configuration.container import get_routing_engine
            import asyncio
            routing_engine = get_routing_engine()

            # Assume homogeneous fleet (use first ship's specs)
            fuel_capacity = ships[0].fuel_capacity
            engine_speed = ships[0].engine_speed

            # Run VRP in thread pool to avoid blocking event loop
            loop = asyncio.get_event_loop()
            assignments = await loop.run_in_executor(
                None,
                routing_engine.optimize_fleet_tour,
                graph,
                request.markets,
                ship_locations,
                fuel_capacity,
                engine_speed
            )

            if not assignments:
                raise Exception("Fleet tour optimization failed")

            logger.info(f"VRP partitioning complete:")
            for ship, markets in assignments.items():
                logger.info(f"  {ship}: {len(markets)} markets")

        # 4. Create scout tour containers for each ship (with idempotency check)
        from configuration.container import get_daemon_client
        daemon = get_daemon_client()

        container_ids = []

        # First, check for existing active containers for these ships
        existing_containers = daemon.list_containers(player_id=request.player_id)
        existing_ship_containers = {}

        for container_info in existing_containers.get('containers', []):
            # Parse ship symbol from container_id pattern: scout-tour-{ship}-{uuid}
            container_id = container_info['container_id']
            if container_id.startswith('scout-tour-') and container_info['status'] in ['STARTING', 'RUNNING']:
                # Extract ship symbol (everything between 'scout-tour-' and last '-')
                parts = container_id[len('scout-tour-'):].rsplit('-', 1)
                if len(parts) == 2:
                    ship_from_container = parts[0].upper()
                    # Store the first active container for each ship
                    if ship_from_container not in existing_ship_containers:
                        existing_ship_containers[ship_from_container] = container_id
                        logger.info(f"Found existing container {container_id} for {ship_from_container}")

        for ship_symbol in request.ship_symbols:
            assigned_markets = assignments.get(ship_symbol, [])

            if not assigned_markets:
                logger.warning(f"Ship {ship_symbol} has no markets assigned, skipping container creation")
                continue

            # Check if container already exists for this ship (idempotency)
            if ship_symbol in existing_ship_containers:
                existing_container_id = existing_ship_containers[ship_symbol]
                container_ids.append(existing_container_id)
                logger.info(f"♻️  Reusing existing container {existing_container_id} for {ship_symbol} (idempotent)")
                continue

            # Generate unique container ID
            container_id = f"scout-tour-{ship_symbol.lower()}-{uuid.uuid4().hex[:8]}"

            # Create container for this ship's tour
            daemon.create_container({
                'container_id': container_id,
                'player_id': request.player_id,
                'container_type': 'command',
                'config': {
                    'command_type': 'ScoutTourCommand',
                    'params': {
                        'ship_symbol': ship_symbol,
                        'player_id': request.player_id,
                        'system': request.system,
                        'markets': assigned_markets
                    },
                    'iterations': request.iterations
                },
                'restart_policy': 'no'
            })

            container_ids.append(container_id)
            logger.info(f"✅ Created container {container_id} for {ship_symbol} ({len(assigned_markets)} markets)")

        logger.info(f"Scout markets deployment complete: {len(container_ids)} containers (reused or created)")

        return ScoutMarketsResult(
            container_ids=container_ids,
            assignments=assignments
        )

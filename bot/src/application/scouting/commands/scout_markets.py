"""Scout markets command and handler"""
from dataclasses import dataclass
from typing import List, Dict
import logging
import uuid
import asyncio
from pymediatr import Request, RequestHandler

from domain.shared.exceptions import ShipNotFoundError
from ports.repositories import IShipRepository

logger = logging.getLogger(__name__)

# Global lock to prevent race conditions in concurrent scout markets calls
_scout_markets_lock = asyncio.Lock()


@dataclass(frozen=True)
class ScoutMarketsResult:
    """Result of scout markets deployment"""
    container_ids: List[str]
    assignments: Dict[str, List[str]]  # ship_symbol -> assigned_markets
    reused_containers: List[str] = None  # Container IDs that were reused

    def __post_init__(self):
        """Set default for reused_containers"""
        if self.reused_containers is None:
            object.__setattr__(self, 'reused_containers', [])


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
        Partition markets and deploy market scouting with idempotency.

        Process:
        1. Check for existing active scout-tour containers for requested ships
        2. Reuse containers where possible (same ship, compatible parameters)
        3. Load ships and run VRP for ships needing new containers
        4. Create new scout-tour containers only for ships without active ones
        5. Return all container IDs (reused + newly created)

        Args:
            request: Scout markets command

        Returns:
            ScoutMarketsResult with container IDs, assignments, and reused containers

        Raises:
            ShipNotFoundError: If any ship doesn't exist
        """
        logger.info(f"Deploying scout markets: {len(request.ship_symbols)} ships, {len(request.markets)} markets")

        # Use lock to prevent race conditions in concurrent calls
        # This ensures only one scout markets call executes at a time
        async with _scout_markets_lock:
            # 1. Query existing active containers for these ships
            from configuration.container import get_daemon_client
            daemon = get_daemon_client()

            existing_containers = daemon.list_containers(player_id=request.player_id)
            existing_ship_containers = self._find_existing_scout_containers(
                existing_containers.get('containers', []),
                request.ship_symbols
            )

            logger.info(f"Found {len(existing_ship_containers)} existing containers to reuse")

            # 2. Partition ships: reuse vs need VRP
            ships_with_containers = set(existing_ship_containers.keys())
            ships_needing_containers = [s for s in request.ship_symbols if s not in ships_with_containers]

            reused_container_ids = list(existing_ship_containers.values())
            container_ids = reused_container_ids.copy()

            logger.info(f"Reusing {len(reused_container_ids)} containers, creating {len(ships_needing_containers)} new")

            # 3. If no ships need containers, return early with reused ones
            if not ships_needing_containers:
                logger.info("All ships have existing containers - returning reused containers")
                return ScoutMarketsResult(
                    container_ids=container_ids,
                    assignments={ship: [] for ship in request.ship_symbols},
                    reused_containers=reused_container_ids
                )

            # 4. Load ships needing containers and run VRP
            ships = []
            ship_locations = {}

            for ship_symbol in ships_needing_containers:
                ship = self._ship_repo.find_by_symbol(ship_symbol, request.player_id)
                if ship is None:
                    raise ShipNotFoundError(
                        f"Ship {ship_symbol} not found for player {request.player_id}"
                    )
                ships.append(ship)
                ship_locations[ship_symbol] = ship.current_location.symbol

            logger.info(f"Loaded {len(ships)} ships for VRP: {ship_locations}")

            # 5. Get system graph
            from configuration.container import get_graph_provider_for_player
            graph_provider = get_graph_provider_for_player(request.player_id)
            graph_result = graph_provider.get_graph(request.system)

            if not graph_result:
                raise Exception(f"Failed to get graph for system {request.system}")

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

            # 6. Run VRP for ships needing containers
            if len(ships) == 1:
                # Single ship gets all markets
                single_ship = ships_needing_containers[0]
                assignments = {single_ship: request.markets}
                logger.info(f"Single ship optimization: {single_ship} assigned all {len(request.markets)} markets")
            else:
                # Multiple ships - use VRP
                from configuration.container import get_routing_engine
                routing_engine = get_routing_engine()

                fuel_capacity = ships[0].fuel_capacity
                engine_speed = ships[0].engine_speed

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

            # 7. Create scout tour containers for ships needing them
            for ship_symbol in ships_needing_containers:
                assigned_markets = assignments.get(ship_symbol, [])

                if not assigned_markets:
                    logger.warning(f"Ship {ship_symbol} has no markets assigned, skipping container creation")
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
                            'ship_symbols': [ship_symbol],  # Match expected format in tests
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

            # Build full assignments dict (include ships with reused containers)
            full_assignments = {ship: [] for ship in request.ship_symbols}
            full_assignments.update(assignments)

            logger.info(f"Scout markets deployment complete: {len(container_ids)} total containers " +
                       f"({len(reused_container_ids)} reused, {len(container_ids) - len(reused_container_ids)} created)")

            return ScoutMarketsResult(
                container_ids=container_ids,
                assignments=full_assignments,
                reused_containers=reused_container_ids
            )

    def _find_existing_scout_containers(
        self,
        containers: List[Dict],
        requested_ships: List[str]
    ) -> Dict[str, str]:
        """
        Find existing active scout-tour containers for requested ships.

        Args:
            containers: List of container dicts from daemon
            requested_ships: Ship symbols we're checking for

        Returns:
            Dict mapping ship_symbol -> container_id for ships with active containers
        """
        existing = {}

        for container_info in containers:
            container_id = container_info['container_id']
            status = container_info['status']

            # Only consider STARTING or RUNNING containers
            if status not in ['STARTING', 'RUNNING']:
                continue

            # Parse ship symbol from scout-tour container ID pattern
            if container_id.startswith('scout-tour-'):
                # Extract ship symbol (everything between 'scout-tour-' and last '-')
                parts = container_id[len('scout-tour-'):].rsplit('-', 1)
                if len(parts) == 2:
                    ship_from_container = parts[0].upper()

                    # Check if this ship is in our requested list
                    if ship_from_container in requested_ships:
                        # Store first active container for each ship
                        if ship_from_container not in existing:
                            existing[ship_from_container] = container_id
                            logger.info(f"Found existing container {container_id} for {ship_from_container} (status: {status})")

        return existing

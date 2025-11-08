"""Scout markets VRP command - does VRP optimization and creates tour containers"""
from dataclasses import dataclass
from typing import List
import logging
import uuid
from pymediatr import Request, RequestHandler

from domain.shared.exceptions import ShipNotFoundError
from ports.repositories import IShipRepository

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class ScoutMarketsVRPCommand(Request[None]):
    """
    Command to run VRP optimization and create scout tour containers.

    This is meant to run in a background container so the CLI returns immediately.
    """
    ship_symbols: List[str]
    player_id: int
    system: str
    markets: List[str]
    iterations: int = 1


class ScoutMarketsVRPHandler(RequestHandler[ScoutMarketsVRPCommand, None]):
    """Handler that does VRP and creates scout tour containers"""

    def __init__(self, ship_repository: IShipRepository):
        self._ship_repo = ship_repository

    async def handle(self, request: ScoutMarketsVRPCommand) -> None:
        """Run VRP and create scout tour containers"""
        logger.info(f"Starting VRP optimization for {len(request.ship_symbols)} ships, {len(request.markets)} markets")

        # 1. Load all ships
        ships = []
        ship_locations = {}

        for ship_symbol in request.ship_symbols:
            ship = self._ship_repo.find_by_symbol(ship_symbol, request.player_id)
            if ship is None:
                raise ShipNotFoundError(f"Ship {ship_symbol} not found for player {request.player_id}")
            ships.append(ship)
            ship_locations[ship_symbol] = ship.current_location.symbol

        logger.info(f"Loaded {len(ships)} ships: {ship_locations}")

        # 2. Get system graph
        from configuration.container import get_graph_provider_for_player
        graph_provider = get_graph_provider_for_player(request.player_id)
        graph_result = graph_provider.get_graph(request.system)

        if not graph_result:
            raise Exception(f"Failed to get graph for system {request.system}")

        graph_data = graph_result.graph if hasattr(graph_result, 'graph') else graph_result

        if 'waypoints' not in graph_data:
            raise Exception(f"Graph for system {request.system} has no waypoints")

        # Convert to Dict[str, Waypoint]
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

        # 3. Run VRP optimization
        if len(ships) == 1:
            # Single ship gets all markets
            single_ship = request.ship_symbols[0]
            assignments = {single_ship: request.markets}
            logger.info(f"Single ship optimization: {single_ship} assigned all {len(request.markets)} markets")
        else:
            # Multi-ship VRP
            from configuration.container import get_routing_engine
            routing_engine = get_routing_engine()

            fuel_capacity = ships[0].fuel_capacity
            engine_speed = ships[0].engine_speed

            logger.info(f"Running VRP optimization...")
            assignments = routing_engine.optimize_fleet_tour(
                graph=graph,
                markets=request.markets,
                ship_locations=ship_locations,
                fuel_capacity=fuel_capacity,
                engine_speed=engine_speed
            )

            if not assignments:
                raise Exception("Fleet tour optimization failed")

            logger.info(f"VRP complete:")
            for ship, markets in assignments.items():
                logger.info(f"  {ship}: {len(markets)} markets")

        # 4. Create scout tour containers via ContainerManager (NOT daemon client - we're inside daemon!)
        from configuration.container import get_container_manager

        container_mgr = get_container_manager()

        for ship_symbol in request.ship_symbols:
            assigned_markets = assignments.get(ship_symbol, [])

            if not assigned_markets:
                logger.warning(f"Ship {ship_symbol} has no markets assigned, skipping")
                continue

            container_id = f"scout-tour-{ship_symbol.lower()}-{uuid.uuid4().hex[:8]}"

            logger.info(f"Creating container {container_id} for {ship_symbol} with {len(assigned_markets)} markets")

            # Create container directly via container manager
            await container_mgr.create_container(
                container_id=container_id,
                player_id=request.player_id,
                container_type='command',
                config={
                    'command_type': 'ScoutTourCommand',
                    'params': {
                        'ship_symbol': ship_symbol,
                        'player_id': request.player_id,
                        'system': request.system,
                        'markets': assigned_markets
                    },
                    'iterations': request.iterations
                }
            )

        logger.info(f"✅ Created {len([m for m in assignments.values() if m])} scout tour containers")

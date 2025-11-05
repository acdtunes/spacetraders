"""Navigate ship command and handler"""
from dataclasses import dataclass
from typing import Dict, Any
import asyncio
import logging
from datetime import datetime, timezone
from dateutil import parser as dateparser
from pymediatr import Request, RequestHandler

logger = logging.getLogger(__name__)

from domain.navigation.route import Route, RouteSegment, RouteStatus
from domain.shared.ship import Ship, InvalidNavStatusError, InsufficientFuelError
from domain.shared.exceptions import ShipNotFoundError, DomainException
from domain.shared.value_objects import Waypoint, FlightMode
from ports.repositories import IShipRepository
from ports.outbound.graph_provider import ISystemGraphProvider
from ports.routing_engine import IRoutingEngine
from ports.outbound.api_client import ISpaceTradersAPI
from ._ship_converter import convert_api_ship_to_entity


def calculate_arrival_wait_time(arrival_time_str: str) -> int:
    """
    Calculate seconds to wait until arrival.

    Args:
        arrival_time_str: ISO format arrival time from API (e.g., "2024-01-01T12:00:00Z")

    Returns:
        Seconds to wait (minimum 0)
    """
    # Handle both Z suffix and +00:00 suffix
    arrival_time = dateparser.isoparse(arrival_time_str.replace('Z', '+00:00'))
    now = datetime.now(timezone.utc)
    wait_seconds = (arrival_time - now).total_seconds()
    return max(0, int(wait_seconds))


@dataclass(frozen=True)
class NavigateShipCommand(Request[Route]):
    """
    Command to navigate a ship to a destination waypoint.

    This command orchestrates the complete navigation process including:
    - Path finding with fuel constraints
    - Refueling stops as needed
    - State transitions (orbit/dock)
    - API calls for navigation actions
    """
    ship_symbol: str
    destination_symbol: str
    player_id: int


class NavigateShipHandler(RequestHandler[NavigateShipCommand, Route]):
    """
    Handler for ship navigation operations.

    Responsibilities:
    - Load ship from repository
    - Get system graph for pathfinding
    - Find optimal route using routing engine
    - Execute route step by step (navigate/refuel/dock/orbit)
    - Update ship state after each step
    - Persist ship changes
    - Return completed route
    """

    def __init__(
        self,
        ship_repository: IShipRepository,
        routing_engine: IRoutingEngine
    ):
        """
        Initialize NavigateShipHandler.

        Args:
            ship_repository: Repository for ship persistence
            routing_engine: Engine for pathfinding and route optimization
        """
        self._ship_repo = ship_repository
        self._routing_engine = routing_engine

    async def handle(self, request: NavigateShipCommand) -> Route:
        """
        Execute ship navigation command.

        Process:
        1. Get API client for player
        2. Load ship from repository
        3. Get system graph for current location
        4. Find optimal path using routing engine
        5. Create Route entity
        6. Execute route (loop through steps: navigate/refuel/dock/orbit)
        7. Update ship state after each step
        8. Persist ship
        9. Return completed Route

        Args:
            request: Navigation command with ship symbol, destination, and player ID

        Returns:
            Route entity with execution details

        Raises:
            ShipNotFoundError: If ship doesn't exist
            InvalidNavStatusError: If ship is in invalid state for navigation
            InsufficientFuelError: If ship cannot complete journey even with refueling
            ValueError: If destination is unreachable or invalid
        """
        # 1. Get API client and graph provider for this player (reads token from database)
        from configuration.container import get_api_client_for_player, get_graph_provider_for_player
        api_client = get_api_client_for_player(request.player_id)
        graph_provider = get_graph_provider_for_player(request.player_id)

        # 2. Load ship from repository and sync from API to ensure fresh state
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if ship is None:
            raise ShipNotFoundError(
                f"Ship '{request.ship_symbol}' not found for player {request.player_id}"
            )

        # 2b. Sync ship state from API before navigation to prevent stale database state
        # This ensures we have accurate nav_status, fuel, cargo, and location
        ship = self._ship_repo.sync_from_api(
            request.ship_symbol,
            request.player_id,
            api_client,
            graph_provider
        )

        # 3. Get system graph
        system_symbol = self._extract_system_symbol(ship.current_location.symbol)
        graph_result = graph_provider.get_graph(system_symbol)
        graph = graph_result.graph

        # Convert graph waypoints to Waypoint objects if needed
        waypoint_objects = self._convert_graph_to_waypoints(graph)

        # 3. Find optimal path using routing engine
        route_plan = self._routing_engine.find_optimal_path(
            graph=waypoint_objects,
            start=ship.current_location.symbol,
            goal=request.destination_symbol,
            current_fuel=ship.fuel.current,
            fuel_capacity=ship.fuel_capacity,
            engine_speed=ship.engine_speed,
            prefer_cruise=True
        )

        if route_plan is None:
            raise ValueError(
                f"No path found from {ship.current_location.symbol} to {request.destination_symbol}"
            )

        # 4. Create Route entity from plan
        route = self._create_route_from_plan(
            route_plan=route_plan,
            ship_symbol=request.ship_symbol,
            player_id=request.player_id,
            ship_fuel_capacity=ship.fuel_capacity,
            waypoint_objects=waypoint_objects
        )

        # 5. Execute route
        route.start_execution()
        ship = await self._execute_route(route, ship, api_client, graph_provider)

        # 6. Persist ship (final state after route execution)
        # Note: ship was already synced during execution with from_api=True
        # This is a final persistence to ensure consistency
        self._ship_repo.update(ship, from_api=True)

        # 7. Return Route
        return route

    def _extract_system_symbol(self, waypoint_symbol: str) -> str:
        """
        Extract system symbol from waypoint symbol.

        Format: SYSTEM-SECTOR-WAYPOINT -> SYSTEM-SECTOR
        Example: X1-ABC123-AB12 -> X1-ABC123

        Args:
            waypoint_symbol: Full waypoint symbol

        Returns:
            System symbol
        """
        parts = waypoint_symbol.split('-')
        if len(parts) >= 2:
            return f"{parts[0]}-{parts[1]}"
        return waypoint_symbol

    def _convert_graph_to_waypoints(self, graph: Dict[str, Any]) -> Dict[str, Waypoint]:
        """
        Convert graph waypoints dict to Waypoint objects.

        Args:
            graph: Graph data with waypoints

        Returns:
            Dict mapping waypoint symbol to Waypoint object
        """
        waypoint_objects = {}

        if 'waypoints' in graph:
            for symbol, wp_data in graph['waypoints'].items():
                # If it's already a Waypoint object, use it
                if isinstance(wp_data, Waypoint):
                    waypoint_objects[symbol] = wp_data
                else:
                    # Convert from dict
                    traits_data = wp_data.get('traits', [])

                    # Use has_fuel from graph data (already calculated by graph provider)
                    # If not present, fallback to checking traits for MARKETPLACE
                    if 'has_fuel' in wp_data:
                        has_fuel_station = wp_data['has_fuel']
                    else:
                        # Fallback: check if traits contain MARKETPLACE
                        # Traits can be either list of dicts [{'symbol': 'MARKETPLACE'}]
                        # or list of strings ['MARKETPLACE']
                        has_fuel_station = any(
                            (t.get('symbol') == 'MARKETPLACE' if isinstance(t, dict) else t == 'MARKETPLACE')
                            for t in traits_data
                        )

                    # Convert traits to tuple of symbols
                    if traits_data and isinstance(traits_data[0], dict):
                        traits_tuple = tuple(t.get('symbol', '') for t in traits_data)
                    else:
                        traits_tuple = tuple(traits_data)  # Already strings

                    waypoint_objects[symbol] = Waypoint(
                        symbol=wp_data.get('symbol', symbol),
                        waypoint_type=wp_data.get('type', 'UNKNOWN'),
                        x=wp_data.get('x', 0),
                        y=wp_data.get('y', 0),
                        system_symbol=wp_data.get('systemSymbol', self._extract_system_symbol(symbol)),
                        traits=traits_tuple,
                        has_fuel=has_fuel_station
                    )

        return waypoint_objects

    def _create_route_from_plan(
        self,
        route_plan: Dict[str, Any],
        ship_symbol: str,
        player_id: int,
        ship_fuel_capacity: int,
        waypoint_objects: Dict[str, Waypoint]
    ) -> Route:
        """
        Create Route entity from routing engine plan.

        Args:
            route_plan: Plan from routing engine
            ship_symbol: Ship identifier
            player_id: Player identifier
            ship_fuel_capacity: Ship's fuel capacity
            waypoint_objects: Map of waypoint symbols to objects

        Returns:
            Route entity
        """
        segments = []
        refuel_before_departure = False

        steps = route_plan.get('steps', [])

        # Check if first action is REFUEL (ship at fuel station with low fuel)
        if steps and steps[0]['action'] == 'REFUEL':
            refuel_before_departure = True

        for step in steps:
            if step['action'] == 'TRAVEL':
                # Find the from_waypoint (previous step's destination or start)
                if segments:
                    from_waypoint = segments[-1].to_waypoint
                else:
                    from_waypoint = waypoint_objects[step.get('from', step['waypoint'])]

                to_waypoint = waypoint_objects[step['waypoint']]

                segment = RouteSegment(
                    from_waypoint=from_waypoint,
                    to_waypoint=to_waypoint,
                    distance=step.get('distance', 0.0),
                    fuel_required=step.get('fuel_cost', 0),
                    travel_time=step.get('time', 0),
                    flight_mode=step.get('mode', FlightMode.CRUISE),
                    requires_refuel=False
                )
                segments.append(segment)
            elif step['action'] == 'REFUEL':
                # Mark previous segment as requiring refuel (mid-route refueling)
                if segments:
                    # Create new segment with updated refuel flag
                    prev = segments[-1]
                    segments[-1] = RouteSegment(
                        from_waypoint=prev.from_waypoint,
                        to_waypoint=prev.to_waypoint,
                        distance=prev.distance,
                        fuel_required=prev.fuel_required,
                        travel_time=prev.travel_time,
                        flight_mode=prev.flight_mode,
                        requires_refuel=True
                    )

        # Generate route ID
        route_id = f"{ship_symbol}_{route_plan.get('total_time', 0)}"

        return Route(
            route_id=route_id,
            ship_symbol=ship_symbol,
            player_id=player_id,
            segments=segments,
            ship_fuel_capacity=ship_fuel_capacity,
            refuel_before_departure=refuel_before_departure
        )

    async def _execute_route(self, route: Route, ship: Ship, api_client: ISpaceTradersAPI, graph_provider: ISystemGraphProvider) -> Ship:
        """
        Execute route step by step.

        For each segment:
        1. Ensure ship is in orbit (domain handles state transition)
        2. Call API navigate
        3. Auto-sync ship state from API response
        4. If refuel required: dock, refuel, orbit (domain handles transitions)
        5. Complete segment

        Args:
            route: Route to execute
            ship: Ship to navigate
            api_client: API client for player
            graph_provider: System graph provider for waypoint data

        Returns:
            Updated Ship entity after route execution

        Raises:
            InvalidNavStatusError: If ship state transitions fail
        """
        try:
            # IDEMPOTENCY: If ship is IN_TRANSIT from a previous command, wait for arrival first
            # This makes navigation commands idempotent - you can send them at any time
            if ship.nav_status == Ship.IN_TRANSIT:
                logger.info(f"Ship {ship.ship_symbol} is IN_TRANSIT from previous command, waiting for arrival...")

                # Fetch current ship state to get arrival time
                ship_response = api_client.get_ship(ship.ship_symbol)
                ship_data = ship_response.get('data')

                if ship_data and ship_data['nav']['status'] == 'IN_TRANSIT' and 'route' in ship_data['nav']:
                    arrival_time_str = ship_data['nav']['route'].get('arrival')
                    if arrival_time_str:
                        wait_time = calculate_arrival_wait_time(arrival_time_str)

                        if wait_time > 0:
                            logger.info(f"Waiting {wait_time + 3} seconds for ship to complete previous transit")
                            await asyncio.sleep(wait_time + 3)  # Use async sleep to avoid blocking event loop

                        # Sync ship state after arrival
                        ship = self._ship_repo.sync_from_api(
                            ship.ship_symbol,
                            route.player_id,
                            api_client,
                            graph_provider
                        )
                        logger.info(f"Ship arrived, status now: {ship.nav_status}")

            # Handle refuel before departure if needed (ship at fuel station with low fuel)
            if route.refuel_before_departure:
                # Dock for refuel - domain handles state transition
                state_changed = ship.ensure_docked()
                if state_changed:
                    # Call API to dock ship
                    api_client.dock_ship(ship.ship_symbol)

                    # Auto-sync: Fetch full ship state after dock
                    # Dock endpoint returns {data: {nav: {...}}} not full ship object
                    # So we need to fetch the complete ship state
                    ship = self._ship_repo.sync_from_api(
                        ship.ship_symbol,
                        route.player_id,
                        api_client,
                        graph_provider
                    )

                # Refuel before starting journey
                refuel_result = api_client.refuel_ship(ship.ship_symbol)

                # Auto-sync: Extract and convert ship data
                ship_data = refuel_result.get('data', {}).get('ship')
                if ship_data:
                    ship = convert_api_ship_to_entity(
                        ship_data,
                        route.player_id,
                        ship.current_location  # Refueling doesn't change location
                    )
                    self._ship_repo.update(ship, from_api=True)

                # Return to orbit - domain handles DOCKED → IN_ORBIT transition
                state_changed = ship.ensure_in_orbit()
                if state_changed:
                    # Call API to orbit ship
                    api_client.orbit_ship(ship.ship_symbol)

                    # Auto-sync: Fetch full ship state after orbit
                    # Orbit endpoint returns {data: {nav: {...}}} not full ship object
                    # So we need to fetch the complete ship state
                    ship = self._ship_repo.sync_from_api(
                        ship.ship_symbol,
                        route.player_id,
                        api_client,
                        graph_provider
                    )

            # Execute route segments
            for segment in route.segments:
                # Ensure ship is in orbit - domain handles DOCKED → IN_ORBIT transition
                state_changed = ship.ensure_in_orbit()
                if state_changed:
                    # Call API to orbit ship
                    api_client.orbit_ship(ship.ship_symbol)

                    # Auto-sync: Fetch full ship state after orbit
                    # Orbit endpoint returns {data: {nav: {...}}} not full ship object
                    # So we need to fetch the complete ship state
                    ship = self._ship_repo.sync_from_api(
                        ship.ship_symbol,
                        route.player_id,
                        api_client,
                        graph_provider
                    )

                # Set flight mode before navigation
                # This ensures the ship uses the mode planned by the routing engine
                api_client.set_flight_mode(ship.ship_symbol, segment.flight_mode.mode_name)

                # Navigate to destination
                nav_result = api_client.navigate_ship(
                    ship.ship_symbol,
                    segment.to_waypoint.symbol
                )

                # Auto-sync: Fetch full ship state after navigation
                # Navigate endpoint returns {data: {nav, fuel, events}} not full ship
                # So we need to fetch the complete ship state
                ship = self._ship_repo.sync_from_api(
                    ship.ship_symbol,
                    route.player_id,
                    api_client,
                    graph_provider
                )

                # Wait for arrival if ship is IN_TRANSIT
                # This prevents attempting to navigate to next segment while still in transit
                logger.debug(f"Ship status after navigation: {ship.nav_status}")
                if ship.nav_status == Ship.IN_TRANSIT:
                    # Extract arrival time from navigation result
                    nav_data = nav_result.get('data', {})
                    arrival_time_str = nav_data.get('nav', {}).get('route', {}).get('arrival')
                    logger.debug(f"Ship IN_TRANSIT, arrival time: {arrival_time_str}")
                    if arrival_time_str:
                        wait_time = calculate_arrival_wait_time(arrival_time_str)
                        logger.debug(f"Calculated wait time: {wait_time} seconds")

                        # Sleep for wait time + 3 second buffer to account for API delays
                        if wait_time > 0:
                            logger.info(f"Waiting {wait_time + 3} seconds for ship to arrive at {segment.to_waypoint.symbol}")
                            await asyncio.sleep(wait_time + 3)  # Use async sleep to avoid blocking event loop
                            logger.debug("Wait complete, ship should have arrived")
                        else:
                            logger.warning(f"Wait time is 0 or negative: {wait_time}, ship might already be at destination")

                        # Sync ship state again after arrival to verify ship is IN_ORBIT
                        ship = self._ship_repo.sync_from_api(
                            ship.ship_symbol,
                            route.player_id,
                            api_client,
                            graph_provider
                        )
                else:
                    logger.debug(f"Ship not IN_TRANSIT after navigation, status: {ship.nav_status}")

                # Only call arrive() if ship is still IN_TRANSIT
                # (after wait, ship may already be IN_ORBIT)
                if ship.nav_status == Ship.IN_TRANSIT:
                    ship.arrive()

                self._ship_repo.update(ship, from_api=True)

                # Handle refueling if required
                if segment.requires_refuel:
                    # Dock for refuel - domain handles IN_ORBIT → DOCKED transition
                    state_changed = ship.ensure_docked()
                    if state_changed:
                        # Call API to dock ship
                        api_client.dock_ship(ship.ship_symbol)

                        # Auto-sync: Fetch full ship state after dock
                        # Dock endpoint returns {data: {nav: {...}}} not full ship object
                        # So we need to fetch the complete ship state to ensure proper sync
                        ship = self._ship_repo.sync_from_api(
                            ship.ship_symbol,
                            route.player_id,
                            api_client,
                            graph_provider
                        )

                    # Refuel
                    refuel_result = api_client.refuel_ship(ship.ship_symbol)

                    # Auto-sync: Extract and convert ship data
                    ship_data = refuel_result.get('data', {}).get('ship')
                    if ship_data:
                        ship = convert_api_ship_to_entity(
                            ship_data,
                            route.player_id,
                            ship.current_location  # Refueling doesn't change location
                        )
                        self._ship_repo.update(ship, from_api=True)

                    # Return to orbit - domain handles DOCKED → IN_ORBIT transition
                    state_changed = ship.ensure_in_orbit()
                    if state_changed:
                        # Call API to orbit ship
                        api_client.orbit_ship(ship.ship_symbol)

                        # Auto-sync: Fetch full ship state after orbit
                        # Orbit endpoint returns {data: {nav: {...}}} not full ship object
                        # So we need to fetch the complete ship state
                        ship = self._ship_repo.sync_from_api(
                            ship.ship_symbol,
                            route.player_id,
                            api_client,
                            graph_provider
                        )

                # Complete segment
                route.complete_segment()

            return ship

        except Exception as e:
            # Mark route as failed
            route.fail_route(str(e))
            raise

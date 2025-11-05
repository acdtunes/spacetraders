from dataclasses import dataclass
from typing import List, Dict, Any
from pymediatr import Request, RequestHandler

from domain.navigation.route import Route, RouteSegment
from domain.shared.value_objects import Waypoint, FlightMode
from domain.shared.exceptions import ShipNotFoundError
from ports.outbound.repositories import IShipRepository
from ports.outbound.graph_provider import ISystemGraphProvider
from ports.routing_engine import IRoutingEngine


@dataclass(frozen=True)
class PlanRouteQuery(Request[Route]):
    """
    Query to plan a route from ship's current location to destination

    Returns a Route entity with optimized path segments
    Note: This is read-only - route is not persisted, just planned
    """
    ship_symbol: str
    destination_symbol: str
    player_id: int
    prefer_cruise: bool = True


class PlanRouteHandler(RequestHandler[PlanRouteQuery, Route]):
    """
    Handler for route planning using routing engine

    Process:
    1. Load ship from repository
    2. Get system graph for ship's system
    3. Use routing engine to find optimal path
    4. Convert path steps to RouteSegment entities
    5. Create and return Route aggregate (not persisted)
    """

    def __init__(
        self,
        ship_repository: IShipRepository,
        routing_engine: IRoutingEngine
    ):
        self._ship_repo = ship_repository
        self._routing_engine = routing_engine

    async def handle(self, request: PlanRouteQuery) -> Route:
        """
        Plan optimal route from ship's current location to destination

        Args:
            request: Route planning query

        Returns:
            Route entity with optimized path segments

        Raises:
            ShipNotFoundError: If ship doesn't exist
            ValueError: If destination not in same system or path not found
        """
        # 1. Get graph provider for this player (reads token from database)
        from configuration.container import get_graph_provider_for_player
        graph_provider = get_graph_provider_for_player(request.player_id)

        # 2. Load ship
        ship = self._ship_repo.find_by_symbol(request.ship_symbol, request.player_id)
        if not ship:
            raise ShipNotFoundError(
                f"Ship {request.ship_symbol} not found for player {request.player_id}"
            )

        # 3. Validate destination is in same system
        current_location = ship.current_location
        if not request.destination_symbol.startswith(current_location.system_symbol or ""):
            raise ValueError(
                f"Destination {request.destination_symbol} must be in same system "
                f"as ship's current location {current_location.symbol}"
            )

        # 4. Get system graph
        system_symbol = current_location.system_symbol
        graph_result = graph_provider.get_graph(system_symbol, force_refresh=False)
        waypoints_dict = graph_result.graph.get("waypoints", {})

        # Convert dict to Waypoint objects
        graph: Dict[str, Waypoint] = {}
        for symbol, data in waypoints_dict.items():
            graph[symbol] = Waypoint(
                symbol=data["symbol"],
                x=data["x"],
                y=data["y"],
                system_symbol=data.get("system_symbol"),
                waypoint_type=data.get("type"),
                traits=tuple(data.get("traits", [])),
                has_fuel=data.get("has_fuel", False),
                orbitals=tuple(data.get("orbitals", []))
            )

        # 4. Use routing engine to find optimal path
        path_result = self._routing_engine.find_optimal_path(
            graph=graph,
            start=current_location.symbol,
            goal=request.destination_symbol,
            current_fuel=ship.fuel.current,
            fuel_capacity=ship.fuel_capacity,
            engine_speed=ship.engine_speed,
            prefer_cruise=request.prefer_cruise
        )

        if not path_result:
            raise ValueError(
                f"No valid path found from {current_location.symbol} to "
                f"{request.destination_symbol} with current fuel constraints"
            )

        # 5. Convert path steps to RouteSegment entities
        segments = self._convert_steps_to_segments(path_result["steps"], graph)

        # 6. Create Route entity
        route_id = f"ROUTE-{request.ship_symbol}-{request.destination_symbol}"
        route = Route(
            route_id=route_id,
            ship_symbol=request.ship_symbol,
            player_id=request.player_id,
            segments=segments,
            ship_fuel_capacity=ship.fuel_capacity
        )

        return route

    def _convert_steps_to_segments(
        self,
        steps: List[Dict[str, Any]],
        graph: Dict[str, Waypoint]
    ) -> List[RouteSegment]:
        """
        Convert routing engine steps to RouteSegment value objects

        Args:
            steps: List of path steps from routing engine
            graph: Waypoint graph for lookups

        Returns:
            List of RouteSegment entities
        """
        segments: List[RouteSegment] = []

        for step in steps:
            if step["action"] == "TRAVEL":
                from_waypoint = graph[step["waypoint"]]

                # Find the 'to' waypoint from next step
                next_idx = steps.index(step) + 1
                if next_idx < len(steps):
                    # Look ahead to find next travel step
                    to_symbol = None
                    for future_step in steps[next_idx:]:
                        if future_step["action"] == "TRAVEL":
                            to_symbol = future_step["waypoint"]
                            break

                    if to_symbol:
                        to_waypoint = graph[to_symbol]

                        segment = RouteSegment(
                            from_waypoint=from_waypoint,
                            to_waypoint=to_waypoint,
                            distance=step.get("distance", 0.0),
                            fuel_required=step.get("fuel_cost", 0),
                            travel_time=step.get("time", 0),
                            flight_mode=step.get("mode", FlightMode.CRUISE),
                            requires_refuel=False  # Will be set based on next step
                        )

                        # Check if next step is REFUEL
                        if next_idx < len(steps) and steps[next_idx]["action"] == "REFUEL":
                            segment = RouteSegment(
                                from_waypoint=segment.from_waypoint,
                                to_waypoint=segment.to_waypoint,
                                distance=segment.distance,
                                fuel_required=segment.fuel_required,
                                travel_time=segment.travel_time,
                                flight_mode=segment.flight_mode,
                                requires_refuel=True
                            )

                        segments.append(segment)

        return segments

"""
gRPC service handler for routing operations.

Implements the RoutingService interface defined in routing.proto
"""
import logging
from typing import Dict

import sys
import os

# Add the generated proto files to the path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'generated'))

from generated import routing_pb2
from generated import routing_pb2_grpc

from utils.routing_engine import ORToolsRoutingEngine, Waypoint

logger = logging.getLogger(__name__)


class RoutingServiceHandler(routing_pb2_grpc.RoutingServiceServicer):
    """
    gRPC service implementation for routing operations.

    Delegates to ORToolsRoutingEngine for actual routing logic.
    """

    def __init__(self, tsp_timeout: int = 5, vrp_timeout: int = 30):
        """
        Initialize the routing service handler.

        Args:
            tsp_timeout: Timeout for TSP solver (seconds)
            vrp_timeout: Timeout for VRP solver (seconds)
        """
        self.engine = ORToolsRoutingEngine(tsp_timeout=tsp_timeout, vrp_timeout=vrp_timeout)
        logger.info(f"RoutingServiceHandler initialized (TSP timeout={tsp_timeout}s, VRP timeout={vrp_timeout}s)")

    def PlanRoute(self, request: routing_pb2.PlanRouteRequest, context) -> routing_pb2.PlanRouteResponse:
        """
        Plan a fuel-constrained route using Dijkstra pathfinding.

        Args:
            request: PlanRouteRequest with start, goal, fuel constraints, and waypoint graph
            context: gRPC context

        Returns:
            PlanRouteResponse with route steps, fuel cost, time, and distance
        """
        try:
            logger.info(f"PlanRoute: {request.start_waypoint} -> {request.goal_waypoint}")

            # Build waypoint graph
            graph = self._build_waypoint_graph(request.waypoints)

            # Find optimal path
            result = self.engine.find_optimal_path(
                graph=graph,
                start=request.start_waypoint,
                goal=request.goal_waypoint,
                current_fuel=request.current_fuel,
                fuel_capacity=request.fuel_capacity,
                engine_speed=request.engine_speed
            )

            if result is None:
                return routing_pb2.PlanRouteResponse(
                    success=False,
                    error_message=f"No path found from {request.start_waypoint} to {request.goal_waypoint}"
                )

            # Convert result to protobuf
            steps = []
            for step in result['steps']:
                action = routing_pb2.ROUTE_ACTION_TRAVEL if step['action'] == 'TRAVEL' else routing_pb2.ROUTE_ACTION_REFUEL

                # Debug: log mode extraction
                mode = step.get('mode', 'CRUISE')
                logger.info(f"PlanRoute step: action={step['action']}, waypoint={step['waypoint']}, mode_from_engine='{step.get('mode')}', final_mode='{mode}'")

                route_step = routing_pb2.RouteStep(
                    action=action,
                    waypoint=step['waypoint'],
                    fuel_cost=step['fuel_cost'],
                    time_seconds=step['time'],
                    distance=step.get('distance', 0.0),
                    mode=mode  # Include flight mode from routing engine
                )

                # Add refuel_amount if this is a refuel action
                if step['action'] == 'REFUEL' and 'refuel_amount' in step:
                    route_step.refuel_amount = step['refuel_amount']

                steps.append(route_step)

            return routing_pb2.PlanRouteResponse(
                steps=steps,
                total_fuel_cost=result['total_fuel_cost'],
                total_time_seconds=result['total_time'],
                total_distance=result['total_distance'],
                success=True
            )

        except Exception as e:
            logger.error(f"PlanRoute error: {e}", exc_info=True)
            return routing_pb2.PlanRouteResponse(
                success=False,
                error_message=str(e)
            )

    def OptimizeTour(self, request: routing_pb2.OptimizeTourRequest, context) -> routing_pb2.OptimizeTourResponse:
        """
        Optimize a multi-waypoint tour using TSP solver.

        Args:
            request: OptimizeTourRequest with waypoints to visit and constraints
            context: gRPC context

        Returns:
            OptimizeTourResponse with optimized visit order and route steps
        """
        try:
            logger.info(f"OptimizeTour: {len(request.target_waypoints)} waypoints from {request.start_waypoint}")

            # Build waypoint graph
            graph = self._build_waypoint_graph(request.all_waypoints)

            # Optimize tour (always returns to start by definition)
            result = self.engine.optimize_tour(
                graph=graph,
                waypoints=list(request.target_waypoints),
                start=request.start_waypoint,
                fuel_capacity=request.fuel_capacity,
                engine_speed=request.engine_speed
            )

            if result is None:
                return routing_pb2.OptimizeTourResponse(
                    success=False,
                    error_message="Failed to optimize tour"
                )

            # Convert legs to route steps
            route_steps = []
            for leg in result['legs']:
                # For each leg, we need to plan the actual route (may include refueling)
                leg_route = self.engine.find_optimal_path(
                    graph=graph,
                    start=leg['from'],
                    goal=leg['to'],
                    current_fuel=request.fuel_capacity,  # Assume full fuel at each leg
                    fuel_capacity=request.fuel_capacity,
                    engine_speed=request.engine_speed
                )

                if leg_route:
                    for step in leg_route['steps']:
                        action = routing_pb2.ROUTE_ACTION_TRAVEL if step['action'] == 'TRAVEL' else routing_pb2.ROUTE_ACTION_REFUEL

                        route_step = routing_pb2.RouteStep(
                            action=action,
                            waypoint=step['waypoint'],
                            fuel_cost=step['fuel_cost'],
                            time_seconds=step['time'],
                            distance=step.get('distance', 0.0)
                        )

                        if step['action'] == 'REFUEL' and 'refuel_amount' in step:
                            route_step.refuel_amount = step['refuel_amount']

                        route_steps.append(route_step)

            return routing_pb2.OptimizeTourResponse(
                visit_order=result['ordered_waypoints'],
                route_steps=route_steps,
                total_time_seconds=result['total_time'],
                total_distance=result['total_distance'],
                success=True
            )

        except Exception as e:
            logger.error(f"OptimizeTour error: {e}", exc_info=True)
            return routing_pb2.OptimizeTourResponse(
                success=False,
                error_message=str(e)
            )

    def PartitionFleet(self, request: routing_pb2.PartitionFleetRequest, context) -> routing_pb2.PartitionFleetResponse:
        """
        Partition markets across multiple ships using VRP solver.

        Args:
            request: PartitionFleetRequest with ships and markets to distribute
            context: gRPC context

        Returns:
            PartitionFleetResponse with ship assignments and tours
        """
        try:
            logger.info(f"PartitionFleet: {len(request.ship_symbols)} ships, {len(request.market_waypoints)} markets")

            # Build waypoint graph
            graph = self._build_waypoint_graph(request.all_waypoints)

            # Build ship locations dict
            ship_locations = {}
            for ship_symbol in request.ship_symbols:
                if ship_symbol in request.ship_configs:
                    ship_locations[ship_symbol] = request.ship_configs[ship_symbol].current_location
                else:
                    logger.warning(f"No config for ship {ship_symbol}")

            # Use first ship's config for fuel/speed (assumes homogeneous fleet)
            first_ship = request.ship_symbols[0]
            first_config = request.ship_configs[first_ship]
            fuel_capacity = first_config.fuel_capacity
            engine_speed = first_config.engine_speed

            # Partition fleet
            assignments = self.engine.optimize_fleet_tour(
                graph=graph,
                markets=list(request.market_waypoints),
                ship_locations=ship_locations,
                fuel_capacity=fuel_capacity,
                engine_speed=engine_speed
            )

            if assignments is None:
                return routing_pb2.PartitionFleetResponse(
                    success=False,
                    error_message="Failed to partition fleet"
                )

            # Convert to protobuf
            pb_assignments = {}
            total_assigned = 0
            ships_utilized = 0

            for ship_symbol, waypoints in assignments.items():
                if len(waypoints) > 0:
                    ships_utilized += 1
                    total_assigned += len(waypoints)

                    # Optimize tour for this ship (always returns to start)
                    ship_config = request.ship_configs[ship_symbol]
                    tour_result = self.engine.optimize_tour(
                        graph=graph,
                        waypoints=waypoints,
                        start=ship_config.current_location,
                        fuel_capacity=ship_config.fuel_capacity,
                        engine_speed=ship_config.engine_speed
                    )

                    if tour_result:
                        # Build route steps for this tour
                        route_steps = []
                        for leg in tour_result['legs']:
                            leg_route = self.engine.find_optimal_path(
                                graph=graph,
                                start=leg['from'],
                                goal=leg['to'],
                                current_fuel=ship_config.fuel_capacity,
                                fuel_capacity=ship_config.fuel_capacity,
                                engine_speed=ship_config.engine_speed
                            )

                            if leg_route:
                                for step in leg_route['steps']:
                                    action = routing_pb2.ROUTE_ACTION_TRAVEL if step['action'] == 'TRAVEL' else routing_pb2.ROUTE_ACTION_REFUEL

                                    route_step = routing_pb2.RouteStep(
                                        action=action,
                                        waypoint=step['waypoint'],
                                        fuel_cost=step['fuel_cost'],
                                        time_seconds=step['time'],
                                        distance=step.get('distance', 0.0)
                                    )

                                    if step['action'] == 'REFUEL' and 'refuel_amount' in step:
                                        route_step.refuel_amount = step['refuel_amount']

                                    route_steps.append(route_step)

                        pb_assignments[ship_symbol] = routing_pb2.ShipTour(
                            waypoints=tour_result['ordered_waypoints'],
                            route_steps=route_steps,
                            total_time_seconds=tour_result['total_time'],
                            total_distance=tour_result['total_distance'],
                            returns_to_start=True
                        )
                    else:
                        # Fallback: just return waypoints without route
                        pb_assignments[ship_symbol] = routing_pb2.ShipTour(
                            waypoints=waypoints,
                            route_steps=[],
                            total_time_seconds=0,
                            total_distance=0.0,
                            returns_to_start=True
                        )

            return routing_pb2.PartitionFleetResponse(
                assignments=pb_assignments,
                success=True,
                total_waypoints_assigned=total_assigned,
                ships_utilized=ships_utilized
            )

        except Exception as e:
            logger.error(f"PartitionFleet error: {e}", exc_info=True)
            return routing_pb2.PartitionFleetResponse(
                success=False,
                error_message=str(e)
            )

    def _build_waypoint_graph(self, waypoints_pb) -> Dict[str, Waypoint]:
        """
        Convert protobuf waypoints to internal graph representation.

        Args:
            waypoints_pb: List of routing_pb2.Waypoint messages

        Returns:
            Dict mapping waypoint symbol to Waypoint object
        """
        graph = {}

        for wp_pb in waypoints_pb:
            waypoint = Waypoint(
                symbol=wp_pb.symbol,
                x=wp_pb.x,
                y=wp_pb.y,
                has_fuel=wp_pb.has_fuel,
                fuel_price=wp_pb.fuel_price if wp_pb.HasField('fuel_price') else None,
                orbitals=()  # TODO: Add orbitals support if needed
            )
            graph[wp_pb.symbol] = waypoint

        return graph

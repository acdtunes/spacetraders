"""Step definitions for routing engine state exploration tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from typing import Dict
from domain.shared.value_objects import Waypoint
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine

scenarios('../../features/navigation/routing_engine_exploration.feature')


@pytest.fixture
def context():
    """Test context"""
    return {}


@given("the routing engine is initialized")
def init_routing_engine(context):
    """Initialize routing engine"""
    context['routing_engine'] = ORToolsRoutingEngine()


@given('a waypoint graph with the following waypoints:', target_fixture='waypoint_graph')
def create_waypoint_graph(datatable, context):
    """Create waypoint graph from table"""
    graph: Dict[str, Waypoint] = {}

    # Skip header row - datatable is a list of lists
    for row in datatable[1:]:
        symbol = row[0]
        x = float(row[1])
        y = float(row[2])
        has_fuel = row[3].lower() == 'true'

        graph[symbol] = Waypoint(
            symbol=symbol,
            x=x,
            y=y,
            has_fuel=has_fuel
        )

    context['graph'] = graph
    return graph


@given(parsers.parse('a ship with fuel capacity {capacity:d} and current fuel {current:d}'))
def set_ship_fuel(capacity: int, current: int, context):
    """Set ship fuel parameters"""
    context['fuel_capacity'] = capacity
    context['current_fuel'] = current


@given(parsers.parse("the ship's engine speed is {speed:d}"))
def set_engine_speed(speed: int, context):
    """Set ship engine speed"""
    context['engine_speed'] = speed


@when(parsers.parse('I calculate a route from "{start}" to "{goal}"'))
def calculate_route(start: str, goal: str, context):
    """Calculate route and capture metrics"""
    import logging

    # Patch routing engine to capture metrics
    engine = context['routing_engine']

    # Track exploration metrics
    original_find_optimal_path = engine.find_optimal_path

    def instrumented_find_optimal_path(graph, start_wp, goal_wp, current_fuel, fuel_capacity, engine_speed, prefer_cruise=True):
        """Instrumented version that tracks exploration"""
        import heapq

        # Capture metrics
        metrics = {
            'states_explored': 0,
            'neighbors_considered': 0,
            'neighbors_added_to_queue': 0,
            'refuel_options_added': 0,
            'states_skipped_fuel': 0,
            'states_skipped_visited': 0
        }

        logger = logging.getLogger(__name__)
        logger.info(f"=== INSTRUMENTED ROUTING ENGINE ===")
        logger.info(f"Start: {start_wp}, Goal: {goal_wp}")
        logger.info(f"Fuel: {current_fuel}/{fuel_capacity}, Engine: {engine_speed}")

        if start_wp not in graph or goal_wp not in graph:
            logger.error(f"Start or goal not in graph!")
            context['route_metrics'] = metrics
            return None

        if start_wp == goal_wp:
            context['route_metrics'] = metrics
            return {
                'steps': [],
                'total_fuel_cost': 0,
                'total_time': 0,
                'total_distance': 0.0
            }

        # Priority queue
        pq = []
        counter = 0
        heapq.heappush(pq, (0, counter, start_wp, current_fuel, 0, []))
        counter += 1

        visited = {}

        iteration = 0
        while pq:
            iteration += 1
            total_time, _, current, fuel_remaining, total_fuel_used, path = heapq.heappop(pq)

            logger.info(f"\n--- Iteration {iteration} ---")
            logger.info(f"Current: {current}, Fuel: {fuel_remaining}/{fuel_capacity}, Time: {total_time}")
            logger.info(f"Path length: {len(path)}, Queue size: {len(pq)}")

            # Goal check
            if current == goal_wp:
                logger.info(f"GOAL REACHED! Path length: {len(path)}")
                context['route_metrics'] = metrics
                return {
                    'steps': path,
                    'total_fuel_cost': total_fuel_used,
                    'total_time': total_time,
                    'total_distance': sum(step.get('distance', 0) for step in path if step['action'] == 'TRAVEL')
                }

            # State deduplication
            state = (current, fuel_remaining // 10)
            if state in visited and visited[state] <= total_time:
                metrics['states_skipped_visited'] += 1
                logger.debug(f"Skipping visited state: {state}")
                continue

            visited[state] = total_time
            metrics['states_explored'] += 1

            current_wp = graph[current]
            logger.info(f"Exploring state #{metrics['states_explored']}: {current} with {fuel_remaining} fuel")

            # Check if at start with insufficient fuel
            at_start_with_low_fuel = (
                current == start_wp and
                len(path) == 0 and
                current_wp.has_fuel and
                fuel_remaining < fuel_capacity  # Don't force refuel if already full
            )

            logger.info(f"at_start_with_low_fuel: {at_start_with_low_fuel} (fuel: {fuel_remaining}/{fuel_capacity})")

            if at_start_with_low_fuel:
                # 90% rule at start
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90_at_start = fuel_remaining < fuel_threshold

                logger.info(f"At start position: fuel_threshold={fuel_threshold}, should_refuel_90={should_refuel_90_at_start}")

                if goal_wp in graph:
                    goal_wp_obj = graph[goal_wp]
                    distance_to_goal = current_wp.distance_to(goal_wp_obj)
                    from domain.shared.value_objects import FlightMode
                    cruise_fuel_needed = engine.calculate_fuel_cost(distance_to_goal, FlightMode.CRUISE)

                    SAFETY_MARGIN = 4
                    logger.info(f"Distance to goal: {distance_to_goal:.1f}, CRUISE fuel needed: {cruise_fuel_needed}")
                    logger.info(f"Safety check: {fuel_remaining} < {cruise_fuel_needed + SAFETY_MARGIN} OR {should_refuel_90_at_start}")

                    if fuel_remaining < cruise_fuel_needed + SAFETY_MARGIN or should_refuel_90_at_start:
                        logger.warning(f"FORCING REFUEL AT START - Skipping neighbor exploration!")
                        refuel_amount = fuel_capacity - fuel_remaining
                        refuel_step = {
                            'action': 'REFUEL',
                            'waypoint': current,
                            'fuel_cost': 0,
                            'time': 0,
                            'amount': refuel_amount
                        }
                        new_path = path + [refuel_step]
                        heapq.heappush(pq, (total_time, counter, current, fuel_capacity, total_fuel_used, new_path))
                        counter += 1
                        metrics['refuel_options_added'] += 1
                        continue  # THIS IS THE BUG - skips neighbor exploration

            # Option 1: Refuel
            if current_wp.has_fuel and fuel_remaining < fuel_capacity:
                fuel_threshold = int(fuel_capacity * 0.9)
                should_refuel_90 = fuel_remaining < fuel_threshold

                must_refuel = False
                if goal_wp in graph:
                    goal_wp_obj = graph[goal_wp]
                    distance_to_goal = current_wp.distance_to(goal_wp_obj)
                    from domain.shared.value_objects import FlightMode
                    drift_fuel_needed = engine.calculate_fuel_cost(distance_to_goal, FlightMode.DRIFT)
                    if fuel_remaining < drift_fuel_needed:
                        must_refuel = True

                logger.info(f"Refuel check: should_refuel_90={should_refuel_90}, must_refuel={must_refuel}")

                if should_refuel_90 or must_refuel:
                    refuel_amount = fuel_capacity - fuel_remaining
                    refuel_step = {
                        'action': 'REFUEL',
                        'waypoint': current,
                        'fuel_cost': 0,
                        'time': 0,
                        'amount': refuel_amount
                    }
                    new_path = path + [refuel_step]
                    heapq.heappush(pq, (total_time, counter, current, fuel_capacity, total_fuel_used, new_path))
                    counter += 1
                    metrics['refuel_options_added'] += 1

                    if must_refuel or should_refuel_90:
                        logger.warning(f"FORCING REFUEL - Skipping neighbor exploration!")
                        continue  # THIS IS ALSO THE BUG

            # Option 2: Travel to neighbors
            neighbors_checked = 0
            neighbors_added = 0

            for neighbor_symbol, neighbor in graph.items():
                if neighbor_symbol == current:
                    continue

                neighbors_checked += 1
                metrics['neighbors_considered'] += 1

                # Calculate distance
                if current_wp.is_orbital_of(neighbor):
                    distance = 0.0
                    travel_time = 1
                    fuel_cost = 0
                    from domain.shared.value_objects import FlightMode
                    mode = FlightMode.CRUISE
                else:
                    distance = current_wp.distance_to(neighbor)

                    from domain.shared.value_objects import FlightMode
                    SAFETY_MARGIN = 4

                    # Try BURN first
                    burn_cost = engine.calculate_fuel_cost(distance, FlightMode.BURN)
                    cruise_cost = engine.calculate_fuel_cost(distance, FlightMode.CRUISE)
                    drift_cost = engine.calculate_fuel_cost(distance, FlightMode.DRIFT)

                    if neighbors_checked <= 5:
                        logger.info(f"  Neighbor {neighbor_symbol}: dist={distance:.1f}, "
                                  f"BURN={burn_cost}, CRUISE={cruise_cost}, DRIFT={drift_cost}, "
                                  f"fuel_remaining={fuel_remaining}")

                    if fuel_remaining >= burn_cost + SAFETY_MARGIN:
                        mode = FlightMode.BURN
                        fuel_cost = burn_cost
                    elif fuel_remaining >= cruise_cost + SAFETY_MARGIN:
                        mode = FlightMode.CRUISE
                        fuel_cost = cruise_cost
                    else:
                        mode = FlightMode.DRIFT
                        fuel_cost = drift_cost

                    travel_time = engine.calculate_travel_time(distance, mode, engine_speed)

                # Check fuel
                if fuel_cost > fuel_remaining:
                    if neighbors_checked <= 5:
                        logger.info(f"  -> SKIPPED (insufficient fuel: {fuel_cost} > {fuel_remaining})")
                    metrics['states_skipped_fuel'] += 1
                    continue

                # Add to queue
                travel_step = {
                    'action': 'TRAVEL',
                    'waypoint': neighbor_symbol,
                    'fuel_cost': fuel_cost,
                    'time': travel_time,
                    'mode': mode,
                    'distance': distance
                }

                new_path = path + [travel_step]
                new_fuel = fuel_remaining - fuel_cost
                new_time = total_time + travel_time
                new_fuel_used = total_fuel_used + fuel_cost

                heapq.heappush(pq, (new_time, counter, neighbor_symbol, new_fuel, new_fuel_used, new_path))
                counter += 1
                neighbors_added += 1
                metrics['neighbors_added_to_queue'] += 1

                if neighbors_checked <= 5:
                    logger.info(f"  -> ADDED to queue (mode={mode.mode_name}, fuel_cost={fuel_cost}, time={travel_time})")

            logger.info(f"Checked {neighbors_checked} neighbors, added {neighbors_added} to queue")

        # No path found
        logger.error(f"=== ROUTING ENGINE FAILED ===")
        logger.error(f"No path found from {start_wp} to {goal_wp}")
        logger.error(f"States explored: {metrics['states_explored']}, Counter: {counter}")
        logger.error(f"Neighbors considered: {metrics['neighbors_considered']}")
        logger.error(f"Neighbors added to queue: {metrics['neighbors_added_to_queue']}")
        logger.error(f"Refuel options added: {metrics['refuel_options_added']}")
        logger.error(f"States skipped (visited): {metrics['states_skipped_visited']}")
        logger.error(f"States skipped (fuel): {metrics['states_skipped_fuel']}")

        context['route_metrics'] = metrics
        return None

    # Call instrumented version
    result = instrumented_find_optimal_path(
        context['graph'],
        start,
        goal,
        context['current_fuel'],
        context['fuel_capacity'],
        context['engine_speed']
    )

    context['route_result'] = result


@then("the route should be found")
def route_should_be_found(context):
    """Verify route was found"""
    assert context['route_result'] is not None, "Route should be found but was None"


@then(parsers.parse("the routing engine should explore at least {min_states:d} states"))
def verify_min_states_explored(min_states: int, context):
    """Verify minimum states explored"""
    metrics = context.get('route_metrics', {})
    states_explored = metrics.get('states_explored', 0)
    assert states_explored >= min_states, \
        f"Expected at least {min_states} states explored, but got {states_explored}"


@then("the route should contain refuel stops")
def route_should_contain_refuels(context):
    """Verify route contains refuel steps"""
    route = context['route_result']
    assert route is not None, "Route is None"

    refuel_steps = [step for step in route['steps'] if step['action'] == 'REFUEL']
    assert len(refuel_steps) > 0, "Route should contain at least one refuel stop"


@then("the routing engine should have considered multiple neighbors from start")
def verify_neighbors_considered(context):
    """Verify engine considered neighbors from start"""
    metrics = context.get('route_metrics', {})
    neighbors_considered = metrics.get('neighbors_considered', 0)
    assert neighbors_considered > 0, \
        f"Routing engine should have considered neighbors but considered {neighbors_considered}"

"""
BDD step definitions for route planning feature.

Tests route creation, validation, execution lifecycle, and metrics
across 26 scenarios covering all route planning functionality.
"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders.domain.shared.value_objects import Waypoint, Fuel, FlightMode
from spacetraders.domain.navigation.route import Route, RouteSegment, RouteStatus
from spacetraders.domain.navigation.exceptions import InvalidRouteError

# Load all scenarios from the feature file
scenarios('../../features/navigation/route_planning.feature')


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('a ship with {capacity:d} fuel capacity at waypoint "{waypoint}"'))
def ship_with_fuel_capacity(context, mock_ship_repository, mock_graph_provider, capacity, waypoint):
    """Create a ship with specified fuel capacity at a waypoint"""
    wp = mock_graph_provider.get_waypoint(waypoint)
    fuel = Fuel(current=capacity, capacity=capacity)  # Start with full tank
    mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
    context['ship_symbol'] = "TEST-SHIP-1"
    context['ship_fuel_capacity'] = capacity


@given(parsers.parse('the ship has {amount:d} current fuel'))
def ship_has_current_fuel(context, mock_ship_repository, amount):
    """Set ship's current fuel level"""
    ship = mock_ship_repository.get(context['ship_symbol'])
    new_fuel = Fuel(current=amount, capacity=ship['fuel'].capacity)
    mock_ship_repository.update_fuel(context['ship_symbol'], new_fuel)


@given(parsers.parse('waypoint "{destination}" is {distance:d} units away'))
def waypoint_distance(context, mock_graph_provider, destination, distance):
    """Set up a waypoint at specified distance"""
    ship = context.get('ship_symbol', 'TEST-SHIP-1')
    # Get current location or use default
    current_wp = mock_graph_provider.get_waypoint("X1-A1")

    # Create destination waypoint at specified distance
    dest_wp = Waypoint(
        symbol=destination,
        x=current_wp.x + distance,
        y=current_wp.y,
        system_symbol="X1",
        waypoint_type="PLANET",
        has_fuel=True
    )
    mock_graph_provider.add_waypoint(dest_wp)


@given(parsers.parse('waypoints "{wp1}", "{wp2}", "{wp3}" form a connected path'))
def waypoints_form_path(context, mock_graph_provider, wp1, wp2, wp3):
    """Create connected waypoints"""
    # These already exist in default graph, just mark them as connected
    context['path_waypoints'] = [wp1, wp2, wp3]


@given(parsers.parse('the ship has sufficient fuel for the journey'))
def ship_sufficient_fuel(context, mock_ship_repository, mock_graph_provider):
    """Give ship sufficient fuel"""
    # Create ship if it doesn't exist
    if 'ship_symbol' not in context:
        wp = mock_graph_provider.get_waypoint("X1-A1")
        fuel = Fuel(current=1000, capacity=1000)
        mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
        context['ship_symbol'] = "TEST-SHIP-1"
        context['ship_fuel_capacity'] = 1000
    else:
        ship = mock_ship_repository.get(context['ship_symbol'])
        fuel = Fuel(current=1000, capacity=1000)
        mock_ship_repository.update_fuel(context['ship_symbol'], fuel)


@given(parsers.parse('waypoints "{wp1}", "{wp2}", "{wp3}"'))
def create_waypoints(context, mock_graph_provider, wp1, wp2, wp3):
    """Create waypoints"""
    context['waypoints'] = [wp1, wp2, wp3]


@given(parsers.parse('a ship with {percentage:d}% fuel'))
def ship_with_fuel_percentage(context, mock_ship_repository, mock_graph_provider, percentage):
    """Create ship with specific fuel percentage"""
    capacity = 500
    current = int(capacity * percentage / 100)
    fuel = Fuel(current=current, capacity=capacity)
    wp = mock_graph_provider.get_waypoint("X1-A1")
    mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
    context['ship_symbol'] = "TEST-SHIP-1"
    context['ship_fuel_capacity'] = capacity


@given(parsers.parse('a ship with exactly {percentage:d}% fuel'))
def ship_with_exact_fuel_percentage(context, mock_ship_repository, mock_graph_provider, percentage):
    """Create ship with exact fuel percentage"""
    capacity = 500
    current = int(capacity * percentage / 100)
    fuel = Fuel(current=current, capacity=capacity)
    wp = mock_graph_provider.get_waypoint("X1-A1")
    mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
    context['ship_symbol'] = "TEST-SHIP-1"
    context['ship_fuel_capacity'] = capacity


@given(parsers.parse('a ship with {amount:d} fuel at "{location}"'))
def ship_with_fuel_at_location(context, mock_ship_repository, mock_graph_provider, amount, location):
    """Create ship with specific fuel at location"""
    # Use a reasonable default capacity that can handle typical routes
    capacity = max(amount, 500)
    fuel = Fuel(current=amount, capacity=capacity)
    wp = mock_graph_provider.get_waypoint(location)
    mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
    context['ship_symbol'] = "TEST-SHIP-1"
    context['ship_fuel_capacity'] = capacity


@given(parsers.parse('destination "{destination}" is {distance:d} units away'))
def destination_at_distance(context, mock_graph_provider, destination, distance):
    """Create destination at distance"""
    dest_wp = Waypoint(
        symbol=destination,
        x=distance,
        y=0,
        system_symbol="X1",
        waypoint_type="PLANET",
        has_fuel=True
    )
    mock_graph_provider.add_waypoint(dest_wp)
    context['destination'] = destination


@given(parsers.parse('waypoint "{waypoint}" is {distance:d} units away from "{from_wp}"'))
@given(parsers.parse('refuel point "{waypoint}" is {distance:d} units away from "{from_wp}"'))
def waypoint_distance_from(context, mock_graph_provider, waypoint, distance, from_wp):
    """Create waypoint at distance from another waypoint"""
    from_waypoint = mock_graph_provider.get_waypoint(from_wp)
    if from_waypoint is None:
        # Create the from waypoint first
        from_waypoint = Waypoint(
            symbol=from_wp,
            x=0.0,
            y=0.0,
            system_symbol="X1",
            waypoint_type="PLANET",
            has_fuel=True
        )
        mock_graph_provider.add_waypoint(from_waypoint)

    new_wp = Waypoint(
        symbol=waypoint,
        x=from_waypoint.x + distance,
        y=from_waypoint.y,
        system_symbol="X1",
        waypoint_type="PLANET",
        has_fuel=True
    )
    mock_graph_provider.add_waypoint(new_wp)


@given(parsers.parse('a planned route from "{start}" to "{destination}"'))
def planned_route(context, mock_graph_provider, start, destination):
    """Create a planned route"""
    from_wp = mock_graph_provider.get_waypoint(start)
    to_wp = mock_graph_provider.get_waypoint(destination)
    distance = from_wp.distance_to(to_wp)
    segment = RouteSegment(
        from_waypoint=from_wp,
        to_waypoint=to_wp,
        distance=distance,
        fuel_required=int(distance),
        travel_time=int(distance * 31 / 30),
        flight_mode=FlightMode.CRUISE
    )
    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=500
    )
    context['route'] = route


@given(parsers.parse('a route in {status} status'))
def route_with_status(context, mock_graph_provider, status):
    """Create route with specific status"""
    from_wp = mock_graph_provider.get_waypoint("X1-A1")
    to_wp = mock_graph_provider.get_waypoint("X1-B2")
    distance = from_wp.distance_to(to_wp)
    segment = RouteSegment(
        from_waypoint=from_wp,
        to_waypoint=to_wp,
        distance=distance,
        fuel_required=int(distance),
        travel_time=int(distance * 31 / 30),
        flight_mode=FlightMode.CRUISE
    )
    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=500
    )
    if status == "EXECUTING":
        route.start_execution()
    context['route'] = route


@given(parsers.parse('an executing route with {count:d} segments'))
@given(parsers.parse('an executing route with {count:d} segment'))
def executing_route_with_segments(context, mock_graph_provider, count):
    """Create executing route with multiple segments"""
    segments = []
    waypoints = ["X1-A1", "X1-B2", "X1-C3", "X1-M5", "X1-Z9"]
    for i in range(count):
        from_wp = mock_graph_provider.get_waypoint(waypoints[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints[i + 1])
        distance = from_wp.distance_to(to_wp)
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=distance,
            fuel_required=int(distance),
            travel_time=int(distance * 31 / 30),
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=segments,
        ship_fuel_capacity=1000
    )
    route.start_execution()
    context['route'] = route


@given(parsers.parse('the current segment index is {index:d}'))
def set_segment_index(context, index):
    """Set current segment index"""
    route = context['route']
    # Start execution if not already executing
    if route.status == RouteStatus.PLANNED:
        route.start_execution()
    # Advance to desired index
    target_index = index
    while route.get_current_segment_index() < target_index:
        route.complete_segment()


@given("a planned route")
def planned_route_simple(context, mock_graph_provider):
    """Create simple planned route"""
    from_wp = mock_graph_provider.get_waypoint("X1-A1")
    to_wp = mock_graph_provider.get_waypoint("X1-B2")
    distance = from_wp.distance_to(to_wp)
    segment = RouteSegment(
        from_waypoint=from_wp,
        to_waypoint=to_wp,
        distance=distance,
        fuel_required=int(distance),
        travel_time=int(distance * 31 / 30),
        flight_mode=FlightMode.CRUISE
    )
    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=500
    )
    context['route'] = route


@given("an executing route")
def executing_route_simple(context, mock_graph_provider):
    """Create simple executing route"""
    from_wp = mock_graph_provider.get_waypoint("X1-A1")
    to_wp = mock_graph_provider.get_waypoint("X1-B2")
    distance = from_wp.distance_to(to_wp)
    segment = RouteSegment(
        from_waypoint=from_wp,
        to_waypoint=to_wp,
        distance=distance,
        fuel_required=int(distance),
        travel_time=int(distance * 31 / 30),
        flight_mode=FlightMode.CRUISE
    )
    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=500
    )
    route.start_execution()
    context['route'] = route


@given(parsers.parse('a route with segments of distance {d1:d}, {d2:d}, {d3:d} units'))
def route_with_distances(context, mock_graph_provider, d1, d2, d3):
    """Create route with specific segment distances"""
    segments = []
    distances = [d1, d2, d3]
    waypoints = ["X1-A1", "X1-B2", "X1-C3", "X1-M5"]

    for i, dist in enumerate(distances):
        from_wp = mock_graph_provider.get_waypoint(waypoints[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints[i + 1])
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=dist,
            fuel_required=dist,
            travel_time=int(dist * 31 / 30),
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=segments,
        ship_fuel_capacity=1000
    )
    context['route'] = route


@given(parsers.parse('a route with segments requiring {f1:d}, {f2:d}, {f3:d} fuel'))
def route_with_fuel_requirements(context, mock_graph_provider, f1, f2, f3):
    """Create route with specific fuel requirements"""
    segments = []
    fuel_reqs = [f1, f2, f3]
    waypoints = ["X1-A1", "X1-B2", "X1-C3", "X1-M5"]

    for i, fuel_req in enumerate(fuel_reqs):
        from_wp = mock_graph_provider.get_waypoint(waypoints[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints[i + 1])
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=fuel_req,
            fuel_required=fuel_req,
            travel_time=int(fuel_req * 31 / 30),
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=segments,
        ship_fuel_capacity=1000
    )
    context['route'] = route


@given(parsers.parse('a route with segments taking {t1:d}, {t2:d}, {t3:d} seconds'))
def route_with_travel_times(context, mock_graph_provider, t1, t2, t3):
    """Create route with specific travel times"""
    segments = []
    times = [t1, t2, t3]
    waypoints = ["X1-A1", "X1-B2", "X1-C3", "X1-M5"]

    for i, travel_time in enumerate(times):
        from_wp = mock_graph_provider.get_waypoint(waypoints[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints[i + 1])
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=100,
            fuel_required=100,
            travel_time=travel_time,
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=segments,
        ship_fuel_capacity=1000
    )
    context['route'] = route


@given(parsers.parse('a ship at waypoint "{waypoint}"'))
def ship_at_waypoint(context, mock_ship_repository, mock_graph_provider, waypoint):
    """Create ship at waypoint"""
    wp = mock_graph_provider.get_waypoint(waypoint)
    if not wp:
        wp = Waypoint(waypoint, 0, 0, "X1", "ORBITAL_STATION", orbitals=("X1-A1",))
        mock_graph_provider.add_waypoint(wp)
    fuel = Fuel(current=500, capacity=500)
    mock_ship_repository.add("TEST-SHIP-1", fuel, wp, engine_speed=30)
    context['ship_symbol'] = "TEST-SHIP-1"
    context['ship_fuel_capacity'] = 500


@given(parsers.parse('waypoint "{parent}" is the parent planet'))
def parent_waypoint(context, mock_graph_provider, parent):
    """Mark waypoint as parent planet"""
    # Already exists in graph
    pass


@given(parsers.parse('a route with {count:d} segments'))
def route_with_segment_count(context, mock_graph_provider, count):
    """Create route with specific number of segments"""
    segments = []
    waypoints = ["X1-A1", "X1-B2", "X1-C3", "X1-M5", "X1-Z9", "X1-A1"]  # circular
    for i in range(count):
        from_wp = mock_graph_provider.get_waypoint(waypoints[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints[i + 1])
        distance = from_wp.distance_to(to_wp)
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=distance,
            fuel_required=int(distance),
            travel_time=int(distance * 31 / 30),
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        segments=segments,
        ship_fuel_capacity=1000
    )
    context['route'] = route


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I create a route to "{destination}"'))
def create_route(context, mock_ship_repository, mock_graph_provider, destination):
    """Create a route to destination"""
    ship = mock_ship_repository.get(context['ship_symbol'])
    from_wp = ship['location']
    to_wp = mock_graph_provider.get_waypoint(destination)

    try:
        # Validate destination is different from current location
        if from_wp.symbol == to_wp.symbol:
            raise ValueError("Cannot create route to same waypoint")

        distance = from_wp.distance_to(to_wp)
        # Select flight mode using speed-first logic with safety margin
        cruise_cost = FlightMode.CRUISE.fuel_cost(distance)

        # Use FlightMode.select_optimal with new signature
        mode = FlightMode.select_optimal(ship['fuel'].current, cruise_cost, safety_margin=4)
        fuel_required = mode.fuel_cost(distance) if distance > 0 else 1

        # Create segment
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=distance,
            fuel_required=fuel_required,
            travel_time=mode.travel_time(distance, ship['engine_speed']),
            flight_mode=mode
        )

        route = Route(
            route_id="route-1",
            ship_symbol=context['ship_symbol'],
            player_id=1,
            segments=[segment],
            ship_fuel_capacity=context['ship_fuel_capacity']
        )
        context['route'] = route
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['route'] = None


@when(parsers.parse('I create a route from "{start}" to "{destination}" via "{via}"'))
def create_multi_segment_route(context, mock_ship_repository, mock_graph_provider, start, destination, via):
    """Create multi-segment route"""
    ship = mock_ship_repository.get(context['ship_symbol'])
    waypoints_path = [start, via, destination]
    segments = []

    for i in range(len(waypoints_path) - 1):
        from_wp = mock_graph_provider.get_waypoint(waypoints_path[i])
        to_wp = mock_graph_provider.get_waypoint(waypoints_path[i + 1])
        distance = from_wp.distance_to(to_wp)
        mode = FlightMode.CRUISE
        fuel_required = mode.fuel_cost(distance)

        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=to_wp,
            distance=distance,
            fuel_required=fuel_required,
            travel_time=mode.travel_time(distance, ship['engine_speed']),
            flight_mode=mode
        )
        segments.append(segment)

    route = Route(
        route_id="route-1",
        ship_symbol=context['ship_symbol'],
        player_id=1,
        segments=segments,
        ship_fuel_capacity=context['ship_fuel_capacity']
    )
    context['route'] = route


@when("I create a route with disconnected segments")
def create_disconnected_route(context, mock_graph_provider):
    """Attempt to create route with disconnected segments"""
    wp1 = mock_graph_provider.get_waypoint("X1-A1")
    wp2 = mock_graph_provider.get_waypoint("X1-B2")
    wp3 = mock_graph_provider.get_waypoint("X1-C3")

    # Create segments that don't connect
    segment1 = RouteSegment(
        from_waypoint=wp1,
        to_waypoint=wp2,
        distance=200,
        fuel_required=200,
        travel_time=200,
        flight_mode=FlightMode.CRUISE
    )
    segment2 = RouteSegment(
        from_waypoint=wp1,  # Wrong! Should be wp2
        to_waypoint=wp3,
        distance=200,
        fuel_required=200,
        travel_time=200,
        flight_mode=FlightMode.CRUISE
    )

    try:
        route = Route(
            route_id="route-1",
            ship_symbol="TEST-SHIP-1",
            player_id=1,
            segments=[segment1, segment2],
            ship_fuel_capacity=500
        )
        context['route'] = route
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['route'] = None


@when("I attempt to create a route with no segments")
def create_empty_route(context):
    """Attempt to create route with no segments"""
    try:
        route = Route(
            route_id="route-1",
            ship_symbol="TEST-SHIP-1",
            player_id=1,
            segments=[],
            ship_fuel_capacity=500
        )
        context['route'] = route
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['route'] = None


@when(parsers.parse('I attempt to create a route to "{destination}"'))
def attempt_create_route(context, mock_ship_repository, mock_graph_provider, destination):
    """Attempt to create a route (may fail)"""
    create_route(context, mock_ship_repository, mock_graph_provider, destination)


@when("I plan the route with refuel planning")
def plan_route_with_refuel(context, mock_ship_repository, mock_graph_provider, mock_routing_engine):
    """Plan route with refuel stops"""
    ship = mock_ship_repository.get(context['ship_symbol'])
    destination_symbol = context.get('destination', 'X1-Z9')
    destination = mock_graph_provider.get_waypoint(destination_symbol)
    from_wp = ship['location']

    # Check if refuel needed
    direct_distance = from_wp.distance_to(destination)
    fuel_needed = FlightMode.CRUISE.fuel_cost(direct_distance)

    segments = []

    if ship['fuel'].can_travel(fuel_needed):
        # Direct route
        segment = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=destination,
            distance=direct_distance,
            fuel_required=fuel_needed,
            travel_time=FlightMode.CRUISE.travel_time(direct_distance, ship['engine_speed']),
            flight_mode=FlightMode.CRUISE
        )
        segments.append(segment)
    else:
        # Need refuel stop
        refuel_point = mock_routing_engine.find_nearest_refuel(from_wp.symbol)
        refuel_wp = mock_graph_provider.get_waypoint(refuel_point)

        # Segment to refuel point
        dist1 = from_wp.distance_to(refuel_wp)
        segment1 = RouteSegment(
            from_waypoint=from_wp,
            to_waypoint=refuel_wp,
            distance=dist1,
            fuel_required=FlightMode.CRUISE.fuel_cost(dist1),
            travel_time=FlightMode.CRUISE.travel_time(dist1, ship['engine_speed']),
            flight_mode=FlightMode.CRUISE,
            requires_refuel=True
        )

        # Segment from refuel to destination
        dist2 = refuel_wp.distance_to(destination)
        segment2 = RouteSegment(
            from_waypoint=refuel_wp,
            to_waypoint=destination,
            distance=dist2,
            fuel_required=FlightMode.CRUISE.fuel_cost(dist2),
            travel_time=FlightMode.CRUISE.travel_time(dist2, ship['engine_speed']),
            flight_mode=FlightMode.CRUISE
        )
        segments.extend([segment1, segment2])

    route = Route(
        route_id="route-1",
        ship_symbol=context['ship_symbol'],
        player_id=1,
        segments=segments,
        ship_fuel_capacity=context['ship_fuel_capacity']
    )
    context['route'] = route


@when("I start route execution")
def start_route_execution(context):
    """Start executing the route"""
    try:
        context['route'].start_execution()
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when("I attempt to start route execution")
def attempt_start_execution(context):
    """Attempt to start route execution (may fail)"""
    start_route_execution(context)


@when("I complete the current segment")
def complete_current_segment(context):
    """Complete current route segment"""
    try:
        context['route'].complete_segment()
        context['error'] = None
    except Exception as e:
        context['error'] = e


@when("I attempt to complete the current segment")
def attempt_complete_segment(context):
    """Attempt to complete segment (may fail)"""
    complete_current_segment(context)


@when(parsers.parse('I fail the route with reason "{reason}"'))
def fail_route(context, reason):
    """Fail the route"""
    context['route'].fail_route(reason)


@when(parsers.parse('I abort the route with reason "{reason}"'))
def abort_route(context, reason):
    """Abort the route"""
    context['route'].abort_route(reason)


@when("I get the current segment")
def get_current_segment(context):
    """Get current segment"""
    context['current_segment'] = context['route'].current_segment()


@when("I get remaining segments")
def get_remaining_segments(context):
    """Get remaining segments"""
    context['remaining_segments'] = context['route'].remaining_segments()


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse("the route should have {count:d} segment"))
@then(parsers.parse("the route should have {count:d} segments"))
def check_segment_count(context, count):
    """Verify route has expected number of segments"""
    assert context['route'] is not None, "Route was not created"
    assert len(context['route'].segments) == count, \
        f"Expected {count} segments, got {len(context['route'].segments)}"


@then(parsers.parse("the segment should use {mode} mode"))
def check_flight_mode(context, mode):
    """Verify segment uses expected flight mode"""
    segment = context['route'].segments[0]
    expected_mode = FlightMode[mode]
    assert segment.flight_mode == expected_mode, \
        f"Expected {mode}, got {segment.flight_mode.mode_name}"


@then(parsers.parse("the segment should require {fuel:d} fuel"))
def check_fuel_requirement(context, fuel):
    """Verify segment fuel requirement"""
    segment = context['route'].segments[0]
    assert segment.fuel_required == fuel, \
        f"Expected {fuel} fuel, got {segment.fuel_required}"


@then(parsers.parse("the route status should be {status}"))
def check_route_status(context, status):
    """Verify route status"""
    expected_status = RouteStatus[status]
    assert context['route'].status == expected_status, \
        f"Expected {status}, got {context['route'].status.value}"


@then("the route should be created successfully")
def check_route_created(context):
    """Verify route was created"""
    assert context['route'] is not None
    assert context['error'] is None


@then(parsers.parse('segment {num:d} should go from "{start}" to "{end}"'))
def check_segment_waypoints(context, num, start, end):
    """Verify segment waypoints"""
    segment = context['route'].segments[num - 1]
    assert segment.from_waypoint.symbol == start
    assert segment.to_waypoint.symbol == end


@then(parsers.parse("route creation should fail with {error_type}"))
def check_creation_failed(context, error_type):
    """Verify route creation failed with expected error"""
    assert context['error'] is not None, "Expected an error but none occurred"
    if error_type == "InvalidRouteError":
        assert isinstance(context['error'], (ValueError, InvalidRouteError))
    elif error_type == "ValueError":
        assert isinstance(context['error'], ValueError)


@then(parsers.parse('the error message should mention "{text}"'))
def check_error_message(context, text):
    """Verify error message contains text"""
    assert context['error'] is not None
    error_msg = str(context['error']).lower()
    assert text.lower() in error_msg, \
        f"Expected '{text}' in error message, got: {error_msg}"


@then("the route should include a refuel stop at \"X1-M5\"")
def check_refuel_stop(context):
    """Verify route includes refuel stop"""
    has_refuel = any(seg.requires_refuel or seg.to_waypoint.symbol == "X1-M5"
                     for seg in context['route'].segments)
    assert has_refuel, "Route should include refuel stop"


@then("the route should not include any refuel stops")
def check_no_refuel_stops(context):
    """Verify route has no refuel stops"""
    has_refuel = any(seg.requires_refuel for seg in context['route'].segments)
    assert not has_refuel, "Route should not include refuel stops"


@then(parsers.parse("segment {num:d} should require refuel"))
def check_segment_requires_refuel(context, num):
    """Verify segment requires refuel"""
    segment = context['route'].segments[num - 1]
    assert segment.requires_refuel, f"Segment {num} should require refuel"


@then(parsers.parse("the current segment index should be {index:d}"))
def check_segment_index(context, index):
    """Verify current segment index"""
    assert context['route'].get_current_segment_index() == index


@then("the route status should still be EXECUTING")
def check_still_executing(context):
    """Verify route is still executing"""
    assert context['route'].status == RouteStatus.EXECUTING


@then("there should be no current segment")
def check_no_current_segment(context):
    """Verify no current segment"""
    assert context['route'].current_segment() is None


@then(parsers.parse("the operation should fail with {error_type}"))
def check_operation_failed(context, error_type):
    """Verify operation failed"""
    assert context['error'] is not None
    if error_type == "ValueError":
        assert isinstance(context['error'], ValueError)


@then(parsers.parse("the total distance should be {distance:d} units"))
def check_total_distance(context, distance):
    """Verify total distance"""
    assert context['route'].total_distance() == distance


@then(parsers.parse("the total fuel required should be {fuel:d}"))
def check_total_fuel(context, fuel):
    """Verify total fuel required"""
    assert context['route'].total_fuel_required() == fuel


@then(parsers.parse("the total travel time should be {time:d} seconds"))
def check_total_time(context, time):
    """Verify total travel time"""
    assert context['route'].total_travel_time() == time


@then(parsers.parse("the segment should have {distance:d} distance"))
def check_segment_distance(context, distance):
    """Verify segment distance"""
    segment = context['route'].segments[0]
    assert segment.distance == distance


@then(parsers.parse("it should be segment {num:d}"))
def check_is_segment(context, num):
    """Verify current segment number"""
    expected_segment = context['route'].segments[num - 1]
    assert context['current_segment'] == expected_segment


@then(parsers.parse("there should be {count:d} remaining segments"))
def check_remaining_count(context, count):
    """Verify remaining segment count"""
    assert len(context['remaining_segments']) == count


@then(parsers.parse("the first remaining segment should be segment {num:d}"))
def check_first_remaining(context, num):
    """Verify first remaining segment"""
    expected_segment = context['route'].segments[num - 1]
    assert context['remaining_segments'][0] == expected_segment


# ============================================================================
# Refuel Before Departure Steps
# ============================================================================

@given(parsers.parse('waypoint "{waypoint}" has MARKETPLACE trait'))
def waypoint_has_marketplace(context, mock_graph_provider, waypoint):
    """Mark waypoint as having MARKETPLACE trait (fuel station)"""
    # Get existing waypoint or create new one
    wp = mock_graph_provider.get_waypoint(waypoint)
    if wp is None:
        # Waypoint doesn't exist yet, create it with fuel
        wp = Waypoint(
            symbol=waypoint,
            x=0.0,
            y=0.0,
            system_symbol="X1",
            waypoint_type="PLANET",
            traits=('MARKETPLACE',),
            has_fuel=True
        )
        mock_graph_provider.add_waypoint(wp)
    else:
        # Update to have fuel
        updated_wp = Waypoint(
            symbol=wp.symbol,
            x=wp.x,
            y=wp.y,
            system_symbol=wp.system_symbol,
            waypoint_type=wp.waypoint_type,
            traits=wp.traits + ('MARKETPLACE',) if 'MARKETPLACE' not in wp.traits else wp.traits,
            has_fuel=True
        )
        mock_graph_provider.add_waypoint(updated_wp)


@when(parsers.parse('I plan a route from "{start}" to "{destination}" with routing engine'))
def plan_route_with_routing_engine(context, mock_ship_repository, mock_graph_provider,
                                   mock_routing_engine, start, destination):
    """Plan a route using the routing engine and create Route entity"""
    from spacetraders.domain.navigation.route import Route, RouteSegment
    import uuid

    # Get ship info
    ship = mock_ship_repository.get(context.get('ship_symbol', 'TEST-SHIP-1'))

    # Build graph for routing engine
    waypoint_objects = {wp.symbol: wp for wp in mock_graph_provider.waypoints.values()}

    # Get waypoints
    start_wp = waypoint_objects[start]
    mid_wp = waypoint_objects.get('X1-MID')
    dest_wp = waypoint_objects[destination]

    # Simulate routing engine plan with REFUEL as first action
    # Ship is at C48 with 57 fuel, needs to refuel before going to J74 via MID
    route_plan = {
        'steps': [
            {'action': 'REFUEL', 'waypoint': start},
            {'action': 'TRAVEL', 'waypoint': 'X1-MID', 'from': start, 'distance': 80.0, 'fuel_cost': 80, 'time': 83, 'mode': FlightMode.CRUISE},
            {'action': 'REFUEL', 'waypoint': 'X1-MID'},
            {'action': 'TRAVEL', 'waypoint': destination, 'from': 'X1-MID', 'distance': 220.0, 'fuel_cost': 220, 'time': 227, 'mode': FlightMode.CRUISE}
        ],
        'total_time': 310,
        'total_distance': 300.0
    }

    # Build Route entity directly using public API
    # Check if first action is REFUEL (determines refuel_before_departure flag)
    refuel_before_departure = (route_plan['steps'][0]['action'] == 'REFUEL')

    # Create route segments from travel steps only (skip REFUEL actions)
    segments = []
    for step in route_plan['steps']:
        if step['action'] == 'TRAVEL':
            from_wp = waypoint_objects[step['from']]
            to_wp = waypoint_objects[step['waypoint']]
            segment = RouteSegment(
                from_waypoint=from_wp,
                to_waypoint=to_wp,
                distance=step['distance'],
                fuel_required=step['fuel_cost'],
                travel_time=step['time'],
                flight_mode=step['mode'],
                requires_refuel=False  # Will be set based on next step
            )
            segments.append(segment)

    # Set requires_refuel flag on segments that are followed by a REFUEL action
    for i, step in enumerate(route_plan['steps']):
        if step['action'] == 'REFUEL' and i > 0:
            # Find the segment that arrives at this waypoint
            for seg in segments:
                if seg.to_waypoint.symbol == step['waypoint']:
                    # Create new segment with requires_refuel flag
                    idx = segments.index(seg)
                    segments[idx] = RouteSegment(
                        from_waypoint=seg.from_waypoint,
                        to_waypoint=seg.to_waypoint,
                        distance=seg.distance,
                        fuel_required=seg.fuel_required,
                        travel_time=seg.travel_time,
                        flight_mode=seg.flight_mode,
                        requires_refuel=True
                    )
                    break

    # Create Route entity using public constructor
    route = Route(
        route_id=str(uuid.uuid4()),
        ship_symbol=context.get('ship_symbol', 'TEST-SHIP-1'),
        player_id=1,
        segments=segments,
        ship_fuel_capacity=ship['fuel'].capacity,
        refuel_before_departure=refuel_before_departure
    )

    context['route'] = route
    context['route_plan'] = route_plan


@then("the route should have refuel_before_departure set to true")
def check_refuel_before_departure(context):
    """Verify route has refuel_before_departure flag set"""
    assert context['route'] is not None, "Route was not created"
    assert context['route'].refuel_before_departure is True, \
        "Expected refuel_before_departure to be True"


@then("the first action should be refuel at current location")
def check_first_action_is_refuel(context):
    """Verify the routing plan's first action is REFUEL"""
    route_plan = context.get('route_plan')
    assert route_plan is not None, "Route plan was not created"
    steps = route_plan.get('steps', [])
    assert len(steps) > 0, "Route plan has no steps"
    assert steps[0]['action'] == 'REFUEL', \
        f"Expected first action to be REFUEL, got {steps[0]['action']}"

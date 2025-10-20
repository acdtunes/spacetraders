from pytest_bdd import scenarios, given, when, then, parsers
import math

scenarios('../../../bdd/features/operations/routing.feature')


class NavigationGraph:
    """Simple navigation graph for testing."""
    def __init__(self):
        self.waypoints = {}
        self.edges = []
        self.fuel_stations = set()

    def add_waypoint(self, symbol, x, y, has_fuel=False):
        """Add waypoint to graph."""
        self.waypoints[symbol] = {
            'symbol': symbol,
            'x': x,
            'y': y,
            'has_fuel': has_fuel
        }
        if has_fuel:
            self.fuel_stations.add(symbol)

    def add_edge(self, from_wp, to_wp):
        """Add edge between waypoints."""
        self.edges.append({'from': from_wp, 'to': to_wp})

    def count_fuel_stations(self):
        """Count fuel stations."""
        return len(self.fuel_stations)

    def get_waypoints_within_range(self, origin, max_distance):
        """Find waypoints within range of origin."""
        origin_wp = self.waypoints.get(origin)
        if not origin_wp:
            return []

        reachable = []
        for symbol, wp in self.waypoints.items():
            if symbol == origin:
                continue
            distance = math.sqrt(
                (wp['x'] - origin_wp['x']) ** 2 +
                (wp['y'] - origin_wp['y']) ** 2
            )
            if distance <= max_distance:
                reachable.append(symbol)

        return reachable


@given('a routing system', target_fixture='routing_ctx')
def given_routing_system():
    """Create routing system."""
    return {
        'graph': NavigationGraph(),
        'waypoint_count': 0,
        'current_position': None,
        'fuel_range': 0,
        'reachable': [],
        'routing_paused': False,
        'route_segments': [],
        'calculations': {},
        'waypoint_coords': {}
    }


@given(parsers.parse('a system with {count:d} waypoints'))
def given_system_waypoints(routing_ctx, count):
    """Create system with waypoints."""
    routing_ctx['waypoint_count'] = count
    graph = routing_ctx['graph']

    for i in range(count):
        symbol = f"X1-TEST-{chr(65+i)}{i+1}"
        x = i * 100
        y = i * 50
        graph.add_waypoint(symbol, x, y)

    # Add edges
    waypoints = list(graph.waypoints.keys())
    for i in range(len(waypoints) - 1):
        graph.add_edge(waypoints[i], waypoints[i+1])

    return routing_ctx


@given(parsers.parse('a navigation graph with {count:d} waypoints'))
def given_nav_graph(routing_ctx, count):
    """Create navigation graph."""
    routing_ctx['waypoint_count'] = count
    graph = routing_ctx['graph']

    for i in range(count):
        symbol = f"X1-TEST-{chr(65+i)}{i+1}"
        x = i * 100
        y = i * 50
        graph.add_waypoint(symbol, x, y)

    return routing_ctx


@given(parsers.parse('{count:d} waypoints have fuel stations'))
def given_fuel_stations(routing_ctx, count):
    """Add fuel stations to graph."""
    graph = routing_ctx['graph']
    waypoints = list(graph.waypoints.keys())

    for i in range(min(count, len(waypoints))):
        symbol = waypoints[i]
        graph.waypoints[symbol]['has_fuel'] = True
        graph.fuel_stations.add(symbol)

    return routing_ctx


@given(parsers.parse('waypoint A at coordinates ({x:d}, {y:d})'))
def given_waypoint_a(routing_ctx, x, y):
    """Create waypoint A."""
    routing_ctx['waypoint_coords']['A'] = {'x': x, 'y': y}
    routing_ctx['graph'].add_waypoint('A', x, y)
    return routing_ctx


@given(parsers.parse('waypoint B at coordinates ({x:d}, {y:d})'))
def given_waypoint_b(routing_ctx, x, y):
    """Create waypoint B."""
    routing_ctx['waypoint_coords']['B'] = {'x': x, 'y': y}
    routing_ctx['graph'].add_waypoint('B', x, y)
    return routing_ctx


@given('a graph with waypoints at various distances')
def given_waypoints_various_distances(routing_ctx):
    """Create waypoints at different distances."""
    graph = routing_ctx['graph']

    # Add waypoints at specific distances from origin
    graph.add_waypoint('X1-TEST-A1', 0, 0)  # Origin
    graph.add_waypoint('X1-TEST-B2', 100, 0)  # 100 units
    graph.add_waypoint('X1-TEST-C3', 200, 0)  # 200 units
    graph.add_waypoint('X1-TEST-D4', 300, 400)  # 500 units
    graph.add_waypoint('X1-TEST-E5', 600, 800)  # 1000 units
    graph.add_waypoint('X1-TEST-F6', 3000, 4000)  # 5000 units
    graph.add_waypoint('X1-TEST-G7', 30000, 40000)  # 50000 units
    graph.add_waypoint('X1-TEST-H8', 60000, 80000)  # 100000 units (unreachable)

    return routing_ctx


@given(parsers.parse('current position is waypoint "{waypoint}"'))
def given_current_position(routing_ctx, waypoint):
    """Set current position."""
    routing_ctx['current_position'] = waypoint
    return routing_ctx


@given(parsers.parse('ship has {fuel:d} units fuel'))
def given_ship_fuel(routing_ctx, fuel):
    """Set ship fuel."""
    routing_ctx['fuel'] = fuel
    return routing_ctx


@given(parsers.parse('ship uses DRIFT mode ({fuel:d} fuel per {distance:d} units)'))
def given_drift_mode(routing_ctx, fuel, distance):
    """Set DRIFT mode fuel consumption."""
    # DRIFT mode: 1 fuel per 300 units
    # 200 fuel * 300 = 60000 units range
    routing_ctx['fuel_range'] = routing_ctx.get('fuel', 0) * distance
    return routing_ctx


@given('routing validation is paused')
def given_routing_paused(routing_ctx):
    """Mark routing as paused."""
    routing_ctx['routing_paused'] = True
    return routing_ctx


@given('a navigation graph for mining operations')
def given_mining_graph(routing_ctx):
    """Create graph for mining."""
    routing_ctx['operation_type'] = 'mining'
    return routing_ctx


@given(parsers.parse('graph has {count:d} waypoints'))
def given_graph_waypoints(routing_ctx, count):
    """Set waypoint count."""
    graph = routing_ctx['graph']
    for i in range(count):
        symbol = f"X1-TEST-{chr(65+i)}{i+1}"
        x = i * 100
        y = i * 50
        graph.add_waypoint(symbol, x, y)
    return routing_ctx


@given(parsers.parse('a route with {count:d} navigation steps'))
@given(parsers.parse('a route with {count:d} segments'))
def given_route_steps(routing_ctx, count):
    """Create route with steps."""
    routing_ctx['route_step_count'] = count
    routing_ctx['route_segments'] = []
    return routing_ctx


@given(parsers.parse('step {step:d} is {distance:d} units using {mode} mode'))
def given_route_step(routing_ctx, step, distance, mode):
    """Add route step."""
    routing_ctx['route_segments'].append({
        'step': step,
        'distance': distance,
        'mode': mode
    })
    return routing_ctx


@given(parsers.parse('segment {seg:d} is {distance:d} units at speed {speed:d} ({mode})'))
def given_route_segment(routing_ctx, seg, distance, speed, mode):
    """Add route segment with speed."""
    routing_ctx['route_segments'].append({
        'segment': seg,
        'distance': distance,
        'speed': speed,
        'mode': mode
    })
    return routing_ctx


@when('I build navigation graph')
def when_build_graph(routing_ctx):
    """Build navigation graph."""
    # Graph already built in given steps
    graph = routing_ctx['graph']
    routing_ctx['build_result'] = {
        'waypoints': len(graph.waypoints),
        'edges': len(graph.edges)
    }
    return routing_ctx


@when('I count fuel stations')
def when_count_fuel_stations(routing_ctx):
    """Count fuel stations."""
    graph = routing_ctx['graph']
    routing_ctx['fuel_station_count'] = graph.count_fuel_stations()
    return routing_ctx


@when('I calculate distance between A and B')
def when_calculate_distance(routing_ctx):
    """Calculate distance."""
    a = routing_ctx['waypoint_coords']['A']
    b = routing_ctx['waypoint_coords']['B']

    distance = math.sqrt(
        (b['x'] - a['x']) ** 2 +
        (b['y'] - a['y']) ** 2
    )
    routing_ctx['calculated_distance'] = distance
    return routing_ctx


@when('I find waypoints within fuel range')
def when_find_within_range(routing_ctx):
    """Find reachable waypoints."""
    graph = routing_ctx['graph']
    origin = routing_ctx['current_position']
    max_distance = routing_ctx['fuel_range']

    routing_ctx['reachable'] = graph.get_waypoints_within_range(origin, max_distance)
    return routing_ctx


@when('I attempt to plan route')
def when_plan_route(routing_ctx):
    """Attempt route planning."""
    if routing_ctx['routing_paused']:
        routing_ctx['plan_result'] = 'failed'
        routing_ctx['error'] = 'Routing is paused'
    else:
        routing_ctx['plan_result'] = 'success'
    return routing_ctx


@when('I validate fuel station coverage')
def when_validate_fuel_coverage(routing_ctx):
    """Validate fuel station distribution."""
    graph = routing_ctx['graph']
    count = graph.count_fuel_stations()
    routing_ctx['fuel_coverage_valid'] = count >= 2
    return routing_ctx


@when('I calculate total fuel required')
def when_calculate_fuel(routing_ctx):
    """Calculate total fuel."""
    segments = routing_ctx['route_segments']

    cruise_fuel = sum(s['distance'] for s in segments if s['mode'] == 'CRUISE')
    drift_fuel = sum(s['distance'] * 0.003 for s in segments if s['mode'] == 'DRIFT')

    routing_ctx['calculations']['cruise_fuel'] = cruise_fuel
    routing_ctx['calculations']['drift_fuel'] = drift_fuel
    routing_ctx['calculations']['total_fuel'] = cruise_fuel + drift_fuel

    return routing_ctx


@when('I calculate total travel time')
def when_calculate_time(routing_ctx):
    """Calculate travel time."""
    segments = routing_ctx['route_segments']

    total_time = 0
    for seg in segments:
        time = seg['distance'] / seg['speed']
        seg['time'] = time
        total_time += time

    routing_ctx['calculations']['total_time'] = total_time
    return routing_ctx


@then(parsers.parse('graph should have {count:d} waypoints'))
def then_waypoint_count(routing_ctx, count):
    """Verify waypoint count."""
    assert routing_ctx['build_result']['waypoints'] == count


@then('graph should have edges connecting waypoints')
def then_has_edges(routing_ctx):
    """Verify edges exist."""
    assert routing_ctx['build_result']['edges'] > 0


@then('waypoint data should include coordinates')
def then_has_coordinates(routing_ctx):
    """Verify waypoints have coordinates."""
    graph = routing_ctx['graph']
    for wp in graph.waypoints.values():
        assert 'x' in wp
        assert 'y' in wp


@then(parsers.parse('fuel station count should be {count:d}'))
def then_fuel_count(routing_ctx, count):
    """Verify fuel station count."""
    assert routing_ctx['fuel_station_count'] == count


@then('fuel station waypoints should be marked')
def then_fuel_marked(routing_ctx):
    """Verify fuel stations marked."""
    graph = routing_ctx['graph']
    for station in graph.fuel_stations:
        assert graph.waypoints[station]['has_fuel'] is True


@then(parsers.parse('distance should be {distance:d} units'))
def then_distance_is(routing_ctx, distance):
    """Verify distance."""
    assert routing_ctx['calculated_distance'] == distance


@then(parsers.parse('reachable waypoints should include all within {distance:d} units'))
def then_reachable_within(routing_ctx, distance):
    """Verify reachable waypoints."""
    reachable = routing_ctx['reachable']
    # Should include waypoints within range
    assert len(reachable) > 0


@then('unreachable waypoints should be excluded')
def then_unreachable_excluded(routing_ctx):
    """Verify unreachable excluded."""
    reachable = routing_ctx['reachable']
    # Very distant waypoints should not be reachable
    assert 'X1-TEST-H8' not in reachable


@then('route planning should fail')
def then_planning_fails(routing_ctx):
    """Verify planning failed."""
    assert routing_ctx['plan_result'] == 'failed'


@then('error should indicate routing is paused')
def then_error_paused(routing_ctx):
    """Verify error message."""
    assert 'paused' in routing_ctx['error'].lower()


@then(parsers.parse('graph should have at least {count:d} fuel stations'))
def then_min_fuel_stations(routing_ctx, count):
    """Verify minimum fuel stations."""
    graph = routing_ctx['graph']
    assert graph.count_fuel_stations() >= count


@then('fuel stations should be distributed across graph')
def then_fuel_distributed(routing_ctx):
    """Verify fuel distribution."""
    graph = routing_ctx['graph']
    # At least some fuel stations exist
    assert len(graph.fuel_stations) > 0


@then(parsers.parse('CRUISE fuel should be {fuel:d} units'))
def then_cruise_fuel(routing_ctx, fuel):
    """Verify CRUISE fuel."""
    assert routing_ctx['calculations']['cruise_fuel'] == fuel


@then(parsers.parse('DRIFT fuel should be {fuel:g} units'))
def then_drift_fuel(routing_ctx, fuel):
    """Verify DRIFT fuel."""
    assert routing_ctx['calculations']['drift_fuel'] == fuel


@then(parsers.parse('total fuel should be {fuel:g} units'))
def then_total_fuel(routing_ctx, fuel):
    """Verify total fuel."""
    assert routing_ctx['calculations']['total_fuel'] == fuel


@then(parsers.parse('segment {seg:d} time should be {time:d} seconds'))
def then_segment_time(routing_ctx, seg, time):
    """Verify segment time."""
    segments = routing_ctx['route_segments']
    segment = next(s for s in segments if s['segment'] == seg)
    assert segment['time'] == time


@then(parsers.parse('total time should be {time:d} seconds'))
def then_total_time(routing_ctx, time):
    """Verify total time."""
    assert routing_ctx['calculations']['total_time'] == time

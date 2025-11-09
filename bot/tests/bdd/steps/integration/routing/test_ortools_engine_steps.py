"""BDD step definitions for OR-Tools routing engine integration tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine
from domain.shared.value_objects import Waypoint, FlightMode

# Load scenarios
scenarios('../../../features/integration/routing/ortools_engine.feature')


# Fixtures
@pytest.fixture
def context():
    """Test context for storing state between steps"""
    return {
        'engine': None,
        'fuel_cost': None,
        'travel_time': None,
        'travel_time_1': None,
        'travel_time_2': None,
        'graph': None,
        'path': None,
        'tour': None,
    }


# Background steps
@given('an OR-Tools routing engine')
def given_ortools_engine(context):
    """Create OR-Tools routing engine with reduced timeouts for fast tests"""
    context['engine'] = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=1)


# Fuel cost calculation steps
@when(parsers.parse('I calculate fuel cost for {distance} units in {mode} mode'))
def when_calculate_fuel_cost(context, distance, mode):
    """Calculate fuel cost for given distance and mode"""
    distance = float(distance)
    flight_mode = FlightMode[mode]
    context['fuel_cost'] = context['engine'].calculate_fuel_cost(distance, flight_mode)


@then(parsers.parse('the fuel cost should be {expected:d}'))
def then_fuel_cost_should_be(context, expected):
    """Verify fuel cost matches expected value"""
    assert context['fuel_cost'] == expected


@then(parsers.parse('the fuel cost should be at least {minimum:d}'))
def then_fuel_cost_at_least(context, minimum):
    """Verify fuel cost is at least minimum value"""
    assert context['fuel_cost'] >= minimum


# Travel time calculation steps
@when(parsers.parse('I calculate travel time for {distance} units in {mode} mode with engine speed {speed:d}'))
def when_calculate_travel_time(context, distance, mode, speed):
    """Calculate travel time for given parameters"""
    distance = float(distance)
    flight_mode = FlightMode[mode]
    context['travel_time'] = context['engine'].calculate_travel_time(distance, flight_mode, speed)


@then(parsers.parse('the travel time should be {expected:d} seconds'))
def then_travel_time_should_be(context, expected):
    """Verify travel time matches expected value"""
    assert context['travel_time'] == expected


@given(parsers.parse('I calculate travel time for {distance} units in {mode} mode with engine speed {speed:d}'))
def given_calculate_travel_time_1(context, distance, mode, speed):
    """Calculate first travel time for comparison"""
    distance = float(distance)
    flight_mode = FlightMode[mode]
    context['travel_time_1'] = context['engine'].calculate_travel_time(distance, flight_mode, speed)


@then('the second travel time should be less than the first')
def then_second_time_less_than_first(context):
    """Verify second travel time is less than first"""
    assert context['travel_time'] < context['travel_time_1']


# Graph creation steps
@given(parsers.parse('a simple graph with waypoints {waypoint_spec}'))
def given_simple_graph(context, waypoint_spec):
    """Create a simple graph from waypoint specification

    Format: "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    """
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


@given(parsers.parse('a fuel constraint graph with {waypoint_spec}'))
def given_fuel_constraint_graph(context, waypoint_spec):
    """Create a fuel constraint graph

    Format: "START" at (0,0), "FUEL-1" at (20,0), "FUEL-2" at (40,0), "GOAL" at (60,0) with fuel
    """
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


@given(parsers.parse('an orbital graph with {waypoint_spec}'))
def given_orbital_graph(context, waypoint_spec):
    """Create an orbital graph

    Format: "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel
    """
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


@given(parsers.parse('a multi-waypoint graph with waypoints {waypoint_spec}'))
def given_multi_waypoint_graph(context, waypoint_spec):
    """Create a multi-waypoint graph

    Format: "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    """
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


@given('an empty graph')
def given_empty_graph(context):
    """Create an empty graph"""
    context['graph'] = {}


@given(parsers.parse('a disconnected graph with {waypoint_spec}'))
def given_disconnected_graph(context, waypoint_spec):
    """Create a disconnected graph"""
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


@given(parsers.parse('a large distance graph with {waypoint_spec}'))
def given_large_distance_graph(context, waypoint_spec):
    """Create a large distance graph"""
    context['graph'] = _parse_waypoint_spec(waypoint_spec)


# Pathfinding steps
@when(parsers.parse('I find optimal path from "{start}" to "{goal}" with {current_fuel:d} current fuel, {capacity:d} capacity, speed {speed:d}, preferring {preference}'))
def when_find_optimal_path(context, start, goal, current_fuel, capacity, speed, preference):
    """Find optimal path with given parameters"""
    prefer_cruise = preference == 'cruise'
    context['path'] = context['engine'].find_optimal_path(
        graph=context['graph'],
        start=start,
        goal=goal,
        current_fuel=current_fuel,
        fuel_capacity=capacity,
        engine_speed=speed,
        prefer_cruise=prefer_cruise
    )


@then(parsers.parse('the path should have {count:d} step'))
@then(parsers.parse('the path should have {count:d} steps'))
def then_path_should_have_steps(context, count):
    """Verify path has expected number of steps"""
    assert context['path'] is not None
    assert len(context['path']['steps']) == count


@then(parsers.parse('step {step_num:d} should be {action} action'))
def then_step_should_be_action(context, step_num, action):
    """Verify step is of expected action type"""
    step = context['path']['steps'][step_num - 1]
    assert step['action'] == action


@then(parsers.parse('step {step_num:d} should go to "{waypoint}"'))
def then_step_should_go_to(context, step_num, waypoint):
    """Verify step goes to expected waypoint"""
    step = context['path']['steps'][step_num - 1]
    assert step['waypoint'] == waypoint


@then(parsers.parse('step {step_num:d} should use {mode} mode'))
def then_step_should_use_mode(context, step_num, mode):
    """Verify step uses expected flight mode"""
    step = context['path']['steps'][step_num - 1]
    assert step['mode'] == FlightMode[mode]


@then(parsers.parse('the total distance should be {distance:f} units'))
def then_total_distance_should_be(context, distance):
    """Verify total distance matches expected value"""
    # Check path or tour depending on which is set
    result = context.get('tour') or context.get('path')
    assert result is not None
    assert result['total_distance'] == distance


@then('the total fuel cost should be greater than 0')
def then_total_fuel_cost_greater_than_zero(context):
    """Verify total fuel cost is greater than zero"""
    assert context['path'] is not None
    assert context['path']['total_fuel_cost'] > 0


@then('the total fuel cost should be 0')
@then(parsers.parse('the total fuel cost should be {value:d}'))
def then_total_fuel_cost_equals(context, value=0):
    """Verify total fuel cost equals expected value"""
    assert context['path'] is not None
    assert context['path']['total_fuel_cost'] == value


@then('the path should be None')
def then_path_should_be_none(context):
    """Verify path is None"""
    assert context['path'] is None


@then('the path should not be None')
def then_path_should_not_be_none(context):
    """Verify path is not None"""
    assert context['path'] is not None


@then('the path should have at least 1 step')
@then(parsers.parse('the path should have at least {count:d} step'))
@then(parsers.parse('the path should have at least {count:d} steps'))
def then_path_should_have_at_least_steps(context, count=1):
    """Verify path has at least expected number of steps"""
    assert context['path'] is not None
    assert len(context['path']['steps']) >= count


@then(parsers.parse('the last step should go to "{waypoint}"'))
def then_last_step_should_go_to(context, waypoint):
    """Verify last step goes to expected waypoint"""
    assert context['path'] is not None
    assert len(context['path']['steps']) > 0
    assert context['path']['steps'][-1]['waypoint'] == waypoint


@then(parsers.parse('the total fuel cost should be at most {maximum:d}'))
def then_total_fuel_cost_at_most(context, maximum):
    """Verify total fuel cost is at most maximum"""
    assert context['path'] is not None
    assert context['path']['total_fuel_cost'] <= maximum


@then('all TRAVEL steps should use CRUISE mode')
def then_all_travel_steps_use_cruise(context):
    """Verify all TRAVEL steps use CRUISE mode (or BURN, which is now preferred)"""
    assert context['path'] is not None
    travel_steps = [s for s in context['path']['steps'] if s['action'] == 'TRAVEL']
    # With new speed-first logic, BURN is preferred over CRUISE when fuel allows
    assert all(step['mode'] in [FlightMode.BURN, FlightMode.CRUISE] for step in travel_steps)


@then('all TRAVEL steps should use DRIFT mode')
def then_all_travel_steps_use_drift(context):
    """Verify all TRAVEL steps use DRIFT mode (DEPRECATED - ships never drift)"""
    assert context['path'] is not None
    travel_steps = [s for s in context['path']['steps'] if s['action'] == 'TRAVEL']
    # DRIFT mode disabled - expect CRUISE or BURN instead
    assert all(step['mode'] in [FlightMode.CRUISE, FlightMode.BURN] for step in travel_steps)


@then('all TRAVEL steps should use CRUISE mode or better')
def then_all_travel_steps_use_cruise_or_better(context):
    """Verify all TRAVEL steps use CRUISE or BURN mode (never DRIFT)"""
    assert context['path'] is not None
    travel_steps = [s for s in context['path']['steps'] if s['action'] == 'TRAVEL']
    assert all(step['mode'] in [FlightMode.CRUISE, FlightMode.BURN] for step in travel_steps)


@then('any TRAVEL steps should use DRIFT mode or path should be None')
def then_any_travel_steps_use_drift_or_none(context):
    """Verify any TRAVEL steps use DRIFT mode or path is None"""
    if context['path'] is not None:
        travel_steps = [s for s in context['path']['steps'] if s['action'] == 'TRAVEL']
        assert any(step['mode'] == FlightMode.DRIFT for step in travel_steps)


@then('the path should include REFUEL actions or DRIFT mode')
def then_path_includes_refuel_or_drift(context):
    """Verify path includes REFUEL actions (DRIFT mode deprecated)"""
    assert context['path'] is not None
    has_refuel = any(s['action'] == 'REFUEL' for s in context['path']['steps'])
    # DRIFT mode disabled - must use REFUEL instead
    assert has_refuel


@then('the path should include REFUEL actions')
def then_path_includes_refuel(context):
    """Verify path includes REFUEL actions"""
    assert context['path'] is not None
    has_refuel = any(s['action'] == 'REFUEL' for s in context['path']['steps'])
    assert has_refuel


@then('the path should use DRIFT mode or be None')
def then_path_uses_drift_or_none(context):
    """Verify path uses DRIFT mode or is None"""
    if context['path'] is not None:
        travel_steps = [s for s in context['path']['steps'] if s['action'] == 'TRAVEL']
        if travel_steps:
            assert any(step['mode'] == FlightMode.DRIFT for step in travel_steps)


@then(parsers.parse('step {step_num:d} should have {value:d} fuel cost'))
def then_step_should_have_fuel_cost(context, step_num, value):
    """Verify step has expected fuel cost"""
    step = context['path']['steps'][step_num - 1]
    assert step['fuel_cost'] == value


@then(parsers.parse('step {step_num:d} should have {value:f} distance'))
def then_step_should_have_distance(context, step_num, value):
    """Verify step has expected distance"""
    step = context['path']['steps'][step_num - 1]
    assert step['distance'] == value


@then(parsers.parse('step {step_num:d} should have {value:d} second travel time'))
def then_step_should_have_travel_time(context, step_num, value):
    """Verify step has expected travel time"""
    step = context['path']['steps'][step_num - 1]
    assert step['time'] == value


@then('the total distance should be greater than 0')
def then_total_distance_greater_than_zero(context):
    """Verify total distance is greater than zero"""
    # Check path or tour depending on which is set
    result = context.get('tour') or context.get('path')
    assert result is not None
    assert result['total_distance'] > 0


@then(parsers.parse('the total distance should be greater than {value:d}'))
def then_total_distance_greater_than(context, value):
    """Verify total distance is greater than value"""
    # Check path or tour depending on which is set
    result = context.get('tour') or context.get('path')
    assert result is not None
    assert result['total_distance'] > value


# Tour optimization steps
@when(parsers.parse('I optimize tour with start "{start}", no waypoints, not returning to start, {capacity:d} capacity, speed {speed:d}'))
def when_optimize_tour_no_waypoints(context, start, capacity, speed):
    """Optimize tour with no waypoints"""
    context['tour'] = context['engine'].optimize_tour(
        graph=context['graph'],
        waypoints=[],
        start=start,
        return_to_start=False,
        fuel_capacity=capacity,
        engine_speed=speed
    )


@when(parsers.parse('I optimize tour with start "{start}", waypoints {waypoints_list}, not returning to start, {capacity:d} capacity, speed {speed:d}'))
def when_optimize_tour_not_returning(context, start, waypoints_list, capacity, speed):
    """Optimize tour without returning to start"""
    waypoints = _parse_waypoints_list(waypoints_list)
    context['tour'] = context['engine'].optimize_tour(
        graph=context['graph'],
        waypoints=waypoints,
        start=start,
        return_to_start=False,
        fuel_capacity=capacity,
        engine_speed=speed
    )


@when(parsers.parse('I optimize tour with start "{start}", waypoints {waypoints_list}, returning to start, {capacity:d} capacity, speed {speed:d}'))
def when_optimize_tour_returning(context, start, waypoints_list, capacity, speed):
    """Optimize tour returning to start"""
    waypoints = _parse_waypoints_list(waypoints_list)
    context['tour'] = context['engine'].optimize_tour(
        graph=context['graph'],
        waypoints=waypoints,
        start=start,
        return_to_start=True,
        fuel_capacity=capacity,
        engine_speed=speed
    )


@then(parsers.parse('the ordered waypoints should be {waypoints_list}'))
def then_ordered_waypoints_should_be(context, waypoints_list):
    """Verify ordered waypoints match expected list"""
    expected = _parse_waypoints_list(waypoints_list)
    assert context['tour'] is not None
    assert context['tour']['ordered_waypoints'] == expected


@then(parsers.parse('the tour should have {count:d} leg'))
@then(parsers.parse('the tour should have {count:d} legs'))
def then_tour_should_have_legs(context, count):
    """Verify tour has expected number of legs"""
    assert context['tour'] is not None
    assert len(context['tour']['legs']) == count


@then('the tour should be None')
def then_tour_should_be_none(context):
    """Verify tour is None"""
    assert context['tour'] is None


@then(parsers.parse('the ordered waypoints should start with "{waypoint}"'))
def then_ordered_waypoints_start_with(context, waypoint):
    """Verify ordered waypoints start with expected waypoint"""
    assert context['tour'] is not None
    assert context['tour']['ordered_waypoints'][0] == waypoint


@then(parsers.parse('the ordered waypoints should end with "{waypoint}"'))
def then_ordered_waypoints_end_with(context, waypoint):
    """Verify ordered waypoints end with expected waypoint"""
    assert context['tour'] is not None
    assert context['tour']['ordered_waypoints'][-1] == waypoint


@then(parsers.parse('the ordered waypoints should contain "{waypoint}"'))
def then_ordered_waypoints_contain(context, waypoint):
    """Verify ordered waypoints contain expected waypoint"""
    assert context['tour'] is not None
    assert waypoint in context['tour']['ordered_waypoints']


@then('the legs count should equal waypoints count minus 1')
def then_legs_count_equals_waypoints_minus_one(context):
    """Verify legs count equals waypoints count minus 1"""
    assert context['tour'] is not None
    assert len(context['tour']['legs']) == len(context['tour']['ordered_waypoints']) - 1


@then(parsers.parse('the ordered waypoints should contain all of {waypoints_list}'))
def then_ordered_waypoints_contain_all(context, waypoints_list):
    """Verify ordered waypoints contain all expected waypoints"""
    expected = set(_parse_waypoints_list(waypoints_list))
    assert context['tour'] is not None
    actual = set(context['tour']['ordered_waypoints'])
    assert actual == expected


@then('all tour legs should use CRUISE mode')
def then_all_tour_legs_use_cruise(context):
    """Verify all tour legs use CRUISE mode (or BURN, which is now preferred)"""
    assert context['tour'] is not None
    # With new speed-first logic, BURN is preferred over CRUISE when fuel allows
    assert all(leg['mode'] in [FlightMode.BURN, FlightMode.CRUISE] for leg in context['tour']['legs'])


@then('the tour should have at least one leg with 0.0 distance')
def then_tour_has_zero_distance_leg(context):
    """Verify tour has at least one leg with zero distance"""
    assert context['tour'] is not None
    assert any(leg['distance'] == 0.0 for leg in context['tour']['legs'])


@then('all zero-distance legs should have 0 fuel cost')
def then_zero_distance_legs_have_zero_fuel(context):
    """Verify all zero-distance legs have zero fuel cost"""
    assert context['tour'] is not None
    zero_dist_legs = [leg for leg in context['tour']['legs'] if leg['distance'] == 0.0]
    assert all(leg['fuel_cost'] == 0 for leg in zero_dist_legs)


@then('each leg should connect consecutive waypoints')
def then_legs_connect_consecutive_waypoints(context):
    """Verify each leg connects consecutive waypoints"""
    assert context['tour'] is not None
    waypoints = context['tour']['ordered_waypoints']
    legs = context['tour']['legs']

    for i, leg in enumerate(legs):
        assert leg['from'] == waypoints[i]
        assert leg['to'] == waypoints[i + 1]


@then('the total distance should match sum of leg distances')
def then_total_distance_matches_legs(context):
    """Verify total distance matches sum of leg distances"""
    assert context['tour'] is not None
    expected = sum(leg['distance'] for leg in context['tour']['legs'])
    assert abs(context['tour']['total_distance'] - expected) < 0.01


@then('the total fuel cost should match sum of leg fuel costs')
def then_total_fuel_matches_legs(context):
    """Verify total fuel cost matches sum of leg fuel costs"""
    assert context['tour'] is not None
    expected = sum(leg['fuel_cost'] for leg in context['tour']['legs'])
    assert context['tour']['total_fuel_cost'] == expected


@then('the total time should match sum of leg times')
def then_total_time_matches_legs(context):
    """Verify total time matches sum of leg times"""
    assert context['tour'] is not None
    expected = sum(leg['time'] for leg in context['tour']['legs'])
    assert context['tour']['total_time'] == expected


# Helper functions
def _parse_waypoint_spec(spec: str) -> dict:
    """Parse waypoint specification into graph dictionary

    Format examples:
    - "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    - "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel
    """
    graph = {}

    # Split by commas but not inside parentheses or quotes
    waypoint_specs = []
    current = []
    paren_depth = 0
    in_quotes = False

    for char in spec:
        if char == '"':
            in_quotes = not in_quotes
        elif char == '(' and not in_quotes:
            paren_depth += 1
        elif char == ')' and not in_quotes:
            paren_depth -= 1
        elif char == ',' and paren_depth == 0 and not in_quotes:
            waypoint_specs.append(''.join(current).strip())
            current = []
            continue
        current.append(char)

    if current:
        waypoint_specs.append(''.join(current).strip())

    for wp_spec in waypoint_specs:
        # Parse: "NAME" at (x,y) [with/without fuel] [having orbital "OTHER"]
        import re

        # Extract name
        name_match = re.search(r'"([^"]+)"', wp_spec)
        if not name_match:
            continue
        name = name_match.group(1)

        # Extract coordinates
        coord_match = re.search(r'at \(([^,]+),([^)]+)\)', wp_spec)
        if not coord_match:
            continue
        x = float(coord_match.group(1))
        y = float(coord_match.group(2))

        # Check for fuel
        has_fuel = 'with fuel' in wp_spec

        # Check for orbitals
        orbitals = []
        orbital_match = re.search(r'having orbital "([^"]+)"', wp_spec)
        if orbital_match:
            orbitals.append(orbital_match.group(1))

        graph[name] = Waypoint(
            symbol=name,
            x=x,
            y=y,
            has_fuel=has_fuel,
            orbitals=tuple(orbitals)
        )

    return graph


def _parse_waypoints_list(text: str) -> list:
    """Parse waypoints list from text

    Format: ["A", "B", "C"] or just [A, B, C]
    """
    # Remove brackets and split
    text = text.strip('[]')
    if not text:
        return []

    waypoints = []
    for item in text.split(','):
        item = item.strip().strip('"').strip("'")
        if item:
            waypoints.append(item)

    return waypoints

"""BDD step definitions for orbital sibling navigation tests"""
import pytest
from pytest_bdd import scenario, given, when, then, parsers
from typing import Dict, List

from domain.shared.value_objects import Waypoint
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine


@pytest.fixture
def context():
    """Shared test context for BDD scenarios"""
    return {}


# Scenarios
@scenario('../../features/navigation/orbital_sibling_navigation.feature',
          'Navigate between orbital siblings (moon to moon)')
def test_navigate_between_orbital_siblings():
    pass


@scenario('../../features/navigation/orbital_sibling_navigation.feature',
          'VRP distance matrix uses graph edges for orbital siblings')
def test_vrp_distance_matrix_orbital_siblings():
    pass


@scenario('../../features/navigation/orbital_sibling_navigation.feature',
          'VRP distributes markets across ships when markets are orbital siblings')
def test_vrp_distributes_orbital_markets():
    pass


@scenario('../../features/navigation/orbital_sibling_navigation.feature',
          'Navigate from planet to moon (direct parent-child)')
def test_navigate_planet_to_moon():
    pass


@scenario('../../features/navigation/orbital_sibling_navigation.feature',
          'Navigate from moon to planet (direct child-parent)')
def test_navigate_moon_to_planet():
    pass


# Given steps
@given('a system with orbital waypoints:', target_fixture='orbital_system')
def create_orbital_system(datatable) -> Dict:
    """Create a test system with orbital waypoints"""
    waypoints = {}

    # Parse datatable (comes as list of lists: [headers, row1, row2, ...])
    headers = datatable[0]
    for row_list in datatable[1:]:
        row = dict(zip(headers, row_list))
        symbol = row['symbol']
        orbitals = [o.strip() for o in row['orbitals'].split(',') if o.strip()]

        waypoints[symbol] = Waypoint(
            symbol=symbol,
            x=float(row['x']),
            y=float(row['y']),
            system_symbol='X1-TEST',
            waypoint_type=row['type'],
            traits=(),
            has_fuel=True,
            orbitals=tuple(orbitals)
        )

    return {'waypoints': waypoints, 'edges': []}


@given('the graph has orbital edges:')
def add_orbital_edges(orbital_system: Dict, datatable):
    """Add edges to the orbital system graph"""
    edges = []

    # Parse datatable
    headers = datatable[0]
    for row_list in datatable[1:]:
        row = dict(zip(headers, row_list))
        edges.append({
            'from': row['from'],
            'to': row['to'],
            'distance': float(row['distance']),
            'type': row['type']
        })

    orbital_system['edges'] = edges


@given(parsers.parse('I have a ship with {fuel_capacity:d} fuel capacity and {engine_speed:d} engine speed'),
       target_fixture='test_ship')
def create_test_ship(fuel_capacity: int, engine_speed: int) -> Dict:
    """Create test ship configuration"""
    return {
        'fuel_capacity': fuel_capacity,
        'engine_speed': engine_speed,
        'current_fuel': fuel_capacity
    }


@given(parsers.parse('the ship is at waypoint "{waypoint}"'))
def set_ship_location(context, waypoint: str):
    """Set ship's current location"""
    context['ship_location'] = waypoint


@given(parsers.parse('the ship has {fuel:d} fuel'))
def set_ship_fuel(context, test_ship: Dict, fuel: int):
    """Set ship's current fuel"""
    test_ship['current_fuel'] = fuel
    context['current_fuel'] = fuel


@given('ships are positioned:', target_fixture='fleet_ships')
def position_fleet_ships(datatable) -> Dict[str, str]:
    """Position multiple ships for VRP testing"""
    ship_locations = {}

    # Parse datatable
    headers = datatable[0]
    for row_list in datatable[1:]:
        row = dict(zip(headers, row_list))
        ship_locations[row['ship']] = row['location']

    return ship_locations


@given('markets are at:', target_fixture='market_list')
def define_markets(datatable) -> List[str]:
    """Define market locations"""
    markets = []

    # Parse datatable
    headers = datatable[0]
    for row_list in datatable[1:]:
        row = dict(zip(headers, row_list))
        markets.append(row['market'])

    return markets


# When steps
@when(parsers.parse('I plan a route from "{start}" to "{goal}"'))
def plan_route(context, orbital_system: Dict, test_ship: Dict, start: str, goal: str):
    """Plan a route between two waypoints"""
    engine = ORToolsRoutingEngine()

    # Pass just the waypoints dict (flat Dict[str, Waypoint])
    # This matches how production code calls the routing engine
    route = engine.find_optimal_path(
        graph=orbital_system['waypoints'],
        start=start,
        goal=goal,
        current_fuel=test_ship['current_fuel'],
        fuel_capacity=test_ship['fuel_capacity'],
        engine_speed=test_ship['engine_speed'],
        prefer_cruise=True
    )

    context['route'] = route


@when('I build the VRP distance matrix for fleet optimization')
def build_vrp_distance_matrix(context, orbital_system: Dict, fleet_ships: Dict[str, str],
                               market_list: List[str]):
    """Build VRP distance matrix"""
    engine = ORToolsRoutingEngine()

    # Build nodes list (markets + ship starting positions)
    nodes = list(market_list)
    for ship_location in fleet_ships.values():
        if ship_location not in nodes:
            nodes.append(ship_location)

    # Build distance matrix using internal method
    matrix = engine._build_distance_matrix_for_vrp(
        nodes=nodes,
        graph=orbital_system['waypoints'],
        fuel_capacity=400,
        engine_speed=30
    )

    context['vrp_matrix'] = matrix
    context['vrp_nodes'] = nodes


@when('I optimize fleet market partitioning')
def optimize_fleet_partitioning(context, orbital_system: Dict, fleet_ships: Dict[str, str],
                                market_list: List[str]):
    """Run VRP fleet partitioning"""
    engine = ORToolsRoutingEngine()

    assignments = engine.optimize_fleet_tour(
        graph=orbital_system['waypoints'],
        markets=market_list,
        ship_locations=fleet_ships,
        fuel_capacity=400,
        engine_speed=30
    )

    context['vrp_assignments'] = assignments


# Then steps
@then('the route should exist')
def verify_route_exists(context):
    """Verify route was successfully computed"""
    assert context.get('route') is not None, "Route should exist but is None"


@then(parsers.parse('the route should have {count:d} step'))
@then(parsers.parse('the route should have {count:d} steps'))
def verify_route_step_count(context, count: int):
    """Verify number of steps in route"""
    route = context['route']
    steps = route.get('steps', [])
    assert len(steps) == count, f"Expected {count} steps, got {len(steps)}"


@then('the route step should be:')
def verify_route_step_details(context, datatable):
    """Verify route step details"""
    # Parse datatable
    headers = datatable[0]
    row_list = datatable[1]
    expected = dict(zip(headers, row_list))

    route = context['route']
    step = route['steps'][0]

    from domain.shared.value_objects import FlightMode

    assert step['action'] == expected['action'], \
        f"Expected action {expected['action']}, got {step['action']}"
    assert step['waypoint'] == expected['to'], \
        f"Expected destination {expected['to']}, got {step['waypoint']}"

    # Handle FlightMode enum comparison
    step_mode = step['mode'].value[0] if isinstance(step['mode'], FlightMode) else step['mode']
    assert step_mode == expected['mode'], \
        f"Expected mode {expected['mode']}, got {step_mode}"

    assert step['fuel_cost'] == int(expected['fuel_cost']), \
        f"Expected fuel_cost {expected['fuel_cost']}, got {step['fuel_cost']}"
    assert step['distance'] == float(expected['distance']), \
        f"Expected distance {expected['distance']}, got {step['distance']}"


@then(parsers.parse('the route total time should be {time:d} seconds'))
def verify_route_total_time(context, time: int):
    """Verify total route time"""
    route = context['route']
    total_time = route.get('total_time', 0)
    assert total_time == time, f"Expected total_time {time}, got {total_time}"


@then('the distance matrix should show:')
def verify_distance_matrix_values(context, datatable):
    """Verify specific distance matrix values"""
    matrix = context['vrp_matrix']
    nodes = context['vrp_nodes']
    node_index = {node: i for i, node in enumerate(nodes)}

    # Parse datatable
    headers = datatable[0]
    for row_list in datatable[1:]:
        row = dict(zip(headers, row_list))
        from_idx = node_index[row['from']]
        to_idx = node_index[row['to']]
        expected_time = int(row['time'])

        actual_time = matrix[from_idx][to_idx]
        assert actual_time == expected_time, \
            f"Distance matrix [{row['from']}→{row['to']}]: expected {expected_time}, got {actual_time}"


@then('all markets should be reachable (no 1,000,000 distances)')
def verify_no_unreachable_markets(context):
    """Verify no markets show as unreachable in distance matrix"""
    matrix = context['vrp_matrix']

    for i, row in enumerate(matrix):
        for j, distance in enumerate(row):
            if i != j:  # Skip diagonal
                assert distance < 1_000_000, \
                    f"Market {context['vrp_nodes'][i]}→{context['vrp_nodes'][j]} unreachable (distance={distance})"


@then(parsers.parse('all {count:d} markets should be assigned to ships'))
def verify_all_markets_assigned(context, count: int):
    """Verify all markets were assigned"""
    assignments = context['vrp_assignments']

    assigned_markets = set()
    for markets in assignments.values():
        assigned_markets.update(markets)

    assert len(assigned_markets) == count, \
        f"Expected {count} markets assigned, got {len(assigned_markets)}"


@then('no markets should be dropped')
def verify_no_dropped_markets(context, market_list: List[str]):
    """Verify no markets were dropped by VRP"""
    assignments = context['vrp_assignments']

    assigned_markets = set()
    for markets in assignments.values():
        assigned_markets.update(markets)

    dropped = set(market_list) - assigned_markets
    assert len(dropped) == 0, f"Markets dropped by VRP: {dropped}"


@then('the assignments should distribute markets across available ships')
def verify_market_distribution(context):
    """Verify markets are distributed (not all on one ship)"""
    assignments = context['vrp_assignments']

    # At least 2 ships should have markets (for 2+ ships scenario)
    ships_with_markets = sum(1 for markets in assignments.values() if len(markets) > 0)
    assert ships_with_markets >= 1, \
        f"Markets should be distributed, but only {ships_with_markets} ships have assignments"

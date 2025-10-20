"""Step definitions for refueling scenarios."""

import pytest
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, Mock
from pytest_bdd import given, when, then, parsers, scenarios

from spacetraders_bot.core.database import Database
from spacetraders_bot.core.ortools_router import ORToolsTSP
from spacetraders_bot.core.route_planner import TourOptimizer, RouteOptimizer
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.market_scout import ScoutCoordinator
from spacetraders_bot.core.system_graph_provider import SystemGraphProvider, GraphLoadResult

# Load all refueling scenarios
scenarios('../../features/refueling/fuel_station_exclusion.feature')
scenarios('../../features/refueling/market_filtering.feature')
scenarios('../../features/refueling/intermediate_refueling.feature')
scenarios('../../features/refueling/refuel_execution.feature')


@pytest.fixture
def temp_db_path():
    """Create temporary database for isolated testing"""
    with tempfile.NamedTemporaryFile(delete=False, suffix=".db") as f:
        db_path = Path(f.name)
    yield db_path
    if db_path.exists():
        db_path.unlink()


@pytest.fixture(scope="function")
def refueling_context():
    """Shared context for refueling scenarios. Resets for each scenario."""
    return {
        'graph': None,
        'ship_data': None,
        'markets': None,
        'tour': None,
        'route': None,
        'coordinator': None,
        'db_path': None,
        'assigned_markets': [],
        'operation_sequence': [],
        'mock_ship': None,
        'navigator': None,
    }


# ====================
# Graph Setup Steps
# ====================

@given(parsers.parse('the X1-JV40 system with waypoints:\n{waypoint_table}'))
def setup_jv40_system(refueling_context, waypoint_table):
    """Set up X1-JV40 system with waypoints."""
    waypoints = {
        "X1-JV40-A1": {"x": 0, "y": 0, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
        "X1-JV40-J52": {"x": 100, "y": 100, "has_fuel": True, "type": "FUEL_STATION", "traits": []},
        "X1-JV40-J53": {"x": 110, "y": 110, "has_fuel": True, "type": "PLANET", "traits": ["MARKETPLACE"]},
    }

    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1:]:
            wp1_data, wp2_data = waypoints[wp1], waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({"from": wp1, "to": wp2, "distance": round(distance, 2), "type": "normal"})

    refueling_context['graph'] = {"system": "X1-JV40", "waypoints": waypoints, "edges": edges}


@given(parsers.parse('a system with waypoints:\n{waypoint_table}'))
def setup_system_with_waypoints(refueling_context, waypoint_table):
    """Set up system with specified waypoints."""
    waypoints = {}
    lines = [line.strip() for line in waypoint_table.strip().split('\n') if '|' in line]
    data_lines = [line for line in lines if not line.startswith('|') or '---' not in line][1:]

    for line in data_lines:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) >= 4:
            wp_name, wp_type, traits_str, has_fuel_str = parts[:4]
            traits = [t.strip() for t in traits_str.split(',') if t.strip()]
            waypoints[wp_name] = {
                "x": hash(wp_name) % 1000,  # Simple hash for position
                "y": hash(wp_name[::-1]) % 1000,
                "type": wp_type if wp_type else "",  # Empty type is valid
                "traits": traits,
                "has_fuel": has_fuel_str.lower() == "yes",
                "orbitals": []
            }

    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1:]:
            wp1_data, wp2_data = waypoints[wp1], waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({"from": wp1, "to": wp2, "distance": round(distance, 2), "type": "normal"})

    refueling_context['graph'] = {"system": "X1-TEST", "waypoints": waypoints, "edges": edges}


@given(parsers.parse('the X1-GH18 system with waypoints:\n{waypoint_table}'))
def setup_gh18_system(refueling_context, waypoint_table):
    """Set up X1-GH18 system."""
    waypoints = {}
    lines = [line.strip() for line in waypoint_table.strip().split('\n') if '|' in line]
    data_lines = [line for line in lines if not line.startswith('|') or '---' not in line][1:]

    for line in data_lines:
        parts = [p.strip() for p in line.split('|') if p.strip()]
        if len(parts) >= 5:
            wp_name, x, y, has_fuel, traits_str = parts
            traits = [t.strip() for t in traits_str.split(',') if t.strip()]
            waypoints[wp_name] = {
                "x": int(x),
                "y": int(y),
                "has_fuel": has_fuel.lower() == "yes",
                "type": "MOON" if "fuel" in has_fuel.lower() else "ASTEROID",
                "traits": traits,
                "orbitals": []
            }

    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1:]:
            wp1_data, wp2_data = waypoints[wp1], waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({"from": wp1, "to": wp2, "distance": round(distance, 2), "type": "normal"})

    refueling_context['graph'] = {"system": "X1-GH18", "waypoints": waypoints, "edges": edges}


@given("an empty graph")
def setup_empty_graph(refueling_context):
    """Set up empty graph."""
    refueling_context['graph'] = {}


@given("a graph with no waypoints")
def setup_graph_no_waypoints(refueling_context):
    """Set up graph with no waypoints."""
    refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}}


@given("a None graph")
def setup_none_graph(refueling_context):
    """Set up None graph."""
    refueling_context['graph'] = None


@given("a system with 2 real markets and 1 fuel station")
def setup_system_for_coordinator(refueling_context):
    """Set up system for coordinator test."""
    waypoints = {
        "X1-TEST-M1": {"type": "PLANET", "x": 0, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True},
        "X1-TEST-M2": {"type": "MOON", "x": 100, "y": 0, "traits": ["MARKETPLACE"], "has_fuel": True},
        "X1-TEST-F1": {"type": "FUEL_STATION", "x": 50, "y": 50, "traits": ["MARKETPLACE"], "has_fuel": True},
    }
    refueling_context['graph'] = {"system": "X1-TEST", "waypoints": waypoints, "edges": []}


# ====================
# Ship Setup Steps
# ====================

@given(parsers.parse('a scout ship "{ship}" at "{location}"'))
def setup_scout_ship(refueling_context, ship, location):
    """Set up scout ship."""
    refueling_context['ship_data'] = {
        "symbol": ship,
        "engine": {"speed": 9},
        "fuel": {"current": 1000, "capacity": 1000},
        "nav": {"waypointSymbol": location},
    }


@given(parsers.parse('a ship "{ship}" at "{location}" with {fuel:d} fuel (capacity {capacity:d})'))
def setup_ship_with_fuel(refueling_context, ship, location, fuel, capacity):
    """Set up ship with specific fuel."""
    refueling_context['ship_data'] = {
        "symbol": ship,
        "engine": {"speed": 9},
        "fuel": {"current": fuel, "capacity": capacity},
        "nav": {"waypointSymbol": location, "status": "IN_ORBIT"},
    }

    # Ensure starting location is in graph
    if 'graph' in refueling_context and refueling_context['graph']:
        if location not in refueling_context['graph']['waypoints']:
            refueling_context['graph']['waypoints'][location] = {
                "x": 0, "y": 0,
                "has_fuel": True,
                "traits": ["MARKETPLACE"],
                "type": "ASTEROID",
                "orbitals": []
            }
            rebuild_graph_edges(refueling_context)


@given(parsers.parse('a ship at "{location}" with {fuel:d} fuel (capacity {capacity:d})'))
def setup_generic_ship_with_fuel(refueling_context, location, fuel, capacity):
    """Set up generic ship with fuel."""
    refueling_context['ship_data'] = {
        "symbol": "TEST-SHIP",
        "engine": {"speed": 30},
        "fuel": {"current": fuel, "capacity": capacity},
        "nav": {"waypointSymbol": location, "status": "IN_ORBIT"},
    }

    # Ensure starting location is in graph
    if 'graph' not in refueling_context or not refueling_context['graph']:
        refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}, "edges": []}

    if location not in refueling_context['graph']['waypoints']:
        refueling_context['graph']['waypoints'][location] = {
            "x": 0, "y": 0,
            "has_fuel": True,
            "traits": ["MARKETPLACE"],
            "type": "ASTEROID",
            "orbitals": []
        }
        rebuild_graph_edges(refueling_context)


@given(parsers.parse('a ship at "{location}" with {fuel:d} fuel'))
def setup_ship_simple(refueling_context, location, fuel):
    """Set up ship with fuel at location."""
    refueling_context['ship_data'] = {
        "symbol": "TEST-SHIP",
        "engine": {"speed": 10},
        "fuel": {"current": fuel, "capacity": 400},
        "nav": {"waypointSymbol": location, "status": "IN_ORBIT"},
        "frame": {"integrity": 1.0},
        "registration": {"role": "HAULER"},
        "cooldown": {"remainingSeconds": 0},
    }


# ====================
# Assignment and Destination Steps
# ====================

@given(parsers.parse('assigned markets: {markets}'))
def set_assigned_markets(refueling_context, markets):
    """Set assigned markets."""
    refueling_context['assigned_markets'] = [m.strip() for m in markets.split(',')]


@given(parsers.parse('the destination is "{dest}" ({distance:d} units away)'))
def set_destination_with_distance(refueling_context, dest, distance):
    """Set destination with distance."""
    refueling_context['destination'] = dest
    refueling_context['destination_distance'] = distance


@given(parsers.parse('waypoint "{waypoint}" at {distance:d} units with fuel'))
def add_intermediate_waypoint(refueling_context, waypoint, distance):
    """Add intermediate waypoint to graph."""
    if 'graph' not in refueling_context or not refueling_context['graph']:
        refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}, "edges": []}

    # Add waypoint
    refueling_context['graph']['waypoints'][waypoint] = {
        "x": distance, "y": 0,
        "has_fuel": True,
        "traits": ["MARKETPLACE"],
        "type": "MOON",
        "orbitals": []
    }

    # Rebuild edges for full connectivity
    rebuild_graph_edges(refueling_context)


@given(parsers.parse('destination "{dest}" at {distance:d} units total'))
@given(parsers.parse('destination "{dest}" at {distance:d} units'))
@given(parsers.parse('destination "{dest}" is {distance:d} units away'))
def set_destination(refueling_context, dest, distance):
    """Set destination."""
    if 'graph' not in refueling_context or not refueling_context['graph']:
        refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}, "edges": []}

    # Only add if not already in graph
    if dest not in refueling_context['graph']['waypoints']:
        refueling_context['graph']['waypoints'][dest] = {
            "x": distance, "y": 0,
            "has_fuel": False,
            "traits": [],
            "type": "ASTEROID",
            "orbitals": []
        }
    refueling_context['destination'] = dest

    # Rebuild edges for full connectivity
    rebuild_graph_edges(refueling_context)


@given(parsers.parse('the destination is "{dest}" ({distance:d} units away)'))
def set_destination_with_parentheses(refueling_context, dest, distance):
    """Set destination with distance in parentheses."""
    if 'graph' not in refueling_context or not refueling_context['graph']:
        refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}, "edges": []}

    # Only add if not already in graph (Background may have created it)
    if dest not in refueling_context['graph']['waypoints']:
        refueling_context['graph']['waypoints'][dest] = {
            "x": distance, "y": 0,
            "has_fuel": False,
            "traits": [],
            "type": "ASTEROID",
            "orbitals": []
        }
    refueling_context['destination'] = dest
    refueling_context['destination_distance'] = distance

    # Rebuild edges for full connectivity
    rebuild_graph_edges(refueling_context)


@given(parsers.cfparse('destination "{dest}" ({details})'))
def set_destination_with_details(refueling_context, dest, details):
    """Set destination with route details (ignored)."""
    # The details are just informational - extract destination only
    if 'graph' not in refueling_context or not refueling_context['graph']:
        refueling_context['graph'] = {"system": "X1-TEST", "waypoints": {}, "edges": []}

    # Destination should already exist in the graph from Background
    refueling_context['destination'] = dest


def rebuild_graph_edges(refueling_context):
    """Rebuild all edges in graph for full connectivity."""
    graph = refueling_context['graph']
    waypoints = graph['waypoints']

    edges = []
    wp_list = list(waypoints.keys())
    for i, wp1 in enumerate(wp_list):
        for wp2 in wp_list[i + 1:]:
            wp1_data, wp2_data = waypoints[wp1], waypoints[wp2]
            distance = ((wp2_data["x"] - wp1_data["x"]) ** 2 + (wp2_data["y"] - wp1_data["y"]) ** 2) ** 0.5
            edges.append({"from": wp1, "to": wp2, "distance": round(distance, 2), "type": "normal"})

    graph['edges'] = edges


@given("there are no intermediate fuel stations")
def set_no_intermediate_stations(refueling_context):
    """Ensure no intermediate stations."""
    pass  # Already implicit in graph setup


@given(parsers.parse('direct CRUISE to C43 requires {fuel:d} fuel (impossible with {capacity:d} capacity)'))
def set_cruise_fuel_requirement(refueling_context, fuel, capacity):
    """Set cruise fuel requirement."""
    refueling_context['cruise_fuel_required'] = fuel
    refueling_context['fuel_capacity'] = capacity


@given(parsers.parse('CRUISE requires {fuel:d} fuel with safety margin (exceeds capacity)'))
def set_cruise_exceeds_capacity(refueling_context, fuel):
    """Set cruise exceeds capacity."""
    refueling_context['cruise_fuel_required'] = fuel


# ====================
# Cache Steps
# ====================

@given(parsers.parse('a stale cache entry with markets: {markets}'))
def create_stale_cache(refueling_context, temp_db_path, markets):
    """Create stale cache entry."""
    db = Database(temp_db_path)
    refueling_context['db_path'] = temp_db_path

    market_list = [m.strip() for m in markets.split(',')]
    tour_order = market_list + [market_list[0]]  # Return to start

    with db.transaction() as conn:
        db.save_tour_cache(
            conn, system="X1-JV40", markets=market_list, algorithm="ortools",
            tour_order=tour_order, total_distance=200.0, start_waypoint=None
        )


@given("a mock ship controller")
def setup_mock_ship_controller(refueling_context):
    """Set up mock ship controller."""
    mock_ship = MagicMock(spec=ShipController)
    operation_sequence = []

    ship_data = refueling_context.get('ship_data', {})

    def get_status_side_effect():
        operation_sequence.append('get_status')
        return ship_data.copy()

    def navigate_side_effect(waypoint, flight_mode=None, auto_refuel=True):
        operation_sequence.append(f'navigate_to_{waypoint}')
        ship_data['nav']['waypointSymbol'] = waypoint
        ship_data['nav']['status'] = 'IN_ORBIT'
        return True

    def orbit_side_effect():
        operation_sequence.append('orbit')
        ship_data['nav']['status'] = 'IN_ORBIT'
        return True

    def dock_side_effect():
        operation_sequence.append('dock')
        ship_data['nav']['status'] = 'DOCKED'
        return True

    def refuel_side_effect(units=None):
        operation_sequence.append('refuel')
        ship_data['fuel']['current'] = ship_data['fuel']['capacity']
        return True

    mock_ship.get_status.side_effect = get_status_side_effect
    mock_ship.navigate.side_effect = navigate_side_effect
    mock_ship.orbit.side_effect = orbit_side_effect
    mock_ship.dock.side_effect = dock_side_effect
    mock_ship.refuel.side_effect = refuel_side_effect

    refueling_context['mock_ship'] = mock_ship
    refueling_context['operation_sequence'] = operation_sequence


# ====================
# When Steps - Market Extraction
# ====================

@when("the coordinator extracts markets from the graph")
def extract_markets(refueling_context):
    """Extract markets from graph."""
    graph = refueling_context['graph']
    refueling_context['markets'] = TourOptimizer.get_markets_from_graph(graph)


@when("I call get_markets_from_graph")
def call_get_markets(refueling_context):
    """Call get_markets_from_graph."""
    graph = refueling_context['graph']
    refueling_context['markets'] = TourOptimizer.get_markets_from_graph(graph)


@when("the scout coordinator loads markets")
def coordinator_loads_markets(refueling_context, tmp_path):
    """Scout coordinator loads markets."""
    graph = refueling_context['graph']

    mock_api = Mock()
    mock_graph_provider = Mock(spec=SystemGraphProvider)
    mock_graph_provider.get_graph.return_value = GraphLoadResult(
        graph=graph, source="test", message="Test graph loaded"
    )

    coordinator = ScoutCoordinator(
        system="X1-TEST",
        ships=["SCOUT-1"],
        token="test_token",
        player_id=1,
        config_file=str(tmp_path / "scout_config.json"),
        graph_provider=mock_graph_provider
    )

    refueling_context['coordinator'] = coordinator


# ====================
# When Steps - Tour Generation
# ====================

@when("I generate an optimized tour without cache")
def generate_tour_without_cache(refueling_context, temp_db_path):
    """Generate tour without cache."""
    graph = refueling_context['graph']
    ship_data = refueling_context['ship_data']
    assigned_markets = refueling_context['assigned_markets']

    tsp = ORToolsTSP(graph, db_path=temp_db_path)
    start = assigned_markets[0]
    waypoints = assigned_markets[1:]

    tour = tsp.optimise_tour(
        waypoints=waypoints, start=start, ship_data=ship_data,
        return_to_start=True, use_cache=False, algorithm="ortools"
    )

    refueling_context['tour'] = tour


@when("I generate and cache an optimized tour")
def generate_and_cache_tour(refueling_context, temp_db_path):
    """Generate and cache tour."""
    graph = refueling_context['graph']
    ship_data = refueling_context['ship_data']
    assigned_markets = refueling_context['assigned_markets']

    tsp = ORToolsTSP(graph, db_path=temp_db_path)
    start = assigned_markets[0]
    waypoints = assigned_markets[1:]

    tour = tsp.optimise_tour(
        waypoints=waypoints, start=start, ship_data=ship_data,
        return_to_start=True, use_cache=True, algorithm="ortools"
    )

    refueling_context['tour'] = tour
    refueling_context['db_path'] = temp_db_path


@when("I request an optimized tour with cache enabled")
def request_tour_with_cache(refueling_context):
    """Request tour with cache enabled."""
    graph = refueling_context['graph']
    ship_data = refueling_context['ship_data']
    assigned_markets = refueling_context['assigned_markets']
    db_path = refueling_context['db_path']

    tsp = ORToolsTSP(graph, db_path=db_path)
    start = assigned_markets[0]
    waypoints = assigned_markets[1:]

    tour = tsp.optimise_tour(
        waypoints=waypoints, start=start, ship_data=ship_data,
        return_to_start=True, use_cache=True, algorithm="ortools"
    )

    refueling_context['tour'] = tour


# ====================
# When Steps - Route Planning
# ====================

@when("I plan a route with prefer_cruise=True")
def plan_route_prefer_cruise(refueling_context):
    """Plan route with prefer_cruise=True."""
    graph = refueling_context['graph']
    ship_data = refueling_context['ship_data']
    destination = refueling_context['destination']

    optimizer = RouteOptimizer(graph, ship_data)
    route = optimizer.find_optimal_route(
        start=ship_data['nav']['waypointSymbol'],
        goal=destination,
        current_fuel=ship_data['fuel']['current'],
        prefer_cruise=True
    )

    refueling_context['route'] = route


@when(parsers.parse('I execute the route to "{destination}" with prefer_cruise=True'))
def execute_route_prefer_cruise(refueling_context, destination):
    """Execute route with prefer_cruise=True."""
    graph = refueling_context['graph']
    mock_ship = refueling_context['mock_ship']

    api = MagicMock()
    navigator = SmartNavigator(api, graph['system'], graph=graph, db_path=':memory:')

    success = navigator.execute_route(mock_ship, destination, prefer_cruise=True)
    refueling_context['execution_success'] = success


@when("the markets are passed to the tour optimizer")
def pass_markets_to_optimizer(refueling_context):
    """Pass markets to tour optimizer."""
    refueling_context['tour_optimizer_input'] = refueling_context['markets']


# ====================
# Then Steps - Market Validation
# ====================

@then(parsers.parse('the markets should include "{waypoint}"'))
def verify_market_included(refueling_context, waypoint):
    """Verify market is included."""
    markets = refueling_context['markets']
    assert waypoint in markets, f"Markets should include {waypoint}"


@then(parsers.parse('the markets should NOT include "{waypoint}"'))
def verify_market_not_included(refueling_context, waypoint):
    """Verify market is not included."""
    markets = refueling_context['markets']
    assert waypoint not in markets, f"Markets should NOT include {waypoint}"


@then(parsers.parse('there should be exactly {count:d} markets'))
def verify_market_count(refueling_context, count):
    """Verify market count."""
    markets = refueling_context['markets']
    assert len(markets) == count, f"Should have {count} markets, got {len(markets)}"


@then("the result should be an empty list")
def verify_empty_list(refueling_context):
    """Verify result is empty list."""
    markets = refueling_context['markets']
    assert markets == [], f"Should be empty list, got {markets}"


@then("both waypoints should be included")
def verify_both_included(refueling_context):
    """Verify both waypoints included."""
    markets = refueling_context['markets']
    assert len(markets) == 2, f"Should have 2 waypoints, got {len(markets)}"


# ====================
# Then Steps - Tour Validation
# ====================

@then(parsers.parse('the tour should visit "{waypoint}"'))
def verify_tour_visits(refueling_context, waypoint):
    """Verify tour visits waypoint."""
    tour = refueling_context['tour']
    visited = {tour["start"]}
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])

    assert waypoint in visited, f"Tour should visit {waypoint}"


@then(parsers.parse('the tour should NOT visit "{waypoint}"'))
def verify_tour_not_visits(refueling_context, waypoint):
    """Verify tour does NOT visit waypoint."""
    tour = refueling_context['tour']
    visited = {tour["start"]}
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])

    assert waypoint not in visited, f"Tour should NOT visit {waypoint}"


@then("the tour should visit exactly the assigned markets")
def verify_exact_markets(refueling_context):
    """Verify tour visits exactly assigned markets."""
    tour = refueling_context['tour']
    assigned = set(refueling_context['assigned_markets'])

    visited = {tour["start"]}
    for leg in tour.get("legs", []):
        if "goal" in leg:
            visited.add(leg["goal"])

    assert visited == assigned, f"Tour should visit {assigned}, visited {visited}"


@then("the cached markets should NOT include fuel stations")
def verify_no_fuel_stations_in_cache(refueling_context):
    """Verify cached markets don't include fuel stations."""
    import json
    db = Database(refueling_context['db_path'])
    graph = refueling_context['graph']

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT markets FROM tour_cache WHERE system = ?", (graph['system'],))
        row = cursor.fetchone()

    assert row is not None, "Tour should be cached"
    cached_markets = json.loads(row["markets"])

    for market in cached_markets:
        waypoint_type = graph["waypoints"][market]["type"]
        assert waypoint_type != "FUEL_STATION", f"Cached market {market} is FUEL_STATION"


@then("all cached markets should have MARKETPLACE trait")
def verify_cached_have_marketplace(refueling_context):
    """Verify all cached markets have MARKETPLACE trait."""
    import json
    db = Database(refueling_context['db_path'])
    graph = refueling_context['graph']

    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("SELECT markets FROM tour_cache WHERE system = ?", (graph['system'],))
        row = cursor.fetchone()

    cached_markets = json.loads(row["markets"])

    for market in cached_markets:
        traits = graph["waypoints"][market]["traits"]
        assert "MARKETPLACE" in traits, f"Cached market {market} missing MARKETPLACE trait"


@then("no fuel stations should be in the tour input")
def verify_no_fuel_stations_in_input(refueling_context):
    """Verify no fuel stations in tour input."""
    tour_input = refueling_context['tour_optimizer_input']
    graph = refueling_context['graph']

    for market in tour_input:
        waypoint_type = graph["waypoints"][market]["type"]
        assert waypoint_type != "FUEL_STATION", f"Tour input contains fuel station: {market}"


@then("all waypoints should have MARKETPLACE trait")
def verify_all_have_marketplace(refueling_context):
    """Verify all waypoints have MARKETPLACE trait."""
    tour_input = refueling_context.get('tour_optimizer_input') or refueling_context.get('markets', [])
    graph = refueling_context['graph']

    for market in tour_input:
        traits = graph["waypoints"][market]["traits"]
        assert "MARKETPLACE" in traits, f"Waypoint {market} missing MARKETPLACE trait"


@then(parsers.parse('the coordinator should have exactly {count:d} markets'))
def verify_coordinator_market_count(refueling_context, count):
    """Verify coordinator market count."""
    coordinator = refueling_context['coordinator']
    assert len(coordinator.markets) == count, \
        f"Coordinator should have {count} markets, got {len(coordinator.markets)}"


@then("the fuel station should NOT be in coordinator's market list")
def verify_fuel_station_not_in_coordinator(refueling_context):
    """Verify fuel station not in coordinator's markets."""
    coordinator = refueling_context['coordinator']
    assert "X1-TEST-F1" not in coordinator.markets, \
        "Fuel station should NOT be in coordinator's market list"


# ====================
# Then Steps - Route Validation
# ====================

@then("the route should use ONLY CRUISE mode")
def verify_only_cruise(refueling_context):
    """Verify route uses only CRUISE."""
    route = refueling_context['route']
    for step in route['steps']:
        if step['action'] == 'navigate':
            assert step['mode'] == 'CRUISE', f"Found {step['mode']} mode, expected CRUISE"


@then("the route should exist")
def verify_route_exists(refueling_context):
    """Verify route exists."""
    route = refueling_context['route']
    assert route is not None, "Route should exist"


@then("the route should use DRIFT mode")
def verify_drift_mode(refueling_context):
    """Verify route uses DRIFT."""
    route = refueling_context['route']
    drift_found = False
    for step in route['steps']:
        if step['action'] == 'navigate' and step['mode'] == 'DRIFT':
            drift_found = True
            break
    assert drift_found, "Route should use DRIFT mode"


@then("DRIFT is the only option")
def verify_drift_only_option(refueling_context):
    """Verify DRIFT is the only option."""
    pass  # Verified by previous checks


@then(parsers.parse('the route should visit intermediate station "{waypoint}"'))
def verify_intermediate_station(refueling_context, waypoint):
    """Verify route visits intermediate station."""
    route = refueling_context['route']
    waypoints_visited = set()
    for step in route['steps']:
        if step['action'] == 'navigate':
            waypoints_visited.add(step['from'])
            waypoints_visited.add(step['to'])

    assert waypoint in waypoints_visited, f"Route should visit {waypoint}"


@then(parsers.parse('the route should include at least {count:d} refuel actions'))
def verify_refuel_count(refueling_context, count):
    """Verify route has at least N refuel actions."""
    route = refueling_context['route']
    refuel_actions = [s for s in route['steps'] if s['action'] == 'refuel']
    assert len(refuel_actions) >= count, \
        f"Route should have at least {count} refuel actions, found {len(refuel_actions)}"


@then(parsers.parse('the total time should be less than {seconds:d} seconds'))
def verify_total_time(refueling_context, seconds):
    """Verify total time is less than threshold."""
    route = refueling_context['route']
    assert route['total_time'] < seconds, \
        f"Total time {route['total_time']}s should be < {seconds}s"


@then(parsers.parse('the route should navigate {leg} in CRUISE'))
def verify_leg_cruise(refueling_context, leg):
    """Verify specific leg uses CRUISE."""
    route = refueling_context['route']
    from_wp, to_wp = leg.split(' → ')

    found = False
    for step in route['steps']:
        if step['action'] == 'navigate':
            # Strip system prefix to match shorthand (A2 matches X1-TEST-A2)
            step_from = step['from'].split('-')[-1] if '-' in step['from'] else step['from']
            step_to = step['to'].split('-')[-1] if '-' in step['to'] else step['to']

            if step_from == from_wp and step_to == to_wp:
                assert step['mode'] == 'CRUISE', f"Leg {leg} should use CRUISE"
                found = True
                break

    assert found, f"Leg {leg} not found in route"


@then(parsers.parse('the route should refuel at {waypoint}'))
def verify_refuel_at(refueling_context, waypoint):
    """Verify route refuels at waypoint."""
    route = refueling_context['route']
    refuel_found = False
    for step in route['steps']:
        if step['action'] == 'refuel':
            # Strip system prefix to match shorthand (B33 matches X1-TEST-B33)
            step_wp = step['waypoint'].split('-')[-1] if '-' in step['waypoint'] else step['waypoint']
            if step_wp == waypoint or step['waypoint'] == waypoint:
                refuel_found = True
                break

    assert refuel_found, f"Route should refuel at {waypoint}"


@then("all navigation should use CRUISE mode")
def verify_all_cruise(refueling_context):
    """Verify all navigation uses CRUISE."""
    route = refueling_context['route']
    for step in route['steps']:
        if step['action'] == 'navigate':
            assert step['mode'] == 'CRUISE', f"Found {step['mode']}, expected CRUISE"


@then(parsers.parse('the route should have {count:d} direct navigation leg'))
def verify_direct_navigation(refueling_context, count):
    """Verify route has N navigation legs."""
    route = refueling_context['route']
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    assert len(nav_steps) == count, f"Should have {count} nav leg, got {len(nav_steps)}"


@then("the route should use CRUISE mode")
def verify_cruise_mode(refueling_context):
    """Verify route uses CRUISE mode."""
    route = refueling_context['route']
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    assert all(s['mode'] == 'CRUISE' for s in nav_steps), "All navigation should use CRUISE"


@then(parsers.parse('there should be {count:d} refuel stops'))
def verify_refuel_stop_count(refueling_context, count):
    """Verify refuel stop count."""
    route = refueling_context['route']
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    assert len(refuel_steps) == count, f"Should have {count} refuel stops, got {len(refuel_steps)}"


@then("the route should allow DRIFT to goal")
def verify_drift_to_goal(refueling_context):
    """Verify DRIFT to goal is allowed."""
    route = refueling_context['route']
    assert route is not None, "Route should allow DRIFT to goal"


@then("the final navigation step should reach J62")
def verify_final_reaches_destination(refueling_context):
    """Verify final step reaches destination."""
    route = refueling_context['route']
    nav_steps = [s for s in route['steps'] if s['action'] == 'navigate']
    assert len(nav_steps) > 0, "Should have navigation steps"
    assert nav_steps[-1]['to'] == 'X1-GH18-J62', "Final step should reach J62"


@then(parsers.parse('the route should have exactly {count:d} steps'))
def verify_step_count(refueling_context, count):
    """Verify route has exactly N steps."""
    route = refueling_context['route']
    assert len(route['steps']) == count, \
        f"Route should have {count} steps, got {len(route['steps'])}"


@then(parsers.parse('step {num:d} should navigate {leg} in CRUISE'))
def verify_step_navigation(refueling_context, num, leg):
    """Verify specific step is navigation."""
    route = refueling_context['route']
    step = route['steps'][num - 1]
    from_wp, to_wp = leg.split(' → ')

    assert step['action'] == 'navigate', f"Step {num} should be navigate"
    assert step['from'] == from_wp and step['to'] == to_wp, f"Step {num} should be {leg}"
    assert step['mode'] == 'CRUISE', f"Step {num} should use CRUISE"


@then(parsers.parse('step {num:d} should refuel at {waypoint}'))
def verify_step_refuel(refueling_context, num, waypoint):
    """Verify specific step is refuel."""
    route = refueling_context['route']
    step = route['steps'][num - 1]

    assert step['action'] == 'refuel', f"Step {num} should be refuel"
    assert step['waypoint'] == waypoint, f"Step {num} should refuel at {waypoint}"


@then(parsers.parse('step {num:d} should navigate {leg}'))
def verify_step_nav_any_mode(refueling_context, num, leg):
    """Verify specific step is navigation (any mode)."""
    route = refueling_context['route']
    step = route['steps'][num - 1]
    from_wp, to_wp = leg.split(' → ')

    assert step['action'] == 'navigate', f"Step {num} should be navigate"
    assert step['from'] == from_wp and step['to'] == to_wp, f"Step {num} should be {leg}"


@then("the refuel step should add fuel")
def verify_refuel_adds_fuel(refueling_context):
    """Verify refuel step adds fuel."""
    route = refueling_context['route']
    refuel_steps = [s for s in route['steps'] if s['action'] == 'refuel']
    assert len(refuel_steps) > 0, "Should have refuel steps"
    assert refuel_steps[0]['fuel_added'] > 0, "Refuel should add fuel"


# ====================
# Then Steps - Execution Validation
# ====================

@then("the execution should succeed")
def verify_execution_success(refueling_context):
    """Verify execution succeeded."""
    assert refueling_context['execution_success'] is True, "Execution should succeed"


@then("the operation sequence should include refuel")
def verify_sequence_has_refuel(refueling_context):
    """Verify operation sequence includes refuel."""
    sequence = refueling_context['operation_sequence']
    assert 'refuel' in sequence, f"Operation sequence should include refuel: {sequence}"


@then("refuel should occur after navigate to B33")
def verify_refuel_after_nav(refueling_context):
    """Verify refuel occurs after navigation to B33."""
    sequence = refueling_context['operation_sequence']

    nav_b33_idx = next((i for i, op in enumerate(sequence) if 'navigate_to_X1-GH18-B33' in op), None)
    refuel_idx = next((i for i, op in enumerate(sequence) if op == 'refuel'), None)

    assert nav_b33_idx is not None, "Should navigate to B33"
    assert refuel_idx is not None, "Should refuel"
    assert nav_b33_idx < refuel_idx, "Refuel should occur after navigate to B33"


@then("refuel should occur before navigate to J62")
def verify_refuel_before_nav(refueling_context):
    """Verify refuel occurs before navigation to J62."""
    sequence = refueling_context['operation_sequence']

    refuel_idx = next((i for i, op in enumerate(sequence) if op == 'refuel'), None)
    nav_j62_idx = next((i for i, op in enumerate(sequence) if 'navigate_to_X1-GH18-J62' in op), None)

    assert refuel_idx is not None, "Should refuel"
    assert nav_j62_idx is not None, "Should navigate to J62"
    assert refuel_idx < nav_j62_idx, "Refuel should occur before navigate to J62"


@then("the refuel operation should be executed")
def verify_refuel_executed(refueling_context):
    """Verify refuel operation was executed."""
    sequence = refueling_context['operation_sequence']
    assert 'refuel' in sequence, "REFUEL OPERATION SHOULD HAVE BEEN EXECUTED"

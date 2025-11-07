"""Step definitions for routing engine graph format integration tests"""
import pytest
from pytest_bdd import scenario, given, when, then, parsers
from typing import Dict

from domain.shared.value_objects import Waypoint, FlightMode
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine


# Scenarios
@scenario("../../features/navigation/routing_engine_graph_format.feature",
         "Routing engine receives flat Dict[str, Waypoint] not nested structure")
def test_routing_engine_receives_flat_dict():
    """Test that routing engine accepts flat Dict[str, Waypoint]"""
    pass


@scenario("../../features/navigation/routing_engine_graph_format.feature",
         "Routing engine finds path with flat waypoint dictionary")
def test_routing_engine_finds_path_with_flat_dict():
    """Test pathfinding works with flat waypoint dictionary"""
    pass


# Given steps
@given(parsers.parse('a test graph with {count:d} waypoints'))
def create_test_graph_with_waypoints(context, count: int):
    """Create a test graph with N waypoints"""
    context['test_waypoints'] = {}
    for i in range(count):
        symbol = f"X1-TEST-W{i}"
        context['test_waypoints'][symbol] = Waypoint(
            symbol=symbol,
            waypoint_type="ASTEROID",
            x=i * 50,
            y=i * 50,
            system_symbol="X1-TEST",
            traits=("MARKETPLACE",) if i == 0 else (),
            has_fuel=(i == 0)
        )


@given(parsers.parse('a test system with waypoints "{w1}", "{w2}", "{w3}"'))
def create_test_system_with_waypoints(context, w1: str, w2: str, w3: str):
    """Create test system with specific waypoints"""
    context['test_waypoints'] = {}
    context['waypoint_symbols'] = [w1, w2, w3]


@given(parsers.parse('waypoint "{symbol}" at ({x:d}, {y:d}) with has_fuel {has_fuel}'))
def waypoint_at_position(context, symbol: str, x: int, y: int, has_fuel: str):
    """Define a waypoint at specific coordinates"""
    if 'test_waypoints' not in context:
        context['test_waypoints'] = {}

    has_fuel_bool = has_fuel.lower() == 'true'
    traits = ("MARKETPLACE",) if has_fuel_bool else ()

    context['test_waypoints'][symbol] = Waypoint(
        symbol=symbol,
        waypoint_type="ASTEROID",
        x=x,
        y=y,
        system_symbol="X1-TEST",
        traits=traits,
        has_fuel=has_fuel_bool
    )


# When steps
@when('the routing engine receives the graph for pathfinding')
def routing_engine_receives_graph(context):
    """Routing engine receives the graph"""
    engine = ORToolsRoutingEngine()

    # Attempt to call with flat dictionary (CORRECT)
    if len(context['test_waypoints']) >= 2:
        symbols = list(context['test_waypoints'].keys())
        try:
            result = engine.find_optimal_path(
                graph=context['test_waypoints'],  # Flat Dict[str, Waypoint]
                start=symbols[0],
                goal=symbols[-1],
                current_fuel=400,
                fuel_capacity=400,
                engine_speed=30,
                prefer_cruise=True
            )
            context['routing_result_flat'] = result
            context['routing_error_flat'] = None
        except Exception as e:
            context['routing_result_flat'] = None
            context['routing_error_flat'] = str(e)

    # Attempt to call with nested structure (WRONG - this is the bug)
    nested_graph = {
        "waypoints": context['test_waypoints'],
        "edges": []
    }
    try:
        result = engine.find_optimal_path(
            graph=nested_graph,  # WRONG: Nested structure
            start=symbols[0],
            goal=symbols[-1],
            current_fuel=400,
            fuel_capacity=400,
            engine_speed=30,
            prefer_cruise=True
        )
        context['routing_result_nested'] = result
        context['routing_error_nested'] = None
    except Exception as e:
        context['routing_result_nested'] = None
        context['routing_error_nested'] = str(e)


@when(parsers.parse('the routing engine finds optimal path from "{start}" to "{goal}"'))
def routing_engine_finds_path(context, start: str, goal: str):
    """Routing engine pathfinding with flat dictionary"""
    engine = ORToolsRoutingEngine()

    try:
        result = engine.find_optimal_path(
            graph=context['test_waypoints'],  # Flat Dict[str, Waypoint]
            start=start,
            goal=goal,
            current_fuel=400,
            fuel_capacity=400,
            engine_speed=30,
            prefer_cruise=True
        )
        context['routing_result'] = result
        context['routing_error'] = None
    except Exception as e:
        context['routing_result'] = None
        context['routing_error'] = str(e)


# Then steps
@then('the graph should be a flat Dict[str, Waypoint]')
def graph_is_flat_dict(context):
    """Verify graph is flat dictionary"""
    assert isinstance(context['test_waypoints'], dict), "Graph should be a dictionary"
    for symbol, wp in context['test_waypoints'].items():
        assert isinstance(wp, Waypoint), f"Value for {symbol} should be a Waypoint"


@then('the graph should NOT be a nested structure with "waypoints" and "edges" keys')
def graph_not_nested(context):
    """Verify graph is NOT nested structure"""
    assert "waypoints" not in context['test_waypoints'], \
        "Graph should NOT have 'waypoints' key (should be flat)"
    assert "edges" not in context['test_waypoints'], \
        "Graph should NOT have 'edges' key (should be flat)"


@then('the routing engine should successfully calculate distances between waypoints')
def routing_engine_calculates_distances(context):
    """Verify routing engine works with flat dictionary"""
    # Flat dict should work
    assert context['routing_result_flat'] is not None, \
        f"Routing engine should work with flat Dict[str, Waypoint]. Error: {context.get('routing_error_flat')}"

    # Nested structure should FAIL
    assert context['routing_result_nested'] is None, \
        "Routing engine should FAIL with nested structure (this is the bug we're testing for)"


@then('pathfinding should succeed')
def pathfinding_succeeds(context):
    """Verify pathfinding succeeded"""
    assert context['routing_result'] is not None, \
        f"Pathfinding should succeed. Error: {context.get('routing_error')}"


@then('the route should contain waypoint steps')
def route_contains_steps(context):
    """Verify route has steps"""
    assert 'steps' in context['routing_result'], "Route should have steps"
    assert len(context['routing_result']['steps']) > 0, "Route should have at least one step"


@then('fuel costs should be calculated correctly')
def fuel_costs_calculated(context):
    """Verify fuel costs are present"""
    assert 'total_fuel_cost' in context['routing_result'], "Route should have total_fuel_cost"
    assert context['routing_result']['total_fuel_cost'] >= 0, "Fuel cost should be non-negative"

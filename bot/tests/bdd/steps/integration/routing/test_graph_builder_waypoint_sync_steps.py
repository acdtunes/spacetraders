"""
BDD step definitions for GraphBuilder waypoint synchronization.

Tests the split-caching strategy where GraphBuilder:
1. Returns structure-only graph data (for navigation)
2. Saves full waypoint trait data via waypoint repository (for queries)
"""

import json
from datetime import datetime, timezone
from typing import Any, Dict, List
from unittest.mock import AsyncMock, MagicMock

import pytest
from pytest_bdd import given, parsers, scenarios, then, when

from adapters.secondary.persistence.database import Database
from adapters.secondary.routing.graph_builder import GraphBuilder
from configuration.container import reset_container

scenarios("../../../features/integration/routing/graph_builder_waypoint_sync.feature")


class GraphBuilderContext:
    """Shared context for graph builder test scenarios."""

    def __init__(self):
        self.database: Database = None
        self.graph_builder: GraphBuilder = None
        self.mock_api_client: MagicMock = None
        self.mock_waypoint_repository: MagicMock = None
        self.player_id: int = 1
        self.system_symbol: str = "X1-TEST"
        self.built_graph: Dict[str, Any] = None


@pytest.fixture
def context():
    """Fixture providing test context."""
    reset_container()
    ctx = GraphBuilderContext()
    yield ctx
    reset_container()


# Background Steps


@given("a clean database")
def clean_database(context: GraphBuilderContext):
    """Initialize a clean in-memory database."""
    context.database = Database(":memory:")


@given('a mock API client that returns waypoints for system "X1-TEST"')
def mock_api_client_with_waypoints(context: GraphBuilderContext):
    """Create mock API client that returns test waypoints."""
    context.mock_api_client = MagicMock()

    # Mock waypoint data matching the scenario
    mock_waypoints = [
        {
            "symbol": "X1-TEST-A1",
            "x": 0,
            "y": 0,
            "type": "PLANET",
            "systemSymbol": "X1-TEST",
            "orbitals": [{"symbol": "X1-TEST-B2"}],
            "traits": [{"symbol": "MARKETPLACE"}],
        },
        {
            "symbol": "X1-TEST-B2",
            "x": 100,
            "y": 50,
            "type": "MOON",
            "systemSymbol": "X1-TEST",
            "orbitals": [],
            "traits": [{"symbol": "SHIPYARD"}],
        },
        {
            "symbol": "X1-TEST-C3",
            "x": 50,
            "y": 100,
            "type": "ASTEROID_FIELD",
            "systemSymbol": "X1-TEST",
            "orbitals": [],
            "traits": [{"symbol": "MARKETPLACE"}],
        },
    ]

    # Mock API response format with pagination metadata
    mock_response = {
        "data": mock_waypoints,
        "meta": {
            "total": 3,
            "page": 1,
            "limit": 20,
        }
    }

    context.mock_api_client.list_waypoints.return_value = mock_response


# Scenario 1: Graph builder returns structure data and saves waypoint traits


@when(parsers.parse('I build a system graph for "{system}" with player_id {player_id:d}'))
def build_system_graph(context: GraphBuilderContext, system: str, player_id: int):
    """Build a system graph using GraphBuilder."""
    context.system_symbol = system
    context.player_id = player_id

    # Create mock waypoint repository
    context.mock_waypoint_repository = MagicMock()
    context.mock_waypoint_repository.save_waypoints = MagicMock()

    # Create mock factories
    def api_client_factory(pid: int):
        return context.mock_api_client

    def waypoint_repository_factory(pid: int):
        return context.mock_waypoint_repository

    # Create GraphBuilder with dependencies
    context.graph_builder = GraphBuilder(
        api_client_factory=api_client_factory,
        waypoint_repository_factory=waypoint_repository_factory,
    )

    # Build the graph
    context.built_graph = context.graph_builder.build_system_graph(
        system_symbol=system, player_id=player_id
    )


@then("the returned graph should contain structure data only")
def verify_graph_has_structure_data(context: GraphBuilderContext):
    """Verify returned graph has basic structure."""
    assert context.built_graph is not None, "Graph was not built"
    assert "system" in context.built_graph, "Missing system in graph"
    assert "waypoints" in context.built_graph, "Missing waypoints in graph"
    assert "edges" in context.built_graph, "Missing edges in graph"


@then(parsers.parse("the returned graph should have {count:d} waypoints"))
def verify_graph_waypoint_count(context: GraphBuilderContext, count: int):
    """Verify number of waypoints in returned graph."""
    actual_count = len(context.built_graph["waypoints"])
    assert actual_count == count, f"Expected {count} waypoints, got {actual_count}"


@then("the returned graph structure should not contain traits or has_fuel")
def verify_graph_no_traits(context: GraphBuilderContext):
    """Verify graph waypoints don't contain trait data."""
    for symbol, waypoint in context.built_graph["waypoints"].items():
        assert "traits" not in waypoint, f"Waypoint {symbol} should not have traits"
        assert "has_fuel" not in waypoint, f"Waypoint {symbol} should not have has_fuel"


@then(parsers.parse("the waypoint repository should have been called to save {count:d} waypoints"))
def verify_repository_called(context: GraphBuilderContext, count: int):
    """Verify waypoint repository save was called with correct number of waypoints."""
    assert (
        context.mock_waypoint_repository.save_waypoints.called
    ), "save_waypoints was not called"

    call_args = context.mock_waypoint_repository.save_waypoints.call_args
    waypoint_objects = call_args[0][0]  # First positional argument (waypoints list)

    assert (
        len(waypoint_objects) == count
    ), f"Expected {count} waypoints, got {len(waypoint_objects)}"


@then("the saved waypoints should contain trait data")
def verify_saved_waypoints_have_traits(context: GraphBuilderContext):
    """Verify saved waypoint objects contain trait data."""
    call_args = context.mock_waypoint_repository.save_waypoints.call_args
    waypoint_objects = call_args[0][0]

    for waypoint in waypoint_objects:
        assert hasattr(waypoint, "traits"), f"Waypoint {waypoint.symbol} missing traits"
        assert hasattr(waypoint, "has_fuel"), f"Waypoint {waypoint.symbol} missing has_fuel"


@then(parsers.parse('saved waypoint "{symbol}" should have has_fuel {expected}'))
def verify_saved_waypoint_has_fuel(context: GraphBuilderContext, symbol: str, expected: str):
    """Verify specific saved waypoint has_fuel flag."""
    expected_bool = expected.lower() == "true"

    call_args = context.mock_waypoint_repository.save_waypoints.call_args
    waypoint_objects = call_args[0][0]

    target_waypoint = next((wp for wp in waypoint_objects if wp.symbol == symbol), None)
    assert target_waypoint is not None, f"Waypoint {symbol} not found in saved waypoints"
    assert (
        target_waypoint.has_fuel == expected_bool
    ), f"Expected has_fuel={expected_bool}, got {target_waypoint.has_fuel}"


# Scenario 2: Returned graph excludes traits, saved waypoints include traits


@then("the returned graph waypoints should contain only x, y, type, systemSymbol, orbitals")
def verify_graph_waypoint_fields(context: GraphBuilderContext):
    """Verify graph waypoints have only structure fields."""
    expected_fields = {"x", "y", "type", "systemSymbol"}
    optional_fields = {"orbitals"}

    for symbol, waypoint in context.built_graph["waypoints"].items():
        waypoint_fields = set(waypoint.keys())

        # Must have core navigation fields
        missing_fields = expected_fields - waypoint_fields
        assert (
            not missing_fields
        ), f"Waypoint {symbol} missing fields: {missing_fields}"

        # May have orbitals
        extra_fields = waypoint_fields - expected_fields - optional_fields
        assert not extra_fields, f"Waypoint {symbol} has unexpected fields: {extra_fields}"


@then("the returned graph waypoints should not contain traits or has_fuel")
def verify_graph_waypoints_no_trait_fields(context: GraphBuilderContext):
    """Verify graph waypoints exclude trait fields."""
    for symbol, waypoint in context.built_graph["waypoints"].items():
        assert (
            "traits" not in waypoint
        ), f"Waypoint {symbol} should not have traits in graph structure"
        assert (
            "has_fuel" not in waypoint
        ), f"Waypoint {symbol} should not have has_fuel in graph structure"


@then("the saved waypoint objects should have traits attribute")
def verify_saved_waypoints_have_traits_attr(context: GraphBuilderContext):
    """Verify saved waypoint objects have traits attribute."""
    call_args = context.mock_waypoint_repository.save_waypoints.call_args
    waypoint_objects = call_args[0][0]

    for waypoint in waypoint_objects:
        assert hasattr(waypoint, "traits"), f"Waypoint {waypoint.symbol} missing traits attribute"
        assert isinstance(
            waypoint.traits, tuple
        ), f"Waypoint {waypoint.symbol} traits should be tuple"


@then("the saved waypoint objects should have has_fuel attribute")
def verify_saved_waypoints_have_has_fuel_attr(context: GraphBuilderContext):
    """Verify saved waypoint objects have has_fuel attribute."""
    call_args = context.mock_waypoint_repository.save_waypoints.call_args
    waypoint_objects = call_args[0][0]

    for waypoint in waypoint_objects:
        assert hasattr(
            waypoint, "has_fuel"
        ), f"Waypoint {waypoint.symbol} missing has_fuel attribute"
        assert isinstance(
            waypoint.has_fuel, bool
        ), f"Waypoint {waypoint.symbol} has_fuel should be bool"

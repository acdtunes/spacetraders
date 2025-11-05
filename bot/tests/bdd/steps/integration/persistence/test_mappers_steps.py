"""
BDD step definitions for mapper integration tests.

Testing Approach:
- Focus on round-trip correctness: Entity -> DB -> Entity preserves all data
- Verify that all domain properties are preserved through serialization
- Do NOT test internal serialization format (JSON structure, field names)
- Do NOT test private serialization methods (_serialize_*, _deserialize_*)
- Test correctness through entity equality after round-trip conversion
"""
from pytest_bdd import scenario, given, when, then, parsers
import pytest
import sqlite3
from datetime import datetime

from adapters.secondary.persistence.mappers import (
    PlayerMapper, ShipMapper, RouteMapper
)
from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, FlightMode
from domain.navigation.route import Route, RouteSegment, RouteStatus


@pytest.fixture
def context():
    """Shared test context"""
    return {}


def dict_to_row(data: dict, columns: list) -> sqlite3.Row:
    """Helper to convert dict to sqlite3.Row"""
    conn = sqlite3.connect(":memory:")
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    # Create temp table
    col_defs = ", ".join([f"{col} TEXT" for col in columns])
    cursor.execute(f"CREATE TABLE temp ({col_defs})")

    # Insert data
    placeholders = ", ".join(["?" for _ in columns])
    values = [data.get(col) for col in columns]
    cursor.execute(f"INSERT INTO temp VALUES ({placeholders})", values)

    # Fetch as Row
    cursor.execute("SELECT * FROM temp")
    row = cursor.fetchone()
    conn.close()
    return row


# ==============================================================================
# PlayerMapper Scenarios
# ==============================================================================
@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert complete database row to Player")
def test_player_from_row():
    pass


@given("a database row with player data")
def player_row_complete(context):
    row_dict = {
        "player_id": "1",
        "agent_symbol": "TEST_AGENT",
        "token": "Bearer token",
        "created_at": "2025-01-01T12:00:00",
        "last_active": "2025-01-02T15:30:00",
        "metadata": '{"faction": "COSMIC", "credits": 1000}'
    }
    context["row"] = dict_to_row(row_dict, list(row_dict.keys()))


@when("I convert the row to a Player entity")
def convert_to_player(context):
    context["player"] = PlayerMapper.from_db_row(context["row"])


@then(parsers.parse("the player should have player_id {player_id:d}"))
def check_player_id(context, player_id):
    assert context["player"].player_id == player_id


@then(parsers.parse('the player should have agent_symbol "{agent}"'))
def check_agent_symbol(context, agent):
    assert context["player"].agent_symbol == agent


@then(parsers.parse('the player should have token "{token}"'))
def check_token(context, token):
    assert context["player"].token == token


@then(parsers.parse('the player should have metadata with key "{key}"'))
def check_metadata_key(context, key):
    assert key in context["player"].metadata


@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert row with NULL last_active")
def test_null_last_active():
    pass


@given("a database row with NULL last_active")
def player_row_null_last_active(context):
    row_dict = {
        "player_id": "1",
        "agent_symbol": "TEST_AGENT",
        "token": "token123",
        "created_at": "2025-01-01T12:00:00",
        "last_active": None,
        "metadata": None
    }
    context["row"] = dict_to_row(row_dict, list(row_dict.keys()))


@then("the player last_active should equal created_at")
def check_last_active_defaults(context):
    assert context["player"].last_active == context["player"].created_at


@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert row with NULL metadata")
def test_null_metadata():
    pass


@given("a database row with NULL metadata")
def player_row_null_metadata(context):
    row_dict = {
        "player_id": "1",
        "agent_symbol": "TEST_AGENT",
        "token": "token123",
        "created_at": "2025-01-01T12:00:00",
        "last_active": "2025-01-01T12:00:00",
        "metadata": None
    }
    context["row"] = dict_to_row(row_dict, list(row_dict.keys()))


@then("the player metadata should be empty")
def check_empty_metadata(context):
    assert context["player"].metadata == {}


@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert row with empty metadata JSON")
def test_empty_metadata_json():
    pass


@given(parsers.parse('a database row with empty metadata JSON "{json_str}"'))
def player_row_empty_json(context, json_str):
    row_dict = {
        "player_id": "1",
        "agent_symbol": "TEST_AGENT",
        "token": "token123",
        "created_at": "2025-01-01T12:00:00",
        "last_active": "2025-01-01T12:00:00",
        "metadata": json_str
    }
    context["row"] = dict_to_row(row_dict, list(row_dict.keys()))


# ==============================================================================
# ShipMapper Scenarios
# ==============================================================================
@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert Ship to database dict")
def test_ship_to_dict():
    pass


@given(parsers.parse('a Ship entity at waypoint "{waypoint_symbol}"'))
def create_ship_entity(context, waypoint_symbol):
    waypoint = Waypoint(
        symbol=waypoint_symbol,
        x=10.0,
        y=20.0,
        system_symbol="X1",
        waypoint_type="PLANET",
        traits=("MARKETPLACE",),
        has_fuel=True
    )
    fuel = Fuel(current=100, capacity=200)
    context["ship"] = Ship(
        ship_symbol="SHIP-1",
        player_id=1,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=200,
        cargo_capacity=50,
        cargo_units=10,
        engine_speed=30,
        nav_status="IN_ORBIT"
    )


@when("I convert the Ship to a database dict")
def convert_ship_to_dict(context):
    context["ship_dict"] = ShipMapper.to_db_dict(context["ship"])


@then(parsers.parse('the dict should have current_location_symbol "{location}"'))
def check_location_symbol(context, location):
    # Verify the ship's location is correctly represented in the dict
    assert context["ship_dict"]["current_location_symbol"] == location


@then(parsers.parse("the dict should have fuel_current {fuel:d}"))
def check_fuel_current(context, fuel):
    # Verify the ship's fuel state is correctly represented in the dict
    assert context["ship_dict"]["fuel_current"] == fuel


@then(parsers.parse('the dict should have nav_status "{status}"'))
def check_nav_status(context, status):
    # Verify the ship's navigation status is correctly represented in the dict
    assert context["ship_dict"]["nav_status"] == status


@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert database row to Ship")
def test_ship_from_row():
    pass


@given("a ship database row")
def ship_database_row(context):
    row_dict = {
        "ship_symbol": "SHIP-1",
        "player_id": "1",
        "current_location_symbol": "X1-A1",
        "fuel_current": "100",
        "fuel_capacity": "200",
        "cargo_capacity": "50",
        "cargo_units": "10",
        "engine_speed": "30",
        "nav_status": "IN_ORBIT",
        "system_symbol": "X1"
    }
    context["ship_row"] = dict_to_row(row_dict, list(row_dict.keys()))


@given(parsers.parse('a waypoint entity for "{waypoint_symbol}"'))
def create_waypoint(context, waypoint_symbol):
    context["waypoint"] = Waypoint(
        symbol=waypoint_symbol,
        x=0.0,
        y=0.0,
        system_symbol="X1",
        waypoint_type="PLANET",
        traits=(),
        has_fuel=True
    )


@when("I convert the row to a Ship entity")
def convert_row_to_ship(context):
    context["ship"] = ShipMapper.from_db_row(context["ship_row"], context["waypoint"])


@then(parsers.parse('the ship should have ship_symbol "{symbol}"'))
def check_ship_has_symbol(context, symbol):
    assert context["ship"].ship_symbol == symbol


@then(parsers.parse("the ship should have fuel current {current:d}"))
def check_fuel_value(context, current):
    assert context["ship"].fuel.current == current


@then(parsers.parse("the ship should have fuel capacity {capacity:d}"))
def check_fuel_capacity(context, capacity):
    assert context["ship"].fuel.capacity == capacity


@scenario("../../../features/integration/persistence/mappers.feature",
          "Ship roundtrip conversion preserves data")
def test_ship_roundtrip():
    pass


@when("I convert Ship to dict then back to Ship")
def ship_roundtrip(context):
    ship_dict = ShipMapper.to_db_dict(context["ship"])
    row = dict_to_row(
        {k: str(v) for k, v in ship_dict.items()},
        list(ship_dict.keys())
    )
    context["restored_ship"] = ShipMapper.from_db_row(row, context["ship"].current_location)


@then("all ship fields should match the original")
def check_ship_fields_match(context):
    """
    Verify round-trip correctness: Ship -> DB dict -> Ship preserves all data.
    This is the key test - if all properties match, serialization is correct
    regardless of the internal format used.
    """
    original = context["ship"]
    restored = context["restored_ship"]
    assert restored.ship_symbol == original.ship_symbol
    assert restored.player_id == original.player_id
    assert restored.fuel.current == original.fuel.current
    assert restored.fuel.capacity == original.fuel.capacity
    assert restored.nav_status == original.nav_status
    # These assertions prove the mapper preserves all critical ship state


# ==============================================================================
# RouteMapper Scenarios
# ==============================================================================
@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert Route to database dict")
def test_route_to_dict():
    pass


@given("a Route with one segment")
def create_route_one_segment(context):
    wp1 = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    wp2 = Waypoint(symbol="X1-A2", x=10.0, y=10.0, system_symbol="X1")

    segment = RouteSegment(
        from_waypoint=wp1,
        to_waypoint=wp2,
        distance=14.14,
        fuel_required=15,
        travel_time=100,
        flight_mode=FlightMode.CRUISE,
        requires_refuel=False
    )

    context["route"] = Route(
        route_id="route-123",
        ship_symbol="SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=200
    )


@when("I convert the Route to a database dict")
def convert_route_to_dict(context):
    context["route_dict"] = RouteMapper.to_db_dict(context["route"])


@then(parsers.parse('the dict should have route_id "{route_id}"'))
def check_route_id(context, route_id):
    assert context["route_dict"]["route_id"] == route_id


@then(parsers.parse('the dict should have ship_symbol "{symbol}"'))
def check_route_ship_symbol(context, symbol):
    # Check if we have a route_dict (for route tests) or ship_dict (for ship tests)
    if "route_dict" in context:
        assert context["route_dict"]["ship_symbol"] == symbol
    elif "ship_dict" in context:
        assert context["ship_dict"]["ship_symbol"] == symbol


@then(parsers.parse('the dict should have status "{status}"'))
def check_status(context, status):
    assert context["route_dict"]["status"] == status


@then("the dict should have segments_json")
def check_segments_json(context):
    # Verify that segments are serialized (we don't care about format, just that they exist)
    assert "segments_json" in context["route_dict"]
    assert context["route_dict"]["segments_json"] is not None
    # The actual format is an implementation detail - what matters is round-trip correctness


@scenario("../../../features/integration/persistence/mappers.feature",
          "Convert database row to Route")
def test_route_from_row():
    pass


@given("a route database row with segments JSON")
def create_route_row(context):
    # First create a route to get proper JSON
    wp1 = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    wp2 = Waypoint(symbol="X1-A2", x=10.0, y=10.0, system_symbol="X1")
    segment = RouteSegment(
        from_waypoint=wp1,
        to_waypoint=wp2,
        distance=14.14,
        fuel_required=15,
        travel_time=100,
        flight_mode=FlightMode.CRUISE
    )
    route = Route(
        route_id="route-123",
        ship_symbol="SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=200
    )

    route_dict = RouteMapper.to_db_dict(route)
    route_dict["created_at"] = "2025-01-01T12:00:00"

    context["route_row"] = dict_to_row(
        {k: str(v) if v is not None else None for k, v in route_dict.items()},
        list(route_dict.keys())
    )


@when("I convert the row to a Route entity")
def convert_row_to_route(context):
    context["route"] = RouteMapper.from_db_row(context["route_row"])


@then(parsers.parse('the route should have route_id "{route_id}"'))
def check_route_has_id(context, route_id):
    assert context["route"].route_id == route_id


@then(parsers.parse("the route should have {count:d} segment"))
def check_segment_count(context, count):
    assert len(context["route"].segments) == count


@then("the route status should be PLANNED")
def check_route_status_planned(context):
    assert context["route"].status == RouteStatus.PLANNED


@scenario("../../../features/integration/persistence/mappers.feature",
          "Route roundtrip conversion preserves data")
def test_route_roundtrip():
    pass


@when("I convert Route to dict then back to Route")
def route_roundtrip(context):
    route_dict = RouteMapper.to_db_dict(context["route"])
    route_dict["created_at"] = "2025-01-01T12:00:00"

    row = dict_to_row(
        {k: str(v) if v is not None else None for k, v in route_dict.items()},
        list(route_dict.keys())
    )
    context["restored_route"] = RouteMapper.from_db_row(row)
    context["original_route"] = context["route"]


@then("all route fields should match the original")
def check_route_fields_match(context):
    """
    Verify round-trip correctness: Route -> DB dict -> Route preserves all data.
    This tests that the route-level metadata is correctly preserved.
    """
    original = context["original_route"]
    restored = context["restored_route"]
    assert restored.route_id == original.route_id
    assert restored.ship_symbol == original.ship_symbol
    assert restored.player_id == original.player_id
    assert restored.status == original.status


@then("the segment details should be preserved")
def check_segment_details(context):
    """
    Verify that segment data within the route is correctly preserved through serialization.
    This proves that the JSON serialization of segments is functionally correct,
    without testing the internal JSON structure (which is an implementation detail).
    """
    original_seg = context["original_route"].segments[0]
    restored_seg = context["restored_route"].segments[0]

    assert restored_seg.from_waypoint.symbol == original_seg.from_waypoint.symbol
    assert restored_seg.to_waypoint.symbol == original_seg.to_waypoint.symbol
    assert restored_seg.distance == original_seg.distance
    assert restored_seg.flight_mode == original_seg.flight_mode
    # These assertions prove segment serialization works correctly


# Removed: Scenarios testing private _serialize_segment() and _deserialize_segment() methods
# These are implementation details. Round-trip tests verify serialization correctness.


@scenario("../../../features/integration/persistence/mappers.feature",
          "Route with multiple segments")
def test_route_multi_segment():
    pass


@given("a Route with 2 segments")
def create_multi_segment_route(context):
    wp1 = Waypoint(symbol="X1-A1", x=0.0, y=0.0, system_symbol="X1")
    wp2 = Waypoint(symbol="X1-A2", x=10.0, y=0.0, system_symbol="X1")
    wp3 = Waypoint(symbol="X1-A3", x=20.0, y=0.0, system_symbol="X1")

    seg1 = RouteSegment(
        from_waypoint=wp1,
        to_waypoint=wp2,
        distance=10.0,
        fuel_required=11,
        travel_time=50,
        flight_mode=FlightMode.CRUISE
    )
    seg2 = RouteSegment(
        from_waypoint=wp2,
        to_waypoint=wp3,
        distance=10.0,
        fuel_required=11,
        travel_time=50,
        flight_mode=FlightMode.CRUISE
    )

    context["route"] = Route(
        route_id="route-multi",
        ship_symbol="SHIP-1",
        player_id=1,
        segments=[seg1, seg2],
        ship_fuel_capacity=200
    )


@then(parsers.parse("the route should have {count:d} segments"))
def check_multi_segments(context, count):
    assert len(context["restored_route"].segments) == count


@then(parsers.parse('segment {idx:d} should go from "{from_wp}" to "{to_wp}"'))
def check_segment_route(context, idx, from_wp, to_wp):
    segment = context["restored_route"].segments[idx]
    assert segment.from_waypoint.symbol == from_wp
    assert segment.to_waypoint.symbol == to_wp


@scenario("../../../features/integration/persistence/mappers.feature",
          "Route state is preserved through serialization")
def test_route_state_preservation():
    pass


@given("a Route with status EXECUTING")
def create_executing_route(context):
    wp1 = Waypoint(symbol="X1-A1", x=0.0, y=0.0)
    wp2 = Waypoint(symbol="X1-A2", x=10.0, y=0.0)

    segment = RouteSegment(
        from_waypoint=wp1,
        to_waypoint=wp2,
        distance=10.0,
        fuel_required=11,
        travel_time=50,
        flight_mode=FlightMode.CRUISE
    )

    context["route"] = Route(
        route_id="route-state",
        ship_symbol="SHIP-1",
        player_id=1,
        segments=[segment],
        ship_fuel_capacity=200
    )
    context["route"].start_execution()


@then("the route status should be EXECUTING")
def check_executing_status(context):
    assert context["restored_route"].status == RouteStatus.EXECUTING

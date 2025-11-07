"""Step definitions for ship converter cargo inventory extraction tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from application.navigation.commands._ship_converter import convert_api_ship_to_entity
from domain.shared.value_objects import Waypoint

# Load scenarios
scenarios("../../../features/application/navigation/ship_converter_cargo.feature")


@given("a player with ID 1")
def player_id(context):
    """Set up player ID"""
    context["player_id"] = 1


@given("an API response with ship data containing cargo inventory")
def api_response_with_cargo_inventory(context, datatable):
    """Create API response with cargo inventory"""
    headers = datatable[0]
    inventory = []
    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        inventory.append({
            "symbol": row_dict["symbol"],
            "name": row_dict["name"],
            "units": int(row_dict["units"])
        })

    # Calculate total cargo units
    total_units = sum(item["units"] for item in inventory)

    context["api_response"] = {
        "symbol": "TEST_SHIP-1",
        "nav": {
            "status": "IN_ORBIT",
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST"
        },
        "fuel": {
            "current": 100,
            "capacity": 100
        },
        "cargo": {
            "capacity": 50,
            "units": total_units,
            "inventory": inventory
        },
        "engine": {
            "speed": 30
        }
    }


@given("an API response with ship data containing empty cargo")
def api_response_with_empty_cargo(context):
    """Create API response with empty cargo"""
    context["api_response"] = {
        "symbol": "TEST_SHIP-1",
        "nav": {
            "status": "IN_ORBIT",
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST"
        },
        "fuel": {
            "current": 100,
            "capacity": 100
        },
        "cargo": {
            "capacity": 50,
            "units": 0,
            "inventory": []
        },
        "engine": {
            "speed": 30
        }
    }


@given("an API response with ship data containing cargo units but no inventory array")
def api_response_without_inventory_field(context):
    """Create API response with cargo units but no inventory field"""
    context["api_response"] = {
        "symbol": "TEST_SHIP-1",
        "nav": {
            "status": "IN_ORBIT",
            "waypointSymbol": "X1-TEST-A1",
            "systemSymbol": "X1-TEST"
        },
        "fuel": {
            "current": 100,
            "capacity": 100
        },
        "cargo": {
            "capacity": 50,
            "units": 0
            # No "inventory" field
        },
        "engine": {
            "speed": 30
        }
    }


@when("I convert the API response to a Ship entity")
def convert_api_response(context):
    """Convert API response to Ship entity"""
    api_response = context["api_response"]
    player_id = context["player_id"]

    # Create waypoint from nav data
    nav = api_response["nav"]
    waypoint = Waypoint(
        symbol=nav["waypointSymbol"],
        x=0,  # Not critical for this test
        y=0,
        system_symbol=nav["systemSymbol"]
    )

    # Convert to Ship entity
    ship = convert_api_ship_to_entity(api_response, player_id, waypoint)
    context["ship"] = ship


@then(parsers.parse("the ship should have cargo with {count:d} items"))
def verify_cargo_item_count(context, count):
    """Verify cargo has expected number of items"""
    ship = context["ship"]
    assert ship.cargo is not None, "Ship cargo should not be None"
    assert len(ship.cargo.inventory) == count, \
        f"Expected {count} cargo items, got {len(ship.cargo.inventory)}"


@then(parsers.parse('the cargo should contain "{symbol}" with {units:d} units'))
def verify_cargo_contains_item(context, symbol, units):
    """Verify cargo contains specific item with expected units"""
    ship = context["ship"]
    cargo_item = next((item for item in ship.cargo.inventory if item.symbol == symbol), None)
    assert cargo_item is not None, f"Cargo should contain {symbol}"
    assert cargo_item.units == units, \
        f"Expected {symbol} to have {units} units, got {cargo_item.units}"


@then(parsers.parse('the cargo should NOT contain "{symbol}" items'))
def verify_cargo_does_not_contain(context, symbol):
    """Verify cargo does not contain specific item type"""
    ship = context["ship"]
    has_symbol = any(item.symbol == symbol for item in ship.cargo.inventory)
    assert not has_symbol, f"Cargo should not contain {symbol} items"


@then("the cargo units should be 0")
def verify_cargo_units_zero(context):
    """Verify cargo units are zero"""
    ship = context["ship"]
    assert ship.cargo.units == 0, f"Expected cargo units to be 0, got {ship.cargo.units}"

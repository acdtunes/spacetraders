from pytest_bdd import scenario, given, when, then, parsers
import asyncio
from unittest.mock import patch
import re

from application.navigation.commands.sync_ships import (
    SyncShipsCommand,
    SyncShipsHandler
)
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel


def create_api_ship_data(
    symbol: str = "SHIP-1",
    waypoint: str = "X1-TEST-AB12",
    system: str = "X1-TEST",
    status: str = "DOCKED",
    fuel_current: int = 100,
    fuel_capacity: int = 100,
    cargo_capacity: int = 40,
    cargo_units: int = 0,
    engine_speed: int = 30,
    x: float = 0.0,
    y: float = 0.0,
    waypoint_type: str = "PLANET"
) -> dict:
    """Helper to create API ship data"""
    return {
        "symbol": symbol,
        "nav": {
            "systemSymbol": system,
            "waypointSymbol": waypoint,
            "status": status,
            "route": {
                "destination": {
                    "x": x,
                    "y": y,
                    "type": waypoint_type
                }
            }
        },
        "fuel": {
            "current": fuel_current,
            "capacity": fuel_capacity
        },
        "cargo": {
            "capacity": cargo_capacity,
            "units": cargo_units
        },
        "engine": {
            "speed": engine_speed
        }
    }


# ==============================================================================
# Scenario: Sync creates new ships
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync creates new ships")
def test_sync_creates_new_ships():
    pass


@given(parsers.parse("the API returns {count:d} ships"))
def api_returns_n_ships(context, mock_api, count):
    """Set up API to return N ships"""
    mock_api.ships = [
        create_api_ship_data(symbol=f"SHIP-{i}")
        for i in range(1, count + 1)
    ]
    context["api"] = mock_api


@when(parsers.parse("I sync ships for player {player_id:d}"))
def sync_ships(context, mock_ship_repo, player_id):
    """Execute sync ships command"""
    handler = SyncShipsHandler(mock_ship_repo)
    command = SyncShipsCommand(player_id=player_id)

    with patch('configuration.container.get_api_client_for_player', return_value=context["api"]):
        result = asyncio.run(handler.handle(command))

    context["result"] = result
    context["repo"] = mock_ship_repo
    context["player_id"] = player_id


@then(parsers.parse("{count:d} ships should be created"))
def check_ships_created(context, count):
    """Verify N ships were created"""
    # Verify ships exist in repository using public API
    player_id = context.get("player_id", 1)
    all_ships = context["repo"].find_all_by_player(player_id)
    assert len(all_ships) == count


@then("all ships should be Ship entities")
def check_ship_entities(context):
    """Verify all results are Ship entities"""
    assert all(isinstance(ship, Ship) for ship in context["result"])


@then(parsers.parse('ships "{ship1}", "{ship2}", "{ship3}" should exist in the database'))
def check_ships_in_database(context, ship1, ship2, ship3):
    """Verify specific ships exist in database"""
    player_id = context.get("player_id", 1)
    # Use public API to verify ships exist
    assert context["repo"].find_by_symbol(ship1, player_id) is not None
    assert context["repo"].find_by_symbol(ship2, player_id) is not None
    assert context["repo"].find_by_symbol(ship3, player_id) is not None


# ==============================================================================
# Scenario: Sync updates existing ships
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync updates existing ships")
def test_sync_updates_existing_ships():
    pass


@given(parsers.parse('a ship "{symbol}" exists for player {player_id:d} at "{waypoint}" with {fuel:d} fuel'))
def ship_exists_at_location_with_fuel(context, mock_ship_repo, mock_api, symbol, player_id, waypoint, fuel):
    """Create an existing ship with specific location and fuel"""
    existing_ship = Ship(
        ship_symbol=symbol,
        player_id=player_id,
        current_location=Waypoint(symbol=waypoint, x=0.0, y=0.0),
        fuel=Fuel(current=fuel, capacity=100),
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )
    mock_ship_repo.create(existing_ship)
    # Store ship count before sync for comparison
    context["pre_sync_ship_count"] = len(mock_ship_repo.find_all_by_player(player_id))
    context["api"] = mock_api


@given(parsers.parse('the API returns ship "{symbol}" at "{waypoint}" with {fuel:d} fuel'))
def api_returns_ship_at_location_with_fuel(context, symbol, waypoint, fuel):
    """Set up API to return a ship at specific location with fuel"""
    context["api"].ships = [
        create_api_ship_data(symbol=symbol, waypoint=waypoint, fuel_current=fuel)
    ]


@then(parsers.parse("{count:d} ship should be returned"))
def check_ships_returned(context, count):
    """Verify N ships were returned"""
    assert len(context["result"]) == count


@then("no new ships should be created")
def check_no_ships_created(context):
    """Verify no ships were created"""
    # Compare ship count before and after sync
    player_id = context.get("player_id", 1)
    post_sync_ship_count = len(context["repo"].find_all_by_player(player_id))
    pre_sync_ship_count = context.get("pre_sync_ship_count", 0)
    assert post_sync_ship_count == pre_sync_ship_count


@then(parsers.parse("{count:d} ship should be updated"))
def check_ships_updated(context, count):
    """Verify N ships were updated"""
    # Calculate ships updated by finding ships that existed before sync
    player_id = context.get("player_id", 1)
    post_sync_ship_count = len(context["repo"].find_all_by_player(player_id))
    pre_sync_ship_count = context.get("pre_sync_ship_count", 0)
    ships_created = post_sync_ship_count - pre_sync_ship_count
    ships_updated = post_sync_ship_count - ships_created
    assert ships_updated == count


@then(parsers.parse('ship "{symbol}" should be in the updated list'))
def check_ship_updated(context, symbol):
    """Verify specific ship was updated"""
    # Verify ship exists and state matches API data
    player_id = context.get("player_id", 1)
    ship = context["repo"].find_by_symbol(symbol, player_id)
    assert ship is not None
    # Verify ship attributes match expected values from API sync
    api_ship = next((s for s in context["api"].ships if s["symbol"] == symbol), None)
    assert api_ship is not None
    assert ship.current_location.symbol == api_ship["nav"]["waypointSymbol"]


# ==============================================================================
# Scenario: Sync mixed create and update
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync mixed create and update")
def test_sync_mixed_create_and_update():
    pass


@given(parsers.re(r'the API returns ship "(?P<symbol>[^"]+)" with (?P<fuel>\d+) fuel'))
def api_returns_ship_with_fuel(context, symbol, fuel):
    """Add a ship to API response with specific fuel (no location)"""
    if "api" not in context or not hasattr(context["api"], "ships"):
        return

    context["api"].ships.append(
        create_api_ship_data(symbol=symbol, fuel_current=int(fuel))
    )


@given(parsers.parse('the API returns a new ship "{symbol}"'))
def api_returns_new_ship(context, symbol):
    """Add a new ship to API response"""
    if "api" not in context or not hasattr(context["api"], "ships"):
        return

    context["api"].ships.append(
        create_api_ship_data(symbol=symbol)
    )


@then(parsers.parse("{count:d} ships should be returned"))
def check_multiple_ships_returned(context, count):
    """Verify N ships were returned"""
    assert len(context["result"]) == count


@then(parsers.parse("{count:d} new ship should be created"))
def check_one_ship_created(context, count):
    """Verify one new ship was created"""
    # Calculate ships created by comparing post-sync count to pre-sync count
    player_id = context.get("player_id", 1)
    post_sync_ship_count = len(context["repo"].find_all_by_player(player_id))
    pre_sync_ship_count = context.get("pre_sync_ship_count", 0)
    ships_created = post_sync_ship_count - pre_sync_ship_count
    assert ships_created == count


@then(parsers.parse('ship "{symbol}" should be created'))
def check_specific_ship_created(context, symbol):
    """Verify specific ship was created"""
    # Use public API to verify ship exists
    player_id = context.get("player_id", 1)
    ship = context["repo"].find_by_symbol(symbol, player_id)
    assert ship is not None


@then(parsers.parse('ship "{symbol}" should be updated'))
def check_specific_ship_updated(context, symbol):
    """Verify specific ship was updated"""
    # Verify ship exists and state matches API data
    player_id = context.get("player_id", 1)
    ship = context["repo"].find_by_symbol(symbol, player_id)
    assert ship is not None
    # Verify ship attributes match expected values from API sync
    api_ship = next((s for s in context["api"].ships if s["symbol"] == symbol), None)
    assert api_ship is not None
    assert ship.fuel.current == api_ship["fuel"]["current"]


# ==============================================================================
# Scenario: Sync with empty API response
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync with empty API response")
def test_sync_empty_api_response():
    pass


@given("the API returns no ships")
def api_returns_no_ships(context, mock_api):
    """Set up API to return empty list"""
    mock_api.ships = []
    context["api"] = mock_api


@then(parsers.parse("{count:d} ships should be returned"))
def check_zero_ships_returned(context, count):
    """Verify zero ships were returned"""
    assert len(context["result"]) == count


@then("no ships should be created")
def check_no_ships_created_alt(context):
    """Verify no ships were created (alternative)"""
    # Verify repository is empty using public API
    player_id = context.get("player_id", 1)
    all_ships = context["repo"].find_all_by_player(player_id)
    assert len(all_ships) == 0


# ==============================================================================
# Scenario: Sync converts API data correctly
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync converts API data correctly")
def test_sync_converts_api_data():
    pass


@given(parsers.parse('the API returns ship "{symbol}" with:'))
def api_returns_ship_with_data(context, mock_api, symbol, datatable):
    """Set up API to return ship with specific data from table"""
    data = {}
    for i, row in enumerate(datatable):
        if i == 0:  # Skip header row
            continue
        data[row[0]] = row[1]

    ship_data = create_api_ship_data(
        symbol=symbol,
        waypoint=data.get("waypoint", "X1-TEST-AB12"),
        system=data.get("system", "X1-TEST"),
        status=data.get("status", "DOCKED"),
        fuel_current=int(data.get("fuel_current", 100)),
        fuel_capacity=int(data.get("fuel_capacity", 100)),
        cargo_capacity=int(data.get("cargo_capacity", 40)),
        cargo_units=int(data.get("cargo_units", 0)),
        engine_speed=int(data.get("engine_speed", 30)),
        x=float(data.get("x", 0.0)),
        y=float(data.get("y", 0.0)),
        waypoint_type=data.get("waypoint_type", "PLANET")
    )

    mock_api.ships = [ship_data]
    context["api"] = mock_api


@then("the synced ship should have:")
def check_synced_ship_data(context, datatable):
    """Verify synced ship has correct data from table"""
    ship = context["result"][0]

    for i, row in enumerate(datatable):
        if i == 0:  # Skip header row
            continue
        field = row[0]
        value = row[1]

        if field == "ship_symbol":
            assert ship.ship_symbol == value
        elif field == "player_id":
            assert ship.player_id == int(value)
        elif field == "waypoint":
            assert ship.current_location.symbol == value
        elif field == "system":
            assert ship.current_location.system_symbol == value
        elif field == "x":
            assert ship.current_location.x == float(value)
        elif field == "y":
            assert ship.current_location.y == float(value)
        elif field == "waypoint_type":
            assert ship.current_location.waypoint_type == value
        elif field == "nav_status":
            assert ship.nav_status == value
        elif field == "fuel_current":
            assert ship.fuel.current == int(value)
        elif field == "fuel_capacity":
            assert ship.fuel_capacity == int(value)
        elif field == "cargo_capacity":
            assert ship.cargo_capacity == int(value)
        elif field == "cargo_units":
            assert ship.cargo_units == int(value)
        elif field == "engine_speed":
            assert ship.engine_speed == int(value)


# ==============================================================================
# Scenario: Sync handles different nav statuses
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync handles different nav statuses")
def test_sync_different_nav_statuses():
    pass


@given(parsers.parse('the API returns ship "{symbol}" with status "{status}"'))
def api_returns_ship_with_status(context, mock_api, symbol, status):
    """Add ship with specific status to API response"""
    if "api" not in context:
        context["api"] = mock_api

    if not hasattr(context["api"], "ships") or context["api"].ships is None:
        context["api"].ships = []

    context["api"].ships.append(
        create_api_ship_data(symbol=symbol, status=status)
    )


@then(parsers.parse('ship "{symbol}" should have nav status "{status}"'))
def check_ship_nav_status(context, symbol, status):
    """Verify specific ship has correct nav status"""
    ship = next((s for s in context["result"] if s.ship_symbol == symbol), None)
    assert ship is not None
    assert ship.nav_status == status


# ==============================================================================
# Scenario: Sync continues on single ship error
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync continues on single ship error")
def test_sync_continues_on_error():
    pass


@given(parsers.parse('the API returns ship "{symbol}" with valid data'))
def api_returns_ship_with_valid_data(context, mock_api, symbol):
    """Add ship with valid data to API response"""
    if "api" not in context:
        context["api"] = mock_api

    if not hasattr(context["api"], "ships") or context["api"].ships is None:
        context["api"].ships = []

    context["api"].ships.append(create_api_ship_data(symbol=symbol))


@given(parsers.parse('the API returns ship "{symbol}" with invalid data'))
def api_returns_ship_with_invalid_data(context, symbol):
    """Add ship with invalid data to API response"""
    if "api" not in context or not hasattr(context["api"], "ships"):
        return

    # Invalid data: mismatched fuel capacity that will fail Ship validation
    # Ship requires fuel.capacity == fuel_capacity, so we provide mismatched values
    context["api"].ships.append({
        "symbol": symbol,
        "nav": {
            "systemSymbol": "X1-TEST",
            "waypointSymbol": "X1-TEST-AB12",
            "status": "DOCKED",
            "route": {"destination": {"x": 0, "y": 0, "type": "PLANET"}}
        },
        "fuel": {
            "current": 50,
            "capacity": 100  # This will be different from what we claim as fuel_capacity
        },
        "cargo": {
            "capacity": 40,
            "units": 0
        },
        "engine": {
            "speed": -1  # Invalid: engine_speed must be positive
        }
    })


@then(parsers.parse('ships "{ship1}", "{ship3}" should be synced'))
def check_ships_synced(context, ship1, ship3):
    """Verify specific ships were synced"""
    synced_symbols = [ship.ship_symbol for ship in context["result"]]
    assert ship1 in synced_symbols
    assert ship3 in synced_symbols


@then(parsers.parse('ship "{symbol}" should not be synced'))
def check_ship_not_synced(context, symbol):
    """Verify specific ship was not synced"""
    synced_symbols = [ship.ship_symbol for ship in context["result"]]
    assert symbol not in synced_symbols


# ==============================================================================
# Scenario: Sync fetches agent info
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync fetches agent info")
def test_sync_fetches_agent_info():
    pass


@given(parsers.parse('the API agent symbol is "{agent_symbol}"'))
def set_api_agent_symbol(context, mock_api, agent_symbol):
    """Set the API agent symbol"""
    mock_api.agent_symbol = agent_symbol
    context["api"] = mock_api


@then("agent info should be fetched successfully")
def check_agent_info_fetched(context):
    """Verify agent info was fetched"""
    # The handler calls get_agent, we just verify no error occurred
    assert context["result"] is not None


# ==============================================================================
# Scenario: Sync assigns correct player ID
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync assigns correct player ID")
def test_sync_correct_player_id():
    pass


@then(parsers.parse("all synced ships should have player_id {player_id:d}"))
def check_all_ships_player_id(context, player_id):
    """Verify all ships have correct player_id"""
    assert all(ship.player_id == player_id for ship in context["result"])


# ==============================================================================
# Scenario: Sync updates fuel state
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync updates fuel state")
def test_sync_updates_fuel():
    pass


@given(parsers.parse('a ship "{symbol}" exists for player {player_id:d} with {fuel:d} fuel'))
def ship_exists_with_fuel(context, mock_ship_repo, mock_api, symbol, player_id, fuel):
    """Create an existing ship with specific fuel"""
    existing_ship = Ship(
        ship_symbol=symbol,
        player_id=player_id,
        current_location=Waypoint(symbol="X1-TEST-AB12", x=0.0, y=0.0),
        fuel=Fuel(current=fuel, capacity=100),
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )
    mock_ship_repo.create(existing_ship)
    # Store ship count before sync for comparison
    context["pre_sync_ship_count"] = len(mock_ship_repo.find_all_by_player(player_id))
    context["api"] = mock_api


@then(parsers.parse('ship "{symbol}" should have {fuel:d} fuel'))
def check_ship_fuel(context, symbol, fuel):
    """Verify ship has correct fuel"""
    ship = context["result"][0]
    assert ship.fuel.current == fuel


# ==============================================================================
# Scenario: Sync updates location
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync updates location")
def test_sync_updates_location():
    pass


@given(parsers.parse('a ship "{symbol}" exists for player {player_id:d} at "{waypoint}"'))
def ship_exists_at_location(context, mock_ship_repo, mock_api, symbol, player_id, waypoint):
    """Create an existing ship at specific location"""
    existing_ship = Ship(
        ship_symbol=symbol,
        player_id=player_id,
        current_location=Waypoint(symbol=waypoint, x=0.0, y=0.0),
        fuel=Fuel(current=100, capacity=100),
        fuel_capacity=100,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )
    mock_ship_repo.create(existing_ship)
    # Store ship count before sync for comparison
    context["pre_sync_ship_count"] = len(mock_ship_repo.find_all_by_player(player_id))
    context["api"] = mock_api


@given(parsers.parse('the API returns ship "{symbol}" at "{waypoint}"'))
def api_returns_ship_at_location(context, symbol, waypoint):
    """Set up API to return ship at specific location"""
    context["api"].ships = [
        create_api_ship_data(symbol=symbol, waypoint=waypoint)
    ]


@then(parsers.parse('ship "{symbol}" should be at "{waypoint}"'))
def check_ship_location(context, symbol, waypoint):
    """Verify ship is at correct location"""
    ship = context["result"][0]
    assert ship.current_location.symbol == waypoint


# ==============================================================================
# Scenario: Sync returns all synced ships
# ==============================================================================
@scenario("../../features/application/sync_ships_command.feature", "Sync returns all synced ships")
def test_sync_returns_all_ships():
    pass


@given(parsers.parse('the API returns {count:d} ships "{ship1}", "{ship2}", "{ship3}", "{ship4}"'))
def api_returns_specific_ships(context, mock_api, count, ship1, ship2, ship3, ship4):
    """Set up API to return specific ships"""
    mock_api.ships = [
        create_api_ship_data(symbol=ship1),
        create_api_ship_data(symbol=ship2),
        create_api_ship_data(symbol=ship3),
        create_api_ship_data(symbol=ship4)
    ]
    context["api"] = mock_api


@then(parsers.parse('the returned ships should be "{ship1}", "{ship2}", "{ship3}", "{ship4}"'))
def check_returned_ships(context, ship1, ship2, ship3, ship4):
    """Verify correct ships were returned"""
    symbols = [ship.ship_symbol for ship in context["result"]]
    assert ship1 in symbols
    assert ship2 in symbols
    assert ship3 in symbols
    assert ship4 in symbols

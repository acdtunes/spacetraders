"""
BDD step definitions for list ships query feature.

Tests ListShipsQuery and ListShipsHandler
across 15 scenarios covering all query functionality.
"""
import pytest
import asyncio
from typing import Optional, List
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders.application.navigation.queries.list_ships import (
    ListShipsQuery,
    ListShipsHandler
)
from spacetraders.domain.shared.value_objects import Waypoint, Fuel
from spacetraders.domain.shared.ship import Ship
from spacetraders.ports.outbound.repositories import IShipRepository

# Load all scenarios from the feature file
scenarios('../../features/application/list_ships_query.feature')


# ============================================================================
# Mock Implementations
# ============================================================================

class MockShipRepository(IShipRepository):
    """Mock ship repository for testing - focuses on behavior, not implementation details"""

    def __init__(self):
        self._ships = {}
        self.exception_to_raise = None

    def create(self, ship: Ship) -> Ship:
        self._ships[(ship.ship_symbol, ship.player_id)] = ship
        return ship

    def find_by_symbol(self, ship_symbol: str, player_id: int) -> Optional[Ship]:
        return self._ships.get((ship_symbol, player_id))

    def find_all_by_player(self, player_id: int) -> List[Ship]:
        if self.exception_to_raise:
            raise RuntimeError(self.exception_to_raise)
        return [s for s in self._ships.values() if s.player_id == player_id]

    def update(self, ship: Ship) -> None:
        self._ships[(ship.ship_symbol, ship.player_id)] = ship

    def save(self, ship: Ship) -> None:
        self._ships[(ship.ship_symbol, ship.player_id)] = ship

    def delete(self, ship_symbol: str, player_id: int) -> None:
        key = (ship_symbol, player_id)
        if key in self._ships:
            del self._ships[key]

    def clear_all(self) -> None:
        """Clear all ships from repository (public method for testing)"""
        self._ships.clear()

    def get_all_ships(self) -> List[Ship]:
        """Get all ships in repository (public method for testing)"""
        return list(self._ships.values())

    def sync_from_api(self, ship_symbol: str, player_id: int, api_client, graph_provider) -> Ship:
        """Mock sync from API - just returns the ship from memory"""
        ship = self.find_by_symbol(ship_symbol, player_id)
        if ship is None:
            raise ValueError(f"Ship {ship_symbol} not found for player {player_id}")
        return ship

    def list_by_player(self, player_id: int):
        return self.find_all_by_player(player_id)


# ============================================================================
# Helper Functions
# ============================================================================

def create_test_waypoint(
    symbol: str = "X1-TEST-LOCATION",
    x: float = 0.0,
    y: float = 0.0,
    system_symbol: str = "X1",
    waypoint_type: str = "PLANET",
    traits: tuple = (),
    has_fuel: bool = True,
    orbitals: tuple = ()
) -> Waypoint:
    """Helper to create test waypoint"""
    return Waypoint(
        symbol=symbol,
        x=x,
        y=y,
        system_symbol=system_symbol,
        waypoint_type=waypoint_type,
        traits=traits,
        has_fuel=has_fuel,
        orbitals=orbitals
    )


def create_test_ship(
    ship_symbol: str = "SHIP-1",
    player_id: int = 1,
    current_location: Waypoint = None,
    fuel_current: int = 100,
    fuel_capacity: int = 200,
    cargo_capacity: int = 100,
    cargo_units: int = 0,
    engine_speed: int = 30,
    nav_status: str = Ship.DOCKED
) -> Ship:
    """Helper to create test ship"""
    if current_location is None:
        current_location = create_test_waypoint()

    fuel = Fuel(current=fuel_current, capacity=fuel_capacity)

    return Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=current_location,
        fuel=fuel,
        fuel_capacity=fuel_capacity,
        cargo_capacity=cargo_capacity,
        cargo_units=cargo_units,
        engine_speed=engine_speed,
        nav_status=nav_status
    )


# ============================================================================
# Fixtures
# ============================================================================

@pytest.fixture
def context():
    """Shared test context"""
    return {
        'repository': None,
        'handler': None,
        'query': None,
        'query2': None,
        'result': None,
        'result2': None,
        'exception': None,
        'last_ship': None,
        'second_last_ship': None,
        'third_last_ship': None,
        'last_waypoint': None,
        'second_last_waypoint': None
    }


# ============================================================================
# Background Steps
# ============================================================================

@given('the list ships query handler is initialized')
def initialize_handler(context):
    """Initialize the handler with mock repository"""
    context['repository'] = MockShipRepository()
    context['handler'] = ListShipsHandler(ship_repository=context['repository'])


# ============================================================================
# Query Creation Steps
# ============================================================================

@when(parsers.parse('I create a list ships query for player {player_id:d}'))
def create_query(context, player_id):
    """Create a query for a player"""
    if context.get('query') is None:
        context['query'] = ListShipsQuery(player_id=player_id)
    else:
        context['query2'] = ListShipsQuery(player_id=player_id)


@given(parsers.parse('I create a list ships query for player {player_id:d}'))
def given_create_query(context, player_id):
    """Create a query for a player (given form)"""
    create_query(context, player_id)


@when(parsers.parse('I attempt to modify the list query player id to {new_id:d}'))
def attempt_modify_query(context, new_id):
    """Attempt to modify query (should fail)"""
    try:
        context['query'].player_id = new_id
    except AttributeError as e:
        context['exception'] = e


# ============================================================================
# Ship Setup Steps
# ============================================================================

@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def create_ship(context, ship_symbol, player_id):
    """Create a ship in the repository"""
    ship = create_test_ship(ship_symbol=ship_symbol, player_id=player_id)
    context['repository'].create(ship)
    context['last_ship'] = ship
    context['last_waypoint'] = ship.current_location


@given(parsers.parse('the ship is at waypoint "{waypoint_symbol}" at coordinates {x:f}, {y:f}'))
def set_ship_location(context, waypoint_symbol, x, y):
    """Set ship location"""
    waypoint = create_test_waypoint(symbol=waypoint_symbol, x=x, y=y)
    ship = context['last_ship']
    # Create new ship with updated location
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=waypoint,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship
    context['last_waypoint'] = waypoint


@given(parsers.parse('the waypoint has type "{waypoint_type}"'))
def set_waypoint_type(context, waypoint_type):
    """Set waypoint type"""
    old_waypoint = context['last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=old_waypoint.x,
        y=old_waypoint.y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=waypoint_type,
        traits=old_waypoint.traits,
        has_fuel=old_waypoint.has_fuel,
        orbitals=old_waypoint.orbitals
    )
    ship = context['last_ship']
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=waypoint,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship
    context['last_waypoint'] = waypoint


@given(parsers.parse('the second ship is at waypoint "{waypoint_symbol}" at coordinates {x:f}, {y:f}'))
def set_second_ship_location(context, waypoint_symbol, x, y):
    """Set second ship location"""
    # Find the second-to-last ship
    ships = context['repository'].get_all_ships()
    ship = ships[-1]  # Last created ship

    waypoint = create_test_waypoint(symbol=waypoint_symbol, x=x, y=y)
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=waypoint,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['second_last_ship'] = updated_ship
    context['second_last_waypoint'] = waypoint


@given(parsers.parse('the second waypoint has type "{waypoint_type}"'))
def set_second_waypoint_type(context, waypoint_type):
    """Set second waypoint type"""
    old_waypoint = context['second_last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=old_waypoint.x,
        y=old_waypoint.y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=waypoint_type,
        traits=old_waypoint.traits,
        has_fuel=old_waypoint.has_fuel,
        orbitals=old_waypoint.orbitals
    )
    ship = context['second_last_ship']
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=waypoint,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['second_last_ship'] = updated_ship
    context['second_last_waypoint'] = waypoint


@given(parsers.parse('the ship has {current:d} current fuel and {capacity:d} capacity'))
def set_ship_fuel(context, current, capacity):
    """Set ship fuel"""
    ship = context['last_ship']
    fuel = Fuel(current=current, capacity=capacity)
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=fuel,
        fuel_capacity=capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship


@given(parsers.parse('the second ship has {current:d} current fuel and {capacity:d} capacity'))
def set_second_ship_fuel(context, current, capacity):
    """Set second ship fuel"""
    ships = context['repository'].get_all_ships()
    ship = ships[-1]
    fuel = Fuel(current=current, capacity=capacity)
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=fuel,
        fuel_capacity=capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['second_last_ship'] = updated_ship


@given(parsers.parse('the third ship has {current:d} current fuel and {capacity:d} capacity'))
def set_third_ship_fuel(context, current, capacity):
    """Set third ship fuel"""
    ships = context['repository'].get_all_ships()
    ship = ships[-1]
    fuel = Fuel(current=current, capacity=capacity)
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=fuel,
        fuel_capacity=capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['third_last_ship'] = updated_ship


@given(parsers.parse('the ship has cargo capacity {capacity:d} with {units:d} units'))
def set_ship_cargo(context, capacity, units):
    """Set ship cargo"""
    ship = context['last_ship']
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=capacity,
        cargo_units=units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship


@given(parsers.parse('the second ship has cargo capacity {capacity:d} with {units:d} units'))
def set_second_ship_cargo(context, capacity, units):
    """Set second ship cargo"""
    ships = context['repository'].get_all_ships()
    ship = ships[-1]
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=capacity,
        cargo_units=units,
        engine_speed=ship.engine_speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['second_last_ship'] = updated_ship


@given(parsers.parse('the ship has navigation status "{nav_status}"'))
def set_ship_nav_status(context, nav_status):
    """Set ship navigation status"""
    ship = context['last_ship']
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship


@given(parsers.parse('the second ship has navigation status "{nav_status}"'))
def set_second_ship_nav_status(context, nav_status):
    """Set second ship navigation status"""
    ships = context['repository'].get_all_ships()
    ship = ships[-1]
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=ship.engine_speed,
        nav_status=nav_status
    )
    context['repository'].update(updated_ship)
    context['second_last_ship'] = updated_ship


@given(parsers.parse('the ship has engine speed {speed:d}'))
def set_ship_engine_speed(context, speed):
    """Set ship engine speed"""
    ship = context['last_ship']
    updated_ship = Ship(
        ship_symbol=ship.ship_symbol,
        player_id=ship.player_id,
        current_location=ship.current_location,
        fuel=ship.fuel,
        fuel_capacity=ship.fuel_capacity,
        cargo_capacity=ship.cargo_capacity,
        cargo_units=ship.cargo_units,
        engine_speed=speed,
        nav_status=ship.nav_status
    )
    context['repository'].update(updated_ship)
    context['last_ship'] = updated_ship


@given('no ships exist in the repository')
def clear_repository(context):
    """Clear all ships from repository"""
    context['repository'].clear_all()


@given(parsers.parse('player {player_id:d} has {count:d} ships'))
def create_multiple_ships(context, player_id, count):
    """Create multiple ships for a player"""
    for i in range(count):
        ship = create_test_ship(
            ship_symbol=f"SHIP-{i}",
            player_id=player_id
        )
        context['repository'].create(ship)


@given(parsers.parse('the repository will raise "{error_message}"'))
def set_repository_exception(context, error_message):
    """Configure repository to raise exception"""
    context['repository'].exception_to_raise = error_message


# ============================================================================
# Action Steps
# ============================================================================

@when(parsers.parse('I query all ships for player {player_id:d}'))
def query_ships(context, player_id):
    """Query all ships for a player"""
    try:
        query = ListShipsQuery(player_id=player_id)
        result = asyncio.run(context['handler'].handle(query))
        if context.get('result') is None:
            context['result'] = result
        else:
            context['result2'] = result
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I query all ships for player {player_id:d} again'))
def query_ships_again(context, player_id):
    """Query all ships for a player again"""
    query = ListShipsQuery(player_id=player_id)
    result = asyncio.run(context['handler'].handle(query))
    context['result2'] = result


# ============================================================================
# Assertion Steps - Query Properties
# ============================================================================

@then(parsers.parse('the list query should have player id {player_id:d}'))
def check_query_player_id(context, player_id):
    """Check query player id"""
    assert context['query'].player_id == player_id


@then(parsers.parse('the first list query should have player id {player_id:d}'))
def check_first_query_player_id(context, player_id):
    """Check first query player id"""
    assert context['query'].player_id == player_id


@then(parsers.parse('the second list query should have player id {player_id:d}'))
def check_second_query_player_id(context, player_id):
    """Check second query player id"""
    assert context['query2'].player_id == player_id


@then('the modification should fail with AttributeError')
def check_attribute_error(context):
    """Check that modification raised AttributeError"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], AttributeError)


# ============================================================================
# Assertion Steps - Query Results
# ============================================================================

@then('the query should succeed')
def check_query_success(context):
    """Check that query succeeded"""
    assert context['exception'] is None
    assert context['result'] is not None


@then('the result should be a list')
def check_result_is_list(context):
    """Check that result is a list"""
    assert isinstance(context['result'], list)


@then(parsers.parse('the list should contain {count:d} ships'))
def check_ship_count(context, count):
    """Check ship count"""
    assert len(context['result']) == count


@then(parsers.parse('the first list should contain {count:d} ships'))
def check_first_list_count(context, count):
    """Check first list ship count"""
    assert len(context['result']) == count


@then(parsers.parse('the second list should contain {count:d} ships'))
def check_second_list_count(context, count):
    """Check second list ship count"""
    assert len(context['result2']) == count


@then('the list should be empty')
def check_empty_list(context):
    """Check that list is empty"""
    assert context['result'] == []


@then('all ships should be Ship instances')
def check_ship_instances(context):
    """Check that all items are Ship instances"""
    assert all(isinstance(ship, Ship) for ship in context['result'])


@then(parsers.parse('all ships should have player id {player_id:d}'))
def check_all_ships_player_id(context, player_id):
    """Check that all ships have the correct player id"""
    assert all(ship.player_id == player_id for ship in context['result'])


# ============================================================================
# Assertion Steps - Individual Ship Properties
# ============================================================================

@then(parsers.parse('the ship at index {index:d} should have symbol "{symbol}"'))
def check_ship_symbol(context, index, symbol):
    """Check ship symbol at index"""
    assert context['result'][index].ship_symbol == symbol


@then(parsers.parse('the first list ship at index {index:d} should have symbol "{symbol}"'))
def check_first_list_ship_symbol(context, index, symbol):
    """Check first list ship symbol at index"""
    assert context['result'][index].ship_symbol == symbol


@then(parsers.parse('the second list ship at index {index:d} should have symbol "{symbol}"'))
def check_second_list_ship_symbol(context, index, symbol):
    """Check second list ship symbol at index"""
    assert context['result2'][index].ship_symbol == symbol


@then(parsers.parse('the first list ship at index {index:d} should have player id {player_id:d}'))
def check_first_list_ship_player_id(context, index, player_id):
    """Check first list ship player id at index"""
    assert context['result'][index].player_id == player_id


@then(parsers.parse('the second list ship at index {index:d} should have player id {player_id:d}'))
def check_second_list_ship_player_id(context, index, player_id):
    """Check second list ship player id at index"""
    assert context['result2'][index].player_id == player_id


@then(parsers.parse('the ship at index {index:d} should have player id {player_id:d}'))
def check_ship_player_id(context, index, player_id):
    """Check ship player id at index"""
    assert context['result'][index].player_id == player_id


@then(parsers.parse('the ship at index {index:d} should be at location "{location}"'))
def check_ship_location(context, index, location):
    """Check ship location at index"""
    assert context['result'][index].current_location.symbol == location


@then(parsers.parse('the ship at index {index:d} should have fuel {fuel:d}'))
def check_ship_fuel(context, index, fuel):
    """Check ship fuel at index"""
    assert context['result'][index].fuel.current == fuel


@then(parsers.parse('the ship at index {index:d} should have fuel {fuel:d} with capacity {capacity:d}'))
def check_ship_fuel_with_capacity(context, index, fuel, capacity):
    """Check ship fuel and capacity at index"""
    assert context['result'][index].fuel.current == fuel
    assert context['result'][index].fuel_capacity == capacity


@then(parsers.parse('the ship at index {index:d} should have cargo {units:d} units'))
def check_ship_cargo(context, index, units):
    """Check ship cargo at index"""
    assert context['result'][index].cargo_units == units


@then(parsers.parse('the ship at index {index:d} should have cargo {units:d} units with capacity {capacity:d}'))
def check_ship_cargo_with_capacity(context, index, units, capacity):
    """Check ship cargo and capacity at index"""
    assert context['result'][index].cargo_units == units
    assert context['result'][index].cargo_capacity == capacity


@then(parsers.parse('the ship at index {index:d} should have nav status "{nav_status}"'))
def check_ship_nav_status(context, index, nav_status):
    """Check ship nav status at index"""
    assert context['result'][index].nav_status == nav_status


@then(parsers.parse('the ship at index {index:d} should have engine speed {speed:d}'))
def check_ship_engine_speed(context, index, speed):
    """Check ship engine speed at index"""
    assert context['result'][index].engine_speed == speed


# ============================================================================
# Assertion Steps - Removed: Repository Call Tracking
# ============================================================================
# NOTE: We removed mock call verification steps because they test
# implementation details rather than public behavior.
# Tests now focus on: "Do I get the correct ships back?"
# Not: "Was the repository called N times?"


# ============================================================================
# Assertion Steps - Multiple Queries
# ============================================================================

@then(parsers.parse('both queries should return {count:d} ships'))
def check_both_queries_count(context, count):
    """Check that both queries returned same count"""
    assert len(context['result']) == count
    assert len(context['result2']) == count


# ============================================================================
# Assertion Steps - Error Handling
# ============================================================================

@then('the query should fail with RuntimeError')
def check_runtime_error(context):
    """Check that query raised RuntimeError"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], RuntimeError)


@then(parsers.parse('the error message should contain "{text}"'))
def check_error_message(context, text):
    """Check error message contains text"""
    assert text in str(context['exception'])

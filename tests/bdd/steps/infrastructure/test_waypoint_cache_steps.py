"""Step definitions for waypoint caching feature"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from typing import List, Dict

from spacetraders.domain.shared.value_objects import Waypoint
from spacetraders.adapters.secondary.persistence.database import Database
from spacetraders.configuration.container import get_waypoint_repository, reset_container

# Link feature file
scenarios('../../features/infrastructure/waypoint_cache.feature')


@pytest.fixture
def context():
    """Shared context for step definitions"""
    return {}


@given("the database is initialized", target_fixture="database")
def database_initialized():
    """Initialize in-memory database"""
    reset_container()
    db = Database(":memory:")
    return db


@given(parsers.parse('waypoints exist for system "{system_symbol}":'))
def waypoints_exist(context, system_symbol: str, database, datatable):
    """Save waypoints to the database"""
    waypoint_repo = get_waypoint_repository()
    waypoints = _parse_waypoint_table(datatable, system_symbol)
    waypoint_repo.save_waypoints(waypoints)
    context['saved_waypoints'] = waypoints


@when(parsers.parse('I save waypoints for system "{system_symbol}" with waypoints:'))
def save_waypoints(context, system_symbol: str, database, datatable):
    """Save waypoints to repository"""
    waypoint_repo = get_waypoint_repository()
    waypoints = _parse_waypoint_table(datatable, system_symbol)
    waypoint_repo.save_waypoints(waypoints)
    context['saved_waypoints'] = waypoints


@when(parsers.parse('I query waypoints for system "{system_symbol}"'))
def query_waypoints_by_system(context, system_symbol: str, database):
    """Query all waypoints for a system"""
    waypoint_repo = get_waypoint_repository()
    waypoints = waypoint_repo.find_by_system(system_symbol)
    context['query_result'] = waypoints


@when(parsers.parse('I query waypoints with trait "{trait}" in system "{system_symbol}"'))
def query_waypoints_by_trait(context, trait: str, system_symbol: str, database):
    """Query waypoints with specific trait"""
    waypoint_repo = get_waypoint_repository()
    waypoints = waypoint_repo.find_by_trait(system_symbol, trait)
    context['query_result'] = waypoints


@when(parsers.parse('I query waypoints with fuel in system "{system_symbol}"'))
def query_waypoints_with_fuel(context, system_symbol: str, database):
    """Query waypoints with fuel stations"""
    waypoint_repo = get_waypoint_repository()
    waypoints = waypoint_repo.find_by_fuel(system_symbol)
    context['query_result'] = waypoints


@then("waypoints should be saved in the database")
def waypoints_saved(context, database):
    """Verify waypoints are persisted"""
    waypoint_repo = get_waypoint_repository()
    saved_waypoints = context.get('saved_waypoints', [])

    # Verify we can retrieve the first waypoint as proof of persistence
    if saved_waypoints:
        first_waypoint = saved_waypoints[0]
        retrieved = waypoint_repo.find_by_system(first_waypoint.system_symbol)
        assert len(retrieved) > 0, "No waypoints found in database"


@then(parsers.parse('I should receive {count:d} waypoint'))
@then(parsers.parse('I should receive {count:d} waypoints'))
def verify_waypoint_count(context, count: int):
    """Verify the number of waypoints returned"""
    waypoints = context.get('query_result', [])
    assert len(waypoints) == count, f"Expected {count} waypoints, got {len(waypoints)}"


@then(parsers.parse('waypoint "{symbol}" should have type "{waypoint_type}"'))
def verify_waypoint_type(context, symbol: str, waypoint_type: str):
    """Verify waypoint type"""
    waypoints = context.get('query_result', [])
    waypoint = _find_waypoint(waypoints, symbol)
    assert waypoint is not None, f"Waypoint {symbol} not found in results"
    assert waypoint.waypoint_type == waypoint_type, \
        f"Expected type {waypoint_type}, got {waypoint.waypoint_type}"


@then(parsers.parse('waypoint "{symbol}" should have traits "{traits}"'))
def verify_waypoint_traits(context, symbol: str, traits: str):
    """Verify waypoint traits"""
    waypoint_repo = get_waypoint_repository()
    # Query from database to get the specific waypoint
    system_symbol = symbol.rsplit('-', 1)[0] if '-' in symbol else symbol
    # Extract system symbol properly (e.g., "X1-GZ7" from "X1-GZ7-A1")
    system_symbol = '-'.join(symbol.split('-')[:2])

    all_waypoints = waypoint_repo.find_by_system(system_symbol)
    waypoint = _find_waypoint(all_waypoints, symbol)

    assert waypoint is not None, f"Waypoint {symbol} not found"

    expected_traits = set(traits.split(',')) if traits else set()
    actual_traits = set(waypoint.traits)

    assert actual_traits == expected_traits, \
        f"Expected traits {expected_traits}, got {actual_traits}"


@then(parsers.parse('waypoint "{symbol}" should be in the results'))
def verify_waypoint_in_results(context, symbol: str):
    """Verify waypoint is in query results"""
    waypoints = context.get('query_result', [])
    waypoint = _find_waypoint(waypoints, symbol)
    assert waypoint is not None, f"Waypoint {symbol} not found in results"


@then(parsers.parse('waypoint "{symbol}" should have fuel available'))
def verify_waypoint_has_fuel(context, symbol: str):
    """Verify waypoint has fuel station"""
    waypoint_repo = get_waypoint_repository()
    system_symbol = '-'.join(symbol.split('-')[:2])

    all_waypoints = waypoint_repo.find_by_system(system_symbol)
    waypoint = _find_waypoint(all_waypoints, symbol)

    assert waypoint is not None, f"Waypoint {symbol} not found"
    assert waypoint.has_fuel is True, f"Waypoint {symbol} should have fuel available"


def _parse_waypoint_table(datatable, system_symbol: str) -> List[Waypoint]:
    """Parse datatable into Waypoint value objects"""
    waypoints = []
    headers = datatable[0]  # First row is column names

    # Create column index mapping
    col_idx = {header: idx for idx, header in enumerate(headers)}

    for row in datatable[1:]:  # Skip header row
        traits_str = row[col_idx['traits']].strip() if col_idx.get('traits') is not None else ''
        traits = tuple(traits_str.split(',')) if traits_str else ()

        orbitals_str = row[col_idx['orbitals']].strip() if col_idx.get('orbitals') is not None else ''
        orbitals = tuple(orbitals_str.split(',')) if orbitals_str else ()

        has_fuel = row[col_idx['has_fuel']].lower() == 'true' if col_idx.get('has_fuel') is not None else False

        waypoint = Waypoint(
            symbol=row[col_idx['symbol']],
            x=float(row[col_idx['x']]),
            y=float(row[col_idx['y']]),
            system_symbol=system_symbol,
            waypoint_type=row[col_idx['type']],
            traits=traits,
            has_fuel=has_fuel,
            orbitals=orbitals
        )
        waypoints.append(waypoint)

    return waypoints


def _find_waypoint(waypoints: List[Waypoint], symbol: str) -> Waypoint:
    """Find waypoint by symbol in a list"""
    for waypoint in waypoints:
        if waypoint.symbol == symbol:
            return waypoint
    return None

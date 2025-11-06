"""
BDD step definitions for list waypoints query feature.

Tests ListWaypointsQuery and ListWaypointsHandler
using black-box testing principles - testing behavior through public interfaces only.
"""
import pytest
import asyncio
from typing import List, Optional
from pytest_bdd import scenarios, given, when, then, parsers

from application.waypoints.queries.list_waypoints import (
    ListWaypointsQuery,
    ListWaypointsHandler
)
from domain.shared.value_objects import Waypoint
from ports.outbound.repositories import IWaypointRepository


# Load all scenarios from the feature file
scenarios('../../../features/application/waypoints/list_waypoints.feature')


# ============================================================================
# Mock Implementations
# ============================================================================

class MockWaypointRepository(IWaypointRepository):
    """Mock waypoint repository for testing - focuses on behavior, not implementation details"""

    def __init__(self):
        self._waypoints = []

    def save_waypoints(self, waypoints: List[Waypoint]) -> None:
        """Save or update waypoints in cache"""
        for waypoint in waypoints:
            # Remove existing waypoint with same symbol
            self._waypoints = [w for w in self._waypoints if w.symbol != waypoint.symbol]
            self._waypoints.append(waypoint)

    def find_by_system(self, system_symbol: str) -> List[Waypoint]:
        """Find all waypoints in a system"""
        result = [w for w in self._waypoints if w.system_symbol == system_symbol]
        return sorted(result, key=lambda w: w.symbol)

    def find_by_trait(self, system_symbol: str, trait: str) -> List[Waypoint]:
        """Find waypoints with a specific trait"""
        system_waypoints = self.find_by_system(system_symbol)
        return [w for w in system_waypoints if trait in w.traits]

    def find_by_fuel(self, system_symbol: str) -> List[Waypoint]:
        """Find waypoints with fuel stations"""
        system_waypoints = self.find_by_system(system_symbol)
        return [w for w in system_waypoints if w.has_fuel]

    def clear_all(self) -> None:
        """Clear all waypoints (public method for testing)"""
        self._waypoints.clear()


# ============================================================================
# Helper Functions
# ============================================================================

def create_test_waypoint(
    symbol: str = "X1-TEST-A1",
    x: float = 0.0,
    y: float = 0.0,
    system_symbol: str = "X1-TEST",
    waypoint_type: str = "PLANET",
    traits: tuple = (),
    has_fuel: bool = False,
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
        'result': None,
        'exception': None,
        'last_waypoint': None
    }


# ============================================================================
# Background Steps
# ============================================================================

@given('the list waypoints query handler is initialized')
def initialize_handler(context):
    """Initialize the handler with mock repository"""
    context['repository'] = MockWaypointRepository()
    context['handler'] = ListWaypointsHandler(waypoint_repository=context['repository'])


# ============================================================================
# Waypoint Setup Steps
# ============================================================================

@given(parsers.parse('a waypoint "{waypoint_symbol}" exists in system "{system_symbol}"'))
def create_waypoint(context, waypoint_symbol, system_symbol):
    """Create a waypoint in the repository"""
    waypoint = create_test_waypoint(
        symbol=waypoint_symbol,
        system_symbol=system_symbol
    )
    context['repository'].save_waypoints([waypoint])
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
    context['repository'].save_waypoints([waypoint])
    context['last_waypoint'] = waypoint


@given(parsers.parse('the waypoint has traits {traits}'))
def set_waypoint_traits(context, traits):
    """Set waypoint traits (comma-separated in quotes)"""
    # Parse traits like: "MARKETPLACE", "SHIPYARD"
    trait_list = tuple(t.strip().strip('"') for t in traits.split(','))

    old_waypoint = context['last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=old_waypoint.x,
        y=old_waypoint.y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=old_waypoint.waypoint_type,
        traits=trait_list,
        has_fuel=old_waypoint.has_fuel,
        orbitals=old_waypoint.orbitals
    )
    context['repository'].save_waypoints([waypoint])
    context['last_waypoint'] = waypoint


@given('the waypoint has fuel available')
def set_waypoint_has_fuel(context):
    """Set waypoint fuel available"""
    old_waypoint = context['last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=old_waypoint.x,
        y=old_waypoint.y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=old_waypoint.waypoint_type,
        traits=old_waypoint.traits,
        has_fuel=True,
        orbitals=old_waypoint.orbitals
    )
    context['repository'].save_waypoints([waypoint])
    context['last_waypoint'] = waypoint


@given('the waypoint has no fuel')
def set_waypoint_no_fuel(context):
    """Set waypoint no fuel available"""
    old_waypoint = context['last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=old_waypoint.x,
        y=old_waypoint.y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=old_waypoint.waypoint_type,
        traits=old_waypoint.traits,
        has_fuel=False,
        orbitals=old_waypoint.orbitals
    )
    context['repository'].save_waypoints([waypoint])
    context['last_waypoint'] = waypoint


@given(parsers.parse('the waypoint is at coordinates {x:f}, {y:f}'))
def set_waypoint_coordinates(context, x, y):
    """Set waypoint coordinates"""
    old_waypoint = context['last_waypoint']
    waypoint = Waypoint(
        symbol=old_waypoint.symbol,
        x=x,
        y=y,
        system_symbol=old_waypoint.system_symbol,
        waypoint_type=old_waypoint.waypoint_type,
        traits=old_waypoint.traits,
        has_fuel=old_waypoint.has_fuel,
        orbitals=old_waypoint.orbitals
    )
    context['repository'].save_waypoints([waypoint])
    context['last_waypoint'] = waypoint


@given(parsers.parse('no waypoints exist in system "{system_symbol}"'))
def ensure_no_waypoints(context, system_symbol):
    """Ensure no waypoints exist for a system"""
    # Mock repository already returns empty list for systems with no waypoints
    pass


# ============================================================================
# Query Creation Steps
# ============================================================================

@given(parsers.parse('I create a list waypoints query for system "{system_symbol}"'))
def create_query(context, system_symbol):
    """Create a query for a system"""
    context['query'] = ListWaypointsQuery(system_symbol=system_symbol)


@when(parsers.parse('I attempt to modify the query system to "{new_system}"'))
def attempt_modify_query(context, new_system):
    """Attempt to modify query (should fail)"""
    try:
        context['query'].system_symbol = new_system
    except AttributeError as e:
        context['exception'] = e


# ============================================================================
# Action Steps
# ============================================================================

@when(parsers.parse('I query waypoints for system "{system_symbol}"'))
def query_waypoints_all(context, system_symbol):
    """Query all waypoints in a system"""
    try:
        query = ListWaypointsQuery(system_symbol=system_symbol)
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I query waypoints for system "{system_symbol}" with trait "{trait}"'))
def query_waypoints_by_trait(context, system_symbol, trait):
    """Query waypoints filtered by trait"""
    try:
        query = ListWaypointsQuery(
            system_symbol=system_symbol,
            trait_filter=trait
        )
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I query waypoints for system "{system_symbol}" with fuel filter'))
def query_waypoints_with_fuel(context, system_symbol):
    """Query waypoints filtered by fuel availability"""
    try:
        query = ListWaypointsQuery(
            system_symbol=system_symbol,
            has_fuel=True
        )
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


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


@then(parsers.parse('the list should contain {count:d} waypoints'))
def check_waypoint_count(context, count):
    """Check waypoint count"""
    assert len(context['result']) == count


@then('the list should be empty')
def check_empty_list(context):
    """Check that list is empty"""
    assert context['result'] == []


@then('all waypoints should be Waypoint instances')
def check_waypoint_instances(context):
    """Check that all items are Waypoint instances"""
    assert all(isinstance(wp, Waypoint) for wp in context['result'])


# ============================================================================
# Assertion Steps - Individual Waypoint Properties
# ============================================================================

@then(parsers.parse('the waypoint at index {index:d} should have symbol "{symbol}"'))
def check_waypoint_symbol(context, index, symbol):
    """Check waypoint symbol at index"""
    assert context['result'][index].symbol == symbol


@then(parsers.parse('the waypoint at index {index:d} should have system symbol "{system_symbol}"'))
def check_waypoint_system_symbol(context, index, system_symbol):
    """Check waypoint system symbol at index"""
    assert context['result'][index].system_symbol == system_symbol


@then(parsers.parse('the waypoint at index {index:d} should be at coordinates {x:f}, {y:f}'))
def check_waypoint_coordinates(context, index, x, y):
    """Check waypoint coordinates at index"""
    assert context['result'][index].x == x
    assert context['result'][index].y == y


@then(parsers.parse('the waypoint at index {index:d} should have type "{waypoint_type}"'))
def check_waypoint_type(context, index, waypoint_type):
    """Check waypoint type at index"""
    assert context['result'][index].waypoint_type == waypoint_type


@then(parsers.parse('the waypoint at index {index:d} should have traits {traits}'))
def check_waypoint_traits(context, index, traits):
    """Check waypoint traits at index"""
    # Parse traits like: "MARKETPLACE", "SHIPYARD", "FUEL_STATION"
    expected_traits = tuple(t.strip().strip('"') for t in traits.split(','))
    assert context['result'][index].traits == expected_traits


@then(parsers.parse('the waypoint at index {index:d} should have fuel available'))
def check_waypoint_has_fuel(context, index):
    """Check waypoint has fuel at index"""
    assert context['result'][index].has_fuel is True


# ============================================================================
# Assertion Steps - Query Immutability
# ============================================================================

@then('the modification should fail with AttributeError')
def check_attribute_error(context):
    """Check that modification raised AttributeError"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], AttributeError)

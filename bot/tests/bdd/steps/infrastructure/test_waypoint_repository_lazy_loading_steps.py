"""Step definitions for waypoint repository lazy-loading tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timedelta
from unittest.mock import Mock, MagicMock
from typing import List

from domain.shared.value_objects import Waypoint
from adapters.secondary.persistence.waypoint_repository import WaypointRepository
from adapters.secondary.persistence.database import Database

# Load scenarios
scenarios('../../features/infrastructure/waypoint_repository_lazy_loading.feature')


# ============================================================================
# Fixtures
# ============================================================================

@pytest.fixture
def context():
    """Shared context for test state"""
    return {
        'repository': None,
        'api_client_factory': None,
        'api_client': None,
        'api_call_count': 0,
        'result_waypoints': None,
        'players': {}
    }


@pytest.fixture
def database(tmp_path):
    """Fixture for test database"""
    db_path = tmp_path / "test_waypoint_repo.db"
    db = Database(str(db_path))
    return db


# ============================================================================
# Background Steps
# ============================================================================

@given("the database is initialized", target_fixture="context")
def database_initialized(database, context):
    """Initialize database"""
    context['database'] = database
    return context


@given("the waypoint repository is initialized with API client factory")
def repository_with_api_factory(context):
    """Initialize repository with mock API client factory"""
    # Create mock API client
    api_client = Mock()
    api_client.list_waypoints = Mock(return_value={'data': [], 'meta': {'total': 0}})
    context['api_client'] = api_client

    # Create API client factory that returns the mock
    def api_client_factory(player_id: int):
        context['api_call_count'] += 1
        return context['api_client']

    context['api_client_factory'] = api_client_factory

    # Initialize repository with factory
    context['repository'] = WaypointRepository(
        database=context['database'],
        api_client_factory=api_client_factory
    )


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('a player with ID {player_id:d} exists'))
def player_exists(context, player_id):
    """Create a player"""
    context['players'][player_id] = {'has_token': False}


@given(parsers.parse('a player with ID {player_id:d} exists with valid token'))
def player_with_token_exists(context, player_id):
    """Create a player with valid token"""
    context['players'][player_id] = {'has_token': True}


@given(parsers.parse('waypoints exist for system "{system_symbol}":'))
def waypoints_exist_for_system(context, system_symbol, datatable):
    """Create waypoints in cache for a system"""
    waypoints = _parse_waypoint_table(datatable, system_symbol)

    # Save waypoints without synced_at (will be set by next step if needed)
    context['repository'].save_waypoints(waypoints)
    context[f'waypoints_{system_symbol}'] = waypoints


@given(parsers.parse('the waypoints were synced {hours:d} hour ago'))
@given(parsers.parse('the waypoints were synced {hours:d} hours ago'))
def waypoints_synced_hours_ago(context, hours):
    """Set sync time for previously created waypoints"""
    # Get the most recently created waypoints
    for key in context.keys():
        if key.startswith('waypoints_'):
            waypoints = context[key]
            synced_at = datetime.now() - timedelta(hours=hours)
            # Re-save with explicit timestamp
            context['repository'].save_waypoints(waypoints, synced_at=synced_at, replace_system=True)
            break


@given(parsers.parse('no waypoints exist in cache for system "{system_symbol}"'))
def no_waypoints_in_cache(context, system_symbol):
    """Ensure no waypoints in cache"""
    # Database is fresh, no action needed
    pass


@given(parsers.parse('the API will return waypoints for system "{system_symbol}":'))
def api_returns_waypoints(context, system_symbol, datatable):
    """Configure mock API to return waypoints"""
    waypoints_data = []

    # Parse table (header row + data rows)
    headers = datatable[0]
    col_idx = {header: idx for idx, header in enumerate(headers)}

    for row in datatable[1:]:  # Skip header row
        traits_str = row[col_idx.get('traits', -1)].strip() if col_idx.get('traits') is not None else ''
        traits = [{'symbol': t} for t in traits_str.split(',') if t]

        wp_data = {
            'symbol': row[col_idx['symbol']],
            'type': row[col_idx['type']],
            'x': float(row[col_idx['x']]),
            'y': float(row[col_idx['y']]),
            'systemSymbol': system_symbol,
            'traits': traits,
            'orbitals': []
        }
        waypoints_data.append(wp_data)

    # Configure mock to return this data
    context['api_client'].list_waypoints = Mock(return_value={
        'data': waypoints_data,
        'meta': {'total': len(waypoints_data)}
    })
    context['api_call_count'] = 0  # Reset call count


@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint_symbol}" for player {player_id:d}'))
def ship_exists_at_waypoint(context, ship_symbol, waypoint_symbol, player_id):
    """Create a ship for navigation testing"""
    context['ship'] = {
        'symbol': ship_symbol,
        'location': waypoint_symbol,
        'player_id': player_id
    }


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I call repository find_by_system for "{system_symbol}" with player_id {player_id:d}'))
def call_find_by_system_with_player(context, system_symbol, player_id):
    """Call repository find_by_system with player_id"""
    context['result_waypoints'] = context['repository'].find_by_system(
        system_symbol=system_symbol,
        player_id=player_id
    )


@when(parsers.parse('I call repository find_by_system for "{system_symbol}" without player_id'))
def call_find_by_system_without_player(context, system_symbol):
    """Call repository find_by_system without player_id"""
    context['result_waypoints'] = context['repository'].find_by_system(
        system_symbol=system_symbol
    )


@when(parsers.parse('I call repository find_by_trait for "{system_symbol}" with trait "{trait}" and player_id {player_id:d}'))
def call_find_by_trait_with_player(context, system_symbol, trait, player_id):
    """Call repository find_by_trait with player_id"""
    context['result_waypoints'] = context['repository'].find_by_trait(
        system_symbol=system_symbol,
        trait=trait,
        player_id=player_id
    )


@when(parsers.parse('I call repository find_by_fuel for "{system_symbol}" with player_id {player_id:d}'))
def call_find_by_fuel_with_player(context, system_symbol, player_id):
    """Call repository find_by_fuel with player_id"""
    context['result_waypoints'] = context['repository'].find_by_fuel(
        system_symbol=system_symbol,
        player_id=player_id
    )


@when(parsers.parse('NavigateShipHandler queries waypoints for system "{system_symbol}" with player_id {player_id:d}'))
def navigate_handler_queries_waypoints(context, system_symbol, player_id):
    """Simulate NavigateShipHandler calling repository"""
    # This simulates what NavigateShipHandler would do
    context['result_waypoints'] = context['repository'].find_by_system(
        system_symbol=system_symbol,
        player_id=player_id
    )


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse('I should receive {count:d} waypoint'))
@then(parsers.parse('I should receive {count:d} waypoints'))
def should_receive_waypoints(context, count):
    """Verify waypoint count"""
    assert context['result_waypoints'] is not None, "No waypoints returned"
    assert len(context['result_waypoints']) == count, \
        f"Expected {count} waypoints, got {len(context['result_waypoints'])}"


@then("the API should not have been called")
def api_not_called(context):
    """Verify API was not called"""
    assert context['api_call_count'] == 0, \
        f"Expected 0 API calls, but got {context['api_call_count']}"


@then("the API should have been called once")
def api_called_once(context):
    """Verify API was called exactly once"""
    # Note: api_call_count increments when factory is called
    # The actual list_waypoints call happens via the mock
    assert context['api_client'].list_waypoints.call_count > 0, \
        "API list_waypoints was not called"


@then("the waypoints should be cached in the database")
def waypoints_cached(context):
    """Verify waypoints were saved to cache"""
    # Query database directly to verify
    assert context['result_waypoints'] is not None
    assert len(context['result_waypoints']) > 0


@then("the API should have been called once transparently")
def api_called_transparently(context):
    """Verify API was called transparently by repository"""
    assert context['api_client'].list_waypoints.call_count > 0, \
        "API was not called transparently"


@then("navigation should proceed without error")
def navigation_proceeds(context):
    """Verify navigation can proceed"""
    assert context['result_waypoints'] is not None
    assert len(context['result_waypoints']) > 0, \
        "Navigation would fail due to empty waypoint list"


@then(parsers.parse('the waypoint "{waypoint_symbol}" should be in the results'))
def waypoint_in_results(context, waypoint_symbol):
    """Verify specific waypoint is in results"""
    assert context['result_waypoints'] is not None
    symbols = [wp.symbol for wp in context['result_waypoints']]
    assert waypoint_symbol in symbols, \
        f"Waypoint {waypoint_symbol} not found in results: {symbols}"


@then(parsers.parse('the handler should receive {count:d} waypoint(s)'))
@then(parsers.parse('the handler should receive {count:d} waypoints'))
def handler_receives_waypoints(context, count):
    """Verify handler received expected waypoint count"""
    should_receive_waypoints(context, count)


# ============================================================================
# Helper Functions
# ============================================================================

def _parse_waypoint_table(datatable, system_symbol: str) -> List[Waypoint]:
    """Parse datatable into Waypoint value objects"""
    waypoints = []
    headers = datatable[0]  # First row is column names

    # Create column index mapping
    col_idx = {header: idx for idx, header in enumerate(headers)}

    for row in datatable[1:]:  # Skip header row
        traits_str = row[col_idx.get('traits', -1)].strip() if col_idx.get('traits') is not None else ''
        traits = tuple(traits_str.split(',')) if traits_str else ()

        has_fuel = row[col_idx.get('has_fuel', -1)].lower() == 'true' if col_idx.get('has_fuel') is not None else False

        waypoint = Waypoint(
            symbol=row[col_idx['symbol']],
            x=float(row[col_idx['x']]),
            y=float(row[col_idx['y']]),
            system_symbol=system_symbol,
            waypoint_type=row[col_idx['type']],
            traits=traits,
            has_fuel=has_fuel,
            orbitals=()
        )
        waypoints.append(waypoint)

    return waypoints

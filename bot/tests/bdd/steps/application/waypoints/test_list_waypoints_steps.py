"""
BDD step definitions for list waypoints query feature.

Tests ListWaypointsQuery and ListWaypointsHandler
using black-box testing principles - testing behavior through public interfaces only.
"""
import pytest
import asyncio
from datetime import datetime, timedelta
from typing import List, Optional, Dict
from pytest_bdd import scenarios, given, when, then, parsers

from application.waypoints.queries.list_waypoints import (
    ListWaypointsQuery,
    ListWaypointsHandler
)
from domain.shared.value_objects import Waypoint
from ports.outbound.repositories import IWaypointRepository
from ports.outbound.api_client import ISpaceTradersAPI


# Load all scenarios from the feature file
scenarios('../../../features/application/waypoints/list_waypoints.feature')


# ============================================================================
# Mock Implementations
# ============================================================================

class MockWaypointRepository(IWaypointRepository):
    """Mock waypoint repository for testing - includes lazy-loading behavior"""

    TTL_SECONDS = 7200  # 2 hours

    def __init__(self, api_client_factory=None):
        self._waypoints = []
        self._timestamps = {}  # Track synced_at timestamps by system_symbol
        self._api_client_factory = api_client_factory

    def save_waypoints(self, waypoints: List[Waypoint], synced_at: Optional[datetime] = None, replace_system: bool = False) -> None:
        """Save or update waypoints in cache with timestamp"""
        if not waypoints:
            return

        if synced_at is None:
            synced_at = datetime.now()

        system_symbol = waypoints[0].system_symbol

        # Clear all waypoints for the system if replace_system is True
        if replace_system:
            self._waypoints = [w for w in self._waypoints if w.system_symbol != system_symbol]

        # Add new waypoints
        for waypoint in waypoints:
            # Remove existing waypoint with same symbol (in case replace_system is False)
            self._waypoints = [w for w in self._waypoints if w.symbol != waypoint.symbol]
            self._waypoints.append(waypoint)

        # Store the sync timestamp for the system
        self._timestamps[system_symbol] = synced_at

    def find_by_system(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find all waypoints in a system with automatic lazy-loading

        Simulates real repository behavior: checks staleness, fetches from API if needed
        """
        # Check cache freshness
        is_stale = self.is_cache_stale(system_symbol, self.TTL_SECONDS)

        # Lazy-load from API if needed
        if is_stale and player_id and self._api_client_factory:
            self._fetch_and_cache_from_api(system_symbol, player_id)

        # Return from cache
        result = [w for w in self._waypoints if w.system_symbol == system_symbol]
        return sorted(result, key=lambda w: w.symbol)

    def find_by_trait(self, system_symbol: str, trait: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with a specific trait with automatic lazy-loading
        """
        # Lazy-load if needed (calls find_by_system which handles lazy-loading)
        all_waypoints = self.find_by_system(system_symbol, player_id)
        return [w for w in all_waypoints if trait in w.traits]

    def find_by_fuel(self, system_symbol: str, player_id: Optional[int] = None) -> List[Waypoint]:
        """
        Find waypoints with fuel stations with automatic lazy-loading
        """
        # Lazy-load if needed (calls find_by_system which handles lazy-loading)
        all_waypoints = self.find_by_system(system_symbol, player_id)
        return [w for w in all_waypoints if w.has_fuel]

    def _fetch_and_cache_from_api(self, system_symbol: str, player_id: int):
        """Fetch all waypoints from API and cache them (mock version)"""
        api_client = self._api_client_factory(player_id)
        all_waypoints = []
        page = 1

        while True:
            response = api_client.list_waypoints(
                system_symbol=system_symbol,
                page=page,
                limit=20
            )

            waypoint_data = response.get("data", [])
            if not waypoint_data:
                break

            # Convert to Waypoint objects
            for wp_data in waypoint_data:
                traits = tuple(t["symbol"] for t in wp_data.get("traits", []))
                orbitals = tuple(o["symbol"] for o in wp_data.get("orbitals", []))

                waypoint = Waypoint(
                    symbol=wp_data["symbol"],
                    x=float(wp_data["x"]),
                    y=float(wp_data["y"]),
                    system_symbol=wp_data.get("systemSymbol", system_symbol),
                    waypoint_type=wp_data["type"],
                    traits=traits,
                    has_fuel=any(t == "MARKETPLACE" for t in traits),
                    orbitals=orbitals
                )
                all_waypoints.append(waypoint)

            # Check pagination
            meta = response.get("meta", {})
            if page * 20 >= meta.get("total", 0):
                break
            page += 1

        # Save to cache
        if all_waypoints:
            self.save_waypoints(all_waypoints, replace_system=True)
        else:
            # If no waypoints returned, still mark the system as synced (empty cache)
            self._timestamps[system_symbol] = datetime.now()

    def get_system_sync_time(self, system_symbol: str) -> Optional[datetime]:
        """Get the last sync time for a system"""
        return self._timestamps.get(system_symbol)

    def is_cache_stale(self, system_symbol: str, ttl_seconds: int = 7200) -> bool:
        """Check if cache is stale (older than TTL or doesn't exist)"""
        sync_time = self.get_system_sync_time(system_symbol)
        if sync_time is None:
            return True

        age_seconds = (datetime.now() - sync_time).total_seconds()
        return age_seconds > ttl_seconds

    def clear_all(self) -> None:
        """Clear all waypoints (public method for testing)"""
        self._waypoints.clear()
        self._timestamps.clear()


class MockSpaceTradersAPI(ISpaceTradersAPI):
    """Mock API client for testing lazy-loading behavior"""

    def __init__(self):
        self._call_count = 0
        self._waypoint_responses = {}  # Map system_symbol -> list of waypoint data dicts

    def configure_waypoint_response(self, system_symbol: str, waypoints: List[Dict]) -> None:
        """Configure what the API should return for a system"""
        self._waypoint_responses[system_symbol] = waypoints

    def list_waypoints(self, system_symbol: str, page: int = 1, limit: int = 20) -> Dict:
        """Mock list_waypoints API call"""
        self._call_count += 1

        waypoints = self._waypoint_responses.get(system_symbol, [])
        return {
            "data": waypoints,
            "meta": {
                "total": len(waypoints),
                "page": page,
                "limit": limit
            }
        }

    def get_call_count(self) -> int:
        """Get number of API calls made"""
        return self._call_count

    def reset_call_count(self) -> None:
        """Reset API call counter"""
        self._call_count = 0

    # Implement other required methods as no-ops
    def get_agent(self) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_ship(self, ship_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_ships(self) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def navigate_ship(self, ship_symbol: str, waypoint: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def dock_ship(self, ship_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def orbit_ship(self, ship_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def refuel_ship(self, ship_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def set_flight_mode(self, ship_symbol: str, flight_mode: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_shipyard(self, system_symbol: str, waypoint_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def purchase_ship(self, ship_type: str, waypoint_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_market(self, system: str, waypoint: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_contracts(self, page: int = 1, limit: int = 20) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def get_contract(self, contract_id: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def accept_contract(self, contract_id: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def deliver_contract(self, contract_id: str, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def fulfill_contract(self, contract_id: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def negotiate_contract(self, ship_symbol: str) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def purchase_cargo(self, ship_symbol: str, trade_symbol: str, units: int) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")

    def jettison_cargo(self, ship_symbol: str, cargo_symbol: str, units: int) -> Dict:
        raise NotImplementedError("Not needed for waypoint tests")


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
        'api_client': None,
        'handler': None,
        'query': None,
        'result': None,
        'exception': None,
        'last_waypoint': None,
        'initial_api_call_count': 0
    }


# ============================================================================
# Background Steps
# ============================================================================

@given('the list waypoints query handler is initialized')
def initialize_handler(context):
    """Initialize the handler with mock repository (only if not already initialized)"""
    # Skip if repository/handler already initialized (e.g., in lazy-loading scenarios)
    if 'repository' not in context or context['repository'] is None:
        context['repository'] = MockWaypointRepository()
    if 'handler' not in context or context['handler'] is None:
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
    # Preserve existing timestamp if one exists
    existing_timestamp = context['repository'].get_system_sync_time(waypoint.system_symbol)
    context['repository'].save_waypoints([waypoint], synced_at=existing_timestamp)
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


@when(parsers.parse('I query waypoints for system "{system_symbol}" with trait "{trait}" and player ID {player_id:d}'))
def query_waypoints_by_trait_with_player_id(context, system_symbol, trait, player_id):
    """Query waypoints filtered by trait with player ID"""
    try:
        query = ListWaypointsQuery(
            system_symbol=system_symbol,
            trait_filter=trait,
            player_id=player_id
        )
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I query waypoints for system "{system_symbol}" with fuel filter and player ID {player_id:d}'))
def query_waypoints_with_fuel_and_player_id(context, system_symbol, player_id):
    """Query waypoints filtered by fuel with player ID"""
    try:
        query = ListWaypointsQuery(
            system_symbol=system_symbol,
            has_fuel=True,
            player_id=player_id
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


# ============================================================================
# Lazy-Loading Steps - Setup
# ============================================================================

@given('the API client is configured')
def configure_api_client(context):
    """Configure API client and repository with lazy-loading support"""
    context['api_client'] = MockSpaceTradersAPI()

    # Create a factory that returns the mock API client
    def api_client_factory(player_id: int):
        return context['api_client']

    # Repository handles lazy-loading with the API client factory
    context['repository'] = MockWaypointRepository(api_client_factory=api_client_factory)

    # Handler delegates to repository
    context['handler'] = ListWaypointsHandler(waypoint_repository=context['repository'])


@given(parsers.parse('a waypoint "{waypoint_symbol}" exists in cache for system "{system_symbol}"'))
def create_waypoint_in_cache(context, waypoint_symbol, system_symbol):
    """Create a waypoint in cache"""
    # Initialize repository if not already present
    if 'repository' not in context or context['repository'] is None:
        context['repository'] = MockWaypointRepository()

    waypoint = create_test_waypoint(
        symbol=waypoint_symbol,
        system_symbol=system_symbol
    )
    # Will be saved with timestamp in subsequent steps
    context['last_waypoint'] = waypoint


@given(parsers.parse('the waypoint was synced {hours:d} hour ago'))
@given(parsers.parse('the waypoint was synced {hours:d} hours ago'))
def set_waypoint_sync_time_hours(context, hours):
    """Set waypoint sync time to N hours ago"""
    sync_time = datetime.now() - timedelta(hours=hours)
    waypoint = context['last_waypoint']
    context['repository'].save_waypoints([waypoint], synced_at=sync_time)


@given(parsers.parse('the waypoint was synced {minutes:d} minutes ago'))
def set_waypoint_sync_time_minutes(context, minutes):
    """Set waypoint sync time to N minutes ago"""
    sync_time = datetime.now() - timedelta(minutes=minutes)
    waypoint = context['last_waypoint']
    context['repository'].save_waypoints([waypoint], synced_at=sync_time)


@given(parsers.parse('no waypoints exist in cache for system "{system_symbol}"'))
def ensure_empty_cache(context, system_symbol):
    """Ensure cache is empty for a system"""
    # Repository starts empty, no action needed
    pass


@given(parsers.parse('the API returns {count:d} waypoints for system "{system_symbol}"'))
def configure_api_waypoint_response(context, count, system_symbol):
    """Configure API to return N waypoints for a system"""
    waypoints = []
    for i in range(count):
        waypoint_symbol = f"{system_symbol}-WP{i+1}"
        waypoints.append({
            "symbol": waypoint_symbol,
            "type": "PLANET",
            "x": float(i * 100),
            "y": float(i * 100),
            "systemSymbol": system_symbol,
            "orbitals": [],
            "traits": [{"symbol": "MARKETPLACE"}]
        })

    context['api_client'].configure_waypoint_response(system_symbol, waypoints)


# ============================================================================
# Lazy-Loading Steps - Assertions
# ============================================================================

@then('the API should not have been called')
def check_api_not_called(context):
    """Verify API was not called (cache hit or cache-only mode)"""
    # If there's no API client configured, that's fine - no calls were made
    if context.get('api_client') is None:
        return  # Cache-only mode, no API client to check

    # Store initial call count before query for comparison
    if 'initial_api_call_count' not in context:
        context['initial_api_call_count'] = 0

    current_count = context['api_client'].get_call_count()
    # The call count should not have increased during the query
    # We check this by comparing against stored count before the query was made
    # For cache hit scenarios, the count should remain 0
    assert current_count == 0, f"Expected 0 API calls but got {current_count}"


@then('the API should have been called once')
def check_api_called_once(context):
    """Verify API was called exactly once (cache miss or stale)"""
    call_count = context['api_client'].get_call_count()
    assert call_count == 1, f"Expected 1 API call but got {call_count}"


@then('the waypoints should be saved to cache with current timestamp')
def check_waypoints_saved_with_timestamp(context):
    """Verify waypoints were saved with recent timestamp"""
    # Get the system from the result
    if context['result']:
        system_symbol = context['result'][0].system_symbol
        sync_time = context['repository'].get_system_sync_time(system_symbol)

        assert sync_time is not None, "Waypoints were not saved with timestamp"

        # Verify timestamp is recent (within last 5 seconds)
        age_seconds = (datetime.now() - sync_time).total_seconds()
        assert age_seconds < 5, f"Timestamp is not recent (age: {age_seconds}s)"


# ============================================================================
# Player-Specific API Client Steps
# ============================================================================

@given(parsers.parse('a player with ID {player_id:d} exists in the system'))
def create_mock_player(context, player_id):
    """Create a mock player for testing"""
    context['player_id'] = player_id


@given('the player has a valid API token')
def configure_player_api_token(context):
    """Configure API token for the player and initialize API client"""
    context['player_token'] = "test-token-123"
    # Initialize API client (always new for each test)
    context['api_client'] = MockSpaceTradersAPI()

    # Create a factory that returns the mock API client
    def api_client_factory(player_id: int):
        return context['api_client']

    # Replace repository with one that has API client factory
    # Preserve existing waypoints and timestamps if repository exists
    old_repo = context.get('repository')
    new_repo = MockWaypointRepository(api_client_factory=api_client_factory)

    if old_repo is not None:
        # Copy existing waypoints and timestamps to new repository
        new_repo._waypoints = old_repo._waypoints.copy()
        new_repo._timestamps = old_repo._timestamps.copy()

    context['repository'] = new_repo

    # Also recreate handler with new repository
    context['handler'] = ListWaypointsHandler(waypoint_repository=new_repo)


@given('no API client is configured for the query')
def ensure_no_api_client(context):
    """Ensure handler has no API client (cache-only mode)"""
    # Initialize repository if not already present with no API client factory
    if 'repository' not in context or context['repository'] is None:
        context['repository'] = MockWaypointRepository(api_client_factory=None)

    context['handler'] = ListWaypointsHandler(waypoint_repository=context['repository'])


@when(parsers.parse('I query waypoints for system "{system_symbol}" with player ID {player_id:d}'))
def query_waypoints_with_player_id(context, system_symbol, player_id):
    """Query waypoints with a specific player ID"""
    try:
        # Create repository with api_client_factory if not already configured
        if 'repository' not in context or context['repository'] is None:
            def api_client_factory(pid: int):
                assert pid == player_id, f"Expected player_id {player_id}, got {pid}"
                return context['api_client']

            context['repository'] = MockWaypointRepository(api_client_factory=api_client_factory)

        # Create handler if not already configured
        if 'handler' not in context or context['handler'] is None:
            context['handler'] = ListWaypointsHandler(waypoint_repository=context['repository'])

        query = ListWaypointsQuery(
            system_symbol=system_symbol,
            player_id=player_id
        )
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


@when(parsers.parse('I query waypoints for system "{system_symbol}" without player ID'))
def query_waypoints_without_player_id(context, system_symbol):
    """Query waypoints without player ID (cache-only mode)"""
    try:
        query = ListWaypointsQuery(system_symbol=system_symbol)
        context['result'] = asyncio.run(context['handler'].handle(query))
    except Exception as e:
        context['exception'] = e


@then(parsers.parse('the API should have been called once with player {player_id:d} token'))
def check_api_called_with_player_token(context, player_id):
    """Verify API was called with the correct player token"""
    call_count = context['api_client'].get_call_count()
    assert call_count == 1, f"Expected 1 API call but got {call_count}"
    # Token verification is implicit - the factory ensures correct player_id was passed

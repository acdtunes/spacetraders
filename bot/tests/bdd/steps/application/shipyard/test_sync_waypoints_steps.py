"""Step definitions for syncing waypoints feature"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock, MagicMock
from typing import List, Dict

from domain.shared.value_objects import Waypoint
from adapters.secondary.persistence.database import Database
from configuration.container import (
    get_waypoint_repository,
    get_mediator,
    get_player_repository,
    reset_container
)
from application.shipyard.commands.sync_waypoints import SyncSystemWaypointsCommand

# Link feature file
scenarios('../../../features/application/shipyard/sync_waypoints.feature')


@pytest.fixture
def context():
    """Shared context for step definitions"""
    return {}


@pytest.fixture(autouse=True)
def database():
    """Initialize in-memory database for each test"""
    reset_container()

    # Initialize SQLAlchemy schema
    from configuration.container import get_engine
    from adapters.secondary.persistence.models import metadata
    engine = get_engine()
    metadata.create_all(engine)

    db = Database(":memory:")
    return db


@given("the database is initialized")
def database_initialized(database):
    """Database is already initialized via fixture"""
    pass


@given(parsers.parse('a player exists with agent "{agent}" and player_id {player_id:d}'))
def player_exists(agent: str, player_id: int):
    """Create a test player"""
    from domain.shared.player import Player
    from datetime import datetime

    player = Player(
        player_id=None,  # Let repository assign ID
        agent_symbol=agent,
        token="test-token",
        created_at=datetime.now()
    )
    player_repo = get_player_repository()
    player_repo.create(player)


@given(parsers.parse('the API will return waypoints for system "{system_symbol}":'))
def mock_api_waypoints(context, system_symbol: str, datatable, monkeypatch):
    """Mock API client to return waypoint data"""
    waypoints_data = _parse_api_waypoint_table(datatable, system_symbol)

    # Mock the API client factory with pagination support
    # First call returns data, subsequent calls return empty
    call_count = {'count': 0}

    def list_waypoints_with_pagination(system_symbol, page=1, limit=20):
        call_count['count'] += 1
        if call_count['count'] == 1:
            return {'data': waypoints_data}
        return {'data': []}  # Empty data signals end of pagination

    mock_api_client = Mock()
    mock_api_client.list_waypoints = Mock(side_effect=list_waypoints_with_pagination)

    # Store in context for assertions
    context['mock_api_client'] = mock_api_client
    context['mocked_waypoints'] = waypoints_data

    # Patch get_api_client_for_player to return our mock
    from configuration import container
    monkeypatch.setattr(
        container,
        'get_api_client_for_player',
        lambda player_id: mock_api_client
    )


@given(parsers.parse('waypoints are already cached for system "{system_symbol}":'))
def cached_waypoints_exist(context, system_symbol: str, datatable):
    """Pre-populate cache with waypoints"""
    waypoint_repo = get_waypoint_repository()
    waypoints = _parse_waypoint_table_for_cache(datatable, system_symbol)
    waypoint_repo.save_waypoints(waypoints)
    context['initial_cache'] = waypoints


@given(parsers.parse('the API will return waypoints for system "{system_symbol}" across {page_count:d} pages:'))
def mock_api_paginated_waypoints(context, system_symbol: str, page_count: int, datatable, monkeypatch):
    """Mock API client to return paginated waypoint data"""
    # Parse pagination table
    headers = datatable[0]
    col_idx = {header: idx for idx, header in enumerate(headers)}

    page_config = []
    for row in datatable[1:]:
        page_config.append({
            'page': int(row[col_idx['page']]),
            'count': int(row[col_idx['waypoint_count']])
        })

    # Generate waypoint data for each page
    def list_waypoints_paginated(system_symbol, page=1, limit=20):
        for config in page_config:
            if config['page'] == page:
                waypoints = []
                for i in range(config['count']):
                    waypoints.append({
                        'symbol': f"{system_symbol}-WP{page}-{i}",
                        'type': 'MOON',
                        'x': float(i * 10),
                        'y': float(i * 10),
                        'traits': []
                    })
                return {'data': waypoints}
        return {'data': []}  # No more pages

    mock_api_client = Mock()
    mock_api_client.list_waypoints = Mock(side_effect=list_waypoints_paginated)

    context['mock_api_client'] = mock_api_client

    # Patch get_api_client_for_player
    from configuration import container
    monkeypatch.setattr(
        container,
        'get_api_client_for_player',
        lambda player_id: mock_api_client
    )


@when(parsers.parse('I sync waypoints for system "{system_symbol}" for player {player_id:d}'))
def sync_waypoints(context, system_symbol: str, player_id: int):
    """Execute SyncSystemWaypointsCommand"""
    import asyncio
    mediator = get_mediator()
    command = SyncSystemWaypointsCommand(
        system_symbol=system_symbol,
        player_id=player_id
    )
    asyncio.run(mediator.send_async(command))
    context['synced_system'] = system_symbol


@then("waypoints should be cached in the database")
def verify_waypoints_cached(context):
    """Verify waypoints were saved to cache"""
    waypoint_repo = get_waypoint_repository()
    system_symbol = context['synced_system']
    cached = waypoint_repo.find_by_system(system_symbol)
    assert len(cached) > 0, "No waypoints found in cache"


@then(parsers.parse('the cache should contain {count:d} waypoint for system "{system_symbol}"'))
@then(parsers.parse('the cache should contain {count:d} waypoints for system "{system_symbol}"'))
def verify_cache_count(count: int, system_symbol: str):
    """Verify the number of cached waypoints"""
    waypoint_repo = get_waypoint_repository()
    cached = waypoint_repo.find_by_system(system_symbol)
    assert len(cached) == count, f"Expected {count} waypoints, found {len(cached)}"


@then(parsers.parse('waypoint "{symbol}" should have traits "{trait1}" and "{trait2}"'))
def verify_waypoint_has_traits(symbol: str, trait1: str, trait2: str):
    """Verify waypoint has specific traits"""
    waypoint_repo = get_waypoint_repository()
    system_symbol = '-'.join(symbol.split('-')[:2])
    all_waypoints = waypoint_repo.find_by_system(system_symbol)

    waypoint = None
    for wp in all_waypoints:
        if wp.symbol == symbol:
            waypoint = wp
            break

    assert waypoint is not None, f"Waypoint {symbol} not found in cache"
    assert trait1 in waypoint.traits, f"Trait {trait1} not found in waypoint traits"
    assert trait2 in waypoint.traits, f"Trait {trait2} not found in waypoint traits"


def _parse_api_waypoint_table(datatable, system_symbol: str) -> List[Dict]:
    """Parse datatable into API response format"""
    waypoints_data = []
    headers = datatable[0]
    col_idx = {header: idx for idx, header in enumerate(headers)}

    for row in datatable[1:]:
        traits_str = row[col_idx['traits']].strip() if col_idx.get('traits') is not None else ''
        traits_list = [{'symbol': t.strip()} for t in traits_str.split(',') if t.strip()]

        waypoint_data = {
            'symbol': row[col_idx['symbol']],
            'type': row[col_idx['type']],
            'x': float(row[col_idx['x']]),
            'y': float(row[col_idx['y']]),
            'traits': traits_list
        }
        waypoints_data.append(waypoint_data)

    return waypoints_data


def _parse_waypoint_table_for_cache(datatable, system_symbol: str) -> List[Waypoint]:
    """Parse datatable into Waypoint value objects for cache"""
    waypoints = []
    headers = datatable[0]
    col_idx = {header: idx for idx, header in enumerate(headers)}

    for row in datatable[1:]:
        traits_str = row[col_idx['traits']].strip() if col_idx.get('traits') is not None else ''
        traits = tuple(traits_str.split(',')) if traits_str else ()

        waypoint = Waypoint(
            symbol=row[col_idx['symbol']],
            x=float(row[col_idx['x']]),
            y=float(row[col_idx['y']]),
            system_symbol=system_symbol,
            waypoint_type=row[col_idx['type']],
            traits=traits,
            has_fuel=False,
            orbitals=()
        )
        waypoints.append(waypoint)

    return waypoints

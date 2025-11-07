"""Step definitions for scout markets idempotency tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
from datetime import datetime, timezone
from typing import Dict, List

from application.scouting.commands.scout_markets import ScoutMarketsCommand
from configuration.container import (
    get_mediator,
    get_ship_repository,
    get_database,
    reset_container
)

# Load scenarios
scenarios('../../../features/application/scouting/scout_markets_idempotency.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    reset_container()
    return {
        'mediator': get_mediator(),
        'ship_repo': get_ship_repository(),
        'player_id': 1,
        'ships': [],
        'markets': [],
        'existing_containers': [],
        'created_containers': [],
        'concurrent_results': []
    }


@pytest.fixture(autouse=True)
def cleanup():
    """Cleanup after each test"""
    yield
    reset_container()


# Background steps (reuse from test_scout_markets_steps.py)

@given(parsers.parse('a player with ID {player_id:d}'))
def create_player(context, player_id):
    """Create a player in the database"""
    context['player_id'] = player_id

    from configuration.container import get_database
    db = get_database()
    with db.transaction() as conn:
        conn.execute("""
            INSERT OR IGNORE INTO players (player_id, agent_symbol, token, created_at)
            VALUES (?, ?, ?, ?)
        """, (player_id, f'TEST_AGENT_{player_id}', 'test_token', '2025-01-01T00:00:00Z'))


@given(parsers.parse('ships in system "{system}":'))
def create_ships(context, system, datatable):
    """Setup ship data for mocking API responses"""
    headers = datatable[0]
    context['ships_data'] = []

    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        ship_symbol = row_dict['ship_symbol']
        waypoint = row_dict['waypoint']
        status = row_dict['status']

        # Store ship data for API mocking
        context['ships_data'].append({
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': waypoint,
                'systemSymbol': system,
                'status': status,
                'flightMode': 'CRUISE'
            },
            'fuel': {
                'current': 300,
                'capacity': 400
            },
            'cargo': {
                'capacity': 40,
                'units': 0,
                'inventory': []
            },
            'frame': {
                'symbol': 'FRAME_PROBE'
            },
            'reactor': {
                'symbol': 'REACTOR_SOLAR_I'
            },
            'engine': {
                'symbol': 'ENGINE_IMPULSE_DRIVE_I',
                'speed': 30
            },
            'modules': [],
            'mounts': []
        })
        context['ships'].append(ship_symbol)


# Idempotency-specific steps

@given('existing scout containers:')
def setup_existing_containers(context, datatable):
    """Setup existing scout containers in mock daemon"""
    headers = datatable[0]
    for row in datatable[1:]:
        row_dict = dict(zip(headers, row))
        context['existing_containers'].append({
            'container_id': row_dict['container_id'],
            'ship_symbol': row_dict['ship_symbol'],
            'status': row_dict['status'],
            'player_id': context['player_id']
        })


@when(parsers.parse('I execute scout markets for system "{system}" with ships:'))
def set_ships_for_scout_markets(context, system, datatable):
    """Store ships for scout markets"""
    context['system'] = system
    context['scout_ships'] = [row[0] for row in datatable]


@when('markets:')
def set_markets_and_execute(context, datatable):
    """Store markets and execute scout markets command"""
    context['markets'] = [row[0] for row in datatable]
    _execute_scout_markets(context)


@when(parsers.parse('I execute scout markets concurrently {count:d} times for system "{system}" with ships:'))
def set_concurrent_execution(context, count, system, datatable):
    """Setup for concurrent execution"""
    context['system'] = system
    context['scout_ships'] = [row[0] for row in datatable]
    context['concurrent_count'] = count


@when('the first call times out after 5 minutes')
def simulate_timeout(context):
    """Simulate timeout - this is a no-op since we're testing retry behavior"""
    # The timeout is implicit - we just test that a second call reuses containers
    pass


@when('I retry scout markets with the same parameters')
def retry_scout_markets(context):
    """Retry scout markets with same parameters"""
    _execute_scout_markets(context)


# Assertion steps

@then('scout markets should complete successfully')
def check_scout_success(context):
    """Verify scout markets completed successfully"""
    if not context.get('scout_succeeded'):
        error_msg = context.get('scout_error', 'Unknown error')
        assert False, f"Scout markets failed: {error_msg}"


@then('all scout market calls should complete successfully')
def check_all_concurrent_calls_succeeded(context):
    """Verify all concurrent calls succeeded"""
    for i, result in enumerate(context['concurrent_results']):
        if not result.get('succeeded'):
            error_msg = result.get('error', 'Unknown error')
            assert False, f"Concurrent call {i+1} failed: {error_msg}"


@then(parsers.parse('{count:d} container IDs should be returned'))
def check_container_count(context, count):
    """Verify number of container IDs returned"""
    result = context['scout_result']
    assert len(result.container_ids) == count, \
        f"Expected {count} container IDs but got {len(result.container_ids)}"


@then('no new containers should be created')
def check_no_new_containers_created(context):
    """Verify no new containers were created"""
    new_containers_count = len(context['created_containers'])
    assert new_containers_count == 0, \
        f"Expected 0 new containers but {new_containers_count} were created: {context['created_containers']}"


@then(parsers.parse('{count:d} new containers should be created'))
def check_new_containers_created(context, count):
    """Verify expected number of new containers were created"""
    new_containers_count = len(context['created_containers'])
    assert new_containers_count == count, \
        f"Expected {count} new containers but {new_containers_count} were created: {context['created_containers']}"


@then(parsers.parse('container "{container_id}" should be reused'))
def check_container_reused(context, container_id):
    """Verify specific container was reused (not created)"""
    result = context['scout_result']

    # Check container is in returned IDs
    assert container_id in result.container_ids, \
        f"Container {container_id} not in returned IDs: {result.container_ids}"

    # Check container was NOT newly created
    newly_created_ids = [c['container_id'] for c in context['created_containers']]
    assert container_id not in newly_created_ids, \
        f"Container {container_id} should be reused but was newly created"


@then(parsers.parse('container "{container_id}" should not be reused'))
def check_container_not_reused(context, container_id):
    """Verify specific container was not reused"""
    result = context['scout_result']

    # Check container is NOT in returned IDs
    assert container_id not in result.container_ids, \
        f"Container {container_id} should not be reused but was returned in IDs: {result.container_ids}"


@then(parsers.parse('container for "{ship_symbol}" should be newly created'))
def check_ship_has_new_container(context, ship_symbol):
    """Verify ship got a newly created container"""
    # Find newly created container for this ship
    ship_containers = [
        c for c in context['created_containers']
        if c['ship'] == ship_symbol
    ]

    assert len(ship_containers) > 0, \
        f"No new container created for ship {ship_symbol}"


@then(parsers.parse('exactly {count:d} unique containers should exist'))
def check_unique_container_count(context, count):
    """Verify exact number of unique containers exist across all concurrent calls"""
    all_container_ids = set()
    for result in context['concurrent_results']:
        if result.get('succeeded') and 'result' in result:
            all_container_ids.update(result['result'].container_ids)

    assert len(all_container_ids) == count, \
        f"Expected {count} unique containers but found {len(all_container_ids)}: {all_container_ids}"


@then(parsers.parse('no duplicate containers should exist for "{ship_symbol}"'))
def check_no_duplicates_for_ship(context, ship_symbol):
    """Verify no duplicate containers exist for a specific ship"""
    # Collect all containers for this ship from the created containers list
    # (The concurrent results may show the same container ID multiple times if it was reused,
    #  which is correct behavior. We need to check that only 1 unique container was created.)
    ship_containers = []
    for container in context['all_created_containers']:
        if container['ship_symbol'] == ship_symbol:
            ship_containers.append(container['container_id'])

    # Check that only one unique container exists for this ship
    unique_containers = set(ship_containers)
    assert len(unique_containers) == 1, \
        f"Expected exactly 1 container for {ship_symbol} but found {len(unique_containers)}: {ship_containers}"


@then('no duplicate containers should be created')
def check_no_duplicate_containers(context):
    """Verify no duplicate containers were created in retry scenario"""
    result = context['scout_result']

    # Check all returned container IDs are unique
    unique_ids = set(result.container_ids)
    assert len(result.container_ids) == len(unique_ids), \
        f"Duplicate containers detected: {result.container_ids}"


@then('the retry should complete successfully')
def check_retry_succeeded(context):
    """Verify retry completed successfully"""
    check_scout_success(context)


# Helper functions

def _execute_scout_markets(context):
    """Execute scout markets command with mocked dependencies"""
    # Mock ship repository to return ships
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    mock_ship_repo = Mock()

    def mock_find_by_symbol(ship_symbol, player_id):
        ship_data = next((s for s in context['ships_data'] if s['symbol'] == ship_symbol), None)
        if not ship_data:
            return None

        # Convert API data to Ship entity
        waypoint_symbol = ship_data['nav']['waypointSymbol']
        system_symbol = ship_data['nav']['systemSymbol']

        return Ship(
            ship_symbol=ship_symbol,
            player_id=player_id,
            current_location=Waypoint(
                symbol=waypoint_symbol,
                waypoint_type='PLANET',
                x=0,
                y=0,
                system_symbol=system_symbol,
                traits=tuple(),
                has_fuel=True
            ),
            fuel=Fuel(
                current=ship_data['fuel']['current'],
                capacity=ship_data['fuel']['capacity']
            ),
            fuel_capacity=ship_data['fuel']['capacity'],
            cargo_capacity=ship_data['cargo']['capacity'],
            cargo_units=ship_data['cargo']['units'],
            engine_speed=ship_data['engine']['speed'],
            nav_status=ship_data['nav']['status']
        )

    mock_ship_repo.find_by_symbol.side_effect = mock_find_by_symbol

    # Mock graph provider
    mock_graph = Mock()
    import sys
    import os
    tests_dir = os.path.join(os.path.dirname(__file__), '../../../../')
    sys.path.insert(0, tests_dir)
    try:
        from fixtures.graph_fixtures import get_mock_graph_for_system
        mock_graph.get_graph.return_value = get_mock_graph_for_system(context['system'])
    finally:
        sys.path.pop(0)

    # Mock routing engine for single-ship optimization (skip VRP for simplicity)
    mock_routing_engine = Mock()

    def mock_optimize_fleet_tour(graph, markets, ship_locations, fuel_capacity, engine_speed):
        """Mock VRP partitioning - distribute markets evenly across ships"""
        ships = list(ship_locations.keys())
        assignments = {ship: [] for ship in ships}

        # Round-robin distribution
        for i, market in enumerate(markets):
            ship_idx = i % len(ships)
            assignments[ships[ship_idx]].append(market)

        return assignments

    mock_routing_engine.optimize_fleet_tour.side_effect = mock_optimize_fleet_tour

    # Mock daemon client to track container creation and simulate existing containers
    mock_daemon = Mock()

    # Setup list_containers to return existing containers
    def mock_list_containers(player_id=None):
        return {
            'containers': [
                {
                    'container_id': c['container_id'],
                    'status': c['status'],
                    'player_id': c['player_id']
                }
                for c in context['existing_containers']
            ]
        }

    mock_daemon.list_containers.side_effect = mock_list_containers

    # Track newly created containers
    def mock_create_container(config):
        container_id = config['container_id']
        ship_symbol = config['config']['params']['ship_symbol']

        # Add to created containers list
        context['created_containers'].append({
            'container_id': container_id,
            'ship': ship_symbol,
            'markets': config['config']['params']['markets']
        })

        # Add to existing containers so subsequent calls see it
        context['existing_containers'].append({
            'container_id': container_id,
            'ship_symbol': ship_symbol,
            'status': 'STARTING',
            'player_id': context['player_id']
        })

        return {'container_id': container_id}

    mock_daemon.create_container.side_effect = mock_create_container

    # Reset container and register handler with mocked dependencies
    from pymediatr import Mediator
    from application.scouting.commands.scout_markets import ScoutMarketsCommand, ScoutMarketsHandler
    from application.common.behaviors import LoggingBehavior, ValidationBehavior

    test_mediator = Mediator()
    test_mediator.register_behavior(LoggingBehavior())
    test_mediator.register_behavior(ValidationBehavior())
    test_mediator.register_handler(
        ScoutMarketsCommand,
        lambda: ScoutMarketsHandler(mock_ship_repo)
    )

    # Patch at container level
    with patch('configuration.container.get_graph_provider_for_player') as mock_graph_fn, \
         patch('configuration.container.get_daemon_client') as mock_daemon_fn, \
         patch('configuration.container.get_routing_engine') as mock_routing_fn:

        mock_graph_fn.return_value = mock_graph
        mock_daemon_fn.return_value = mock_daemon
        mock_routing_fn.return_value = mock_routing_engine

        # Create and execute command
        command = ScoutMarketsCommand(
            ship_symbols=context['scout_ships'],
            player_id=context['player_id'],
            system=context['system'],
            markets=context['markets'],
            iterations=1,
            return_to_start=False
        )

        try:
            result = asyncio.run(test_mediator.send_async(command))
            context['scout_result'] = result
            context['scout_succeeded'] = True
        except Exception as e:
            context['scout_error'] = str(e)
            context['scout_succeeded'] = False


def _execute_scout_markets_concurrent(context):
    """Execute scout markets concurrently multiple times"""
    # Mock ship repository to return ships
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    mock_ship_repo = Mock()

    def mock_find_by_symbol(ship_symbol, player_id):
        ship_data = next((s for s in context['ships_data'] if s['symbol'] == ship_symbol), None)
        if not ship_data:
            return None

        # Convert API data to Ship entity
        waypoint_symbol = ship_data['nav']['waypointSymbol']
        system_symbol = ship_data['nav']['systemSymbol']

        return Ship(
            ship_symbol=ship_symbol,
            player_id=player_id,
            current_location=Waypoint(
                symbol=waypoint_symbol,
                waypoint_type='PLANET',
                x=0,
                y=0,
                system_symbol=system_symbol,
                traits=tuple(),
                has_fuel=True
            ),
            fuel=Fuel(
                current=ship_data['fuel']['current'],
                capacity=ship_data['fuel']['capacity']
            ),
            fuel_capacity=ship_data['fuel']['capacity'],
            cargo_capacity=ship_data['cargo']['capacity'],
            cargo_units=ship_data['cargo']['units'],
            engine_speed=ship_data['engine']['speed'],
            nav_status=ship_data['nav']['status']
        )

    mock_ship_repo.find_by_symbol.side_effect = mock_find_by_symbol

    # Mock graph provider
    mock_graph = Mock()
    import sys
    import os
    tests_dir = os.path.join(os.path.dirname(__file__), '../../../../')
    sys.path.insert(0, tests_dir)
    try:
        from fixtures.graph_fixtures import get_mock_graph_for_system
        mock_graph.get_graph.return_value = get_mock_graph_for_system(context['system'])
    finally:
        sys.path.pop(0)

    # Mock routing engine
    mock_routing_engine = Mock()

    def mock_optimize_fleet_tour(graph, markets, ship_locations, fuel_capacity, engine_speed):
        """Mock VRP partitioning"""
        ships = list(ship_locations.keys())
        assignments = {ship: [] for ship in ships}
        for i, market in enumerate(markets):
            ship_idx = i % len(ships)
            assignments[ships[ship_idx]].append(market)
        return assignments

    mock_routing_engine.optimize_fleet_tour.side_effect = mock_optimize_fleet_tour

    # Mock daemon client with thread-safe container tracking
    import threading
    container_lock = threading.Lock()
    all_containers = []

    mock_daemon = Mock()

    def mock_list_containers(player_id=None):
        with container_lock:
            return {
                'containers': [
                    {
                        'container_id': c['container_id'],
                        'status': c['status'],
                        'player_id': context['player_id']
                    }
                    for c in all_containers
                ]
            }

    mock_daemon.list_containers.side_effect = mock_list_containers

    def mock_create_container(config):
        container_id = config['container_id']
        ship_symbol = config['config']['params']['ship_symbol']

        with container_lock:
            # Add to all_containers
            all_containers.append({
                'container_id': container_id,
                'ship_symbol': ship_symbol,
                'status': 'STARTING',
                'player_id': context['player_id']
            })

        return {'container_id': container_id}

    mock_daemon.create_container.side_effect = mock_create_container

    # Reset container and register handler with mocked dependencies
    from pymediatr import Mediator
    from application.scouting.commands.scout_markets import ScoutMarketsCommand, ScoutMarketsHandler
    from application.common.behaviors import LoggingBehavior, ValidationBehavior

    test_mediator = Mediator()
    test_mediator.register_behavior(LoggingBehavior())
    test_mediator.register_behavior(ValidationBehavior())
    test_mediator.register_handler(
        ScoutMarketsCommand,
        lambda: ScoutMarketsHandler(mock_ship_repo)
    )

    # Execute multiple times concurrently
    async def execute_once():
        command = ScoutMarketsCommand(
            ship_symbols=context['scout_ships'],
            player_id=context['player_id'],
            system=context['system'],
            markets=context['markets'],
            iterations=1,
            return_to_start=False
        )

        try:
            result = await test_mediator.send_async(command)
            return {'succeeded': True, 'result': result}
        except Exception as e:
            return {'succeeded': False, 'error': str(e)}

    async def execute_concurrent():
        tasks = [execute_once() for _ in range(context['concurrent_count'])]
        return await asyncio.gather(*tasks)

    # Patch and execute
    with patch('configuration.container.get_graph_provider_for_player') as mock_graph_fn, \
         patch('configuration.container.get_daemon_client') as mock_daemon_fn, \
         patch('configuration.container.get_routing_engine') as mock_routing_fn:

        mock_graph_fn.return_value = mock_graph
        mock_daemon_fn.return_value = mock_daemon
        mock_routing_fn.return_value = mock_routing_engine

        results = asyncio.run(execute_concurrent())
        context['concurrent_results'] = results
        context['all_created_containers'] = all_containers


# Hook up concurrent execution
@when('markets:')
def set_markets_and_execute_variant(context, datatable):
    """Store markets and execute (handles both single and concurrent)"""
    context['markets'] = [row[0] for row in datatable]

    if 'concurrent_count' in context:
        _execute_scout_markets_concurrent(context)
    else:
        _execute_scout_markets(context)

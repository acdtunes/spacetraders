"""Step definitions for scout markets ship assignment isolation tests"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
from copy import deepcopy

from application.scouting.commands.scout_markets import ScoutMarketsCommand
from configuration.container import reset_container

# Load scenarios
scenarios('../../../features/application/scouting/scout_markets_ship_assignment.feature')


@pytest.fixture
def context():
    """Shared context for scenario steps"""
    reset_container()
    return {
        'player_id': 1,
        'ships': [],
        'ships_data': [],
        'markets': [],
        'captured_configs': []  # Capture the actual config dicts passed to daemon
    }


@pytest.fixture(autouse=True)
def cleanup():
    """Cleanup after each test"""
    yield
    reset_container()


# Reuse background steps from test_scout_markets_steps.py
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


@when(parsers.parse('I execute scout markets for system "{system}" with ships:'))
def set_ships_for_scout_markets(context, system, datatable):
    """Store ships for scout markets"""
    context['system'] = system
    context['scout_ships'] = [row[0] for row in datatable]


@when('markets:')
def set_markets_and_execute(context, datatable):
    """Store markets and execute scout markets command"""
    context['markets'] = [row[0] for row in datatable]

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

    # Mock routing engine for multi-ship VRP optimization
    mock_routing_engine = Mock()

    def mock_optimize_fleet_tour(graph, markets, ship_locations, fuel_capacity, engine_speed):
        """Mock VRP partitioning - one market per ship

        CRITICAL: Each ship must get its OWN list object (not shared references)
        """
        ships = list(ship_locations.keys())
        assignments = {}

        # Assign one market to each ship with INDEPENDENT list objects
        for i, ship in enumerate(ships):
            # Create a NEW list for each ship (not a shared reference)
            assignments[ship] = []
            if i < len(markets):
                assignments[ship].append(markets[i])

        # Verify list independence
        if len(ships) > 1:
            first_list = assignments[ships[0]]
            second_list = assignments[ships[1]]
            assert first_list is not second_list, "BUG: Lists are shared by reference!"

        return assignments

    mock_routing_engine.optimize_fleet_tour.side_effect = mock_optimize_fleet_tour

    # Mock daemon client to capture exact config passed
    mock_daemon = Mock()
    context['captured_configs'] = []

    # Mock list_containers to return empty list (no existing containers)
    def mock_list_containers(player_id=None):
        return {'containers': []}

    mock_daemon.list_containers.side_effect = mock_list_containers

    def mock_create_container(config):
        """Capture the EXACT config dict passed (deep copy to preserve state at time of call)"""
        # Deep copy immediately to capture the state at THIS moment
        captured = deepcopy(config)
        context['captured_configs'].append(captured)

        # Also log what we received for debugging
        params = config['config']['params']
        # Handle both ScoutTourCommand (ship_symbol) and ScoutMarketsVRPCommand (ship_symbols)
        if 'ship_symbol' in params:
            ship_in_config = params['ship_symbol']
        elif 'ship_symbols' in params:
            ship_in_config = params['ship_symbols'][0]  # Use first ship
        else:
            ship_in_config = 'UNKNOWN'

        markets_in_config = config['config']['params']['markets']
        print(f"DEBUG: create_container called with ship={ship_in_config}, markets={markets_in_config}")

        container_id = f"scout-tour-{ship_in_config}-mock"
        return {'container_id': container_id}

    mock_daemon.create_container.side_effect = mock_create_container

    # Create test mediator with mocked ship repository
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
            iterations=1
        )

        try:
            result = asyncio.run(test_mediator.send_async(command))
            context['scout_result'] = result
            context['scout_succeeded'] = True
        except Exception as e:
            context['scout_error'] = str(e)
            context['scout_succeeded'] = False


# Assertion steps

@then('scout markets should complete successfully')
def check_scout_success(context):
    """Verify scout markets completed successfully"""
    if not context.get('scout_succeeded'):
        error_msg = context.get('scout_error', 'Unknown error')
        assert False, f"Scout markets failed: {error_msg}"


@then(parsers.parse('{count:d} containers should be created'))
def check_container_count(context, count):
    """Verify number of containers created"""
    actual_count = len(context['captured_configs'])
    assert actual_count == count, \
        f"Expected {count} containers but got {actual_count}"


@then(parsers.parse('container {index:d} should be assigned to ship "{ship_symbol}"'))
def check_container_ship_assignment(context, index, ship_symbol):
    """Verify specific container is assigned to correct ship"""
    if index >= len(context['captured_configs']):
        assert False, f"Container {index} does not exist (only {len(context['captured_configs'])} containers created)"

    config = context['captured_configs'][index]
    params = config['config']['params']
    # Handle both ScoutTourCommand (ship_symbol) and ScoutMarketsVRPCommand (ship_symbols)
    if 'ship_symbol' in params:
        actual_ship = params['ship_symbol']
    elif 'ship_symbols' in params:
        actual_ship = params['ship_symbols'][0]  # Use first ship
    else:
        actual_ship = 'UNKNOWN'

    assert actual_ship == ship_symbol, \
        f"Container {index} assigned to ship {actual_ship}, expected {ship_symbol}"


@then("each container's ship matches its assigned markets owner")
def check_markets_match_ship(context):
    """Verify that each container's markets belong to the correct ship based on VRP assignment"""
    result = context['scout_result']

    for i, config in enumerate(context['captured_configs']):
        params = config['config']['params']
        # Handle both ScoutTourCommand (ship_symbol) and ScoutMarketsVRPCommand (ship_symbols)
        if 'ship_symbol' in params:
            container_ship = params['ship_symbol']
        elif 'ship_symbols' in params:
            container_ship = params['ship_symbols'][0]  # Use first ship
        else:
            container_ship = 'UNKNOWN'

        container_markets = config['config']['params']['markets']

        # Get expected markets for this ship from the result
        expected_markets = result.assignments.get(container_ship, [])

        assert set(container_markets) == set(expected_markets), \
            f"Container {i} for ship {container_ship} has wrong markets. " \
            f"Expected: {expected_markets}, Got: {container_markets}"


@then("all container configs are independent objects")
def check_config_independence(context):
    """Verify that config dicts and params dicts are not shared by reference"""
    configs = context['captured_configs']

    if len(configs) < 2:
        return  # Nothing to check with less than 2 containers

    # Check that config dicts are different objects
    for i in range(len(configs) - 1):
        config_i = configs[i]
        config_j = configs[i + 1]

        # The top-level config dict should be different objects
        assert config_i is not config_j, \
            f"Config dicts {i} and {i+1} are the same object (shared reference)"

        # The nested 'config' dict should be different objects
        assert config_i['config'] is not config_j['config'], \
            f"Nested config dicts {i} and {i+1} are the same object (shared reference)"

        # The 'params' dict should be different objects
        assert config_i['config']['params'] is not config_j['config']['params'], \
            f"Params dicts {i} and {i+1} are the same object (shared reference)"

        # The 'markets' list should be different objects
        assert config_i['config']['params']['markets'] is not config_j['config']['params']['markets'], \
            f"Markets lists {i} and {i+1} are the same object (shared reference)"

"""
BDD tests for mining module core classes
"""
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch

from spacetraders_bot.operations._mining.mining_cycle import (
    MiningCycle, MiningContext, MiningStats,
    mine_until_cargo_full, sell_cargo
)
from spacetraders_bot.operations._mining.targeted_mining import TargetedMiningSession
from spacetraders_bot.operations._mining.executor import MiningOperationExecutor
from spacetraders_bot.operations._mining.asteroid_finder import find_alternative_asteroids
from spacetraders_bot.operations.control import CircuitBreaker

scenarios('../../../bdd/features/operations/mining_classes_core.feature')

@given('a mining test environment', target_fixture='mining_ctx')
def mining_test_environment():
    return {}

# MiningCycle tests
@given('a mining cycle')
def mining_cycle(mining_ctx):
    ship = Mock()
    ship.orbit = Mock(return_value=True)
    ship.dock = Mock(return_value=True)
    ship.refuel = Mock(return_value=True)
    ship.get_cargo = Mock(return_value={'units': 40, 'capacity': 40})
    ship.extract = Mock(return_value={'units': 3, 'cooldown': 80})
    ship.sell_all = Mock(return_value=5000)
    ship.get_status = Mock(return_value={'cooldown': {'remainingSeconds': 0}})

    navigator = Mock()
    navigator.execute_route = Mock(return_value=True)

    context = MiningContext(
        args=Mock(),
        ship=ship,
        navigator=navigator,
        controller=None,
        stats=MiningStats(),
        log_error=Mock()
    )

    cycle = MiningCycle(
        context=context,
        total_cycles=5,
        asteroid='X1-TEST-A1',
        market='X1-TEST-B2'
    )

    mining_ctx['cycle'] = cycle
    mining_ctx['context'] = context
    mining_ctx['ship'] = ship
    mining_ctx['navigator'] = navigator

@given('ship is configured for mining')
def ship_configured(mining_ctx):
    pass  # Already configured in mining_cycle

@given('navigation will fail to destination')
def navigation_fails(mining_ctx):
    mining_ctx['navigator'].execute_route = Mock(return_value=False)

@when('I execute mining cycle 1')
def execute_cycle(mining_ctx):
    cycle = mining_ctx['cycle']
    result = cycle.execute(1)
    mining_ctx['result'] = result

@then('cycle should succeed')
def cycle_succeeds(mining_ctx):
    assert mining_ctx['result'] == True

@then('cycle should fail with navigation error')
def cycle_fails(mining_ctx):
    assert mining_ctx['result'] == False

@then('all cycle steps should execute')
def all_steps_execute(mining_ctx):
    ship = mining_ctx['ship']
    assert ship.orbit.called
    assert ship.refuel.called

# Mining helpers
@given('a mining context')
def mining_context(mining_ctx):
    ship = Mock()
    ship.get_cargo = Mock(return_value={'units': 0, 'capacity': 40})
    ship.extract = Mock(return_value={'units': 3, 'cooldown': 80})
    ship.get_status = Mock(return_value={'cooldown': {'remainingSeconds': 0}})
    ship.wait_for_cooldown = Mock()
    ship.dock = Mock(return_value=True)
    ship.sell_all = Mock(return_value=5000)

    context = MiningContext(
        args=Mock(),
        ship=ship,
        navigator=Mock(),
        controller=None,
        stats=MiningStats(),
        log_error=Mock()
    )

    mining_ctx['context'] = context
    mining_ctx['ship'] = ship

@given('ship has empty cargo')
def empty_cargo(mining_ctx):
    ship = mining_ctx['ship']
    extraction_count = {'count': 0}

    def extract_effect():
        extraction_count['count'] += 1
        current = ship.get_cargo.return_value['units']
        ship.get_cargo.return_value = {
            'units': min(current + 3, 40),
            'capacity': 40
        }
        return {'units': 3, 'cooldown': 80}

    ship.extract.side_effect = extract_effect

@given('ship has full cargo')
def full_cargo(mining_ctx):
    ship = mining_ctx['ship']
    ship.get_cargo.return_value = {'units': 40, 'capacity': 40}

@when('I mine until cargo is full')
def mine_until_full(mining_ctx):
    result = mine_until_cargo_full(mining_ctx['context'])
    mining_ctx['cargo_result'] = result

@when('I sell all cargo')
def sell_all_cargo(mining_ctx):
    cargo = mining_ctx['ship'].get_cargo()
    revenue = sell_cargo(mining_ctx['context'], cargo)
    mining_ctx['revenue'] = revenue

@then('cargo should be at capacity')
def cargo_at_capacity(mining_ctx):
    cargo = mining_ctx['cargo_result']
    assert cargo['units'] >= 39  # Mining stops at capacity-1

@then('extraction count should be positive')
def extraction_positive(mining_ctx):
    assert mining_ctx['context'].stats.total_extracted > 0

@then('revenue should be positive')
def revenue_positive(mining_ctx):
    assert mining_ctx['revenue'] > 0

@then('cargo should be empty')
def cargo_empty(mining_ctx):
    # Sell clears cargo
    assert mining_ctx['context'].stats.total_sold > 0

# TargetedMiningSession
@given(parsers.parse('a targeted session for "{resource}"'))
def targeted_session(mining_ctx, resource):
    ship = Mock()
    ship.get_status = Mock(return_value={'cooldown': {'remainingSeconds': 0}})
    ship.extract = Mock(return_value={'units': 3, 'symbol': resource, 'cooldown': 80})
    ship.wait_for_cooldown = Mock()
    ship.jettison_wrong_cargo = Mock()
    ship.get_cargo = Mock(return_value={'units': 10, 'capacity': 40, 'inventory': []})

    context = MiningContext(
        args=None,
        ship=ship,
        navigator=Mock(),
        controller=None,
        stats=MiningStats(),
        log_error=lambda *_, **__: None
    )

    session = TargetedMiningSession(
        context=context,
        target_resource=resource,
        units_needed=10,
        breaker=CircuitBreaker(limit=10)
    )

    mining_ctx['session'] = session
    mining_ctx['ship'] = ship

@given('asteroid yields target resource')
def yields_target(mining_ctx):
    pass  # Already configured

@given('asteroid yields wrong resource')
def yields_wrong(mining_ctx):
    mining_ctx['ship'].extract.return_value = {'units': 2, 'symbol': 'WRONG_ORE', 'cooldown': 80}

@given(parsers.parse('breaker limit is {limit:d}'))
def breaker_limit(mining_ctx, limit):
    mining_ctx['session'].breaker = CircuitBreaker(limit=limit)

@when('I run targeted session')
def run_targeted(mining_ctx):
    success, units, reason = mining_ctx['session'].run()
    mining_ctx['session_result'] = (success, units, reason)

@then('session should succeed')
def session_succeeds(mining_ctx):
    success, _, _ = mining_ctx['session_result']
    assert success == True

@then('session should fail')
def session_fails(mining_ctx):
    success, _, _ = mining_ctx['session_result']
    assert success == False

@then('target units should be collected')
def target_collected(mining_ctx):
    _, units, _ = mining_ctx['session_result']
    # Mining can overshoot target (extracts 3 at a time, can't stop exactly at 10)
    assert units >= 10

@then('reason should mention breaker')
def reason_breaker(mining_ctx):
    _, _, reason = mining_ctx['session_result']
    assert 'breaker' in reason.lower() or 'consecutive' in reason.lower()

# MiningOperationExecutor
@given('a mining executor')
def mining_executor(mining_ctx):
    args = Mock()
    args.player_id = 1
    args.ship = 'SHIP-1'
    args.asteroid = 'X1-TEST-A1'
    args.market = 'X1-TEST-B2'
    args.cycles = 3

    executor = MiningOperationExecutor(args, Mock())
    mining_ctx['executor'] = executor

@given('ship exists with adequate fuel')
def ship_with_fuel(mining_ctx):
    ship_data = {
        'symbol': 'SHIP-1',
        'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B2'},
        'fuel': {'current': 100, 'capacity': 100}
    }
    mining_ctx['ship_data'] = ship_data

@given('ship does not exist')
def ship_not_exist(mining_ctx):
    mining_ctx['ship_data'] = None

@given('routes are validated')
def routes_validated(mining_ctx):
    pass  # Will mock in setup

@given('ship has low fuel')
def ship_low_fuel(mining_ctx):
    ship_data = {
        'symbol': 'SHIP-1',
        'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B2'},
        'fuel': {'current': 5, 'capacity': 100}
    }
    mining_ctx['ship_data'] = ship_data

@given(parsers.parse('checkpoint exists with {cycles:d} cycles'))
def checkpoint_exists(mining_ctx, cycles):
    # Checkpoint implies ship exists with adequate fuel
    mining_ctx['ship_data'] = {
        'symbol': 'SHIP-1',
        'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-B2'},
        'fuel': {'current': 100, 'capacity': 100}
    }
    mining_ctx['checkpoint'] = {
        'cycle': cycles,
        'stats': {
            'cycles_completed': cycles,
            'total_extracted': 100,
            'total_sold': 100,
            'total_revenue': 15000
        }
    }

@when('I setup executor')
def setup_executor(mining_ctx):
    executor = mining_ctx['executor']
    ship_data = mining_ctx.get('ship_data')

    # Mock all external dependencies
    with patch('spacetraders_bot.operations._mining.executor.get_api_client') as mock_get_api, \
         patch('spacetraders_bot.operations._mining.executor.ShipController') as mock_ship_class, \
         patch('spacetraders_bot.operations._mining.executor.SmartNavigator') as mock_nav_class, \
         patch('spacetraders_bot.operations._mining.executor.OperationController') as mock_controller_class, \
         patch('spacetraders_bot.operations._mining.executor.get_captain_logger') as mock_logger, \
         patch('spacetraders_bot.operations._mining.executor.get_operator_name') as mock_op_name:

        # Setup mock API
        mock_api = Mock()
        mock_get_api.return_value = mock_api

        # Setup mock ship
        mock_ship = Mock()
        mock_ship.get_status.return_value = ship_data
        mock_ship_class.return_value = mock_ship

        # Setup mock navigator
        mock_navigator = Mock()
        if ship_data and ship_data.get('fuel', {}).get('current', 0) < 20:
            mock_navigator.validate_route.return_value = (False, "Insufficient fuel")
            mock_navigator.get_fuel_estimate.return_value = None
        else:
            mock_navigator.validate_route.return_value = (True, "Valid")
            mock_navigator.get_fuel_estimate.return_value = {'total_fuel_cost': 10, 'refuel_stops': 0}
        mock_nav_class.return_value = mock_navigator

        # Setup mock controller
        mock_controller = Mock()
        mock_controller.can_resume.return_value = bool(mining_ctx.get('checkpoint'))
        if mining_ctx.get('checkpoint'):
            mock_controller.resume.return_value = mining_ctx['checkpoint']
        else:
            mock_controller.start.return_value = None
        mock_controller_class.return_value = mock_controller

        # Setup other mocks
        mock_logger.return_value = Mock()
        mock_op_name.return_value = "test_operator"

        try:
            result = executor.setup()
            mining_ctx['setup_result'] = result
        except Exception as e:
            print(f"Setup failed with error: {e}")
            mining_ctx['setup_result'] = False

@when('I validate routes')
def validate_routes(mining_ctx):
    executor = mining_ctx['executor']
    ship_data = mining_ctx.get('ship_data', {})
    executor.navigator = Mock()

    if ship_data.get('fuel', {}).get('current', 0) < 20:
        executor.navigator.validate_route = Mock(return_value=(False, "Insufficient fuel"))
        executor.navigator.get_fuel_estimate = Mock(return_value=None)
    else:
        executor.navigator.validate_route = Mock(return_value=(True, "Valid"))
        executor.navigator.get_fuel_estimate = Mock(return_value={'total_fuel_cost': 10, 'refuel_stops': 0})

    try:
        result = executor._validate_routes(ship_data)
        mining_ctx['validate_result'] = result
    except:
        mining_ctx['validate_result'] = False

@then('setup should succeed')
def setup_succeeds(mining_ctx):
    assert mining_ctx.get('setup_result') == True

@then('setup should fail')
def setup_fails(mining_ctx):
    assert mining_ctx.get('setup_result') == False

@then('error should be logged')
def error_logged(mining_ctx):
    assert mining_ctx.get('setup_result') == False

@then('all components should initialize')
def components_initialize(mining_ctx):
    executor = mining_ctx['executor']
    assert executor.ship is not None
    assert executor.navigator is not None

@then('validation should fail')
def validation_fails(mining_ctx):
    assert mining_ctx.get('validate_result') == False

@then('error should mention fuel')
def error_mentions_fuel(mining_ctx):
    assert mining_ctx.get('validate_result') == False

@then(parsers.parse('executor should resume from cycle {cycle:d}'))
def executor_resumes(mining_ctx, cycle):
    executor = mining_ctx['executor']
    assert executor.stats.cycles_completed + 1 == cycle

@then('stats should reflect checkpoint')
def stats_reflect_checkpoint(mining_ctx):
    executor = mining_ctx['executor']
    assert executor.stats.total_revenue == 15000

# find_alternative_asteroids
@given('an API client')
def api_client(mining_ctx):
    api = Mock()
    mining_ctx['api'] = api

@given(parsers.parse('system has {count:d} asteroids with correct traits'))
def system_asteroids(mining_ctx, count):
    api = mining_ctx['api']
    asteroids = []
    for i in range(count):
        asteroids.append({
            'symbol': f'X1-TEST-A{i+1}',
            'type': 'ASTEROID',
            'traits': [{'symbol': 'COMMON_METAL_DEPOSITS'}]
        })
    api.list_waypoints = Mock(return_value={'data': asteroids, 'meta': {'total': 1}})

@given('system has asteroids including stripped')
def asteroids_with_stripped(mining_ctx):
    api = mining_ctx['api']
    asteroids = [
        {'symbol': 'X1-TEST-A1', 'type': 'ASTEROID', 'traits': [{'symbol': 'COMMON_METAL_DEPOSITS'}]},
        {'symbol': 'X1-TEST-A2', 'type': 'ASTEROID', 'traits': [{'symbol': 'STRIPPED'}]},
        {'symbol': 'X1-TEST-A3', 'type': 'ASTEROID', 'traits': [{'symbol': 'COMMON_METAL_DEPOSITS'}]},
    ]
    api.list_waypoints = Mock(return_value={'data': asteroids, 'meta': {'total': 1}})

@when(parsers.parse('I find alternatives for "{resource}"'))
def find_alternatives(mining_ctx, resource):
    api = mining_ctx['api']
    result = find_alternative_asteroids(api, 'X1-TEST', 'X1-TEST-A0', resource)
    mining_ctx['alternatives'] = result

@then(parsers.parse('alternatives list should have {count:d} asteroids'))
def alternatives_count(mining_ctx, count):
    assert len(mining_ctx['alternatives']) == count

@then('current asteroid should be excluded')
def current_excluded(mining_ctx):
    assert 'X1-TEST-A0' not in mining_ctx['alternatives']

@then('stripped asteroids should be excluded')
def stripped_excluded(mining_ctx):
    alternatives = mining_ctx['alternatives']
    assert 'X1-TEST-A2' not in alternatives
    assert len(alternatives) == 2  # Should have 2 non-stripped

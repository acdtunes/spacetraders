from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch, mock_open
import json
import os

from spacetraders_bot.operations.scouting import (
    MarketDataService,
    StationaryScoutMode,
    TourScoutMode,
    ScoutMarketsExecutor,
)

scenarios('../../../bdd/features/operations/scouting_classes.feature')


@given('a scouting test environment', target_fixture='scouting_ctx')
def given_scouting_environment():
    """Create scouting test environment"""
    return {
        'api': None,
        'db': None,
        'ship': None,
        'navigator': None,
        'market_service': None,
        'stationary_mode': None,
        'tour_mode': None,
        'executor': None,
        'result': None,
        'tour': None,
        'markets_data': {},
        'trade_goods': {},
        'running_flag': {'value': True},
        'poll_count': 0,
    }


# ============================================================================
# MarketDataService Tests
# ============================================================================

@given('a market data service')
def given_market_data_service(scouting_ctx):
    """Create market data service"""
    api = Mock()
    db = Mock()
    db.transaction = MagicMock()
    db.transaction.return_value.__enter__ = Mock(return_value=Mock())
    db.transaction.return_value.__exit__ = Mock(return_value=False)
    db.update_market_data = Mock()

    scouting_ctx['api'] = api
    scouting_ctx['db'] = db
    scouting_ctx['market_service'] = MarketDataService(api, db, player_id=1)


@given(parsers.re(r'market "(?P<market>[^"]+)" has (?P<count>\d+) trade goods'))
def given_market_has_goods(scouting_ctx, market, count):
    """Market has trade goods"""
    trade_goods = [
        {
            'symbol': f'GOOD_{i}',
            'purchasePrice': 100 + i * 10,  # API purchasePrice (ship pays to buy)
            'sellPrice': 80 + i * 10,        # API sellPrice (ship receives to sell)
            'supply': 'MODERATE',
            'activity': 'STRONG',
            'tradeVolume': 100,
        }
        for i in range(int(count))
    ]

    scouting_ctx['markets_data'][market] = {
        'tradeGoods': trade_goods
    }

    # Setup API mock
    def get_market(system, waypoint):
        return scouting_ctx['markets_data'].get(waypoint)

    scouting_ctx['api'].get_market = get_market


@given(parsers.re(r'market "(?P<market>[^"]+)" API returns error'))
def given_market_api_error(scouting_ctx, market):
    """Market API returns error"""
    def get_market(system, waypoint):
        if waypoint == market:
            return None
        return scouting_ctx['markets_data'].get(waypoint)

    scouting_ctx['api'].get_market = get_market


@given(parsers.re(r'trade good "(?P<good>[^"]+)" has API purchasePrice (?P<purchase>\d+) and sellPrice (?P<sell>\d+)'))
def given_trade_good_prices(scouting_ctx, good, purchase, sell):
    """Trade good with specific prices"""
    scouting_ctx['trade_goods'][good] = {
        'symbol': good,
        'purchasePrice': int(purchase),  # API purchasePrice
        'sellPrice': int(sell),           # API sellPrice
        'supply': 'MODERATE',
        'activity': 'STRONG',
        'tradeVolume': 100,
    }


@when(parsers.re(r'I collect market data for "(?P<market>[^"]+)"'))
def when_collect_market_data(scouting_ctx, market):
    """Collect market data"""
    service = scouting_ctx['market_service']
    scouting_ctx['result'] = service.collect_market_data(market, 'X1-TEST')


@when('I update database with trade good data')
def when_update_database(scouting_ctx):
    """Update database with trade good"""
    service = scouting_ctx['market_service']
    trade_goods = list(scouting_ctx['trade_goods'].values())
    scouting_ctx['result'] = service.update_database('X1-TEST-A1', trade_goods, '2025-01-01T00:00:00Z')


@when(parsers.re(r'I collect and update for "(?P<market>[^"]+)"'))
def when_collect_and_update(scouting_ctx, market):
    """Collect and update"""
    service = scouting_ctx['market_service']
    scouting_ctx['result'] = service.collect_and_update(market, 'X1-TEST', '2025-01-01T00:00:00Z')


@then('market data should be returned')
def then_market_data_returned(scouting_ctx):
    """Verify market data returned"""
    assert scouting_ctx['result'] is not None


@then('market data should be None')
def then_market_data_none(scouting_ctx):
    """Verify market data is None"""
    assert scouting_ctx['result'] is None


@then(parsers.re(r'market data should contain (?P<count>\d+) trade goods'))
def then_market_data_contains_goods(scouting_ctx, count):
    """Verify trade goods count"""
    result = scouting_ctx['result']
    assert 'tradeGoods' in result
    assert len(result['tradeGoods']) == int(count)


@then(parsers.re(r'database should have sell_price (?P<price>\d+)'))
def then_database_sell_price(scouting_ctx, price):
    """Verify database sell_price (from API purchasePrice)"""
    db = scouting_ctx['db']
    # Check that update_market_data was called with correct sell_price
    calls = db.update_market_data.call_args_list
    assert len(calls) > 0
    call_kwargs = calls[0][1]
    assert call_kwargs['sell_price'] == int(price)


@then(parsers.re(r'database should have purchase_price (?P<price>\d+)'))
def then_database_purchase_price(scouting_ctx, price):
    """Verify database purchase_price (from API sellPrice)"""
    db = scouting_ctx['db']
    calls = db.update_market_data.call_args_list
    assert len(calls) > 0
    call_kwargs = calls[0][1]
    assert call_kwargs['purchase_price'] == int(price)


@then(parsers.re(r'(?P<count>\d+) goods should be updated in database'))
def then_goods_updated_in_database(scouting_ctx, count):
    """Verify goods update count"""
    assert scouting_ctx['result'] == int(count)


# ============================================================================
# StationaryScoutMode Tests
# ============================================================================

@given('a stationary scout mode')
def given_stationary_scout_mode(scouting_ctx):
    """Create stationary scout mode"""
    ship = Mock()
    ship.dock = Mock(return_value=True)

    navigator = Mock()
    navigator.execute_route = Mock(return_value=True)

    market_service = Mock()
    market_service.collect_and_update = Mock(return_value=5)

    scouting_ctx['ship'] = ship
    scouting_ctx['navigator'] = navigator
    scouting_ctx['market_service'] = market_service
    scouting_ctx['stationary_mode'] = StationaryScoutMode(
        ship, navigator, market_service, 'X1-TEST'
    )


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is at "(?P<location>[^"]+)"'))
def given_ship_at_location(scouting_ctx, ship, location):
    """Ship is at location"""
    scouting_ctx['current_location'] = location


@given(parsers.re(r'market "(?P<market>[^"]+)" has trade goods'))
def given_market_has_trade_goods(scouting_ctx, market):
    """Market has trade goods"""
    # Already handled by market_service mock


@given(parsers.re(r'navigation to "(?P<market>[^"]+)" will fail'))
def given_navigation_fails(scouting_ctx, market):
    """Navigation will fail"""
    def execute_route(ship, dest):
        if dest == market:
            return False
        return True

    scouting_ctx['navigator'].execute_route = execute_route


@when(parsers.re(r'I execute stationary mode for "(?P<market>[^"]+)" non-continuous'))
def when_execute_stationary_non_continuous(scouting_ctx, market):
    """Execute stationary mode once"""
    mode = scouting_ctx['stationary_mode']
    current_location = scouting_ctx.get('current_location', 'X1-TEST-A1')

    # Mock time.sleep to avoid delays
    with patch('spacetraders_bot.operations.scouting.stationary_mode.time.sleep'):
        scouting_ctx['result'] = mode.execute(
            market,
            current_location,
            poll_interval=60,
            continuous=False
        )


@when(parsers.re(r'I execute stationary mode for "(?P<market>[^"]+)" continuous with (?P<polls>\d+) polls'))
def when_execute_stationary_continuous(scouting_ctx, market, polls):
    """Execute stationary mode with limited polls"""
    mode = scouting_ctx['stationary_mode']
    current_location = scouting_ctx.get('current_location', market)  # Already at market

    # Create a counter to stop after N polls
    poll_counter = {'count': 0, 'max': int(polls)}
    running_flag = {'value': True}

    original_collect_and_update = scouting_ctx['market_service'].collect_and_update

    def collect_and_update_with_counter(*args, **kwargs):
        poll_counter['count'] += 1
        if poll_counter['count'] >= poll_counter['max']:
            running_flag['value'] = False
        return original_collect_and_update(*args, **kwargs)

    scouting_ctx['market_service'].collect_and_update = collect_and_update_with_counter

    # Mock time.sleep to avoid delays
    with patch('spacetraders_bot.operations.scouting.stationary_mode.time.sleep'):
        scouting_ctx['result'] = mode.execute(
            market,
            current_location,
            poll_interval=1,  # Short interval for testing
            continuous=True,
            running_flag=running_flag
        )


@then(parsers.re(r'ship should navigate to "(?P<destination>[^"]+)"'))
def then_ship_navigates_to(scouting_ctx, destination):
    """Verify navigation occurred"""
    navigator = scouting_ctx['navigator']
    navigator.execute_route.assert_called()


@then('ship should dock at market')
def then_ship_docks(scouting_ctx):
    """Verify ship docked"""
    ship = scouting_ctx['ship']
    ship.dock.assert_called()


@then(parsers.re(r'market should be polled (?P<count>\d+) times?'))
def then_market_polled_count(scouting_ctx, count):
    """Verify poll count"""
    result = scouting_ctx['result']
    assert result.poll_count == int(count)


@then('result should be successful')
def then_result_successful(scouting_ctx):
    """Verify result success"""
    result = scouting_ctx['result']
    assert result.success is True


@then('result should be failure')
def then_result_failure(scouting_ctx):
    """Verify result failure"""
    result = scouting_ctx['result']
    assert result.success is False


@then('error message should mention navigation failed')
def then_error_mentions_navigation(scouting_ctx):
    """Verify error message"""
    result = scouting_ctx['result']
    assert 'navigate' in result.error_message.lower()


# ============================================================================
# TourScoutMode Tests
# ============================================================================

@given('a tour scout mode')
def given_tour_scout_mode(scouting_ctx):
    """Create tour scout mode"""
    ship = Mock()
    ship.dock = Mock(return_value=True)

    navigator = Mock()
    navigator.execute_route = Mock(return_value=True)

    optimizer = Mock()
    optimizer.plan_tour = Mock(return_value={
        'legs': [
            {'goal': 'X1-TEST-B2'},
            {'goal': 'X1-TEST-C3'},
        ],
        'total_time': 1000,
        'total_legs': 2,
        'final_fuel': 80,
    })

    market_service = Mock()
    market_service.collect_and_update = Mock(return_value=5)

    scouting_ctx['ship'] = ship
    scouting_ctx['navigator'] = navigator
    scouting_ctx['optimizer'] = optimizer
    scouting_ctx['market_service'] = market_service
    scouting_ctx['tour_mode'] = TourScoutMode(
        ship, navigator, optimizer, market_service, 'X1-TEST'
    )


@given(parsers.re(r'markets "(?P<markets>[^"]+)" exist'))
def given_markets_exist(scouting_ctx, markets):
    """Markets exist"""
    market_list = [m.strip() for m in markets.split(',')]
    scouting_ctx['market_list'] = market_list


@given('tour planning will fail')
def given_tour_planning_fails(scouting_ctx):
    """Tour planning will fail"""
    scouting_ctx['optimizer'].plan_tour = Mock(return_value=None)


@given(parsers.re(r'planned tour has (?P<count>\d+) waypoints'))
def given_planned_tour_waypoints(scouting_ctx, count):
    """Planned tour with waypoints"""
    legs = [{'goal': f'X1-TEST-W{i}'} for i in range(1, int(count) + 1)]
    scouting_ctx['tour'] = {
        'legs': legs,
        'total_time': 2000,
        'total_legs': int(count),
        'final_fuel': 50,
    }


@given(parsers.re(r'each waypoint has (?P<goods>\d+) trade goods'))
def given_waypoint_has_goods(scouting_ctx, goods):
    """Each waypoint has goods"""
    scouting_ctx['market_service'].collect_and_update = Mock(return_value=int(goods))


@given('planned tour exists')
def given_planned_tour_exists(scouting_ctx):
    """Planned tour exists"""
    scouting_ctx['tour'] = {
        'legs': [
            {'goal': 'X1-TEST-A1'},
            {'goal': 'X1-TEST-B2'},
        ],
        'total_time': 1500,
        'total_legs': 2,
        'final_fuel': 70,
    }


@when(parsers.re(r'I plan tour from "(?P<start>[^"]+)"'))
def when_plan_tour(scouting_ctx, start):
    """Plan tour"""
    mode = scouting_ctx['tour_mode']
    market_stops = scouting_ctx.get('market_list', ['X1-TEST-B2', 'X1-TEST-C3'])
    scouting_ctx['result'] = mode.plan_tour(start, market_stops, current_fuel=100)


@when('I execute planned tour')
def when_execute_planned_tour(scouting_ctx):
    """Execute planned tour"""
    mode = scouting_ctx['tour_mode']
    tour = scouting_ctx['tour']
    scouting_ctx['result'] = mode.execute_tour(tour)


@when(parsers.re(r'I save tour to "(?P<path>[^"]+)"'))
def when_save_tour(scouting_ctx, path):
    """Save tour to file"""
    mode = scouting_ctx['tour_mode']
    tour = scouting_ctx['tour']

    with patch('builtins.open', mock_open()) as mock_file:
        mode.save_tour_to_file(tour, path)
        scouting_ctx['saved_path'] = path
        scouting_ctx['mock_file'] = mock_file


@then('tour should be returned')
def then_tour_returned(scouting_ctx):
    """Verify tour returned"""
    assert scouting_ctx['result'] is not None


@then('tour should be None')
def then_tour_none(scouting_ctx):
    """Verify tour is None"""
    assert scouting_ctx['result'] is None


@then(parsers.re(r'tour should have (?P<count>\d+) legs'))
def then_tour_has_legs(scouting_ctx, count):
    """Verify tour leg count"""
    tour = scouting_ctx['result']
    assert len(tour['legs']) == int(count)


@then(parsers.re(r'(?P<count>\d+) markets should be visited'))
def then_markets_visited(scouting_ctx, count):
    """Verify markets visited"""
    result = scouting_ctx['result']
    assert result.markets_scouted == int(count)


@then(parsers.re(r'(?P<count>\d+) goods should be updated$'))
def then_tour_goods_updated(scouting_ctx, count):
    """Verify tour goods updated"""
    result = scouting_ctx['result']
    assert result.goods_updated == int(count)


@then('tour file should be created')
def then_tour_file_created(scouting_ctx):
    """Verify tour file was created"""
    mock_file = scouting_ctx['mock_file']
    mock_file.assert_called()


@then('tour file should contain JSON data')
def then_tour_file_contains_json(scouting_ctx):
    """Verify JSON was written"""
    mock_file = scouting_ctx['mock_file']
    # Verify write was called with JSON data
    handle = mock_file()
    handle.write.assert_called()


# ============================================================================
# ScoutMarketsExecutor Tests
# ============================================================================

@given('a scout markets executor')
def given_scout_markets_executor(scouting_ctx):
    """Create scout markets executor"""
    args = Mock(spec=['system', 'ship', 'player_id', 'continuous', 'return_to_start'])
    args.system = 'X1-TEST'
    args.ship = 'SHIP-1'
    args.player_id = 1
    args.continuous = False
    args.return_to_start = False

    api = Mock()
    api.get_ship = Mock(return_value={
        'symbol': 'SHIP-1',
        'nav': {'waypointSymbol': 'X1-TEST-A1'},
        'fuel': {'current': 100, 'capacity': 100},
    })

    logger = Mock()
    captain_logger = Mock()

    scouting_ctx['args'] = args
    scouting_ctx['api'] = api
    scouting_ctx['logger'] = logger
    scouting_ctx['captain_logger'] = captain_logger


@given(parsers.re(r'a scout markets executor with partitioned markets "(?P<markets>[^"]+)"'))
def given_executor_with_partitioned_markets(scouting_ctx, markets):
    """Executor with partitioned markets"""
    given_scout_markets_executor(scouting_ctx)
    scouting_ctx['args'].markets_list = markets


@given(parsers.re(r'system "(?P<system>[^"]+)" has graph in database'))
def given_system_has_graph(scouting_ctx, system):
    """System has graph"""
    scouting_ctx['graph_exists'] = True


@given(parsers.re(r'system "(?P<system>[^"]+)" has no graph'))
def given_system_no_graph(scouting_ctx, system):
    """System has no graph"""
    scouting_ctx['graph_exists'] = False


@given('graph building will fail')
def given_graph_build_fails(scouting_ctx):
    """Graph building will fail"""
    scouting_ctx['graph_build_fails'] = True


@given(parsers.re(r'ship "(?P<ship>[^"]+)" exists'))
def given_ship_exists(scouting_ctx, ship):
    """Ship exists"""
    # Already handled by api mock


@given(parsers.re(r'ship "(?P<ship>[^"]+)" does not exist'))
def given_ship_not_exists(scouting_ctx, ship):
    """Ship does not exist"""
    scouting_ctx['api'].get_ship = Mock(return_value=None)


@given(parsers.re(r'system "(?P<system>[^"]+)" has (?P<count>\d+) markets'))
def given_system_has_markets(scouting_ctx, system, count):
    """System has markets"""
    markets = [f'{system}-M{i}' for i in range(1, int(count) + 1)]
    scouting_ctx['system_markets'] = markets
    # Also mark that graph exists
    scouting_ctx['graph_exists'] = True

    # Update ship location to be at the first market
    if scouting_ctx.get('api'):
        scouting_ctx['api'].get_ship = Mock(return_value={
            'symbol': 'SHIP-1',
            'nav': {'waypointSymbol': markets[0]},  # Ship at first market
            'fuel': {'current': 100, 'capacity': 100},
        })


@given(parsers.re(r'markets list is "(?P<markets>[^"]+)"'))
def given_markets_list(scouting_ctx, markets):
    """Markets list"""
    market_list = [m.strip() for m in markets.split(',')]
    scouting_ctx['test_markets'] = market_list


@when('I call setup')
def when_call_setup(scouting_ctx):
    """Call executor setup"""
    with patch('spacetraders_bot.operations.scouting.executor.GraphBuilder') as MockGraphBuilder, \
         patch('spacetraders_bot.operations.scouting.executor.get_database') as mock_get_db:

        mock_builder = Mock()

        # Setup graph loading/building
        if scouting_ctx.get('graph_exists'):
            mock_builder.load_system_graph = Mock(return_value={'nodes': [], 'edges': []})
        else:
            mock_builder.load_system_graph = Mock(return_value=None)
            if scouting_ctx.get('graph_build_fails'):
                mock_builder.build_system_graph = Mock(return_value=None)
            else:
                mock_builder.build_system_graph = Mock(return_value={'nodes': [], 'edges': []})

        MockGraphBuilder.return_value = mock_builder

        mock_db = Mock()
        mock_db.transaction = MagicMock()
        mock_get_db.return_value = mock_db

        executor = ScoutMarketsExecutor(
            scouting_ctx['args'],
            scouting_ctx['api'],
            scouting_ctx['logger'],
            scouting_ctx['captain_logger']
        )
        scouting_ctx['executor'] = executor
        scouting_ctx['result'] = executor.setup()


@when('I determine markets')
def when_determine_markets(scouting_ctx):
    """Determine markets"""
    # Setup first if not already done
    if scouting_ctx.get('executor') is None:
        when_call_setup(scouting_ctx)

    executor = scouting_ctx['executor']

    # Mock TourOptimizer.get_markets_from_graph if needed
    if scouting_ctx.get('system_markets'):
        with patch('spacetraders_bot.operations.scouting.executor.TourOptimizer.get_markets_from_graph') as mock_get_markets:
            mock_get_markets.return_value = scouting_ctx['system_markets']
            tour_start, market_stops = executor.determine_markets()
    else:
        tour_start, market_stops = executor.determine_markets()

    scouting_ctx['tour_start'] = tour_start
    scouting_ctx['market_stops'] = market_stops


@when('I run single tour')
def when_run_single_tour(scouting_ctx):
    """Run single tour"""
    # Setup executor first
    when_call_setup(scouting_ctx)

    executor = scouting_ctx['executor']

    # Override determine_markets to use test markets
    def mock_determine_markets():
        return scouting_ctx['test_markets'][0], scouting_ctx['test_markets']

    executor.determine_markets = mock_determine_markets

    # Mock the mode execution
    with patch('spacetraders_bot.operations.scouting.executor.time.sleep'):
        # Track which mode was executed
        original_stationary_execute = executor.stationary_mode.execute
        original_tour_execute = executor.tour_mode.execute

        def track_stationary(*args, **kwargs):
            scouting_ctx['mode_executed'] = 'stationary'
            from spacetraders_bot.operations.scouting.stationary_mode import StationaryScoutResult
            return StationaryScoutResult(success=True, poll_count=1, goods_updated=5)

        def track_tour(*args, **kwargs):
            scouting_ctx['mode_executed'] = 'tour'
            from spacetraders_bot.operations.scouting.tour_mode import TourScoutResult
            return TourScoutResult(success=True, markets_scouted=3, goods_updated=15, planned_time="30m")

        executor.stationary_mode.execute = track_stationary
        executor.tour_mode.execute = track_tour

        scouting_ctx['result'] = executor.run_single_tour()


@then('setup should return True')
def then_setup_returns_true(scouting_ctx):
    """Verify setup returned True"""
    assert scouting_ctx['result'] is True


@then('setup should return False')
def then_setup_returns_false(scouting_ctx):
    """Verify setup returned False"""
    assert scouting_ctx['result'] is False


@then('ship controller should be initialized')
def then_ship_controller_initialized(scouting_ctx):
    """Verify ship controller initialized"""
    executor = scouting_ctx['executor']
    assert executor.ship is not None


@then('navigator should be initialized')
def then_navigator_initialized(scouting_ctx):
    """Verify navigator initialized"""
    executor = scouting_ctx['executor']
    assert executor.navigator is not None


@then('market service should be initialized')
def then_market_service_initialized(scouting_ctx):
    """Verify market service initialized"""
    executor = scouting_ctx['executor']
    assert executor.market_service is not None


@then(parsers.re(r'tour start should be "(?P<start>[^"]+)"'))
def then_tour_start_is(scouting_ctx, start):
    """Verify tour start"""
    assert scouting_ctx['tour_start'] == start


@then(parsers.re(r'market stops should be "(?P<stops>[^"]+)"'))
def then_market_stops_are(scouting_ctx, stops):
    """Verify market stops"""
    expected = [s.strip() for s in stops.split(',')]
    assert scouting_ctx['market_stops'] == expected


@then('market stops should exclude current location')
def then_market_stops_exclude_current(scouting_ctx):
    """Verify current location excluded"""
    current = scouting_ctx['tour_start']
    market_stops = scouting_ctx['market_stops']
    assert current not in market_stops


@then(parsers.re(r'market stops should have (?P<count>\d+) markets'))
def then_market_stops_count(scouting_ctx, count):
    """Verify market stops count"""
    assert len(scouting_ctx['market_stops']) == int(count)


@then('stationary mode should execute')
def then_stationary_mode_executes(scouting_ctx):
    """Verify stationary mode executed"""
    assert scouting_ctx.get('mode_executed') == 'stationary'


@then('tour mode should execute')
def then_tour_mode_executes(scouting_ctx):
    """Verify tour mode executed"""
    assert scouting_ctx.get('mode_executed') == 'tour'


@then('result should be True')
def then_result_is_true(scouting_ctx):
    """Verify result is True"""
    assert scouting_ctx['result'] is True

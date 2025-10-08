import json
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest
from pytest_bdd import scenarios, given, when, then, parsers

import spacetraders_bot.operations.multileg_trader as multileg_module
from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    MarketEvaluation,
    MultiLegRoute,
    MultiLegTradeOptimizer,
    RouteSegment,
    TradeAction,
    TradeEvaluationStrategy,
    ProfitFirstStrategy,
    trade_plan_operation,
)

scenarios('features/multileg_trader_strategy.feature')


@pytest.fixture
def context():
    return {}


@given('a profit-first strategy')
def given_profit_strategy(context):
    context['strategy'] = ProfitFirstStrategy(logger=MagicMock())


@given('an empty opportunity pool')
def given_empty_opportunities(context):
    context['opportunities'] = []


@given('trade opportunities for a reinvestment cycle')
def given_reinvestment_opportunities(context):
    context['opportunities'] = [
        {
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-B',
            'good': 'IRON',
            'buy_price': 15,
            'sell_price': 30,
            'trade_volume': 5,
        },
        {
            'buy_waypoint': 'X1-TEST-B',
            'sell_waypoint': 'X1-TEST-C',
            'good': 'COPPER',
            'buy_price': 10,
            'sell_price': 25,
            'trade_volume': 3,
        },
    ]


@given('trade opportunities with invalid spreads')
def given_invalid_opportunities(context):
    context['opportunities'] = [
        {
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-A',
            'good': 'IRON',
            'buy_price': 20,
            'sell_price': 15,
            'trade_volume': 0,
        },
        {
            'buy_waypoint': 'X1-TEST-A',
            'sell_waypoint': 'X1-TEST-B',
            'good': 'COPPER',
            'buy_price': 0,
            'sell_price': 40,
            'trade_volume': 4,
        },
    ]


@given(parsers.parse('the current cargo is {cargo_json}'))
def given_current_cargo(context, cargo_json):
    context['cargo'] = json.loads(cargo_json)


@when(parsers.parse('the strategy evaluates market "{market}" with {credits:d} credits and {fuel_cost:d} fuel cost'))
def when_strategy_evaluates(context, market, credits, fuel_cost):
    strategy = context['strategy']
    opportunities = context.get('opportunities', [])
    evaluation = strategy.evaluate(
        market=market,
        current_cargo=context.get('cargo', {}),
        current_credits=credits,
        trade_opportunities=opportunities,
        cargo_capacity=10,
        fuel_cost=fuel_cost,
    )
    context['evaluation'] = evaluation


@then('the evaluation cargo should be empty')
def then_evaluation_cargo_empty(context):
    assert context['evaluation'].cargo_after == {}


@then('the evaluation net profit should be -5')
def then_evaluation_profit_negative(context):
    assert context['evaluation'].net_profit == -5


@then('the evaluation credits should be 100')
def then_evaluation_credits_exact(context):
    assert context['evaluation'].credits_after == 100


@then(parsers.parse('the evaluation should include actions {actions}'))
def then_evaluation_actions(context, actions):
    expected = [part.strip() for part in actions.split(',') if part.strip()]
    actual = [action.action for action in context['evaluation'].actions]
    assert actual == expected


@then('the evaluation should include no actions')
def then_evaluation_no_actions(context):
    assert context['evaluation'].actions == []

@then(parsers.parse('the evaluation credits should equal {credits:d}'))
def then_evaluation_specific_credits(context, credits):
    assert context['evaluation'].credits_after == credits


@then(parsers.parse('the evaluation cargo should equal {cargo_json}'))
def then_evaluation_specific_cargo(context, cargo_json):
    expected = json.loads(cargo_json)
    assert context['evaluation'].cargo_after == expected


@then(parsers.parse('the evaluation net profit should equal {profit:d}'))
def then_evaluation_specific_profit(context, profit):
    assert context['evaluation'].net_profit == profit


@given(parsers.parse('a greedy route planner starting at "{waypoint}" with no cargo or credits'))
def given_greedy_planner(context, waypoint):
    planner = GreedyRoutePlanner(logger=MagicMock(), db=MagicMock())
    planner._estimate_distance = MagicMock(return_value=10)
    context['planner'] = planner
    context['current_waypoint'] = waypoint
    context['cargo'] = {}
    context['credits'] = 0


@given(parsers.parse('a candidate market list of {markets}'))
def given_candidate_markets(context, markets):
    parsed = markets.strip('[]')
    context['candidate_markets'] = [m.strip().strip('"') for m in parsed.split(',')] if parsed else []


@given(parsers.parse('starting credits are {amount:d}'))
def given_starting_credits(context, amount):
    context['credits'] = amount


@given('the strategy evaluation favors "C" with higher profit')
def given_strategy_favors_market(context):
    planner = context['planner']

    def fake_evaluate(market, **kwargs):
        if market == 'C':
            return MarketEvaluation(actions=[], cargo_after={}, credits_after=context.get('credits', 0), net_profit=40)
        return MarketEvaluation(actions=[], cargo_after={}, credits_after=context.get('credits', 0), net_profit=15)

    planner.strategy.evaluate = MagicMock(side_effect=fake_evaluate)


@when('the planner searches for the next market')
def when_planner_searches(context):
    planner = context['planner']
    result = planner._find_best_next_market(
        current_waypoint=context['current_waypoint'],
        current_cargo=context['cargo'],
        current_credits=context['credits'],
        markets=context.get('candidate_markets', []),
        trade_opportunities=context.get('opportunities', []),
        cargo_capacity=10,
        visited={context['current_waypoint']},
    )
    context['planner_result'] = result


@then('no market option should be selected')
def then_no_market_option(context):
    assert context['planner_result'] is None


@then(parsers.parse('the planner should choose market "{market}"'))
def then_planner_selects_market(context, market):
    assert context['planner_result'][0] == market


@then(parsers.parse('the planner result profit should be {profit:d}'))
def then_planner_profit(context, profit):
    assert context['planner_result'][4] == profit


@given('no markets provide profit')
def given_no_profit(monkeypatch, context):
    planner = context['planner']
    monkeypatch.setattr(planner, '_find_best_next_market', lambda **_: None)


@when('the planner builds a route with max stops 1')
def when_planner_builds_route(context):
    planner = context['planner']
    route = planner.find_route(
        start_waypoint=context['current_waypoint'],
        markets=context.get('candidate_markets', []),
        trade_opportunities=context.get('opportunities', []),
        max_stops=1,
        cargo_capacity=10,
        starting_credits=context['credits'],
        ship_speed=10,
    )
    context['built_route'] = route


@then('the resulting route should be empty')
def then_route_empty(context):
    assert context['built_route'] is None


@given('an executed multileg route with total profit -100')
def given_loss_route(context):
    buy_action = TradeAction(
        waypoint='B',
        good='ORE',
        action='BUY',
        units=5,
        price_per_unit=100,
        total_value=500,
    )
    segment = RouteSegment(
        from_waypoint='A',
        to_waypoint='B',
        distance=10,
        fuel_cost=5,
        actions_at_destination=[buy_action],
        cargo_after={'ORE': 5},
        credits_after=500,
        cumulative_profit=-100,
    )
    context['route'] = MultiLegRoute(
        segments=[segment],
        total_profit=-100,
        total_distance=10,
        total_fuel_cost=5,
        estimated_time_minutes=60,
    )


@when('the execution routine runs')
def when_execute_route(monkeypatch, context):
    class NavigatorStub:
        def __init__(self, *_):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    def fake_post(*_args, **_kwargs):
        return {'data': {'contract': {}}}

    ship = MagicMock()
    ship.get_status.return_value = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'A'},
        'cargo': {'inventory': [{'symbol': 'ORE', 'units': 5}], 'capacity': 20, 'units': 5},
        'fuel': {'current': 100, 'capacity': 100},
    }
    ship.dock.return_value = True
    ship.sell_all.side_effect = [0]

    api = MagicMock()
    api.get_agent.return_value = {'credits': 1000}
    api.post.side_effect = fake_post

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)

    result = multileg_module.execute_multileg_route(context['route'], ship, api, MagicMock(), player_id=7)
    context['execution_result'] = result


@then('the execution should report failure')
def then_execution_failed(context):
    assert context['execution_result'] is False


@given('an executed multileg route with profitable actions')
def given_profitable_route(monkeypatch, context):
    buy_action = TradeAction(
        waypoint='B',
        good='ALLOY',
        action='BUY',
        units=2,
        price_per_unit=50,
        total_value=100,
    )
    sell_action = TradeAction(
        waypoint='B',
        good='GADGET',
        action='SELL',
        units=2,
        price_per_unit=90,
        total_value=180,
    )

    segment = RouteSegment(
        from_waypoint='A',
        to_waypoint='B',
        distance=20,
        fuel_cost=22,
        actions_at_destination=[buy_action, sell_action],
        cargo_after={'GADGET': 0},
        credits_after=1200,
        cumulative_profit=80,
    )

    context['route'] = MultiLegRoute(
        segments=[segment],
        total_profit=58,
        total_distance=20,
        total_fuel_cost=22,
        estimated_time_minutes=60,
    )

    class AgentAPI:
        def __init__(self):
            self.calls = 0
            self.credit_sequence = [1000, 1040, 1040]

        def get_agent(self):
            value = self.credit_sequence[min(self.calls, len(self.credit_sequence) - 1)]
            self.calls += 1
            return {'credits': value}

        def get_market(self, system, waypoint):
            return {
                'tradeGoods': [
                    {'symbol': 'ALLOY', 'sellPrice': 50, 'purchasePrice': 55, 'tradeVolume': 10},
                    {'symbol': 'GADGET', 'purchasePrice': 90, 'sellPrice': 85, 'tradeVolume': 10},
                ]
            }

    api = AgentAPI()

    ship = MagicMock()
    ship.get_status.return_value = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'A'},
        'cargo': {'inventory': [], 'capacity': 30, 'units': 0},
        'fuel': {'current': 80, 'capacity': 100},
    }
    ship.dock.return_value = True
    ship.buy.return_value = {'units': 2, 'totalPrice': 100}

    def fake_sell(good, units, **_):
        return {'units': units, 'totalPrice': 180, 'aborted': False}

    ship.sell.side_effect = fake_sell

    class NavigatorStub:
        def __init__(self, *_):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)

    context['ship'] = ship
    context['api'] = api
    context['player_id'] = 9


@when('the execution routine runs successfully')
def when_execution_runs_success(monkeypatch, context):
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_: None)
    result = multileg_module.execute_multileg_route(
        context['route'],
        context['ship'],
        context['api'],
        MagicMock(),
        player_id=context['player_id'],
    )
    context['execution_result'] = result


@then('the execution should report success')
def then_execution_success(context):
    assert context['execution_result'] is True


@given(parsers.parse('a multi-leg optimizer with database data for system "{system}"'))
def given_optimizer_with_db(system, context):
    markets = ['SYS-B', 'SYS-C']
    coords = {
        'SYS-A': (0, 0),
        'SYS-B': (10, 0),
        'SYS-C': (20, 0),
    }

    class ConnectionStub:
        def __init__(self):
            self._result = []
            self._single = None

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def cursor(self):
            return self

        def execute(self, query, params=()):
            if 'FROM market_data' in query:
                self._result = [(m,) for m in markets]
            elif 'FROM waypoints' in query:
                waypoint = params[0]
                coord = coords.get(waypoint)
                self._single = coord
            else:
                self._result = []
                self._single = None

        def fetchall(self):
            return self._result

        def fetchone(self):
            if self._single is None:
                return None
            return self._single

    class DBStub:
        def __init__(self):
            self.market_requests = []

        def connection(self):
            return ConnectionStub()

        def get_market_data(self, conn, waypoint, good):
            self.market_requests.append((waypoint, good))
            if good is None:
                if waypoint == 'SYS-B':
                    return [{'good_symbol': 'COPPER', 'sell_price': 30, 'trade_volume': 3}]
                return []

            if waypoint == 'SYS-C' and good == 'COPPER':
                return [{'purchase_price': 60, 'trade_volume': 3}]

            return []

    db_stub = DBStub()
    optimizer = MultiLegTradeOptimizer(
        api=SimpleNamespace(),
        db=db_stub,
        player_id=1,
        logger=MagicMock(),
    )

    context['optimizer'] = optimizer
    context['db_stub'] = db_stub
    context['system'] = system


@given(parsers.parse('a multi-leg optimizer with empty market data for system "{system}"'))
def given_optimizer_no_markets(system, context):
    class ConnectionStub:
        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def cursor(self):
            return self

        def execute(self, *_args, **_kwargs):
            self._result = []

        def fetchall(self):
            return []

        def fetchone(self):
            return None

    class EmptyDB:
        def connection(self):
            return ConnectionStub()

        def get_market_data(self, *_args, **_kwargs):
            return []

    optimizer = MultiLegTradeOptimizer(
        api=SimpleNamespace(),
        db=EmptyDB(),
        player_id=1,
        logger=MagicMock(),
    )

    context['optimizer'] = optimizer
    context['system'] = system


@given('the greedy planner returns no route')
def given_planner_returns_none(monkeypatch):
    monkeypatch.setattr(GreedyRoutePlanner, 'find_route', lambda *a, **k: None)


@when(parsers.parse('I find an optimal route from "{start_waypoint}"'))
def when_optimizer_finds_route(context, start_waypoint):
    optimizer = context['optimizer']
    route = optimizer.find_optimal_route(
        start_waypoint=start_waypoint,
        system=context['system'],
        max_stops=2,
        cargo_capacity=10,
        starting_credits=200,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=80,
    )
    context['optimizer_route'] = route


@then(parsers.parse('the optimizer should return a route with {segments:d} segments'))
def then_optimizer_route_segments(context, segments):
    route = context['optimizer_route']
    assert route is not None
    assert len(route.segments) == segments
    assert route.total_profit > 0


@then('the optimizer should record market lookups')
def then_optimizer_recorded_market_data(context):
    requests = context['db_stub'].market_requests
    assert ('SYS-B', None) in requests
    assert ('SYS-C', 'COPPER') in requests


@then('the optimizer should return None')
def then_optimizer_returns_none(context):
    assert context['optimizer_route'] is None


@given('a trade plan request for ship "SHIP-1" with max stops 2')
def given_trade_plan_request(context):
    context['trade_args'] = SimpleNamespace(
        ship='SHIP-1',
        player_id=1,
        system='SYS',
        max_stops=2,
        token='TOKEN',
        log_level='INFO',
    )


@given(parsers.parse('trade plan args for ship "{ship}"'))
def given_trade_plan_args(context, ship):
    context['trade_args'] = SimpleNamespace(
        ship=ship,
        player_id=1,
        system='SYS',
        max_stops=2,
        token='TOKEN',
        log_level='INFO',
    )


@given('trade plan ship status is unavailable')
def given_trade_plan_missing_status(context):
    context['trade_plan_ship_status_override'] = None


@given('trade plan agent data is unavailable')
def given_trade_plan_missing_agent(context):
    context['trade_plan_agent_override'] = None


@given('the optimizer returns no route')
def given_optimizer_failure(monkeypatch):
    class OptimizerStub:
        def __init__(self, *args, **kwargs):
            pass

        def find_optimal_route(self, *args, **kwargs):
            return None

    monkeypatch.setattr(multileg_module, 'MultiLegTradeOptimizer', OptimizerStub)


@given('the optimizer returns a profitable route')
def given_optimizer_success(monkeypatch):
    class OptimizerStub:
        def __init__(self, *args, **kwargs):
            pass

        def find_optimal_route(self, *args, **kwargs):
            segment = RouteSegment(
                from_waypoint='A',
                to_waypoint='B',
                distance=10,
                fuel_cost=5,
                actions_at_destination=[],
                cargo_after={},
                credits_after=1000,
                cumulative_profit=100,
            )
            return MultiLegRoute(
                segments=[segment],
                total_profit=100,
                total_distance=10,
                total_fuel_cost=5,
                estimated_time_minutes=60,
            )

    monkeypatch.setattr(multileg_module, 'MultiLegTradeOptimizer', OptimizerStub)


@when('the trade plan operation runs')
def when_trade_plan_runs(monkeypatch, context):
    default_status = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'START'},
        'cargo': {'capacity': 40},
    }

    def get_ship(_ship):
        override = context.get('trade_plan_ship_status_override', default_status)
        return override

    def get_agent():
        override = context.get('trade_plan_agent_override', {'credits': 1000})
        return override

    fake_api = SimpleNamespace(get_ship=get_ship, get_agent=get_agent)

    monkeypatch.setattr('spacetraders_bot.operations.common.get_api_client', lambda *_: fake_api)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: MagicMock())

    class ShipStub:
        def __init__(self, api, symbol):
            self.api = api

        def get_status(self):
            return fake_api.get_ship('SHIP-1')

    monkeypatch.setattr(multileg_module, 'ShipController', ShipStub)

    if 'execute_spy' in context:
        monkeypatch.setattr(multileg_module, 'execute_multileg_route', context['execute_spy'])
    else:
        monkeypatch.setattr(multileg_module, 'execute_multileg_route', lambda *a, **k: True)

    result = trade_plan_operation(context['trade_args'])
    context['trade_result'] = result


@then('the trade plan should exit with status 1')
def then_trade_plan_failure(context):
    assert context['trade_result'] == 1


@then('the trade plan should exit with status 0')
def then_trade_plan_success(context):
    assert context['trade_result'] == 0


# ===== FIXED ROUTE CREATION =====


@given(parsers.parse('fixed route market data buy price {buy_price:d} sell price {sell_price:d} trade volume {volume:d}'))
def given_fixed_route_market_data(context, buy_price, sell_price, volume):
    context['fixed_route_buy'] = {
        'sell_price': buy_price,
        'trade_volume': volume,
    }
    context['fixed_route_sell'] = {
        'purchase_price': sell_price,
        'trade_volume': volume,
    }
    context['fixed_route_missing_sell'] = False


@given('the sell market data is unavailable')
def given_fixed_route_missing_sell(context):
    context['fixed_route_missing_sell'] = True


@given(parsers.parse('default distance per leg is {distance:d} units'))
def given_default_distance(context, distance):
    context['fixed_route_distance'] = distance


@when(parsers.parse('I create a fixed route from "{current}" buying at "{buy}" selling at "{sell}" for "{good}"'), target_fixture='fixed_route_result')
def when_create_fixed_route(monkeypatch, context, current, buy, sell, good):
    distance = context.get('fixed_route_distance', 10)

    def fake_distance(a, b):
        return distance

    monkeypatch.setattr('spacetraders_bot.core.utils.calculate_distance', fake_distance)

    buy_data = context['fixed_route_buy']
    sell_data = None if context.get('fixed_route_missing_sell') else context['fixed_route_sell']

    class TransactionCtx:
        def __enter__(self_inner):
            return object()

        def __exit__(self_inner, exc_type, exc, tb):
            return False

    class FixedRouteDB:
        def transaction(self_inner):
            return TransactionCtx()

        def get_market_data(self_inner, _conn, waypoint, requested_good, player_id):
            if waypoint == buy and requested_good == good:
                return buy_data
            if waypoint == sell and requested_good == good:
                return sell_data
            return None

    db = FixedRouteDB()
    api = SimpleNamespace()

    route = multileg_module.create_fixed_route(
        api=api,
        db=db,
        player_id=1,
        current_waypoint=current,
        buy_waypoint=buy,
        sell_waypoint=sell,
        good=good,
        cargo_capacity=10,
        starting_credits=500,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=80,
    )

    context['fixed_route'] = route
    return route


@then(parsers.parse('the fixed route should have {segments:d} segments'))
def then_fixed_route_segments(context, segments):
    route = context['fixed_route']
    assert route is not None
    assert len(route.segments) == segments


@then('the fixed route profit should be positive')
def then_fixed_route_profit_positive(context):
    route = context['fixed_route']
    assert route.total_profit > 0


@then('the fixed route result should be None')
def then_fixed_route_none(context):
    assert context['fixed_route'] is None


# ===== CIRCUIT BREAKER EXECUTION VARIANTS =====


def _build_execution_context(mode):
    buy_action = TradeAction(
        waypoint='B',
        good='ALLOY',
        action='BUY',
        units=2,
        price_per_unit=50,
        total_value=100,
    )
    sell_action = TradeAction(
        waypoint='B',
        good='GADGET',
        action='SELL',
        units=2,
        price_per_unit=90,
        total_value=180,
    )

    segment = RouteSegment(
        from_waypoint='A',
        to_waypoint='B',
        distance=20,
        fuel_cost=22,
        actions_at_destination=[buy_action, sell_action],
        cargo_after={'GADGET': 0},
        credits_after=1200,
        cumulative_profit=80,
    )

    route = MultiLegRoute(
        segments=[segment],
        total_profit=58,
        total_distance=20,
        total_fuel_cost=22,
        estimated_time_minutes=60,
    )

    class AgentAPI:
        def __init__(self, mode):
            self.mode = mode
            self.calls = 0
            if mode == 'profit_loss':
                self.credit_sequence = [1000, 1000, 900, 900, 900]
            else:
                self.credit_sequence = [1000, 1040, 1040]

        def get_agent(self):
            if self.mode == 'agent_fail' and self.calls == 0:
                self.calls += 1
                return None
            value = self.credit_sequence[min(self.calls, len(self.credit_sequence) - 1)]
            self.calls += 1
            return {'credits': value}

        def get_market(self, system, waypoint):
            if self.mode == 'buy_live_spike':
                return {
                    'tradeGoods': [
                        {'symbol': 'ALLOY', 'sellPrice': 80, 'tradeVolume': 10},
                        {'symbol': 'GADGET', 'purchasePrice': 90, 'tradeVolume': 10},
                    ]
                }
            if self.mode == 'sell_price_crash':
                return {
                    'tradeGoods': [
                        {'symbol': 'ALLOY', 'sellPrice': 50, 'tradeVolume': 10},
                        {'symbol': 'GADGET', 'purchasePrice': 40, 'tradeVolume': 10},
                    ]
                }
            return {
                'tradeGoods': [
                    {'symbol': 'ALLOY', 'sellPrice': 50, 'tradeVolume': 10},
                    {'symbol': 'GADGET', 'purchasePrice': 90, 'tradeVolume': 10},
                ]
            }

    api = AgentAPI(mode)

    ship = MagicMock()
    base_status = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'A'},
        'cargo': {'inventory': [], 'capacity': 30, 'units': 0},
        'fuel': {'current': 80, 'capacity': 100},
    }

    def ship_status():
        if mode == 'ship_status_fail' and not getattr(ship, '_status_failed', False):
            ship._status_failed = True
            return None
        return base_status

    ship.get_status.side_effect = ship_status
    ship.dock.return_value = False if mode == 'dock_fail' else True

    def buy_success(_good, _units):
        return {'units': 2, 'totalPrice': 100}

    def buy_actual_spike(_good, _units):
        return {'units': 2, 'totalPrice': 160}

    ship.buy.side_effect = buy_actual_spike if mode == 'buy_actual_spike' else buy_success

    def sell_success(good, units, **_kwargs):
        return {'units': units, 'totalPrice': 180, 'aborted': False}

    def sell_abort(good, units, **_kwargs):
        return {'units': units - 1, 'totalPrice': 90, 'aborted': True, 'remaining_units': 1}

    ship.sell.side_effect = sell_abort if mode == 'sell_abort' else sell_success

    class NavigatorStub:
        def __init__(self, *_):
            pass

        def execute_route(self, *_args, **_kwargs):
            return False if mode == 'navigation_fail' else True

    return route, ship, api, NavigatorStub


def _prepare_execution_context(context, monkeypatch, mode):
    route, ship, api, navigator_cls = _build_execution_context(mode)
    monkeypatch.setattr(multileg_module, 'SmartNavigator', navigator_cls)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_: None)
    context['route'] = route
    context['ship'] = ship
    context['api'] = api
    context['player_id'] = context.get('player_id', 1)


@given('an executed multileg route prepared for buy price spike')
def given_route_buy_spike(monkeypatch, context):
    _prepare_execution_context(context, monkeypatch, 'buy_live_spike')


@given('an executed multileg route prepared for actual buy spike')
def given_route_actual_buy_spike(monkeypatch, context):
    _prepare_execution_context(context, monkeypatch, 'buy_actual_spike')


@given('an executed multileg route prepared for sell price crash')
def given_route_sell_crash(monkeypatch, context):
    _prepare_execution_context(context, monkeypatch, 'sell_price_crash')


@given('an executed multileg route prepared for aborted sale')
def given_route_sell_abort(monkeypatch, context):
    _prepare_execution_context(context, monkeypatch, 'sell_abort')


@given('ship status retrieval fails before execution')
def given_route_ship_status_fail(context):
    original = context['ship'].get_status

    def failing_status():
        context['ship'].get_status = original
        return None

    context['ship'].get_status = failing_status


@given('agent lookup fails before execution')
def given_route_agent_fail(context):
    original = context['api'].get_agent

    def failing_get_agent():
        context['api'].get_agent = original
        return None

    context['api'].get_agent = failing_get_agent


@given('navigation fails during execution')
def given_route_navigation_fail(monkeypatch, context):
    class NavigatorFail:
        def __init__(self, *_):
            pass

        def execute_route(self, *_args, **_kwargs):
            return False

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorFail)


@given('docking fails during execution')
def given_route_dock_fail(context):
    context['ship'].dock.return_value = False


@given('an executed multileg route prepared for profitable actions with loss')
def given_route_profit_loss(monkeypatch, context):
    _prepare_execution_context(context, monkeypatch, 'profit_loss')


@when('the execution routine runs for buy price spike')
def when_execution_buy_spike(context):
    result = multileg_module.execute_multileg_route(
        context['route'],
        context['ship'],
        context['api'],
        MagicMock(),
        player_id=1,
    )
    context['execution_result'] = result


@when('the execution routine runs for sell price crash')
def when_execution_sell_crash(context):
    result = multileg_module.execute_multileg_route(
        context['route'],
        context['ship'],
        context['api'],
        MagicMock(),
        player_id=1,
    )
    context['execution_result'] = result


# ===== MULTILEG TRADE OPERATION =====


@given('multileg trade args for autonomous one-shot')
def given_trade_args_autonomous(context):
    context['trade_operation_args'] = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        max_stops=2,
        min_profit=0,
        log_level='INFO',
        buy_from=None,
        sell_to=None,
        good=None,
        cycles=None,
        duration=None,
        system=None,
    )
    context['use_optimizer'] = True


@given('multileg trade args for fixed route mode')
def given_trade_args_fixed_route(context):
    context['trade_operation_args'] = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        max_stops=2,
        min_profit=0,
        log_level='INFO',
        buy_from='SYS-B',
        sell_to='SYS-C',
        good='COPPER',
        cycles=None,
        duration=None,
        system=None,
    )
    context['use_optimizer'] = False


@given('the optimizer yields a profitable route for operation')
def given_optimizer_route(context):
    segment = RouteSegment(
        from_waypoint='SYS-A',
        to_waypoint='SYS-B',
        distance=20,
        fuel_cost=22,
        actions_at_destination=[],
        cargo_after={},
        credits_after=1100,
        cumulative_profit=120,
    )
    context['optimizer_route'] = MultiLegRoute(
        segments=[segment],
        total_profit=120,
        total_distance=20,
        total_fuel_cost=22,
        estimated_time_minutes=45,
    )


@given('route execution succeeds during operation')
def given_operation_execution_success(context):
    context['execution_result_value'] = True


@given('fixed route builder returns None during operation')
def given_fixed_route_override_none(context):
    context['fixed_route_override'] = None


@given(parsers.parse('API credits sequence is {sequence}'))
def given_api_credits_sequence(context, sequence):
    credits = [int(item.strip()) for item in sequence.split(',') if item.strip()]
    context['api_credits'] = credits


@when('I run the multileg trade operation')
def when_run_trade_operation(monkeypatch, context):
    args = context['trade_operation_args']

    monkeypatch.setattr('spacetraders_bot.operations.common.setup_logging', lambda *a, **k: 'logfile.log')

    credits_sequence = context.get('api_credits', [1000, 1000, 1100, 1100])

    class APIStub:
        def __init__(self, credits):
            self.credits = credits
            self.index = 0

        def get_agent(self):
            value = self.credits[min(self.index, len(self.credits) - 1)]
            self.index += 1
            return {'credits': value}

        def get_ship(self, ship_symbol):
            return {
                'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'SYS-A'},
                'cargo': {'capacity': 20, 'units': 0},
                'engine': {'speed': 10},
                'fuel': {'capacity': 100, 'current': 80},
            }

    api_stub = APIStub(credits_sequence)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_api_client', lambda *_: api_stub)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: SimpleNamespace())

    class ShipStub:
        def __init__(self, api, symbol):
            self.api = api
            self.symbol = symbol

        def get_status(self):
            return {
                'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'SYS-A'},
                'cargo': {'capacity': 20, 'units': 0},
                'engine': {'speed': 10},
                'fuel': {'capacity': 100, 'current': 80},
            }

    monkeypatch.setattr(multileg_module, 'ShipController', ShipStub)

    optimizer_calls = []

    class OptimizerStub:
        def __init__(self, api, db, player_id, logger=None, strategy_factory=None):
            optimizer_calls.append(player_id)

        def find_optimal_route(self, *_, **__):
            return context.get('optimizer_route')

    monkeypatch.setattr(multileg_module, 'MultiLegTradeOptimizer', OptimizerStub)

    execution_calls = []

    def fake_execute(route, ship, api, db, player_id):
        execution_calls.append(route)
        return context.get('execution_result_value', True)

    monkeypatch.setattr(multileg_module, 'execute_multileg_route', fake_execute)

    def fake_create_fixed_route(*_args, **_kwargs):
        return context.get('fixed_route_override', context.get('optimizer_route'))

    monkeypatch.setattr(multileg_module, 'create_fixed_route', fake_create_fixed_route)

    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_: None)

    result = multileg_module.multileg_trade_operation(args)

    context['operation_result'] = result
    context['optimizer_calls'] = optimizer_calls
    context['execution_calls'] = execution_calls


@then(parsers.parse('the trade operation should exit with status {code:d}'))
def then_trade_operation_status(context, code):
    assert context['operation_result'] == code


@then('the optimizer should be called once during operation')
def then_optimizer_called_once(context):
    assert len(context['optimizer_calls']) == 1


@then('the route execution should run once')
def then_execution_run_once(context):
    assert len(context['execution_calls']) == 1


@then('the route execution should not run')
def then_execution_not_run(context):
    assert len(context['execution_calls']) == 0

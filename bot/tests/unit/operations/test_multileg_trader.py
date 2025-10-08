from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

import spacetraders_bot.operations.multileg_trader as multileg_module

from spacetraders_bot.operations.multileg_trader import (
    GreedyRoutePlanner,
    ProfitFirstStrategy,
    MultiLegRoute,
    MultiLegTradeOptimizer,
    RouteSegment,
    TradeAction,
    execute_multileg_route,
    create_fixed_route,
    trade_plan_operation,
)


class FakeCursor:
    def __init__(self, rows):
        self.rows = rows
        self.current = []

    def execute(self, query, params):
        if "DISTINCT waypoint_symbol" in query:
            prefix = params[0].rstrip('%')
            self.current = [(wp,) for wp in self.rows['markets'] if wp.startswith(prefix)]
        elif "FROM waypoints" in query:
            symbol = params[0]
            coords = self.rows['waypoints'].get(symbol)
            self.current = [coords] if coords else []
        else:
            self.current = []
        return self

    def fetchall(self):
        return self.current

    def fetchone(self):
        return self.current[0] if self.current else None


class FakeDB:
    def __init__(self, markets=None, market_data=None, waypoints=None):
        self.rows = {
            'markets': markets or [],
            'waypoints': waypoints or {},
        }
        self.market_data = market_data or {}

    class _Conn:
        def __init__(self, db):
            self.db = db

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def cursor(self):
            return FakeCursor(self.db.rows)

    def connection(self):
        return self._Conn(self)

    def get_market_data(self, _conn, waypoint, good):
        return self.market_data.get((waypoint, good), [])


def test_get_markets_in_system(monkeypatch):
    db = FakeDB(markets=['SYS-A1', 'SYS-B1', 'OTHER-C1'])
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=db, player_id=1, logger=MagicMock())

    markets = optimizer._get_markets_in_system('SYS')
    assert markets == ['SYS-A1', 'SYS-B1']


def test_get_trade_opportunities(monkeypatch):
    market_data = {
        ('SYS-A', None): [
            {'good_symbol': 'IRON', 'sell_price': 50, 'trade_volume': 10},
        ],
        ('SYS-B', 'IRON'): [
            {'purchase_price': 90},
        ],
    }
    db = FakeDB(markets=['SYS-A', 'SYS-B'], market_data=market_data)
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=db, player_id=1, logger=MagicMock())

    opportunities = optimizer._get_trade_opportunities('SYS', ['SYS-A', 'SYS-B'])

    assert len(opportunities) == 1
    opp = opportunities[0]
    assert opp['good'] == 'IRON'
    assert opp['buy_waypoint'] == 'SYS-A'
    assert opp['sell_waypoint'] == 'SYS-B'
    assert opp['spread'] == 40


def test_strategy_evaluation_buys_and_sells():
    strategy = ProfitFirstStrategy(logger=MagicMock())

    trade_ops = [
        {
            'buy_waypoint': 'A',
            'sell_waypoint': 'B',
            'good': 'IRON',
            'buy_price': 10,
            'sell_price': 30,
            'spread': 20,
            'trade_volume': 10,
        }
    ]

    buy_eval = strategy.evaluate(
        market='A',
        current_cargo={'IRON': 5},
        current_credits=100,
        trade_opportunities=trade_ops,
        cargo_capacity=20,
        fuel_cost=5,
    )

    assert any(a.action == 'BUY' for a in buy_eval.actions)
    assert buy_eval.cargo_after['IRON'] > 0
    assert buy_eval.net_profit > 0

    sell_eval = strategy.evaluate(
        market='B',
        current_cargo={'IRON': 5},
        current_credits=100,
        trade_opportunities=trade_ops,
        cargo_capacity=20,
        fuel_cost=5,
    )

    assert any(a.action == 'SELL' for a in sell_eval.actions)
    assert 'IRON' not in sell_eval.cargo_after


def test_find_best_next_market(monkeypatch):
    planner = GreedyRoutePlanner(logger=MagicMock(), db=MagicMock())

    planner._estimate_distance = MagicMock(return_value=10)

    trade_ops = [
        {
            'buy_waypoint': 'A',
            'sell_waypoint': 'B',
            'good': 'IRON',
            'buy_price': 10,
            'sell_price': 30,
            'spread': 20,
            'trade_volume': 10,
        },
        {
            'buy_waypoint': 'A',
            'sell_waypoint': 'C',
            'good': 'FUEL',
            'buy_price': 10,
            'sell_price': 12,
            'spread': 2,
            'trade_volume': 10,
        }
    ]

    result = planner._find_best_next_market(
        current_waypoint='A',
        current_cargo={'IRON': 5},
        current_credits=100,
        markets=['B', 'C'],
        trade_opportunities=trade_ops,
        cargo_capacity=20,
        visited={'A'}
    )

    assert result[0] == 'B'


def test_greedy_route_search(monkeypatch):
    planner = GreedyRoutePlanner(logger=MagicMock(), db=MagicMock())

    def fake_find(*args, **kwargs):
        visited = kwargs.get('visited')
        if visited and len(visited) == 1:
            return (
                'B',
                [TradeAction('B', 'IRON', 'SELL', 5, 30, 150)],
                {},
                150,
                120,
                10,
            )
        return None

    monkeypatch.setattr(planner, '_find_best_next_market', fake_find)

    route = planner.find_route(
        start_waypoint='A',
        markets=['B'],
        trade_opportunities=[],
        max_stops=2,
        cargo_capacity=20,
        starting_credits=1000,
        ship_speed=10,
    )

    assert route is not None
    assert route.segments[0].to_waypoint == 'B'


def test_find_optimal_route_positive_profit(monkeypatch):
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=MagicMock(), player_id=1, logger=MagicMock())

    monkeypatch.setattr(optimizer, '_get_markets_in_system', lambda system: ['A', 'B', 'C'])

    trade_ops = [
        {
            'buy_waypoint': 'B',
            'sell_waypoint': 'C',
            'good': 'COPPER',
            'buy_price': 10,
            'sell_price': 30,
            'spread': 20,
            'trade_volume': 20,
        }
    ]
    monkeypatch.setattr(optimizer, '_get_trade_opportunities', lambda system, markets: trade_ops)

    class StubPlanner:
        def __init__(self, *_, **__):
            pass

        def find_route(self, **kwargs):
            planner = GreedyRoutePlanner(logger=MagicMock(), db=FakeDB())
            planner._estimate_distance = lambda *args, **kwargs: 10
            return planner.find_route(
                start_waypoint='A',
                markets=['B', 'C'],
                trade_opportunities=trade_ops,
                max_stops=3,
                cargo_capacity=20,
                starting_credits=1000,
                ship_speed=10,
            )

    monkeypatch.setattr(multileg_module, 'GreedyRoutePlanner', StubPlanner)

    route = optimizer.find_optimal_route(
        start_waypoint='A',
        system='SYS',
        max_stops=3,
        cargo_capacity=20,
        starting_credits=1000,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=50,
    )

    assert route is not None
    destinations = [segment.to_waypoint for segment in route.segments]
    assert destinations == ['B', 'C']
    assert route.total_profit > 0
    # First segment should contain a BUY action
    assert any(action.action == 'BUY' for action in route.segments[0].actions_at_destination)


def _ship_status_stub():
    return {
        'nav': {
            'systemSymbol': 'SYS',
            'waypointSymbol': 'START',
        },
        'cargo': {'capacity': 50, 'units': 0},
        'fuel': {'capacity': 100, 'current': 80},
    }


def test_execute_multileg_route_returns_false_without_ship_status(monkeypatch):
    route = MultiLegRoute(
        segments=[],
        total_profit=0,
        total_distance=0,
        total_fuel_cost=0,
        estimated_time_minutes=0,
    )

    ship = MagicMock()
    ship.get_status.return_value = None

    api = MagicMock()
    db = MagicMock()

    class DummyNavigator:
        def __init__(self, *_args, **_kwargs):
            pass

    monkeypatch.setattr(multileg_module, 'SmartNavigator', DummyNavigator)

    assert execute_multileg_route(route, ship, api, db, player_id=7) is False


def test_execute_multileg_route_returns_false_without_agent(monkeypatch):
    route = MultiLegRoute(
        segments=[],
        total_profit=0,
        total_distance=0,
        total_fuel_cost=0,
        estimated_time_minutes=0,
    )

    ship = MagicMock()
    ship.get_status.return_value = _ship_status_stub()

    api = MagicMock()
    api.get_agent.return_value = None

    db = MagicMock()

    class DummyNavigator:
        def __init__(self, *_args, **_kwargs):
            pass

    monkeypatch.setattr(multileg_module, 'SmartNavigator', DummyNavigator)

    assert execute_multileg_route(route, ship, api, db, player_id=7) is False


def test_execute_multileg_route_stops_on_navigation_failure(monkeypatch):
    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=42,
        fuel_cost=10,
        actions_at_destination=[],
        cargo_after={},
        credits_after=1000,
        cumulative_profit=200,
    )
    route = MultiLegRoute(
        segments=[segment],
        total_profit=200,
        total_distance=42,
        total_fuel_cost=10,
        estimated_time_minutes=5,
    )

    ship = MagicMock()
    ship.get_status.return_value = _ship_status_stub()

    api = MagicMock()
    api.get_agent.return_value = {'credits': 5_000}

    db = MagicMock()

    class DummyNavigator:
        def __init__(self, *_args, **_kwargs):
            pass

        def execute_route(self, *_args, **_kwargs):
            return False

    monkeypatch.setattr(multileg_module, 'SmartNavigator', DummyNavigator)

    result = execute_multileg_route(route, ship, api, db, player_id=7)

    assert result is False
    ship.dock.assert_not_called()


def test_execute_multileg_route_success_path(monkeypatch):
    buy_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='BUY',
        units=5,
        price_per_unit=100,
        total_value=500,
    )
    sell_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='SELL',
        units=5,
        price_per_unit=140,
        total_value=700,
    )
    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=50,
        fuel_cost=12,
        actions_at_destination=[buy_action, sell_action],
        cargo_after={},
        credits_after=5_200,
        cumulative_profit=300,
    )
    route = MultiLegRoute(
        segments=[segment],
        total_profit=300,
        total_distance=50,
        total_fuel_cost=12,
        estimated_time_minutes=10,
    )

    class DummyNavigator:
        def __init__(self, api, system):
            self.api = api
            self.system = system

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', DummyNavigator)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_args, **_kwargs: None)

    ship = MagicMock()
    ship.get_status.return_value = _ship_status_stub()
    ship.dock.return_value = True
    ship.buy.return_value = {'units': 5, 'totalPrice': 500}
    ship.sell.return_value = {'units': 5, 'totalPrice': 700}

    api = MagicMock()
    api.get_agent.side_effect = [
        {'credits': 5_000},
        {'credits': 5_300},
        {'credits': 5_700},
    ]
    api.get_market.side_effect = [
        {'tradeGoods': [{'symbol': 'FOOD', 'sellPrice': 100, 'tradeVolume': 5}]},
        {'tradeGoods': [{'symbol': 'FOOD', 'purchasePrice': 140, 'tradeVolume': 5}]},
    ]

    db = MagicMock()

    assert execute_multileg_route(route, ship, api, db, player_id=7) is True
    assert api.get_market.call_count == 2
    ship.dock.assert_called_once()
    ship.buy.assert_called_once()
    ship.sell.assert_called_once()


def test_execute_multileg_route_aborted_sale(monkeypatch):
    sell_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='SELL',
        units=5,
        price_per_unit=140,
        total_value=700,
    )

    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=10,
        fuel_cost=5,
        actions_at_destination=[sell_action],
        cargo_after={},
        credits_after=5_500,
        cumulative_profit=200,
    )

    route = MultiLegRoute(
        segments=[segment],
        total_profit=200,
        total_distance=10,
        total_fuel_cost=5,
        estimated_time_minutes=2,
    )

    class NavigatorStub:
        def __init__(self, *_args, **_kwargs):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_args, **_kwargs: None)

    ship = MagicMock()
    ship.get_status.return_value = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'START'},
        'cargo': {'capacity': 20, 'units': 5},
        'fuel': {'capacity': 100, 'current': 80},
    }
    ship.dock.return_value = True
    ship.sell.return_value = {'aborted': True, 'units': 2, 'remaining_units': 3, 'totalPrice': 200}

    api = MagicMock()
    api.get_agent.side_effect = [{'credits': 5_000}, {'credits': 5_200}]
    api.get_market.return_value = {
        'tradeGoods': [{'symbol': 'FOOD', 'purchasePrice': 120, 'tradeVolume': 10}]
    }

    db = MagicMock()

    result = execute_multileg_route(route, ship, api, db, player_id=7)

    assert result is False
    ship.sell.assert_called_once()


def test_execute_multileg_route_buy_price_spike(monkeypatch):
    buy_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='BUY',
        units=5,
        price_per_unit=100,
        total_value=500,
    )

    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=20,
        fuel_cost=5,
        actions_at_destination=[buy_action],
        cargo_after={},
        credits_after=4_500,
        cumulative_profit=0,
    )

    route = MultiLegRoute(
        segments=[segment],
        total_profit=0,
        total_distance=20,
        total_fuel_cost=5,
        estimated_time_minutes=3,
    )

    class NavigatorStub:
        def __init__(self, *_args, **_kwargs):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_args, **_kwargs: None)

    ship = MagicMock()
    ship.get_status.return_value = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'START'},
        'cargo': {'capacity': 10, 'units': 0},
        'fuel': {'capacity': 100, 'current': 90},
    }
    ship.dock.return_value = True

    api = MagicMock()
    api.get_agent.side_effect = [{'credits': 5_000}, {'credits': 5_000}]
    api.get_market.return_value = {
        'tradeGoods': [{'symbol': 'FOOD', 'sellPrice': 150, 'tradeVolume': 10}]
    }

    db = MagicMock()

    result = execute_multileg_route(route, ship, api, db, player_id=7)

    assert result is False
    ship.buy.assert_not_called()
    api.get_market.assert_called_once()


def test_execute_multileg_route_segment_loss(monkeypatch):
    buy_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='BUY',
        units=5,
        price_per_unit=100,
        total_value=500,
    )
    sell_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='SELL',
        units=5,
        price_per_unit=80,
        total_value=400,
    )

    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=15,
        fuel_cost=6,
        actions_at_destination=[buy_action, sell_action],
        cargo_after={},
        credits_after=4_900,
        cumulative_profit=0,
    )

    route = MultiLegRoute(
        segments=[segment],
        total_profit=0,
        total_distance=15,
        total_fuel_cost=6,
        estimated_time_minutes=4,
    )

    class NavigatorStub:
        def __init__(self, *_args, **_kwargs):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_args, **_kwargs: None)

    ship = MagicMock()
    ship.get_status.return_value = _ship_status_stub()
    ship.dock.return_value = True
    ship.buy.return_value = {'units': 5, 'totalPrice': 500}
    ship.sell.return_value = {'units': 5, 'totalPrice': 300}

    api = MagicMock()
    api.get_agent.side_effect = [{'credits': 5_000}, {'credits': 4_900}, {'credits': 4_600}]
    api.get_market.side_effect = [
        {'tradeGoods': [{'symbol': 'FOOD', 'sellPrice': 100, 'tradeVolume': 10}]},
        {'tradeGoods': [{'symbol': 'FOOD', 'purchasePrice': 80, 'tradeVolume': 10}]},
    ]

    db = MagicMock()

    result = execute_multileg_route(route, ship, api, db, player_id=7)

    assert result is False
    ship.buy.assert_called_once()
    ship.sell.assert_called_once()


def test_trade_plan_operation_success(monkeypatch, capsys):
    ship_data = {
        'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'START'},
        'cargo': {'capacity': 20, 'units': 0},
        'engine': {'speed': 10},
        'fuel': {'capacity': 100, 'current': 80},
    }

    class ShipStub:
        def __init__(self, api, symbol):
            self.api = api
            self.symbol = symbol

        def get_status(self):
            return ship_data

    class APIStub:
        def __init__(self):
            self.get_agent = lambda: {'credits': 5000}

    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=50,
        fuel_cost=10,
        actions_at_destination=[],
        cargo_after={},
        credits_after=5_500,
        cumulative_profit=500,
    )
    route = MultiLegRoute(
        segments=[segment],
        total_profit=500,
        total_distance=50,
        total_fuel_cost=10,
        estimated_time_minutes=15,
    )

    class OptimizerStub:
        def __init__(self, api, db, player_id):
            pass

        def find_optimal_route(self, **kwargs):
            return route

    monkeypatch.setattr('spacetraders_bot.operations.common.get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: object())
    monkeypatch.setattr(multileg_module, 'ShipController', ShipStub)
    monkeypatch.setattr(multileg_module, 'MultiLegTradeOptimizer', OptimizerStub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1', max_stops=3, log_level='INFO')
    result = trade_plan_operation(args)

    assert result == 0
    out = capsys.readouterr().out
    assert '"ship": "SHIP-1"' in out


def test_trade_plan_operation_missing_player(capsys):
    args = SimpleNamespace(ship='SHIP-1')
    assert trade_plan_operation(args) == 1
    assert '--player-id' in capsys.readouterr().out


def test_trade_plan_operation_invalid_max_stops(capsys):
    args = SimpleNamespace(player_id=1, ship='SHIP-1', max_stops='bad')
    assert trade_plan_operation(args) == 1
    assert 'must be an integer' in capsys.readouterr().out


def test_trade_plan_operation_max_stops_too_low(capsys):
    args = SimpleNamespace(player_id=1, ship='SHIP-1', max_stops=1)
    assert trade_plan_operation(args) == 1
    assert 'at least 2' in capsys.readouterr().out


def test_trade_plan_operation_api_failure(monkeypatch, capsys):
    monkeypatch.setattr('spacetraders_bot.operations.common.get_api_client', lambda player_id: (_ for _ in ()).throw(RuntimeError('boom')))
    args = SimpleNamespace(player_id=1, ship='SHIP-1', max_stops=3)
    assert trade_plan_operation(args) == 1
    assert 'boom' in capsys.readouterr().out


def test_trade_plan_operation_no_route(monkeypatch, capsys):
    class ShipStub:
        def __init__(self, api, symbol):
            pass

        def get_status(self):
            return {
                'nav': {'systemSymbol': 'SYS', 'waypointSymbol': 'START'},
                'cargo': {'capacity': 20, 'units': 0},
                'engine': {'speed': 10},
                'fuel': {'capacity': 100, 'current': 80},
            }

    class APIStub:
        def get_agent(self):
            return {'credits': 5000}

    class OptimizerStub:
        def __init__(self, api, db, player_id):
            pass

        def find_optimal_route(self, **kwargs):
            return None

    monkeypatch.setattr('spacetraders_bot.operations.common.get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: object())
    monkeypatch.setattr(multileg_module, 'ShipController', ShipStub)
    monkeypatch.setattr(multileg_module, 'MultiLegTradeOptimizer', OptimizerStub)

    args = SimpleNamespace(player_id=1, ship='SHIP-1', max_stops=3)
    assert trade_plan_operation(args) == 1
    assert 'No profitable route found' in capsys.readouterr().out


def test_create_fixed_route_success(monkeypatch):
    class DBStub:
        class Tx:
            def __init__(self, outer):
                self.outer = outer

            def __enter__(self):
                return self.outer

            def __exit__(self, exc_type, exc, tb):
                return False

        def transaction(self):
            return self.Tx(self)

        def get_market_data(self, conn, waypoint, good, player_id):
            if waypoint == 'B':
                return {'sell_price': 100, 'trade_volume': 10}
            return {'purchase_price': 220, 'trade_volume': 10}

    monkeypatch.setattr('spacetraders_bot.core.utils.calculate_distance', lambda a, b: 2)

    db = DBStub()
    api = object()
    route = create_fixed_route(
        api,
        db,
        player_id=1,
        current_waypoint='A',
        buy_waypoint='B',
        sell_waypoint='C',
        good='FOOD',
        cargo_capacity=10,
        starting_credits=10_000,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=80,
    )

    assert route is not None
    assert route.total_profit > 0


def test_create_fixed_route_missing_market(monkeypatch):
    class DBStub:
        class Tx:
            def __enter__(self):
                return self

            def __exit__(self, exc_type, exc, tb):
                return False

        def transaction(self):
            return self.Tx()

        def get_market_data(self, conn, waypoint, good, player_id):
            return None

    monkeypatch.setattr('spacetraders_bot.core.utils.calculate_distance', lambda a, b: 10)

    route = create_fixed_route(
        api=object(),
        db=DBStub(),
        player_id=1,
        current_waypoint='A',
        buy_waypoint='B',
        sell_waypoint='C',
        good='FOOD',
        cargo_capacity=10,
        starting_credits=10_000,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=80,
    )

    assert route is None


def test_create_fixed_route_not_profitable(monkeypatch):
    class DBStub:
        class Tx:
            def __init__(self, outer):
                self.outer = outer

            def __enter__(self):
                return self.outer

            def __exit__(self, exc_type, exc, tb):
                return False

        def transaction(self):
            return self.Tx(self)

        def get_market_data(self, conn, waypoint, good, player_id):
            if waypoint == 'B':
                return {'sell_price': 300, 'trade_volume': 1}
            return {'purchase_price': 200, 'trade_volume': 1}

    monkeypatch.setattr('spacetraders_bot.core.utils.calculate_distance', lambda a, b: 100)

    route = create_fixed_route(
        api=object(),
        db=DBStub(),
        player_id=1,
        current_waypoint='A',
        buy_waypoint='B',
        sell_waypoint='C',
        good='FOOD',
        cargo_capacity=10,
        starting_credits=1_000,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=80,
    )

    assert route is None


def test_execute_multileg_route_overall_loss(monkeypatch):
    buy_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='BUY',
        units=5,
        price_per_unit=100,
        total_value=500,
    )
    sell_action = TradeAction(
        waypoint='DEST',
        good='FOOD',
        action='SELL',
        units=5,
        price_per_unit=120,
        total_value=600,
    )

    segment = RouteSegment(
        from_waypoint='START',
        to_waypoint='DEST',
        distance=20,
        fuel_cost=5,
        actions_at_destination=[buy_action, sell_action],
        cargo_after={},
        credits_after=4_900,
        cumulative_profit=100,
    )

    route = MultiLegRoute(
        segments=[segment],
        total_profit=100,
        total_distance=20,
        total_fuel_cost=5,
        estimated_time_minutes=5,
    )

    class NavigatorStub:
        def __init__(self, *_args, **_kwargs):
            pass

        def execute_route(self, *_args, **_kwargs):
            return True

    monkeypatch.setattr(multileg_module, 'SmartNavigator', NavigatorStub)
    monkeypatch.setattr(multileg_module.time, 'sleep', lambda *_args, **_kwargs: None)

    ship = MagicMock()
    ship.get_status.return_value = _ship_status_stub()
    ship.dock.return_value = True
    ship.buy.return_value = {'units': 5, 'totalPrice': 400}
    ship.sell.return_value = {'units': 5, 'totalPrice': 500}

    api = MagicMock()
    api.get_agent.side_effect = [{'credits': 5_000}, {'credits': 4_900}, {'credits': 4_800}]
    api.get_market.side_effect = [
        {'tradeGoods': [{'symbol': 'FOOD', 'sellPrice': 108, 'tradeVolume': 10}]},
        {'tradeGoods': [{'symbol': 'FOOD', 'purchasePrice': 115, 'tradeVolume': 10}]},
    ]

    db = MagicMock()

    result = execute_multileg_route(route, ship, api, db, player_id=7)

    assert result is True

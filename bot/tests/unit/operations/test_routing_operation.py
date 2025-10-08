from pathlib import Path
from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import routing


class FakeGraphBuilder:
    def __init__(self, api, db=None):
        self.api = api
        self.db = db
        self.called = False

    def build_system_graph(self, system):
        self.called = True
        graph = {
            'waypoints': {},
            'edges': [],
        }
        if self.db is not None:
            self.db.graph = graph
        return graph


class FakeDatabase:
    def __init__(self, graph):
        self.graph = graph

    class Conn:
        def __init__(self, graph):
            self.graph = graph

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

    def connection(self):
        return self.Conn(self.graph)

    def get_system_graph(self, _conn, system):
        return self.graph


class FakeAPI:
    def __init__(self, ship_data, route=None):
        self.ship_data = ship_data
        self.route = route

    def get_ship(self, ship):
        return self.ship_data


class FakeRouteOptimizer:
    def __init__(self, graph, ship_data):
        self.graph = graph
        self.ship_data = ship_data

    def find_optimal_route(self, start, goal, current_fuel, prefer_cruise=True):
        return {
            'total_time': 120,
            'final_fuel': 80,
            'steps': [
                {
                    'action': 'navigate',
                    'from': start,
                    'to': goal,
                    'mode': 'CRUISE',
                    'distance': 10,
                    'time': 120,
                    'fuel_cost': 5,
                }
            ],
        }


@pytest.fixture(autouse=True)
def no_logging(monkeypatch, tmp_path):
    monkeypatch.setattr(routing, 'setup_logging', lambda *a, **k: tmp_path / 'route.log')
    monkeypatch.setattr(routing.TimeCalculator, 'format_time', lambda seconds: f"{int(seconds/60)}m")


def test_route_plan_operation_success(monkeypatch, tmp_path):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph={'waypoints': {}, 'edges': []}))
    monkeypatch.setattr(routing, 'RouteOptimizer', FakeRouteOptimizer)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        start='A',
        goal='B',
        drift_only=False,
        output=None,
        log_level='INFO',
    )

    result = routing.route_plan_operation(args)
    assert result == 0


def test_route_plan_operation_writes_output(monkeypatch, tmp_path):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph={'waypoints': {}, 'edges': []}))
    monkeypatch.setattr(routing, 'RouteOptimizer', FakeRouteOptimizer)

    output_path = tmp_path / 'route.json'

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        start='A',
        goal='B',
        drift_only=False,
        output=str(output_path),
        log_level='INFO',
    )

    result = routing.route_plan_operation(args)
    assert result == 0
    assert output_path.exists()


def test_route_plan_operation_builds_graph_when_missing(monkeypatch):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)

    database_instance = FakeDatabase(graph=None)
    builder = FakeGraphBuilder(fake_api, db=database_instance)
    monkeypatch.setattr(routing, 'GraphBuilder', lambda api: builder)
    monkeypatch.setattr(routing, 'Database', lambda: database_instance)
    monkeypatch.setattr(routing, 'RouteOptimizer', FakeRouteOptimizer)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        start='A',
        goal='B',
        drift_only=False,
        output=None,
        log_level='INFO',
    )

    result = routing.route_plan_operation(args)
    assert result == 0
    assert builder.called is True


def test_route_plan_operation_no_route(monkeypatch):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph={'waypoints': {}, 'edges': []}))

    class NullRouteOptimizer(FakeRouteOptimizer):
        def find_optimal_route(self, *args, **kwargs):  # noqa: D401
            return None

    monkeypatch.setattr(routing, 'RouteOptimizer', NullRouteOptimizer)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        start='A',
        goal='B',
        drift_only=False,
        output=None,
        log_level='INFO',
    )

    result = routing.route_plan_operation(args)
    assert result == 1


def test_graph_build_operation_success(monkeypatch, tmp_path):
    built = {}

    class BuilderStub:
        def __init__(self, api):
            pass

        def build_system_graph(self, system):
            built['system'] = system
            return {'waypoints': {}, 'edges': []}

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: object())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)

    args = SimpleNamespace(player_id=1, system='X1-TEST', log_level='INFO')

    result = routing.graph_build_operation(args)

    assert result == 0
    assert built['system'] == 'X1-TEST'


def test_scout_markets_operation_single_tour(monkeypatch, tmp_path):
    captain_events = []

    class LoggerStub:
        def __init__(self):
            self.messages = []

        def info(self, msg):
            self.messages.append(('info', msg))

        def warning(self, msg):
            self.messages.append(('warning', msg))

        def error(self, msg):
            self.messages.append(('error', msg))

        def addHandler(self, handler):
            pass

        def removeHandler(self, handler):
            pass

    graph = {
        'waypoints': {
            'X1-TEST-M1': {'x': 0, 'y': 0, 'traits': ['MARKETPLACE']},
            'X1-TEST-M2': {'x': 10, 'y': 0, 'traits': ['MARKETPLACE']},
        },
        'edges': [],
    }

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def __init__(self):
            self.market_calls = []

        def get_ship(self, ship_symbol):
            return {
                'nav': {'waypointSymbol': 'X1-TEST-M1'},
                'fuel': {'current': 100, 'capacity': 100},
            }

        def get_market(self, system, waypoint):
            self.market_calls.append(waypoint)
            return {
                'tradeGoods': [
                    {
                        'symbol': 'FOOD',
                        'purchasePrice': 10,
                        'sellPrice': 20,
                        'tradeVolume': 5,
                        'supply': 'ABUNDANT',
                        'activity': 'STRONG',
                    }
                ]
            }

    class TourFactory:
        def __init__(self):
            self.calls = []

        def __call__(self, graph, ship_data):
            factory = self

            class _TourInstance:
                def plan_tour(self, start, stops, current_fuel, return_to_start=False, algorithm='greedy', use_cache=False):
                    factory.calls.append({'use_cache': use_cache, 'stops': stops})
                    return {
                        'total_time': 120,
                        'total_legs': 2,
                        'final_fuel': 90,
                        'legs': [
                            {'goal': 'X1-TEST-M1', 'total_time': 60, 'steps': []},
                            {'goal': 'X1-TEST-M2', 'total_time': 60, 'steps': []},
                        ],
                    }

            return _TourInstance()

        @staticmethod
        def get_markets_from_graph(graph):
            return list(graph['waypoints'].keys())

    class ShipStub:
        def __init__(self, api, ship_symbol):
            self.moves = []

        def dock(self):
            self.moves.append('dock')
            return True

    class NavigatorStub:
        def __init__(self, api, system):
            self.calls = []

        def execute_route(self, ship, destination):
            self.calls.append(destination)
            return True

    class DBStub:
        def __init__(self):
            self.updates = []

        class Tx:
            def __init__(self, outer):
                self.outer = outer

            def __enter__(self):
                return self.outer

            def __exit__(self, exc_type, exc, tb):
                return False

        def transaction(self):
            return self.Tx(self)

        def update_market_data(self, db_conn, **kwargs):
            self.updates.append(kwargs)

    api_stub = APIStub()
    tour_factory = TourFactory()
    navigator_stub_holder = {}
    db_stub = DBStub()

    monkeypatch.setattr(routing, 'setup_logging', lambda *a, **k: tmp_path / 'scout.log')
    monkeypatch.setattr(routing.logging, 'getLogger', lambda name=None: LoggerStub())
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api_stub)
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'TourOptimizer', tour_factory)
    monkeypatch.setattr(routing, 'get_captain_logger', lambda player_id: captain_events)
    monkeypatch.setattr(routing, 'log_captain_event', lambda logger, event_type, **kwargs: captain_events.append((event_type, kwargs)))
    monkeypatch.setattr(routing, 'humanize_duration', lambda delta: '1m')
    monkeypatch.setattr(routing, 'get_operator_name', lambda args: 'Operator')
    monkeypatch.setattr('spacetraders_bot.core.ship_controller.ShipController', ShipStub)
    monkeypatch.setattr('spacetraders_bot.core.utils.timestamp_iso', lambda: '2025-01-01T00:00:00Z')

    def navigator_factory(api, system):
        navigator = NavigatorStub(api, system)
        navigator_stub_holder['navigator'] = navigator
        return navigator

    monkeypatch.setattr('spacetraders_bot.core.smart_navigator.SmartNavigator', navigator_factory)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: db_stub)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        algorithm='greedy',
        continuous=False,
        output=None,
        log_level='INFO',
        return_to_start=False,
    )

    result = routing.scout_markets_operation(args)

    assert result == 0
    assert tour_factory.calls[0]['use_cache'] is True
    assert navigator_stub_holder['navigator'].calls == ['X1-TEST-M1', 'X1-TEST-M2']
    assert len(db_stub.updates) == 2
    assert any(event[0] == 'OPERATION_COMPLETED' for event in captain_events)

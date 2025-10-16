import logging
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


def regression_route_plan_operation_success(monkeypatch, tmp_path):
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


def regression_route_plan_operation_writes_output(monkeypatch, tmp_path):
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


def regression_route_plan_operation_builds_graph_when_missing(monkeypatch):
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


def regression_route_plan_operation_no_route(monkeypatch):
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


def regression_graph_build_operation_success(monkeypatch, tmp_path):
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


def regression_graph_build_operation_failure(monkeypatch):
    class BuilderStub:
        def __init__(self, api):
            pass

        def build_system_graph(self, system):
            return None

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: object())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)

    args = SimpleNamespace(player_id=1, system='X1-TEST', log_level='INFO')

    result = routing.graph_build_operation(args)

    assert result == 1


def regression_scout_markets_operation_single_tour(monkeypatch, tmp_path):
    captain_events = []

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

    class TourOptimizerStub:
        calls = []

        def __init__(self, graph, ship_data):
            self.graph = graph
            self.ship_data = ship_data

        def plan_tour(self, start, stops, current_fuel, return_to_start=False, algorithm='greedy', use_cache=False):
            TourOptimizerStub.calls.append({'use_cache': use_cache, 'stops': stops})
            return {
                'total_time': 120,
                'total_legs': 2,
                'final_fuel': 90,
                'legs': [
                    {'goal': 'X1-TEST-M1', 'total_time': 60, 'steps': []},
                    {'goal': 'X1-TEST-M2', 'total_time': 60, 'steps': []},
                ],
            }

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
    db_stub = DBStub()
    navigator_stub_holder = {}

    def navigator_factory(api, system):
        navigator = NavigatorStub(api, system)
        navigator_stub_holder['navigator'] = navigator
        return navigator

    _patch_scout_common(
        monkeypatch,
        api_stub,
        BuilderStub,
        TourOptimizerStub,
        captain_events,
        ship_cls=ShipStub,
        navigator_factory=navigator_factory,
        database_factory=lambda: db_stub,
    )

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
    assert TourOptimizerStub.calls[0]['use_cache'] is True
    assert navigator_stub_holder['navigator'].calls == ['X1-TEST-M1', 'X1-TEST-M2']
    assert len(db_stub.updates) == 2
    assert any(event[0] == 'OPERATION_COMPLETED' for event in captain_events)


def regression_route_plan_operation_graph_build_failure(monkeypatch):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)

    class BuilderStub:
        def __init__(self, api):
            pass

        def build_system_graph(self, system):
            return None

    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph=None))

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


def regression_route_plan_operation_missing_ship(monkeypatch):
    fake_api = FakeAPI(None)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph={'waypoints': {}, 'edges': []}))

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


def regression_route_plan_operation_includes_refuel(monkeypatch):
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }

    class RefuelRouteOptimizer(FakeRouteOptimizer):
        def find_optimal_route(self, start, goal, current_fuel, prefer_cruise=True):
            return {
                'total_time': 180,
                'final_fuel': 70,
                'steps': [
                    {
                        'action': 'refuel',
                        'waypoint': start,
                        'fuel_added': 30,
                    },
                    {
                        'action': 'navigate',
                        'from': start,
                        'to': goal,
                        'mode': 'CRUISE',
                        'distance': 12,
                        'time': 180,
                        'fuel_cost': 8,
                    },
                ],
            }

    fake_api = FakeAPI(ship_data)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: fake_api)
    monkeypatch.setattr(routing, 'Database', lambda: FakeDatabase(graph={'waypoints': {}, 'edges': []}))
    monkeypatch.setattr(routing, 'RouteOptimizer', RefuelRouteOptimizer)

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


def _patch_scout_common(
    monkeypatch,
    api_stub,
    builder_cls,
    tour_cls,
    captain_events=None,
    ship_cls=None,
    navigator_factory=None,
    database_factory=None,
):
    captain_events = captain_events if captain_events is not None else []

    monkeypatch.setattr(routing, 'setup_logging', lambda *a, **k: Path('scout.log'))
    original_get_logger = logging.getLogger
    monkeypatch.setattr(routing.logging, 'getLogger', lambda name=None: original_get_logger(name or 'test_logger'))
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api_stub)
    monkeypatch.setattr(routing, 'GraphBuilder', builder_cls)
    monkeypatch.setattr(routing, 'TourOptimizer', tour_cls)
    monkeypatch.setattr(routing, 'get_captain_logger', lambda player_id: captain_events)
    monkeypatch.setattr(routing, 'log_captain_event', lambda logger, event_type, **kwargs: captain_events.append((event_type, kwargs)))
    monkeypatch.setattr(routing, 'humanize_duration', lambda delta: '1m')
    monkeypatch.setattr(routing, 'get_operator_name', lambda args: 'Operator')

    if ship_cls is None:
        class _Ship:
            def __init__(self, api, ship_symbol):
                self.api = api

            def dock(self):
                return True

        ship_cls = _Ship

    monkeypatch.setattr('spacetraders_bot.core.ship_controller.ShipController', ship_cls)

    if navigator_factory is None:
        class _Navigator:
            def __init__(self, api, system):
                self.calls = []

            def execute_route(self, ship, destination, prefer_cruise=True, operation_controller=None):
                self.calls.append(destination)
                return True

        navigator_factory = lambda api, system: _Navigator(api, system)

    monkeypatch.setattr('spacetraders_bot.core.smart_navigator.SmartNavigator', navigator_factory)

    if database_factory is None:
        class _DB:
            def transaction(self):
                class _Tx:
                    def __enter__(self_inner):
                        return self_inner

                    def __exit__(self_inner, exc_type, exc, tb):
                        return False

                return _Tx()

            def update_market_data(self, *args, **kwargs):
                pass

        database_factory = lambda: _DB()

    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', database_factory)
    monkeypatch.setattr('spacetraders_bot.core.utils.timestamp_iso', lambda: '2025-01-01T00:00:00Z')

    return captain_events
    return captain_events


def regression_scout_markets_operation_graph_build_failure(monkeypatch):
    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return None

        def build_system_graph(self, system):
            return None

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'X1-TEST-M1'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            raise AssertionError('tour should not run')

        @staticmethod
        def get_markets_from_graph(graph):
            return []

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

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

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain_events)


def regression_scout_markets_operation_missing_ship(monkeypatch):
    graph = {'waypoints': {'A': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def get_ship(self, ship_symbol):
            return None

    class TourStub:
        def __init__(self, graph, ship_data):
            raise AssertionError('tour should not run')

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

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

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain_events)


def regression_scout_markets_operation_no_markets(monkeypatch):
    graph = {'waypoints': {}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'A'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            raise AssertionError('tour should not run')

        @staticmethod
        def get_markets_from_graph(graph):
            return []

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

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

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain_events)


def regression_scout_markets_operation_only_current_location(monkeypatch):
    graph = {'waypoints': {'X1-TEST-M1': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'X1-TEST-M1'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            raise AssertionError('tour should not run')

        @staticmethod
        def get_markets_from_graph(graph):
            return list(graph['waypoints'].keys())

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

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


def regression_scout_markets_operation_unknown_algorithm(monkeypatch):
    graph = {'waypoints': {'A': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'A'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            self.graph = graph
            self.ship_data = ship_data

        @staticmethod
        def get_markets_from_graph(graph):
            return ['A', 'B']

        def plan_tour(self, *args, **kwargs):
            raise AssertionError('plan_tour should not run')

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        algorithm='unsupported',
        continuous=False,
        output=None,
        log_level='INFO',
        return_to_start=False,
    )

    result = routing.scout_markets_operation(args)

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain_events)


def regression_scout_markets_operation_tour_failure(monkeypatch):
    graph = {'waypoints': {'A': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'A'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return ['A', 'B']

        def plan_tour(self, *args, **kwargs):
            return None

    captain_events = _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourStub)

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

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain_events)


def regression_scout_markets_operation_writes_output(monkeypatch, tmp_path):
    captain_events = []

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
            self.market_calls = 0

        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'X1-TEST-M1'}, 'fuel': {'current': 100, 'capacity': 100}}

        def get_market(self, system, waypoint):
            self.market_calls += 1
            return None

    class TourFactory:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return list(graph['waypoints'].keys())

        def plan_tour(self, *args, **kwargs):
            return {
                'total_time': 60,
                'total_legs': 1,
                'final_fuel': 90,
                'legs': [{'goal': 'X1-TEST-M2', 'total_time': 60, 'steps': []}],
            }

    _patch_scout_common(monkeypatch, APIStub(), BuilderStub, TourFactory, captain_events)

    output_path = tmp_path / 'tour.json'

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        algorithm='greedy',
        continuous=False,
        output=str(output_path),
        log_level='INFO',
        return_to_start=False,
    )

    result = routing.scout_markets_operation(args)

    assert result == 0
    assert output_path.exists()

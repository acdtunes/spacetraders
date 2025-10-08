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

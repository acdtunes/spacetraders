from pathlib import Path
from types import SimpleNamespace

from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations import routing

scenarios('../../features/navigation/routing_operations.feature')


# --- Helper doubles ---------------------------------------------------------


class GraphBuilderSuccess:
    def __init__(self, api):
        self.api = api
        self.called = False

    def build_system_graph(self, system):
        self.called = True
        return {'waypoints': {}, 'edges': []}

    def load_system_graph(self, system):
        return {'waypoints': {}, 'edges': []}


class GraphBuilderFail:
    def __init__(self, api):
        self.called = False

    def build_system_graph(self, system):
        self.called = True
        return None

    def load_system_graph(self, system):
        return None


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

    def get_system_graph(self, _conn, _system):
        return self.graph


class FakeAPI:
    def __init__(self, ship_payload, market_payload=None):
        self.ship_payload = ship_payload
        self.market_payload = market_payload
        self.market_calls = []

    def get_ship(self, ship_symbol):
        return self.ship_payload

    def get_market(self, system, waypoint):
        self.market_calls.append(waypoint)
        return self.market_payload


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


class RefuelRouteOptimizer(FakeRouteOptimizer):
    def __init__(self, graph, ship_data, capture):
        super().__init__(graph, ship_data)
        self.capture = capture

    def find_optimal_route(self, start, goal, current_fuel, prefer_cruise=True):
        route = {
            'total_time': 180,
            'final_fuel': 70,
            'steps': [
                {'action': 'refuel', 'waypoint': start, 'fuel_added': 30},
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
        self.capture['route'] = route
        return route


class NullRouteOptimizer(FakeRouteOptimizer):
    def find_optimal_route(self, *args, **kwargs):
        return None


class LoggerStub:
    def __init__(self):
        self.messages = []

    def info(self, message):
        self.messages.append(('info', message))

    def warning(self, message):
        self.messages.append(('warning', message))

    def error(self, message):
        self.messages.append(('error', message))

    def addHandler(self, handler):
        pass

    def removeHandler(self, handler):
        pass


def _patch_route_logging(monkeypatch, tmp_path):
    monkeypatch.setattr(routing, 'setup_logging', lambda *a, **k: tmp_path / 'routing.log')
    monkeypatch.setattr(routing.TimeCalculator, 'format_time', lambda seconds: f"{int(seconds/60)}m")


def _patch_scout_logging(monkeypatch, tmp_path, events):
    monkeypatch.setattr(routing, 'setup_logging', lambda *a, **k: tmp_path / 'scout.log')
    monkeypatch.setattr(routing.logging, 'getLogger', lambda name=None: LoggerStub())
    monkeypatch.setattr(routing, 'get_captain_logger', lambda player_id: events)
    monkeypatch.setattr(routing, 'log_captain_event', lambda logger, event_type, **kwargs: events.append((event_type, kwargs)))
    monkeypatch.setattr(routing, 'humanize_duration', lambda delta: '1m')
    monkeypatch.setattr(routing, 'get_operator_name', lambda args: 'Operator')


# --- Graph build scenarios ----------------------------------------------------


@given('a routing context with a graph builder that succeeds', target_fixture='routing_ctx')
def given_routing_builder_success(monkeypatch, tmp_path):
    _patch_route_logging(monkeypatch, tmp_path)
    api = object()
    builder = GraphBuilderSuccess(api)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api)
    monkeypatch.setattr(routing, 'GraphBuilder', lambda client: builder)
    return {'builder': builder}


@given('a routing context with a graph builder that fails', target_fixture='routing_ctx')
def given_routing_builder_failure(monkeypatch, tmp_path):
    _patch_route_logging(monkeypatch, tmp_path)
    api = object()
    builder = GraphBuilderFail(api)
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api)
    monkeypatch.setattr(routing, 'GraphBuilder', lambda client: builder)
    return {'builder': builder}


@when(parsers.parse('the graph build operation runs for system "{system}"'))
def when_graph_build(routing_ctx, system):
    args = SimpleNamespace(player_id=1, system=system, log_level='INFO')
    routing_ctx['result'] = routing.graph_build_operation(args)


@then('the graph build operation succeeds')
def then_graph_success(routing_ctx):
    assert routing_ctx['result'] == 0


@then('the graph build operation fails')
def then_graph_failure(routing_ctx):
    assert routing_ctx['result'] == 1


# --- Route planning scenarios -------------------------------------------------


@given('a route planning context with an existing graph', target_fixture='route_ctx')
def given_route_plan_context(monkeypatch, tmp_path):
    _patch_route_logging(monkeypatch, tmp_path)
    ship_data = {
        'frame': {'symbol': 'FRAME'},
        'engine': {'symbol': 'ENGINE', 'speed': 10},
        'fuel': {'current': 50, 'capacity': 100},
    }
    api = FakeAPI(ship_data)
    database = FakeDatabase({'waypoints': {}, 'edges': []})
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api)
    monkeypatch.setattr(routing, 'Database', lambda: database)
    monkeypatch.setattr(routing, 'RouteOptimizer', FakeRouteOptimizer)
    return {'api': api, 'database': database, 'monkeypatch': monkeypatch, 'tmp_path': tmp_path}


@given('a route planning context without a graph but with a builder', target_fixture='route_ctx')
def given_route_plan_builds_graph(monkeypatch, tmp_path):
    ctx = given_route_plan_context(monkeypatch, tmp_path)
    builder = GraphBuilderSuccess(ctx['api'])
    ctx['database'].graph = None
    ctx['builder'] = builder
    ctx['monkeypatch'].setattr(routing, 'GraphBuilder', lambda api: builder)
    return ctx


@given('the API returns no ship data', target_fixture='route_ctx')
def given_route_plan_missing_ship(monkeypatch, tmp_path):
    ctx = given_route_plan_context(monkeypatch, tmp_path)
    api = FakeAPI(None)
    ctx['monkeypatch'].setattr(routing, 'get_api_client', lambda player_id: api)
    return ctx


@given('a route planning context with a route optimizer that returns nothing', target_fixture='route_ctx')
def given_route_plan_no_route(monkeypatch, tmp_path):
    ctx = given_route_plan_context(monkeypatch, tmp_path)
    ctx['monkeypatch'].setattr(routing, 'RouteOptimizer', NullRouteOptimizer)
    return ctx


@given('a route planning context with a refuel step route', target_fixture='route_ctx')
def given_route_plan_refuel(monkeypatch, tmp_path):
    ctx = given_route_plan_context(monkeypatch, tmp_path)
    capture = {}
    ctx['capture'] = capture
    ctx['monkeypatch'].setattr(routing, 'RouteOptimizer', lambda graph, ship: RefuelRouteOptimizer(graph, ship, capture))

    def printer(*args, **kwargs):
        ctx.setdefault('prints', []).append(" ".join(str(arg) for arg in args))

    ctx['monkeypatch'].setattr('builtins.print', printer)
    return ctx


@when(parsers.parse('the route plan operation builds a route from "{start}" to "{goal}"'))
def when_route_plan(route_ctx, start, goal):
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        start=start,
        goal=goal,
        drift_only=False,
        output=None,
        log_level='INFO',
    )
    route_ctx['result'] = routing.route_plan_operation(args)


@when('the route plan operation writes the route to a file')
def when_route_plan_writes(route_ctx):
    output_path = route_ctx['tmp_path'] / 'route.json'
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
    routing.route_plan_operation(args)
    route_ctx['output_path'] = output_path


@then('the route plan operation succeeds')
def then_route_success(route_ctx):
    assert route_ctx['result'] == 0


@then('the route file is created')
def then_route_file_created(route_ctx):
    assert route_ctx['output_path'].exists()


@then('the graph builder is invoked')
def then_builder_called(route_ctx):
    assert route_ctx['builder'].called


@then('the route plan operation fails')
def then_route_failure(route_ctx):
    assert route_ctx['result'] == 1


@then('the route output includes a refuel step')
def then_refuel_logged(route_ctx):
    # Refuel route prints include "REFUEL" with the optimizer we installed
    assert any('REFUEL' in line for line in route_ctx.get('prints', []))


# --- Scouting scenarios ------------------------------------------------------


@given('a scouting context with two markets and available tour', target_fixture='scout_ctx')
def given_scout_success(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

    graph = {
        'waypoints': {
            'X1-TEST-M1': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0},
            'X1-TEST-M2': {'traits': ['MARKETPLACE'], 'x': 10, 'y': 0},
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
            return {'nav': {'waypointSymbol': 'X1-TEST-M1'}, 'fuel': {'current': 100, 'capacity': 100}}

        def get_market(self, system, waypoint):
            self.market_calls.append(waypoint)
            return {'tradeGoods': [{'symbol': 'FOOD', 'purchasePrice': 10, 'sellPrice': 20, 'tradeVolume': 5, 'supply': 'ABUNDANT', 'activity': 'STRONG'}]}

    class TourStub:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return list(graph['waypoints'].keys())

        def plan_tour(self, *args, **kwargs):
            return {
                'total_time': 120,
                'total_legs': 2,
                'final_fuel': 90,
                'legs': [
                    {'goal': 'X1-TEST-M1', 'total_time': 60, 'steps': []},
                    {'goal': 'X1-TEST-M2', 'total_time': 60, 'steps': []},
                ],
            }

    class NavigatorStub:
        def __init__(self, api, system):
            self.calls = []

        def execute_route(self, ship, destination):
            self.calls.append(destination)
            return True

    navigator_holder = {}

    def navigator_factory(api, system):
        navigator = NavigatorStub(api, system)
        navigator_holder['navigator'] = navigator
        return navigator

    db_updates = []

    class DBStub:
        def transaction(self):
            class Tx:
                def __enter__(self_inner):
                    return self_inner

                def __exit__(self_inner, exc_type, exc, tb):
                    return False

            return Tx()

        def update_market_data(self, *args, **kwargs):
            db_updates.append(kwargs)

    api_stub = APIStub()
    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: api_stub)
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'TourOptimizer', TourStub)
    monkeypatch.setattr('spacetraders_bot.core.ship_controller.ShipController', lambda api, ship_symbol: SimpleNamespace(dock=lambda: True))
    monkeypatch.setattr('spacetraders_bot.core.smart_navigator.SmartNavigator', navigator_factory)
    monkeypatch.setattr('spacetraders_bot.operations.common.get_database', lambda: DBStub())

    return {
        'events': events,
        'navigator_holder': navigator_holder,
        'db_updates': db_updates,
        'api': api_stub,
        'output_path': tmp_path / 'tour.json',
    }


@given('a scouting context where graph cannot be built', target_fixture='scout_ctx')
def given_scout_graph_fail(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

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

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)

    return {'events': events}


@given('a scouting context with a missing ship record', target_fixture='scout_ctx')
def given_scout_missing_ship(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return {'waypoints': {'A': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class APIStub:
        def get_ship(self, ship_symbol):
            return None

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)

    return {'events': events}


@given('a scouting context with an empty graph', target_fixture='scout_ctx')
def given_scout_empty(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return {'waypoints': {}, 'edges': []}

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'A'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return []

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'TourOptimizer', TourStub)

    return {'events': events}


@given('a scouting context where the ship is at the only market', target_fixture='scout_ctx')
def given_scout_only(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return {'waypoints': {'X1-TEST-M1': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}}, 'edges': []}

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'X1-TEST-M1'}, 'fuel': {'current': 100, 'capacity': 100}}

    class TourStub:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return list(graph['waypoints'].keys())

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'TourOptimizer', TourStub)

    return {'events': events}


@given('a scouting context with two markets and available tour with output', target_fixture='scout_ctx')
def given_scout_output(monkeypatch, tmp_path):
    ctx = given_scout_success(monkeypatch, tmp_path)
    ctx['output_path'] = tmp_path / 'tour.json'
    return ctx


@given('a scouting context where the optimizer returns no tour', target_fixture='scout_ctx')
def given_scout_no_tour(monkeypatch, tmp_path):
    events = []
    _patch_scout_logging(monkeypatch, tmp_path, events)

    graph = {'waypoints': {'A': {'traits': ['MARKETPLACE'], 'x': 0, 'y': 0}, 'B': {'traits': ['MARKETPLACE'], 'x': 10, 'y': 0}}, 'edges': []}

    class BuilderStub:
        def __init__(self, api):
            pass

        def load_system_graph(self, system):
            return graph

    class TourStub:
        def __init__(self, graph, ship_data):
            pass

        @staticmethod
        def get_markets_from_graph(graph):
            return ['A', 'B']

        def plan_tour(self, *args, **kwargs):
            return None

    class APIStub:
        def get_ship(self, ship_symbol):
            return {'nav': {'waypointSymbol': 'A'}, 'fuel': {'current': 100, 'capacity': 100}}

    monkeypatch.setattr(routing, 'get_api_client', lambda player_id: APIStub())
    monkeypatch.setattr(routing, 'GraphBuilder', BuilderStub)
    monkeypatch.setattr(routing, 'TourOptimizer', TourStub)

    return {'events': events}


@when('the scout markets operation runs once')
@when('the scout markets operation runs once')
def when_scout_once(scout_ctx):
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
    scout_ctx['result'] = routing.scout_markets_operation(args)


@when(parsers.parse('the scout markets operation runs with algorithm "{name}"'))
def when_scout_algorithm(scout_ctx, name):
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        system='X1-TEST',
        algorithm=name,
        continuous=False,
        output=None,
        log_level='INFO',
        return_to_start=False,
    )
    scout_ctx['result'] = routing.scout_markets_operation(args)


@when('the scout markets operation writes the tour to a file')
def when_scout_writes(scout_ctx):
    output_path = scout_ctx['output_path']
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
    scout_ctx['result'] = routing.scout_markets_operation(args)


@then('the markets are visited and logged')
def then_scout_completed(scout_ctx):
    assert scout_ctx['result'] == 0
    navigator = scout_ctx['navigator_holder']['navigator']
    assert navigator.calls == ['X1-TEST-M1', 'X1-TEST-M2']
    assert any(event[0] == 'OPERATION_COMPLETED' for event in scout_ctx['events'])
    assert scout_ctx['db_updates']


@then('the scout markets operation fails')
def then_scout_failed(scout_ctx):
    assert scout_ctx['result'] == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in scout_ctx.get('events', []))


@then('the scout markets operation exits without tours')
def then_scout_single_market(scout_ctx):
    assert scout_ctx['result'] == 0


@then('the tour file is created')
def then_scout_output_file(scout_ctx):
    assert scout_ctx['output_path'].exists()

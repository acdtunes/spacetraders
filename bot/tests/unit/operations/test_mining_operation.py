from collections import deque
from types import SimpleNamespace

import pytest

import spacetraders_bot.operations.mining as mining


class _CaptainLogger:
    def __init__(self):
        self.events = []


def _patch_mining_helpers(monkeypatch, captain):
    monkeypatch.setattr(mining, 'setup_logging', lambda *args, **kwargs: 'logfile')
    monkeypatch.setattr(mining, 'get_captain_logger', lambda *_args, **_kwargs: captain)
    monkeypatch.setattr(mining, 'log_captain_event', lambda logger, entry_type, **kwargs: logger.events.append((entry_type, kwargs)) if logger else None)
    monkeypatch.setattr(mining, 'humanize_duration', lambda delta: '1m')
    monkeypatch.setattr(mining, 'get_operator_name', lambda args: 'OPERATOR')


def regression_mining_operation_fails_without_ship_status(monkeypatch):
    captain = _CaptainLogger()
    _patch_mining_helpers(monkeypatch, captain)

    class ShipStub:
        def __init__(self, api, symbol):
            self.api = api
            self.symbol = symbol

        def get_status(self):
            return None

    monkeypatch.setattr(mining, 'get_api_client', lambda player_id: object())
    monkeypatch.setattr(mining, 'ShipController', ShipStub)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        asteroid='AST-1',
        market='MRK-1',
        cycles=1,
        log_level='INFO',
    )

    result = mining.mining_operation(args)

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain.events)


def regression_mining_operation_route_validation_failure(monkeypatch):
    captain = _CaptainLogger()
    _patch_mining_helpers(monkeypatch, captain)

    class ShipStub:
        def __init__(self, api, symbol):
            self.api = api
            self.symbol = symbol

        def get_status(self):
            return {
                'nav': {'systemSymbol': 'X1-TEST'},
                'fuel': {'capacity': 100, 'current': 80},
                'cargo': {'inventory': [], 'capacity': 20, 'units': 0},
            }

    class NavigatorStub:
        def __init__(self, api, system):
            self.api = api
            self.system = system

        def validate_route(self, ship_data, destination):
            return False, 'No path'

    class ControllerStub:
        def __init__(self, op_id):
            self.op_id = op_id

        def can_resume(self):
            return False

        def start(self, data):
            return None

        def should_cancel(self):
            return False

        def should_pause(self):
            return False

        def cancel(self):
            return None

        def fail(self, reason):
            return None

        def checkpoint(self, data):
            return None

        def complete(self, data):
            return None

        def cleanup(self):
            return None

    monkeypatch.setattr(mining, 'get_api_client', lambda player_id: object())
    monkeypatch.setattr(mining, 'ShipController', ShipStub)
    monkeypatch.setattr(mining, 'SmartNavigator', NavigatorStub)
    monkeypatch.setattr(mining, 'OperationController', ControllerStub)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        asteroid='AST-1',
        market='MRK-1',
        cycles=1,
        log_level='INFO',
    )

    result = mining.mining_operation(args)

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain.events)


class _TargetShip:
    def __init__(self, target_resource, extractions, initial_cooldown=0):
        self.target_resource = target_resource
        self.extractions = deque(extractions)
        self.cooldown_remaining = initial_cooldown
        self.capacity = 10
        self.units = 0
        self.inventory = []
        self.waits = []
        self.jettisons = []

    def orbit(self):
        return None

    def get_status(self):
        return {'cooldown': {'remainingSeconds': self.cooldown_remaining}}

    def extract(self):
        if not self.extractions:
            return None
        data = self.extractions.popleft()
        symbol = data['symbol']
        if symbol == self.target_resource:
            self.units = self.capacity - 1
            self.inventory = [{'symbol': symbol, 'units': data['units']}]
        else:
            self.inventory = [{'symbol': symbol, 'units': data['units']}]
            self.units = min(self.units + data['units'], self.capacity - 2)
        return data

    def get_cargo(self):
        return {'units': self.units, 'capacity': self.capacity, 'inventory': list(self.inventory)}

    def wait_for_cooldown(self, seconds):
        self.waits.append(seconds)
        self.cooldown_remaining = max(0, self.cooldown_remaining - seconds)

    def jettison_wrong_cargo(self, target_resource, cargo_threshold=0.8):
        self.jettisons.append((target_resource, cargo_threshold))
        self.units = 0
        self.inventory = []


def regression_targeted_mining_success(monkeypatch):
    ship = _TargetShip(
        target_resource='ALUMINUM_ORE',
        extractions=[
            {'symbol': 'NICKEL_ORE', 'units': 2, 'cooldown': 1},
            {'symbol': 'ALUMINUM_ORE', 'units': 5, 'cooldown': 0},
        ],
        initial_cooldown=1,
    )

    navigator = SimpleNamespace(execute_route=lambda *args, **kwargs: True)

    success, units, reason = mining.targeted_mining_with_circuit_breaker(
        ship,
        navigator,
        asteroid='AST-1',
        target_resource='ALUMINUM_ORE',
        units_needed=5,
        max_consecutive_failures=5,
    )

    assert success is True
    assert units == 5
    assert reason == 'Success'
    assert ship.waits and ship.waits[0] == 1
    assert ship.jettisons  # wrong cargo was jettisoned once


def regression_targeted_mining_circuit_breaker_triggers():
    class FailingShip:
        def __init__(self):
            self.calls = 0
            self.capacity = 10

        def orbit(self):
            return None

        def get_status(self):
            return {'cooldown': {'remainingSeconds': 0}}

        def extract(self):
            self.calls += 1
            return {'symbol': 'WRONG', 'units': 1, 'cooldown': 0}

        def get_cargo(self):
            return {'units': 0, 'capacity': self.capacity, 'inventory': []}

        def wait_for_cooldown(self, seconds):
            return None

        def jettison_wrong_cargo(self, target_resource, cargo_threshold=0.8):
            return None

    ship = FailingShip()
    navigator = SimpleNamespace(execute_route=lambda *args, **kwargs: True)

    success, units, reason = mining.targeted_mining_with_circuit_breaker(
        ship,
        navigator,
        asteroid='AST-1',
        target_resource='ALUMINUM_ORE',
        units_needed=3,
        max_consecutive_failures=3,
    )

    assert success is False
    assert units == 0
    assert 'Circuit breaker' in reason


def regression_targeted_mining_navigation_failure():
    ship = _TargetShip('ALUMINUM_ORE', [])
    navigator = SimpleNamespace(execute_route=lambda *args, **kwargs: False)

    success, units, reason = mining.targeted_mining_with_circuit_breaker(
        ship,
        navigator,
        asteroid='AST-1',
        target_resource='ALUMINUM_ORE',
        units_needed=1,
    )

    assert success is False
    assert units == 0
    assert reason == 'Navigation to asteroid failed'


def regression_mining_operation_success_path(monkeypatch):
    captain = _CaptainLogger()
    _patch_mining_helpers(monkeypatch, captain)

    class NavigatorStub:
        def __init__(self):
            self.calls = []

        def validate_route(self, ship_data, destination):
            return True, 'route valid'

        def get_fuel_estimate(self, ship_data, destination):
            return {'total_fuel_cost': 10, 'refuel_stops': 0}

        def execute_route(self, ship, destination, prefer_cruise=True, operation_controller=None):
            self.calls.append(destination)
            return True

    class ShipStub:
        def __init__(self, api, symbol):
            self.symbol = symbol
            self.api = api
            self.cargo_units = 0
            self.docked = False
            self.refueled = False
            self.orbited = False
            self.extract_calls = 0

        def get_status(self):
            return {
                'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': 'X1-TEST-A1'},
                'fuel': {'capacity': 100, 'current': 80},
                'cargo': {
                    'capacity': 10,
                    'units': self.cargo_units,
                    'inventory': [{'symbol': 'ORE', 'units': self.cargo_units}] if self.cargo_units else [],
                },
                'cooldown': {'remainingSeconds': 0},
            }

        def orbit(self):
            self.orbited = True

        def get_cargo(self):
            return {
                'capacity': 10,
                'units': self.cargo_units,
                'inventory': [{'symbol': 'ORE', 'units': self.cargo_units}] if self.cargo_units else [],
            }

        def wait_for_cooldown(self, seconds):
            pass

        def extract(self):
            if self.extract_calls == 0:
                self.extract_calls += 1
                self.cargo_units = 9
                return {'symbol': 'ORE', 'units': 9, 'cooldown': 0}
            return None

        def dock(self):
            self.docked = True
            return True

        def sell_all(self):
            revenue = self.cargo_units * 100
            self.cargo_units = 0
            return revenue

        def refuel(self):
            self.refueled = True

    class ControllerStub:
        def __init__(self, op_id):
            self.op_id = op_id
            self.started = False
            self.checkpoints = []
            self.completed = None
            self.cleaned = False

        def can_resume(self):
            return False

        def start(self, data):
            self.started = True

        def should_cancel(self):
            return False

        def should_pause(self):
            return False

        def cancel(self):
            pass

        def fail(self, reason):
            raise AssertionError('Should not fail in success path')

        def checkpoint(self, data):
            self.checkpoints.append(data)

        def complete(self, data):
            self.completed = data

        def cleanup(self):
            self.cleaned = True

    navigator = NavigatorStub()
    controller = ControllerStub('op')

    monkeypatch.setattr(mining, 'get_api_client', lambda player_id: object())
    monkeypatch.setattr(mining, 'ShipController', ShipStub)
    monkeypatch.setattr(mining, 'SmartNavigator', lambda api, system: navigator)
    monkeypatch.setattr(mining, 'OperationController', lambda op_id: controller)

    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        asteroid='AST-1',
        market='MRK-1',
        cycles=1,
        log_level='INFO',
    )

    result = mining.mining_operation(args)

    assert result == 0
    assert navigator.calls == ['AST-1', 'MRK-1']
    assert controller.completed == {'cycles': 1, 'revenue': 900}
    assert controller.cleaned is True
    assert any(event[0] == 'OPERATION_COMPLETED' for event in captain.events)


def regression_find_alternative_asteroids(monkeypatch):
    class APIStub:
        def __init__(self):
            self.pages = {
                1: {
                    'data': [
                        {
                            'symbol': 'SYS-A1',
                            'type': 'ASTEROID',
                            'traits': [{'symbol': 'COMMON_METAL_DEPOSITS'}],
                        },
                        {
                            'symbol': 'SYS-A2',
                            'type': 'PLANET',
                            'traits': [],
                        },
                    ],
                    'meta': {'total': 2}
                },
                2: {
                    'data': [
                        {
                            'symbol': 'SYS-A3',
                            'type': 'ASTEROID',
                            'traits': [{'symbol': 'STRIPPED'}],
                        },
                        {
                            'symbol': 'SYS-A4',
                            'type': 'ASTEROID',
                            'traits': [{'symbol': 'MINERAL_DEPOSITS'}],
                        },
                    ],
                    'meta': {'total': 2}
                }
            }

        def list_waypoints(self, system, limit, page):
            return self.pages.get(page, {'data': []})

    api = APIStub()
    alternatives = mining.find_alternative_asteroids(api, 'SYS', 'SYS-A0', 'ALUMINUM_ORE')

    assert 'SYS-A1' in alternatives
    assert 'SYS-A4' in alternatives
    assert 'SYS-A3' not in alternatives  # stripped

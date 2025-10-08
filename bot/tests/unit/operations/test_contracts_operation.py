from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import contracts


class FakeCursor:
    def __init__(self, result):
        self.result = result

    def fetchone(self):
        return self.result


class FakeDB:
    def __init__(self, fetch_fn=lambda symbol, pattern: None):
        self.fetch_fn = fetch_fn

    class _Conn:
        def __init__(self, outer):
            self.outer = outer

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def execute(self, query, params):
            symbol = params[0]
            pattern = params[1] if len(params) > 1 else params[0]
            result = self.outer.fetch_fn(symbol, pattern)
            return FakeCursor(result)

    def connection(self):
        return self._Conn(self)


class FakeShip:
    def __init__(self, location, cargo_units):
        self.ship_symbol = "SHIP-1"
        self.location = location
        self.cargo_units = cargo_units
        self.api = SimpleNamespace(request=lambda method, path: None)

    def get_status(self):
        inventory = []
        if self.cargo_units:
            inventory.append({"symbol": "IRON_ORE", "units": self.cargo_units})
        return {
            "nav": {"systemSymbol": "X1-TEST", "waypointSymbol": self.location},
            "cargo": {
                "inventory": inventory,
                "capacity": 20,
                "units": self.cargo_units,
            },
            "fuel": {"current": 100, "capacity": 100},
        }

    def dock(self):
        return True

    def buy(self, symbol, units):
        self.cargo_units += units
        return {"units": units, "totalPrice": units * 100}


class FakeNavigator:
    def __init__(self):
        self.calls = []

    def execute_route(self, ship, destination, prefer_cruise=True):
        self.calls.append(destination)
        ship.location = destination
        return True


class FakeContractAPI:
    def __init__(self, contract, ship):
        self.contract = contract
        self.ship = ship
        self.delivered_units = []
        self.accept_calls = 0

    def get_contract(self, contract_id):
        assert contract_id == self.contract["id"]
        return self.contract

    def post(self, path, payload=None):
        if path.endswith("/deliver"):
            units = payload["units"]
            self.delivered_units.append(units)
            self.ship.cargo_units -= units
            return {"data": {"delivered": units}}
        if path.endswith("/fulfill"):
            return {"data": {"contract": self.contract}}
        if path.endswith("/accept"):
            self.accept_calls += 1
            self.contract["accepted"] = True
            return {"data": {"contract": self.contract}}
        raise AssertionError(f"Unexpected POST path: {path}")


class NullCaptain:
    def __init__(self):
        self.events = []

    def log_entry(self, entry_type, **kwargs):
        self.events.append((entry_type, kwargs))


class CursorStub:
    def __init__(self, row):
        self._row = row

    def fetchone(self):
        return self._row


class ConnStub:
    def __init__(self, mapping):
        self.mapping = mapping

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def execute(self, query, params):
        key = tuple(params)
        if len(params) == 2 and params[1].endswith('%'):
            key = (params[0], params[1])
        return CursorStub(self.mapping.get(key))


class DBHelperStub:
    def __init__(self, mapping):
        self.mapping = mapping

    def connection(self):
        return ConnStub(self.mapping)


class PurchaseShip:
    def __init__(self, location="X1-TEST-A1", capacity=60):
        self.ship_symbol = "SHIP-1"
        self.location = location
        self.cargo_units = 0
        self.capacity = capacity
        self._failed_once = False
        self.buy_calls = []
        self.api = SimpleNamespace(request=lambda method, path: {"data": "ok"})
        self._initial_attempt = True

    def get_status(self):
        inventory = []
        if self.cargo_units:
            inventory.append({"symbol": "IRON_ORE", "units": self.cargo_units})
        return {
            "nav": {"systemSymbol": "X1-TEST", "waypointSymbol": self.location},
            "cargo": {
                "inventory": inventory,
                "capacity": self.capacity,
                "units": self.cargo_units,
            },
            "fuel": {"current": 100, "capacity": 100},
        }

    def dock(self):
        return True

    def buy(self, symbol, units):
        self.buy_calls.append(units)
        if self._initial_attempt:
            self._initial_attempt = False
            return {"units": 0, "totalPrice": 0}
        if units > 20 and not self._failed_once:
            self._failed_once = True
            return None
        self.cargo_units += units
        return {"units": units, "totalPrice": units * 90}


class DockFailShip(PurchaseShip):
    def __init__(self, fail_on_second=True):
        super().__init__()
        self._dock_calls = 0
        self._fail_on_second = fail_on_second

    def dock(self):
        self._dock_calls += 1
        if self._fail_on_second and self._dock_calls >= 2:
            return False
        return True


def patch_contract_helpers(monkeypatch, captain):
    monkeypatch.setattr(contracts, "setup_logging", lambda *a, **k: "logfile.log")
    monkeypatch.setattr(contracts, "get_captain_logger", lambda *_: captain)
    monkeypatch.setattr(contracts, "log_captain_event", lambda writer, entry_type, **kwargs: writer.log_entry(entry_type, **kwargs) if writer else None)
    monkeypatch.setattr(contracts, "humanize_duration", lambda delta: "1m")
    monkeypatch.setattr(contracts, "get_operator_name", lambda args: "OPERATOR")


def make_contract(accepted=True):
    return {
        "id": "CONTRACT-1",
        "accepted": accepted,
        "terms": {
            "deliver": [
                {
                    "tradeSymbol": "IRON_ORE",
                    "unitsRequired": 10,
                    "unitsFulfilled": 0,
                    "destinationSymbol": "X1-TEST-B1",
                }
            ],
            "payment": {"onAccepted": 1000, "onFulfilled": 5000},
            "deadline": "2025-10-08T00:00:00Z",
        },
    }


def test_contract_operation_delivers_existing_cargo(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    ship = FakeShip(location="X1-TEST-A1", cargo_units=10)
    api = FakeContractAPI(make_contract(), ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(player_id=1, ship="SHIP-1", contract_id="CONTRACT-1", buy_from=None, log_level="INFO")

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(),
        sleep_fn=lambda _: None,
    )

    assert result == 0
    assert api.delivered_units == [10]
    assert navigator.calls == ["X1-TEST-B1"]
    assert any(event[0] == "OPERATION_COMPLETED" for event in captain.events)


def test_contract_operation_resource_unavailable(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    ship = FakeShip(location="X1-TEST-A1", cargo_units=0)
    api = FakeContractAPI(make_contract(), ship)
    navigator = FakeNavigator()
    sleep_calls = []

    db = FakeDB(fetch_fn=lambda symbol, pattern: None)

    args = SimpleNamespace(player_id=1, ship="SHIP-1", contract_id="CONTRACT-1", buy_from=None, log_level="INFO")

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=db,
        sleep_fn=lambda seconds: sleep_calls.append(seconds),
    )

    assert result == 1
    # Should have waited the full retry budget (12 retries at 5 minutes)
    assert len(sleep_calls) == 12
    assert any(event[0] == "CRITICAL_ERROR" for event in captain.events)


def test_contract_operation_accepts_and_handles_transaction_limit(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    contract = make_contract(accepted=False)
    contract["terms"]["deliver"][0]["unitsRequired"] = 30

    ship = PurchaseShip()
    api = FakeContractAPI(contract, ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(
        player_id=1,
        ship="SHIP-1",
        contract_id="CONTRACT-1",
        buy_from=None,
        log_level="INFO",
    )

    def fetch_fn(symbol, pattern):
        if pattern.endswith('%') or pattern == 'X1-TEST-M1':
            return ('X1-TEST-M1', 90, 'ABUNDANT')
        return None

    db = FakeDB(fetch_fn=fetch_fn)

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=db,
        sleep_fn=lambda _: None,
    )

    assert result == 0
    assert api.accept_calls == 1
    assert args.buy_from == 'X1-TEST-M1'
    assert navigator.calls.count('X1-TEST-M1') >= 1
    assert navigator.calls.count("X1-TEST-B1") >= 1
    assert ship.buy_calls and ship.buy_calls[0] > 20
    assert any(event[0] == "OPERATION_COMPLETED" for event in captain.events)


def test_contract_operation_aborts_on_docking_failure(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    ship = DockFailShip()
    api = FakeContractAPI(make_contract(), ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(
        player_id=1,
        ship="SHIP-1",
        contract_id="CONTRACT-1",
        buy_from='X1-TEST-M1',
        log_level="INFO",
    )

    fetch_fn = lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT')

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(fetch_fn=fetch_fn),
        sleep_fn=lambda *_: None,
    )

    assert result == 1
    assert any(event[0] == "CRITICAL_ERROR" for event in captain.events)


def test_contract_operation_retries_delivery_error(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    class RetryAPI(FakeContractAPI):
        def __init__(self, contract, ship):
            super().__init__(contract, ship)
            self.failures = 0

        def post(self, path, payload=None):
            if path.endswith('/deliver'):
                self.failures += 1
                if self.failures == 1:
                    return {'error': {'code': 4502, 'message': 'Try again'}}
                self.delivered_units.append(payload['units'])
                return {'data': {'delivered': payload['units']}}
            return super().post(path, payload)

    contract = make_contract()
    contract['terms']['deliver'][0]['unitsRequired'] = 5

    ship = PurchaseShip()
    ship._initial_attempt = False
    api = RetryAPI(contract, ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id='CONTRACT-1',
        buy_from='X1-TEST-M1',
        log_level='INFO',
    )

    fetch_fn = lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT')

    sleep_calls = []

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(fetch_fn=fetch_fn),
        sleep_fn=lambda seconds: sleep_calls.append(seconds),
    )

    assert result == 0
    assert sleep_calls == [2]
    assert api.delivered_units == [5]
    assert any(event[0] == "OPERATION_COMPLETED" for event in captain.events)


def test_contract_operation_missing_contract(monkeypatch, capsys):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    ship = FakeShip(location="X1-TEST-A1", cargo_units=0)

    class EmptyAPI(FakeContractAPI):
        def get_contract(self, contract_id):
            return None

    api = EmptyAPI(make_contract(), ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(player_id=1, ship="SHIP-1", contract_id="CONTRACT-1", log_level="INFO")

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(),
        sleep_fn=lambda _: None,
    )

    assert result == 1
    assert 'Failed to get contract' in capsys.readouterr().out


def test_contract_operation_already_fulfilled(monkeypatch, capsys):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    fulfilled_contract = make_contract()
    fulfilled_contract['terms']['deliver'][0]['unitsFulfilled'] = fulfilled_contract['terms']['deliver'][0]['unitsRequired']

    ship = FakeShip(location="X1-TEST-A1", cargo_units=0)
    api = FakeContractAPI(fulfilled_contract, ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(player_id=1, ship='SHIP-1', contract_id='CONTRACT-1', buy_from=None, log_level='INFO')

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(),
        sleep_fn=lambda _: None,
    )

    assert result == 0
    assert 'already fulfilled' in capsys.readouterr().out


def test_contract_operation_missing_ship_status(monkeypatch, capsys):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    class ShipMissingStatus(FakeShip):
        def get_status(self):
            return None

    ship = ShipMissingStatus(location='X1-TEST-A1', cargo_units=0)
    api = FakeContractAPI(make_contract(), ship)
    navigator = FakeNavigator()
    args = SimpleNamespace(player_id=1, ship='SHIP-1', contract_id='CONTRACT-1', buy_from=None, log_level='INFO')

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=FakeDB(),
        sleep_fn=lambda _: None,
    )

    assert result == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in captain.events)


def test_contract_operation_full_flow(monkeypatch, capsys):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)

    contract = make_contract()
    contract['terms']['deliver'][0]['unitsRequired'] = 6

    class LiveShip(FakeShip):
        def __init__(self):
            super().__init__(location='X1-TEST-A1', cargo_units=0)
            self.capacity = 5

        def get_status(self):
            return {
                'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': self.location},
                'cargo': {
                    'capacity': self.capacity,
                    'units': self.cargo_units,
                    'inventory': [{'symbol': 'IRON_ORE', 'units': self.cargo_units}] if self.cargo_units else [],
                },
                'fuel': {'capacity': 100, 'current': 90},
                'cooldown': {'remainingSeconds': 0},
            }

        def get_cargo(self):
            return {
                'capacity': self.capacity,
                'units': self.cargo_units,
                'inventory': [{'symbol': 'IRON_ORE', 'units': self.cargo_units}] if self.cargo_units else [],
            }

        def buy(self, symbol, units):
            self.cargo_units += units
            return {'units': units, 'totalPrice': units * 100}

    class LiveAPI(FakeContractAPI):
        def __init__(self, contract, ship):
            super().__init__(contract, ship)
            self.market_calls = 0

        def get_market(self, system, waypoint):
            self.market_calls += 1
            return {
                'tradeGoods': [{'symbol': 'IRON_ORE', 'sellPrice': 100, 'tradeVolume': 10}]
            }

        def get_agent(self):
            return {'credits': 10_000}

        def post(self, path, payload=None):
            if path.endswith('/deliver'):
                units = payload['units']
                self.ship.cargo_units -= units
                self.delivered_units.append(units)
                return {'data': {'delivered': units}}
            if path.endswith('/fulfill'):
                return {'data': {'contract': self.contract}}
            return super().post(path, payload)

    class RouteNavigator(FakeNavigator):
        def execute_route(self, ship, destination, prefer_cruise=True, operation_controller=None):
            ship.location = destination
            return True

    ship = LiveShip()
    api = LiveAPI(contract, ship)
    navigator = RouteNavigator()

    mapping = {
        ('IRON_ORE', 'X1-TEST%'): ('X1-TEST-M1', 100, 'ABUNDANT'),
        ('IRON_ORE', 'X1-TEST-M1'): ('X1-TEST-M1', 100, 'ABUNDANT'),
    }

    args = SimpleNamespace(player_id=1, ship='SHIP-1', contract_id='CONTRACT-1', buy_from=None, log_level='INFO')

    result = contracts.contract_operation(
        args,
        api=api,
        ship=ship,
        navigator=navigator,
        db=DBHelperStub(mapping),
        sleep_fn=lambda _: None,
    )

    assert result == 0
    assert api.delivered_units == [5, 1]
    assert any(event[0] == 'OPERATION_COMPLETED' for event in captain.events)


def test_fetch_and_find_helpers():
    mapping = {
        ('IRON', 'X1-M1'): ('X1-M1', 100, 'ABUNDANT'),
        ('IRON', 'X1-%'): ('X1-M1', 120, 'COMMON'),
    }
    db = DBHelperStub(mapping)

    row = contracts._fetch_market_listing(db, 'IRON', 'X1-M1')
    assert row == ('X1-M1', 100, 'ABUNDANT')

    low = contracts._find_lowest_price_market(db, 'IRON', 'X1-')
    assert low == ('X1-M1', 120, 'COMMON')

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
            self.contract["accepted"] = True
            return {"data": {"contract": self.contract}}
        raise AssertionError(f"Unexpected POST path: {path}")


class NullCaptain:
    def __init__(self):
        self.events = []

    def log_entry(self, entry_type, **kwargs):
        self.events.append((entry_type, kwargs))


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

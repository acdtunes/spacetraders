from types import SimpleNamespace

import spacetraders_bot.core.ship_controller as ship_module


class APIStub:
    def __init__(self, ship_payload):
        self.ship_payload = ship_payload
        self.post_calls = []
        self.patch_calls = []

    def get_ship(self, ship_symbol):
        return self.ship_payload

    def post(self, path, payload=None):
        self.post_calls.append((path, payload))
        if path.endswith('/refuel'):
            return {
                "data": {
                    "transaction": {"totalPrice": 500},
                    "fuel": {"current": 60, "capacity": 100, "consumed": {"amount": 0}},
                }
            }
        if path.endswith('/navigate'):
            return {
                "data": {
                    "nav": {"route": {"arrival": "2025-01-01T00:00:00Z"}},
                    "fuel": {"consumed": {"amount": 1}, "current": 90, "capacity": 100},
                }
            }
        return {"data": {}}

    def patch(self, path, payload=None):
        self.patch_calls.append((path, payload))
        return {"data": {}}

    def get_waypoint(self, system, waypoint):
        return {"symbol": waypoint, "x": 0, "y": 0}


def make_controller(ship_payload):
    api = APIStub(ship_payload)
    controller = ship_module.ShipController(api, "SHIP-1")
    return controller, api


def test_dock_waits_when_in_transit(monkeypatch):
    controller, api = make_controller(
        {
            "nav": {
                "status": "IN_TRANSIT",
                "waypointSymbol": "ORIGIN",
                "route": {
                    "arrival": "2025-01-01T00:00:00Z",
                    "destination": {"symbol": "X1-TEST-B"},
                },
            },
        }
    )

    monkeypatch.setattr(ship_module, "calculate_arrival_wait_time", lambda arrival: 0)
    monkeypatch.setattr(controller, "_wait_for_arrival", lambda seconds: None)

    result = controller.dock()

    assert result is True
    assert api.post_calls[0][0].endswith("/dock")


def test_refuel_skips_when_full(monkeypatch):
    controller, api = make_controller(
        {
            "nav": {"status": "DOCKED"},
            "fuel": {"current": 100, "capacity": 100},
        }
    )

    result = controller.refuel()

    assert result is True
    assert api.post_calls == []


def test_refuel_requires_dock_and_calls_api(monkeypatch):
    controller, api = make_controller(
        {
            "nav": {"status": "IN_ORBIT"},
            "fuel": {"current": 10, "capacity": 100},
        }
    )

    monkeypatch.setattr(controller, "dock", lambda: True)
    api.post_calls.clear()

    result = controller.refuel(units=50)

    assert result is True
    assert api.post_calls[-1][0].endswith("/refuel")
    assert api.post_calls[-1][1] == {"units": 50}


def test_navigate_handles_in_transit_same_destination(monkeypatch):
    controller, api = make_controller(
        {
            "nav": {
                "status": "IN_TRANSIT",
                "waypointSymbol": "ORIGIN",
                "route": {
                    "destination": {"symbol": "TARGET"},
                    "arrival": "2025-01-01T00:00:00Z",
                },
            },
        }
    )

    monkeypatch.setattr(ship_module, "calculate_arrival_wait_time", lambda *_: 0)
    called = []
    monkeypatch.setattr(controller, "_wait_for_arrival", lambda seconds: called.append(seconds))

    result = controller.navigate("TARGET", auto_refuel=False)

    assert result is True
    assert called and called[0] == 3


def test_navigate_performs_full_sequence(monkeypatch):
    controller, api = make_controller(
        {
            "nav": {"status": "DOCKED", "waypointSymbol": "START"},
            "fuel": {"current": 10, "capacity": 100},
        }
    )

    monkeypatch.setattr(ship_module, "parse_waypoint_symbol", lambda symbol: ("SYS", symbol))
    monkeypatch.setattr(ship_module, "calculate_distance", lambda *_: 20)
    monkeypatch.setattr(ship_module, "select_flight_mode", lambda *args, **kwargs: "CRUISE")
    monkeypatch.setattr(ship_module, "estimate_fuel_cost", lambda *_: 5)
    monkeypatch.setattr(ship_module, "calculate_arrival_wait_time", lambda *_: 0)
    monkeypatch.setattr(controller, "_wait_for_arrival", lambda seconds: None)
    monkeypatch.setattr(controller, "refuel", lambda: True)
    monkeypatch.setattr(controller, "orbit", lambda: True)

    result = controller.navigate("TARGET")

    assert result is True
    assert api.patch_calls[-1][1] == {"flightMode": "CRUISE"}
    assert any(call[0].endswith("/navigate") for call in api.post_calls)


def test_wait_for_cooldown_logs(monkeypatch):
    controller, _ = make_controller({"nav": {"status": "DOCKED"}, "fuel": {"current": 0, "capacity": 0}})
    calls = []
    monkeypatch.setattr(ship_module.time, "sleep", lambda seconds: calls.append(seconds))

    controller.wait_for_cooldown(3)

    assert calls

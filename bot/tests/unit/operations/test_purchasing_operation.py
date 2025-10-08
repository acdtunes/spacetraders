from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import purchasing


class FakePurchaseShip:
    def __init__(self, location="X1-TEST-A1"):
        self.ship_symbol = "PURCHASER"
        self.location = location

    def get_status(self):
        return {
            "nav": {"waypointSymbol": self.location, "systemSymbol": "X1-TEST"},
            "cargo": {"capacity": 100, "units": 0},
            "fuel": {"capacity": 100, "current": 100},
        }

    def navigate(self, destination):
        self.location = destination
        return True

    def dock(self):
        return True


class FakePurchaseAPI:
    def __init__(self, price=1000, credits=5000):
        self.price = price
        self.credits = credits
        self.purchase_calls = []

    def get(self, path):
        # Shipyard listing
        return {
            "data": {
                "ships": [
                    {
                        "type": "HEAVY_FREIGHTER",
                        "purchasePrice": self.price,
                    }
                ]
            }
        }

    def get_agent(self):
        return {"credits": self.credits}

    def post(self, path, payload):
        if path == "/my/ships":
            self.purchase_calls.append(payload)
            self.credits -= self.price
            index = len(self.purchase_calls)
            return {
                "data": {
                    "ship": {"symbol": f"NEW-SHIP-{index}"},
                    "transaction": {"totalPrice": self.price},
                    "agent": {"credits": self.credits},
                }
            }
        raise AssertionError(f"Unexpected POST path: {path}")


def patch_purchase_helpers(monkeypatch, captain_events):
    monkeypatch.setattr(purchasing, "setup_logging", lambda *a, **k: "logfile.log")
    monkeypatch.setattr(purchasing, "get_captain_logger", lambda *_: captain_events)
    monkeypatch.setattr(purchasing, "log_captain_event", lambda writer, entry_type, **kwargs: writer.append((entry_type, kwargs)))


def test_purchase_ship_operation_success(monkeypatch):
    captain_events = []
    patch_purchase_helpers(monkeypatch, captain_events)

    api = FakePurchaseAPI(price=1000, credits=5000)
    ship = FakePurchaseShip(location="X1-TEST-A1")
    args = SimpleNamespace(
        player_id=1,
        ship="PURCHASER",
        shipyard="X1-TEST-B1",
        ship_type="HEAVY_FREIGHTER",
        max_budget=3000,
        quantity=2,
        log_level="INFO",
    )

    result = purchasing.purchase_ship_operation(args, api=api, ship=ship, captain_logger=captain_events)

    assert result == 0
    assert len(api.purchase_calls) == 2
    assert captain_events  # Captain log entries recorded


@pytest.mark.parametrize(
    "args, message",
    [
        (SimpleNamespace(ship="A", shipyard="B", ship_type="C", max_budget=1000), "player_id"),
        (SimpleNamespace(player_id=1, shipyard="B", ship_type="C", max_budget=1000), "ship"),
        (SimpleNamespace(player_id=1, ship="A", ship_type="C", max_budget=1000), "shipyard"),
    ],
)
def test_purchase_ship_operation_missing_args(monkeypatch, capsys, args, message):
    patch_purchase_helpers(monkeypatch, [])
    result = purchasing.purchase_ship_operation(args)
    assert result == 1
    assert message in capsys.readouterr().out

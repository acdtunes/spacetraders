from types import SimpleNamespace

import pytest

from spacetraders_bot.operations.analysis import utilities_operation


class DummyAPI:
    def __init__(self, fuel_data=None, distance_data=None, mining_pages=None):
        self.fuel_data = fuel_data or {}
        self.distance_data = distance_data or {}
        self.mining_pages = mining_pages or []
        self._page_index = 0

    # Fuel utilities -------------------------------------------------
    def get_ship(self, ship):
        return self.fuel_data.get("ship")

    def get_waypoint(self, system, waypoint):
        if self.distance_data:
            key = (system, waypoint)
            return self.distance_data.get(key)
        return self.fuel_data.get("waypoint", {}).get(waypoint)

    def list_waypoints(self, system, limit=20, traits=None):
        return {
            "data": self.fuel_data.get("marketplaces", [])
        }

    # Mining utilities -----------------------------------------------
    def get(self, path):
        if self._page_index >= len(self.mining_pages):
            return None
        page = self.mining_pages[self._page_index]
        self._page_index += 1
        return page


@pytest.fixture(autouse=True)
def patch_logging(monkeypatch):
    monkeypatch.setattr(
        "spacetraders_bot.operations.analysis.setup_logging",
        lambda *args, **kwargs: "logfile.log",
    )


def run_util(util_type, api, db=None, navigator_cls=None, **kwargs):
    args = SimpleNamespace(player_id=1, util_type=util_type, **kwargs)

    def fake_get_api_client(_):
        return api

    from spacetraders_bot.operations import analysis

    with pytest.MonkeyPatch.context() as mp:
        mp.setattr(analysis, "get_api_client", fake_get_api_client)
        if db is not None:
            mp.setattr(analysis, "get_database", lambda: db)
        if navigator_cls is not None:
            mp.setattr("spacetraders_bot.core.smart_navigator.SmartNavigator", navigator_cls)
        return utilities_operation(args)


def test_utilities_operation_find_fuel_success(capsys):
    api = DummyAPI(
        fuel_data={
            "ship": {
                "nav": {"waypointSymbol": "X1-TEST-A1"}
            },
            "waypoint": {
                "X1-TEST-A1": {"x": 0, "y": 0}
            },
            "marketplaces": [
                {"symbol": "X1-TEST-B1", "type": "PLANET", "x": 10, "y": 0},
                {"symbol": "X1-TEST-C1", "type": "MOON", "x": 0, "y": 20},
            ],
        }
    )

    result = run_util("find-fuel", api, ship="SHIP-1")
    assert result == 0
    out = capsys.readouterr().out
    assert "Nearest fuel stations" in out
    assert "X1-TEST-B1" in out


def test_utilities_operation_distance(capsys):
    api = DummyAPI(
        distance_data={
            ("X1-TEST", "X1-TEST-A1"): {"x": 0, "y": 0},
            ("X1-TEST", "X1-TEST-B2"): {"x": 3, "y": 4},
        }
    )

    result = run_util(
        "distance",
        api,
        waypoint1="X1-TEST-A1",
        waypoint2="X1-TEST-B2",
    )

    assert result == 0
    output = capsys.readouterr().out
    assert "Distance: 5.0" in output


def test_utilities_operation_find_mining(capsys):
    mining_pages = [
        {
            "data": [
                {
                    "symbol": "X1-TEST-ASTER-1",
                    "type": "ASTEROID",
                    "x": 5,
                    "y": 5,
                    "traits": [
                        {"symbol": "COMMON_METAL_DEPOSITS"},
                        {"symbol": "MINERAL_DEPOSITS"},
                    ],
                },
                {
                    "symbol": "X1-TEST-ASTER-2",
                    "type": "ASTEROID",
                    "x": 10,
                    "y": -5,
                    "traits": [
                        {"symbol": "STRIPPED"},
                        {"symbol": "RARE_METAL_DEPOSITS"},
                    ],
                },
                {
                    "symbol": "X1-TEST-MARKET",
                    "type": "PLANET",
                    "x": 8,
                    "y": 4,
                    "traits": [],
                },
            ],
            "meta": {"total": 1},
        }
    ]

    api = DummyAPI(mining_pages=mining_pages)
    api.get_ship = lambda ship: {
        "engine": {"speed": 12},
        "fuel": {"capacity": 100},
        "cargo": {"capacity": 30},
    }

    class FakeResult:
        def __init__(self, rows):
            self._rows = rows

        def fetchall(self):
            return self._rows

    class FakeConn:
        def __init__(self, rows):
            self.rows = rows

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def execute(self, query, params):
            material = params[0]
            key = (material, params[1]) if len(params) > 1 else (material, None)
            return FakeResult(self.rows.get(key, []))

    class FakeDB:
        def __init__(self, rows):
            self.rows = rows

        def connection(self):
            return FakeConn(self.rows)

    def make_row(waypoint, price):
        return {
            "waypoint_symbol": waypoint,
            "sell_price": price,
            "supply": "ABUNDANT",
            "trade_volume": 100,
            "last_updated": "2025-01-01T00:00:00Z",
        }

    rows = {
        ("IRON_ORE", "X1-TEST-%"): [make_row("X1-TEST-MARKET", 250)],
        ("SILICON_CRYSTALS", "X1-TEST-%"): [],
    }

    db = FakeDB(rows)

    class FakeNavigator:
        def __init__(self, api, system):
            self.api = api
            self.system = system

        def plan_route(self, ship, destination, prefer_cruise=True):
            return {
                "total_time": 120,
                "final_fuel": ship['fuel']['capacity'] - 5,
                "steps": [],
            }

    result = run_util(
        "find-mining",
        api,
        db=db,
        navigator_cls=FakeNavigator,
        system="X1-TEST",
        ship="SHIP-1",
    )

    assert result == 0
    out = capsys.readouterr().out
    assert "FIND MINING OPPORTUNITIES" in out
    assert "X1-TEST-ASTER-1" in out
    assert "MINERAL_DEPOSITS" in out


def test_utilities_operation_find_fuel_missing_data(capsys):
    api = DummyAPI(fuel_data={"ship": None})
    result = run_util("find-fuel", api, ship="SHIP-1")
    assert result == 1
    assert "Failed to get ship status" in capsys.readouterr().out


def test_utilities_operation_unknown_util_type(capsys):
    api = DummyAPI()
    result = run_util("unknown", api)
    assert result == 1

from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from spacetraders_bot.operations.multileg_trader import MultiLegTradeOptimizer


class FakeCursor:
    def __init__(self, rows):
        self.rows = rows
        self.current = []

    def execute(self, query, params):
        if "DISTINCT waypoint_symbol" in query:
            prefix = params[0].rstrip('%')
            self.current = [(wp,) for wp in self.rows['markets'] if wp.startswith(prefix)]
        elif "FROM waypoints" in query:
            symbol = params[0]
            coords = self.rows['waypoints'].get(symbol)
            self.current = [coords] if coords else []
        else:
            self.current = []
        return self

    def fetchall(self):
        return self.current

    def fetchone(self):
        return self.current[0] if self.current else None


class FakeDB:
    def __init__(self, markets=None, market_data=None, waypoints=None):
        self.rows = {
            'markets': markets or [],
            'waypoints': waypoints or {},
        }
        self.market_data = market_data or {}

    class _Conn:
        def __init__(self, db):
            self.db = db

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        def cursor(self):
            return FakeCursor(self.db.rows)

    def connection(self):
        return self._Conn(self)

    def get_market_data(self, _conn, waypoint, good):
        return self.market_data.get((waypoint, good), [])


def test_get_markets_in_system(monkeypatch):
    db = FakeDB(markets=['SYS-A1', 'SYS-B1', 'OTHER-C1'])
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=db, player_id=1, logger=MagicMock())

    markets = optimizer._get_markets_in_system('SYS')
    assert markets == ['SYS-A1', 'SYS-B1']


def test_get_trade_opportunities(monkeypatch):
    market_data = {
        ('SYS-A', None): [
            {'good_symbol': 'IRON', 'sell_price': 50, 'trade_volume': 10},
        ],
        ('SYS-B', 'IRON'): [
            {'purchase_price': 90},
        ],
    }
    db = FakeDB(markets=['SYS-A', 'SYS-B'], market_data=market_data)
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=db, player_id=1, logger=MagicMock())

    opportunities = optimizer._get_trade_opportunities('SYS', ['SYS-A', 'SYS-B'])

    assert len(opportunities) == 1
    opp = opportunities[0]
    assert opp['good'] == 'IRON'
    assert opp['buy_waypoint'] == 'SYS-A'
    assert opp['sell_waypoint'] == 'SYS-B'
    assert opp['spread'] == 40


def test_find_optimal_route_positive_profit(monkeypatch):
    optimizer = MultiLegTradeOptimizer(api=MagicMock(), db=MagicMock(), player_id=1, logger=MagicMock())

    monkeypatch.setattr(optimizer, '_get_markets_in_system', lambda system: ['A', 'B', 'C'])

    trade_ops = [
        {
            'buy_waypoint': 'B',
            'sell_waypoint': 'C',
            'good': 'COPPER',
            'buy_price': 10,
            'sell_price': 30,
            'spread': 20,
            'trade_volume': 20,
        }
    ]
    monkeypatch.setattr(optimizer, '_get_trade_opportunities', lambda system, markets: trade_ops)
    monkeypatch.setattr(MultiLegTradeOptimizer, '_estimate_distance', lambda self, a, b: 10)

    route = optimizer.find_optimal_route(
        start_waypoint='A',
        system='SYS',
        max_stops=3,
        cargo_capacity=20,
        starting_credits=1000,
        ship_speed=10,
        fuel_capacity=100,
        current_fuel=50,
    )

    assert route is not None
    destinations = [segment.to_waypoint for segment in route.segments]
    assert destinations == ['B', 'C']
    assert route.total_profit > 0
    # First segment should contain a BUY action
    assert any(action.action == 'BUY' for action in route.segments[0].actions_at_destination)

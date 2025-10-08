from unittest.mock import Mock

from spacetraders_bot.operations.contracts import ResourceAcquisitionStrategy


class DBStub:
    class _Cursor:
        def __init__(self, row):
            self.row = row

        def fetchone(self):
            return self.row

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
            self.outer.calls.append((symbol, pattern))

            if "waypoint_symbol =" in query:
                return DBStub._Cursor(self.outer.listing_row)
            return DBStub._Cursor(self.outer.lowest_row)

    def __init__(self, listing_row=None, lowest_row=None):
        self.listing_row = listing_row
        self.lowest_row = lowest_row
        self.calls = []

    def connection(self):
        return DBStub._Conn(self)


def make_strategy(**overrides):
    defaults = dict(
        trade_symbol="IRON",
        system="SYS-X",
        database=DBStub(),
        log_error=Mock(),
        sleep_fn=lambda *_: None,
        print_fn=lambda *_: None,
        max_retries=2,
        retry_interval_seconds=0,
    )
    defaults.update(overrides)
    return ResourceAcquisitionStrategy(**defaults)


def test_strategy_respects_preferred_market():
    db = DBStub(listing_row=("SYS-X-A1", 50, "ABUNDANT"))
    strategy = make_strategy(database=db)
    updated = []

    result = strategy.ensure_availability(
        still_needed=20,
        cargo_available=30,
        cargo_capacity=60,
        preferred_market="SYS-X-A1",
        update_preferred_market=lambda market: updated.append(market),
    )

    assert result is True
    assert updated == []
    assert db.calls == [("IRON", "SYS-X-A1")]


def test_strategy_discovers_market_and_updates_preference():
    db = DBStub(lowest_row=("SYS-X-B2", 75, "LIMITED"))
    strategy = make_strategy(database=db)
    updated = []

    result = strategy.ensure_availability(
        still_needed=10,
        cargo_available=40,
        cargo_capacity=60,
        preferred_market=None,
        update_preferred_market=lambda market: updated.append(market),
    )

    assert result is True
    assert updated == ["SYS-X-B2"]
    assert db.calls == [("IRON", "SYS-X%")]


def test_strategy_times_out_after_retries():
    db = DBStub()
    log_error = Mock()
    prints = []
    sleep_calls = []

    strategy = make_strategy(
        database=db,
        log_error=log_error,
        print_fn=lambda message: prints.append(message),
        sleep_fn=lambda seconds: sleep_calls.append(seconds),
        max_retries=1,
        retry_interval_seconds=0,
    )

    result = strategy.ensure_availability(
        still_needed=5,
        cargo_available=10,
        cargo_capacity=30,
        preferred_market=None,
        update_preferred_market=lambda *_: None,
    )

    assert result is False
    assert log_error.call_count >= 2
    assert any("OPERATION FAILED" in line for line in prints)
    assert sleep_calls == [0]

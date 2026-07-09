import json
import pandas as pd
import pytest
from sqlalchemy import create_engine, text
from model.extract import extract_ladders, extract_control_series

@pytest.fixture
def engine():
    eng = create_engine("sqlite://")
    with eng.begin() as c:
        c.execute(text("""CREATE TABLE transactions (
            id TEXT, transaction_type TEXT, amount INTEGER,
            timestamp TIMESTAMP, metadata TEXT)"""))
        c.execute(text("""CREATE TABLE market_data (
            waypoint_symbol TEXT, good_symbol TEXT, supply TEXT, activity TEXT,
            trade_volume INTEGER, last_updated TIMESTAMP)"""))
        c.execute(text("""CREATE TABLE market_price_history (
            waypoint_symbol TEXT, good_symbol TEXT, purchase_price INTEGER,
            sell_price INTEGER, trade_volume INTEGER, recorded_at TIMESTAMP)"""))
        def tx(i, typ, amount, ts, good, units, wp):
            c.execute(text("INSERT INTO transactions VALUES (:i,:t,:a,:ts,:m)"),
                dict(i=str(i), t=typ, a=amount, ts=ts,
                     m=json.dumps({"good_symbol": good, "units": units, "waypoint": wp})))
        # 3-step sell ladder at D39: unit prices 1844, 1750, 1562
        tx(1, "SELL_CARGO",  36880, "2026-07-09 21:28:00", "MEDICINE", 20, "X1-NK36-D39")
        tx(2, "SELL_CARGO",  35000, "2026-07-09 21:30:00", "MEDICINE", 20, "X1-NK36-D39")
        tx(3, "SELL_CARGO",  31240, "2026-07-09 21:32:00", "MEDICINE", 20, "X1-NK36-D39")
        # unrelated (different good) and out-of-window (2h later) rows
        tx(4, "SELL_CARGO",  10000, "2026-07-09 21:31:00", "FABRICS", 10, "X1-NK36-D39")
        tx(5, "SELL_CARGO",  30000, "2026-07-09 23:40:00", "MEDICINE", 20, "X1-NK36-D39")
        c.execute(text("""INSERT INTO market_data VALUES
            ('X1-NK36-D39','MEDICINE','LIMITED','WEAK',20,'2026-07-09 21:00:00')"""))
        for i, bid in enumerate([5200, 5210, 5190]):
            c.execute(text("""INSERT INTO market_price_history VALUES
                ('X1-GQ92-A1','MEDICINE',:b,3000,80,:ts)"""),
                dict(b=bid, ts=f"2026-07-09 2{i}:00:00"))
    return eng

def test_ladder_grouping_and_unit_prices(engine):
    df = extract_ladders(engine)
    ladder = df[(df.good == "MEDICINE") & (df.tx_type == "SELL_CARGO")]
    ladder = ladder[ladder.ladder_id == ladder.ladder_id.iloc[0]]
    assert list(ladder.step_idx) == [0, 1, 2]
    assert list(ladder.unit_price) == [1844, 1750, 1562]
    assert ladder.supply.iloc[0] == "LIMITED" and ladder.activity.iloc[0] == "WEAK"

def test_out_of_window_tx_starts_new_ladder(engine):
    df = extract_ladders(engine)
    med = df[(df.good == "MEDICINE")]
    assert med.ladder_id.nunique() == 2  # the 23:40 row is its own ladder

def test_control_series_excludes_traded_pairs(engine):
    ctrl = extract_control_series(engine)
    assert set(ctrl.waypoint.unique()) == {"X1-GQ92-A1"}  # D39/MEDICINE traded → excluded
    assert len(ctrl) == 3

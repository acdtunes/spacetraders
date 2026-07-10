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
            sell_price INTEGER, trade_volume INTEGER, recorded_at TIMESTAMP,
            supply TEXT, activity TEXT)"""))
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
        # sp-pf60: first row carries a captured tier, the other two are NULL
        # (as pre-sp-pf60-style rows would be) so tests can assert both the
        # real-value and COALESCE-to-empty-string paths through extract_control_series.
        tiers = [("MODERATE", "GROWING"), (None, None), (None, None)]
        for i, (bid, (supply, activity)) in enumerate(zip([5200, 5210, 5190], tiers)):
            c.execute(text("""INSERT INTO market_price_history VALUES
                ('X1-GQ92-A1','MEDICINE',:b,3000,80,:ts,:s,:a)"""),
                dict(b=bid, ts=f"2026-07-09 2{i}:00:00", s=supply, a=activity))
    return eng

def test_ladder_grouping_and_unit_prices(engine):
    df = extract_ladders(engine)
    ladder = df[(df.good == "MEDICINE") & (df.tx_type == "SELL_CARGO")]
    ladder = ladder[ladder.ladder_id == ladder.ladder_id.iloc[0]]
    assert list(ladder.step_idx) == [0, 1, 2]
    assert list(ladder.unit_price) == [1844, 1750, 1562]
    # No market_price_history row precedes the D39 ladder → tier falls back to the
    # market_data tier-now snapshot (LIMITED|WEAK) and is flagged tier_at_time=False.
    assert ladder.supply.iloc[0] == "LIMITED" and ladder.activity.iloc[0] == "WEAK"
    assert (~ladder.tier_at_time).all()

def test_out_of_window_tx_starts_new_ladder(engine):
    df = extract_ladders(engine)
    med = df[(df.good == "MEDICINE")]
    assert med.ladder_id.nunique() == 2  # the 23:40 row is its own ladder

def test_control_series_excludes_traded_pairs(engine):
    ctrl = extract_control_series(engine)
    assert set(ctrl.waypoint.unique()) == {"X1-GQ92-A1"}  # D39/MEDICINE traded → excluded
    assert len(ctrl) == 3

def test_control_series_exposes_tier_at_time(engine):
    # sp-pf60: market_price_history now carries supply/activity as captured
    # at observation time, so the model can calibrate against tier-at-time
    # instead of tier-now. Rows without a captured tier (pre-sp-pf60-style)
    # must read back as '' rather than NaN/None, per the COALESCE in the SELECT.
    ctrl = extract_control_series(engine).sort_values("recorded_at").reset_index(drop=True)
    assert list(ctrl.supply) == ["MODERATE", "", ""]
    assert list(ctrl.activity) == ["GROWING", "", ""]


@pytest.fixture
def tier_engine():
    """A traded (waypoint, good) with a tier timeline in market_price_history, so the
    ladder join must resolve each step's tier-at-observation by nearest-before."""
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
            sell_price INTEGER, trade_volume INTEGER, recorded_at TIMESTAMP,
            supply TEXT, activity TEXT)"""))

        def tx(i, amount, ts, units):
            c.execute(text("INSERT INTO transactions VALUES (:i,:t,:a,:ts,:m)"),
                dict(i=str(i), t="SELL_CARGO", a=amount, ts=ts,
                     m=json.dumps({"good_symbol": "FUEL", "units": units,
                                   "waypoint": "X1-AA11-B2"})))
        # Four sells with distinct unit prices, resolvable by nearest-before below.
        tx(1, 5000, "2026-07-09 19:50:00", 10)   # up 500 — before ALL history → fallback
        tx(2, 4900, "2026-07-09 20:15:00", 10)   # up 490 — after t0 only
        tx(3, 4800, "2026-07-09 20:50:00", 10)   # up 480 — after t1; t2 is null-tier (skipped)
        tx(4, 4700, "2026-07-09 21:05:00", 10)   # up 470 — after t3

        # market_data tier-now = the fallback for steps with no preceding tiered history.
        c.execute(text("""INSERT INTO market_data VALUES
            ('X1-AA11-B2','FUEL','MODERATE','RESTRICTED',50,'2026-07-09 21:30:00')"""))

        # history timeline: t2 (20:45) carries NO tier and must be skipped by the join.
        def hist(ts, tv, supply, activity):
            c.execute(text("""INSERT INTO market_price_history VALUES
                ('X1-AA11-B2','FUEL',1,1,:tv,:ts,:s,:a)"""),
                dict(tv=tv, ts=ts, s=supply, a=activity))
        hist("2026-07-09 20:00:00", 100, "ABUNDANT", "STRONG")   # t0
        hist("2026-07-09 20:30:00", 90,  "HIGH",     "GROWING")  # t1
        hist("2026-07-09 20:45:00", 77,  None,       None)       # t2 (null tier)
        hist("2026-07-09 21:00:00", 20,  "LIMITED",  "WEAK")     # t3
    return eng


def test_ladder_nearest_before_picks_latest_preceding_tier(tier_engine):
    df = extract_ladders(tier_engine).set_index("unit_price")
    # up 490 sold at 20:15 → t0 (20:00) is the only preceding tiered row (t1 is later).
    assert df.loc[490, "supply"] == "ABUNDANT" and df.loc[490, "activity"] == "STRONG"
    assert bool(df.loc[490, "tier_at_time"]) is True
    # up 480 sold at 20:50 → latest preceding TIERED row is t1 (20:30); t2 (20:45, null
    # tier) is skipped, proving the non-null filter, not merely "most recent row".
    assert df.loc[480, "supply"] == "HIGH" and df.loc[480, "activity"] == "GROWING"
    assert bool(df.loc[480, "tier_at_time"]) is True
    # up 470 sold at 21:05 → t3 (21:00).
    assert df.loc[470, "supply"] == "LIMITED" and df.loc[470, "activity"] == "WEAK"


def test_ladder_before_all_history_falls_back_to_tier_now(tier_engine):
    df = extract_ladders(tier_engine).set_index("unit_price")
    # up 500 sold at 19:50 precedes every history row → market_data tier-now, flagged.
    assert df.loc[500, "supply"] == "MODERATE" and df.loc[500, "activity"] == "RESTRICTED"
    assert bool(df.loc[500, "tier_at_time"]) is False


def test_ladder_trade_volume_prefers_history_row(tier_engine):
    df = extract_ladders(tier_engine).set_index("unit_price")
    assert df.loc[490, "trade_volume"] == 100   # from t0 (history), not market_data's 50
    assert df.loc[480, "trade_volume"] == 90    # from t1
    assert df.loc[500, "trade_volume"] == 50    # fallback: market_data's, no preceding history

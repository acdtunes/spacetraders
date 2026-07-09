"""Extract impact-ladder events and untouched control series from the bot DB.

Our own transaction sequences are the treatment events that identify the
price-impact curve (spec: Phase 0). A 'ladder' = consecutive transactions of
the same (waypoint, good, tx_type) within LADDER_WINDOW_MIN minutes.
"""
import json
import os
import pandas as pd
from sqlalchemy import create_engine, text

LADDER_WINDOW_MIN = 30

def db_engine():
    host = os.environ.get("ST_DATABASE_HOST", "127.0.0.1")
    port = os.environ.get("ST_DATABASE_PORT", "5432")
    name = os.environ.get("ST_DATABASE_NAME", "spacetraders")
    user = os.environ.get("ST_DATABASE_USER", "spacetraders")
    pw = os.environ.get("ST_DATABASE_PASSWORD", "dev_password")
    return create_engine(f"postgresql://{user}:{pw}@{host}:{port}/{name}")

def _rows(engine):
    q = text("""SELECT transaction_type, amount, timestamp, metadata
                FROM transactions
                WHERE transaction_type IN ('SELL_CARGO','PURCHASE_CARGO')
                ORDER BY timestamp""")
    with engine.connect() as c:
        return c.execute(q).fetchall()

def extract_ladders(engine) -> pd.DataFrame:
    recs = []
    for tx_type, amount, ts, meta in _rows(engine):
        m = json.loads(meta) if isinstance(meta, str) else (meta or {})
        units = m.get("units") or 0
        if not units or not m.get("good_symbol") or not m.get("waypoint"):
            continue
        recs.append(dict(waypoint=m["waypoint"], good=m["good_symbol"],
                         tx_type=tx_type, units=int(units),
                         unit_price=abs(int(amount)) // int(units),
                         ts=pd.Timestamp(ts)))
    df = pd.DataFrame(recs)
    if df.empty:
        return df
    df = df.sort_values(["waypoint", "good", "tx_type", "ts"]).reset_index(drop=True)
    gap = df.groupby(["waypoint", "good", "tx_type"])["ts"].diff()
    new_ladder = gap.isna() | (gap > pd.Timedelta(minutes=LADDER_WINDOW_MIN))
    df["ladder_id"] = new_ladder.cumsum()
    df["step_idx"] = df.groupby("ladder_id").cumcount()
    tiers = pd.read_sql(text("""SELECT waypoint_symbol AS waypoint, good_symbol AS good,
                                supply, activity, trade_volume FROM market_data"""),
                        engine)
    return df.merge(tiers, on=["waypoint", "good"], how="left")

def extract_control_series(engine) -> pd.DataFrame:
    return pd.read_sql(text("""
        SELECT h.waypoint_symbol AS waypoint, h.good_symbol AS good,
               h.sell_price AS ask, h.purchase_price AS bid,
               h.trade_volume, h.recorded_at
        FROM market_price_history h
        WHERE NOT EXISTS (
            SELECT 1 FROM transactions t
            WHERE t.transaction_type IN ('SELL_CARGO','PURCHASE_CARGO')
              AND CAST(t.metadata AS TEXT) LIKE '%%' || h.waypoint_symbol || '%%'
              AND CAST(t.metadata AS TEXT) LIKE '%%' || h.good_symbol || '%%')
        ORDER BY h.waypoint_symbol, h.good_symbol, h.recorded_at"""), engine)

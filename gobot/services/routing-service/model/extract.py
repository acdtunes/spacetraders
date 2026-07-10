"""Extract impact-ladder events and untouched control series from the bot DB.

Our own transaction sequences are the treatment events that identify the
price-impact curve (spec: Phase 0). A 'ladder' = consecutive transactions of
the same (waypoint, good, tx_type) within LADDER_WINDOW_MIN minutes.

Tier tagging (sp-bkjz): each ladder step carries the market tier (supply,
activity) *as it stood at the transaction's timestamp*, via a nearest-before
join to market_price_history — the latest history row for the (waypoint, good)
pair with recorded_at <= the tx time that carries a captured (non-null) tier.
Steps with no preceding tiered history row fall back to the market_data
tier-now snapshot (the pre-sp-bkjz behavior), flagged by tier_at_time=False.
This closes the D39-class labeling gap where a bygone incident's severe sell
steps were mislabeled with today's (post-recovery) tier.
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

def _to_naive_utc(s: pd.Series) -> pd.Series:
    """Coerce a datetime series to tz-naive UTC.

    Postgres TIMESTAMPTZ columns arrive tz-aware while SQLite TEXT timestamps
    arrive tz-naive; merge_asof requires both keys to share a dtype. Parsing as
    UTC then dropping the tz normalizes every source to the same wall clock.
    """
    s = pd.to_datetime(s, errors="coerce", utc=True)
    return s.dt.tz_localize(None)

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

    market_now = pd.read_sql(text("""SELECT waypoint_symbol AS waypoint, good_symbol AS good,
                                     supply, activity, trade_volume FROM market_data"""),
                             engine)
    # Only history rows that actually captured a tier can date-stamp a step's tier.
    history = pd.read_sql(text("""SELECT waypoint_symbol AS waypoint, good_symbol AS good,
                                  supply, activity, trade_volume, recorded_at
                                  FROM market_price_history
                                  WHERE supply IS NOT NULL AND supply <> ''
                                    AND activity IS NOT NULL AND activity <> ''"""),
                          engine)
    return _tag_tiers(df, market_now, history)

def _tag_tiers(df: pd.DataFrame, market_now: pd.DataFrame,
               history: pd.DataFrame) -> pd.DataFrame:
    """Tag each ladder step with its tier-at-observation, falling back to tier-now.

    tier_at_time=True  → tier taken from the latest market_price_history row at or
                         before the step's timestamp (true tier-at-observation).
    tier_at_time=False → no such history row; supply/activity fall back to the
                         market_data snapshot (tier-now), preserving pre-sp-bkjz
                         behavior exactly.
    trade_volume prefers the matched history row's value, else market_data's.
    """
    df = df.copy()
    df["ts"] = _to_naive_utc(df["ts"])

    # --- nearest-before join: each step -> latest preceding tiered history row ---
    if not history.empty:
        h = history.copy()
        h["recorded_at"] = _to_naive_utc(h["recorded_at"])
        h = (h.dropna(subset=["recorded_at"])
              .rename(columns={"supply": "supply_at", "activity": "activity_at",
                               "trade_volume": "tv_at"})
              .sort_values("recorded_at"))
        tagged = pd.merge_asof(
            df.sort_values("ts"),
            h[["waypoint", "good", "recorded_at", "supply_at", "activity_at", "tv_at"]],
            left_on="ts", right_on="recorded_at",
            by=["waypoint", "good"], direction="backward")
    else:
        tagged = df.copy()
        for col in ("supply_at", "activity_at", "tv_at"):
            tagged[col] = pd.NA
        tagged["recorded_at"] = pd.NaT

    # --- tier-now fallback snapshot ---
    now = market_now.rename(columns={"supply": "supply_now", "activity": "activity_now",
                                     "trade_volume": "tv_now"})
    tagged = tagged.merge(now, on=["waypoint", "good"], how="left")

    tagged["tier_at_time"] = tagged["supply_at"].notna() & tagged["activity_at"].notna()
    tagged["supply"] = tagged["supply_at"].where(tagged["tier_at_time"], tagged["supply_now"])
    tagged["activity"] = tagged["activity_at"].where(tagged["tier_at_time"], tagged["activity_now"])
    tagged["trade_volume"] = tagged["tv_at"].where(tagged["tv_at"].notna(), tagged["tv_now"])

    tagged = tagged.drop(columns=[c for c in ("supply_at", "activity_at", "tv_at",
                                              "recorded_at", "supply_now", "activity_now",
                                              "tv_now") if c in tagged.columns])
    return tagged.sort_values(["waypoint", "good", "tx_type", "ts"]).reset_index(drop=True)

def extract_control_series(engine) -> pd.DataFrame:
    return pd.read_sql(text("""
        SELECT h.waypoint_symbol AS waypoint, h.good_symbol AS good,
               h.sell_price AS ask, h.purchase_price AS bid,
               h.trade_volume, h.recorded_at,
               COALESCE(h.supply, '') AS supply,
               COALESCE(h.activity, '') AS activity
        FROM market_price_history h
        WHERE NOT EXISTS (
            SELECT 1 FROM transactions t
            WHERE t.transaction_type IN ('SELL_CARGO','PURCHASE_CARGO')
              AND CAST(t.metadata AS TEXT) LIKE '%%' || h.waypoint_symbol || '%%'
              AND CAST(t.metadata AS TEXT) LIKE '%%' || h.good_symbol || '%%')
        ORDER BY h.waypoint_symbol, h.good_symbol, h.recorded_at"""), engine)

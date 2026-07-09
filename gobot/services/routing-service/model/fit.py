# gobot/services/routing-service/model/fit.py
"""Fit structural market-model parameters (spec Phase 0).

Curve SHAPES per (supply, activity) are structural and transfer across eras;
equilibrium levels always come from the live snapshot, never from this fit.

Impact is fit per (supply, activity) tier as a per-*tranche* decay/growth, where a
tranche is one ``tradeVolume`` worth of units (spec Phase 0: impact is piecewise
"by tranche, units expressed relative to tradeVolume"). Raw ledger steps execute
arbitrary ``units``, so before pooling we normalize each observed consecutive-step
price ratio to its full-tranche equivalent:

    tranche_ratio = clip(raw_ratio, 0.5, 2.0) ** clamp(tradeVolume / units, 0.2, 5.0)

The exponent ``tv/units`` rescales the observed move to one tranche: ``units == tv``
leaves it unchanged (the D39 incident, whose 20u sells hit a tradeVolume-20 market);
``units < tv`` (a small transaction that barely moves price) amplifies the move toward
its true per-tranche impact; ``units > tv`` (a multi-tranche single transaction)
de-amplifies it. The exponent is clamped to [0.2, 5.0] to bound noise amplification
from very small transactions. Without this normalization the tier pool is diluted
toward 1.0 by sub-tranche steps and understates real market depth (the D39 gate miss).
"""
import numpy as np
import pandas as pd

MIN_OBS = 3
EXP_MIN, EXP_MAX = 0.2, 5.0

def fit_impact(ladders: pd.DataFrame) -> dict:
    out = {}
    if ladders.empty:
        return out
    df = ladders.sort_values(["ladder_id", "step_idx"]).copy()
    df["ratio"] = df.groupby("ladder_id")["unit_price"].pct_change() + 1.0
    df = df.dropna(subset=["ratio", "supply", "activity"])
    # Normalize each raw step ratio to a per-tranche basis before pooling (see module
    # docstring). Clip the RAW ratio first, then raise to clamp(tv/units). Missing/zero
    # units or trade_volume fall back to exponent 1.0 (no normalization).
    clipped = df["ratio"].clip(lower=0.5, upper=2.0)
    exponent = (df["trade_volume"].where(df["trade_volume"] > 0)
                / df["units"].where(df["units"] > 0)
                ).clip(lower=EXP_MIN, upper=EXP_MAX).fillna(1.0)
    df["tranche_ratio"] = clipped ** exponent
    for (supply, activity, tx_type), g in df.groupby(["supply", "activity", "tx_type"]):
        key = f"{supply}|{activity}"
        ratios = g["tranche_ratio"]
        if len(ratios) < MIN_OBS:
            continue
        geo = float(np.exp(np.log(ratios).mean()))
        entry = out.setdefault(key, {"n_obs": 0})
        if tx_type == "SELL_CARGO":
            entry["sell_decay_per_step"] = min(geo, 1.0)
        else:
            entry["buy_growth_per_step"] = max(geo, 1.0)
        entry["n_obs"] += len(ratios)
    return {k: v for k, v in out.items()
            if "sell_decay_per_step" in v or "buy_growth_per_step" in v}

def fit_recovery(control: pd.DataFrame, tiers: pd.DataFrame) -> dict:
    if control.empty:
        return {}
    df = control.merge(tiers, on=["waypoint", "good"], how="inner")
    half_lives = {}
    for (wp, good, activity), g in df.groupby(["waypoint", "good", "activity"]):
        g = g.sort_values("recorded_at")
        if len(g) < 4:
            continue
        eq = g["bid"].median()
        dev = (g["bid"] - eq).abs().replace(0, np.nan).dropna()
        if len(dev) < 4:
            continue
        t_min = (g.loc[dev.index, "recorded_at"] - g["recorded_at"].iloc[0]
                 ).dt.total_seconds() / 60.0
        slope, _ = np.polyfit(t_min, np.log(dev), 1)
        if slope >= 0:
            continue  # not decaying — skip
        half_lives.setdefault(activity, []).append(float(np.log(2) / -slope))
    return {a: {"half_life_minutes": float(np.median(v)), "n_series": len(v)}
            for a, v in half_lives.items()}

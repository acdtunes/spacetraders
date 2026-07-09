# gobot/services/routing-service/model/fit.py
"""Fit structural market-model parameters (spec Phase 0).

Curve SHAPES per (supply, activity) are structural and transfer across eras;
equilibrium levels always come from the live snapshot, never from this fit.
"""
import numpy as np
import pandas as pd

MIN_OBS = 3

def fit_impact(ladders: pd.DataFrame) -> dict:
    out = {}
    if ladders.empty:
        return out
    df = ladders.sort_values(["ladder_id", "step_idx"]).copy()
    df["ratio"] = df.groupby("ladder_id")["unit_price"].pct_change() + 1.0
    df = df.dropna(subset=["ratio", "supply", "activity"])
    for (supply, activity, tx_type), g in df.groupby(["supply", "activity", "tx_type"]):
        key = f"{supply}|{activity}"
        ratios = g["ratio"].clip(lower=0.5, upper=2.0)
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

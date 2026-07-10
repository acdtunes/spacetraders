# gobot/services/routing-service/model/fit.py
"""Fit structural market-model parameters (spec Phase 0).

Curve SHAPES per (supply, activity) are structural and transfer across eras;
equilibrium levels always come from the live snapshot, never from this fit.

Impact is fit per (supply, activity) tier as a per-*tranche* decay/growth, where a
tranche is one ``tradeVolume`` worth of units (spec Phase 0: impact is piecewise
"by tranche, units expressed relative to tradeVolume"). Raw ledger steps execute
arbitrary ``units``, so we express each observed consecutive-step price move on a
per-tranche basis and pool the tier with a UNITS-WEIGHTED mean (sp-bkjz):

    per-tranche log-decay of a step:  d_i = clamp(tv/units, 0.2, 5.0) * log(clip(ratio, 0.5, 2.0))
    weight of a step:                 w_i = min(units, tv) / tv          # in (0, 1]
    tier estimate:                    exp( Σ w_i·d_i / Σ w_i )

For a sub-/full-tranche step (units <= tv) this weighting gives ``w_i·d_i = log(ratio)``,
so the tier estimate reduces to ``Σ log(ratio_i) / Σ (units_i/tv_i)``: a small units=3
sell on a tradeVolume-20 market carries only 3/20 of a full tranche's weight instead of
having its move raised to the 5th power and dominating the pool, while genuine
per-tranche signal still accumulates. Full-tranche steps (``units == tv`` → w=1,
exponent=1) reduce to the plain geometric mean, so the D39 incident (whose 20u sells hit
a tradeVolume-20 market) fits identically to before — the FORM gate is invariant. The
exponent clamp is retained as belt-and-suspenders; the weighting already bounds the
noise amplification from very small transactions that previously understated (via a
handful of amplified sub-tranche outliers) real market depth.
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
    # Units-weighted per-tranche estimator (see module docstring). Clip the RAW ratio,
    # scale to a per-tranche log-decay by clamp(tv/units), and weight each step by
    # min(units,tv)/tv so sub-tranche steps contribute proportionally instead of exploding.
    # Missing/zero units or trade_volume fall back to exponent 1.0 and weight 1.0.
    ratio = df["ratio"].clip(lower=0.5, upper=2.0)
    tv = df["trade_volume"].where(df["trade_volume"] > 0)
    units = df["units"].where(df["units"] > 0)
    exponent = (tv / units).clip(lower=EXP_MIN, upper=EXP_MAX).fillna(1.0)
    df["_perstep_log"] = exponent * np.log(ratio)
    df["_weight"] = (np.minimum(units, tv) / tv).fillna(1.0)
    for (supply, activity, tx_type), g in df.groupby(["supply", "activity", "tx_type"]):
        key = f"{supply}|{activity}"
        if len(g) < MIN_OBS:
            continue
        wsum = float(g["_weight"].sum())
        if wsum <= 0:
            continue
        geo = float(np.exp(float((g["_weight"] * g["_perstep_log"]).sum()) / wsum))
        entry = out.setdefault(key, {"n_obs": 0})
        if tx_type == "SELL_CARGO":
            entry["sell_decay_per_step"] = min(geo, 1.0)
            entry["sell_n_obs"] = len(g)
        else:
            entry["buy_growth_per_step"] = max(geo, 1.0)
            entry["buy_n_obs"] = len(g)
        entry["n_obs"] += len(g)
    return {k: v for k, v in out.items()
            if "sell_decay_per_step" in v or "buy_growth_per_step" in v}

def fit_recovery(control: pd.DataFrame, tiers: pd.DataFrame) -> dict:
    if control.empty:
        return {}
    # Recovery half-life is grouped by the market_data tier-now `activity` supplied in
    # `tiers`. sp-pf60 added tier-at-observation supply/activity to extract_control_series;
    # drop them here so the merge yields a single unambiguous `activity` instead of
    # colliding into activity_x/activity_y (which KeyError'd real calibration). Recovery
    # tier-at-time is a deliberate separate concern from sp-bkjz's ladder (impact) tagging.
    control = control.drop(columns=[c for c in ("supply", "activity")
                                    if c in control.columns])
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

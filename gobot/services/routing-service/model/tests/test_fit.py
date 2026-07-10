# gobot/services/routing-service/model/tests/test_fit.py
import pandas as pd
from model.fit import fit_impact, fit_recovery

def test_fit_impact_geometric_decay():
    ladders = pd.DataFrame([
        dict(ladder_id=1, step_idx=i, unit_price=p, tx_type="SELL_CARGO",
             supply="LIMITED", activity="WEAK", waypoint="W", good="G", units=20,
             ts=pd.Timestamp("2026-07-09"), trade_volume=20)
        for i, p in enumerate([1844, 1750, 1662, 1578])  # ~0.949/step
    ])
    out = fit_impact(ladders)
    tier = out["LIMITED|WEAK"]
    assert 0.94 < tier["sell_decay_per_step"] < 0.96
    assert tier["n_obs"] == 3 and tier["sell_n_obs"] == 3

def test_fit_impact_tranche_weighting_pools_subtranche_steps():
    # Same tier, two ladders: one sells full tranches (units == tradeVolume) at a
    # 0.95/step raw ratio; the other sells fifth-tranches (units == tv/5) that barely
    # move price at 0.99/step. Under the units-weighted estimator the tier is
    # exp(Σ log(ratio) / Σ (units/tv)): the fifth-tranche steps carry 1/5 the weight but
    # their per-tranche log-decay (tv/units=5) still enters, pooling to ~0.95 — NOT the
    # raw geometric mean of 0.95 & 0.99 (~0.970) that dilutes real depth toward 1.0.
    full = [
        dict(ladder_id=1, step_idx=i, unit_price=p, tx_type="SELL_CARGO",
             supply="LIMITED", activity="WEAK", waypoint="W", good="G", units=20,
             ts=pd.Timestamp("2026-07-09"), trade_volume=20)
        for i, p in enumerate([1000.0, 950.0, 902.5, 857.375])  # ratio 0.95/step
    ]
    fifth = [
        dict(ladder_id=2, step_idx=i, unit_price=p, tx_type="SELL_CARGO",
             supply="LIMITED", activity="WEAK", waypoint="W", good="G", units=4,
             ts=pd.Timestamp("2026-07-09"), trade_volume=20)
        for i, p in enumerate([1000.0, 990.0, 980.1, 970.299])  # ratio 0.99/step, tv/units=5
    ]
    out = fit_impact(pd.DataFrame(full + fifth))
    decay = out["LIMITED|WEAK"]["sell_decay_per_step"]
    assert 0.945 <= decay <= 0.955           # weighted per-tranche ~0.950
    assert decay < 0.96                       # NOT the raw pooled ~0.970
    assert out["LIMITED|WEAK"]["n_obs"] == 6 and out["LIMITED|WEAK"]["sell_n_obs"] == 6

def test_fit_impact_weighting_ignores_noisy_tiny_sells():
    # The HIGH|GROWING exhibit: ten solid full-tranche sells at a mild 0.96/step decay,
    # plus THREE tiny units=3 sells (tv=20) at severe raw ratios 0.71/0.76/0.80. The old
    # raw-exponent estimator raised each tiny move to the 5th power (0.71**5≈0.18) and
    # averaged it with EQUAL weight, dragging the tier to ~0.70 — below the 0.85 floor.
    # The units-weighted estimator gives each tiny step only 3/20 weight, so the tier
    # stays ~0.90 (mild, in-bounds): three noisy tiny sells cannot set the tier.
    full = [
        dict(ladder_id=1, step_idx=i, unit_price=1000.0 * (0.96 ** i), tx_type="SELL_CARGO",
             supply="HIGH", activity="GROWING", waypoint="W", good="G", units=20,
             ts=pd.Timestamp("2026-07-09"), trade_volume=20)
        for i in range(11)  # 10 full-tranche ratios at 0.96
    ]
    tiny = [
        dict(ladder_id=2, step_idx=i, unit_price=p, tx_type="SELL_CARGO",
             supply="HIGH", activity="GROWING", waypoint="W", good="G", units=3,
             ts=pd.Timestamp("2026-07-09"), trade_volume=20)
        for i, p in enumerate([1000.0, 710.0, 539.6, 431.68])  # ratios 0.71, 0.76, 0.80
    ]
    decay = fit_impact(pd.DataFrame(full + tiny))["HIGH|GROWING"]["sell_decay_per_step"]
    assert decay > 0.85            # stays above the COVERAGE floor (raw-exponent gave ~0.70)
    assert 0.88 < decay < 0.93     # ~0.905 weighted — the tiny sells are down-weighted

def test_fit_impact_skips_thin_tiers():
    ladders = pd.DataFrame([
        dict(ladder_id=1, step_idx=0, unit_price=100, tx_type="SELL_CARGO",
             supply="HIGH", activity="STRONG", waypoint="W", good="G", units=1,
             ts=pd.Timestamp("2026-07-09"), trade_volume=10),
        dict(ladder_id=1, step_idx=1, unit_price=95, tx_type="SELL_CARGO",
             supply="HIGH", activity="STRONG", waypoint="W", good="G", units=1,
             ts=pd.Timestamp("2026-07-09"), trade_volume=10),
    ])
    assert fit_impact(ladders) == {}  # only 1 ratio observation < 3

def test_fit_recovery_half_life():
    base = pd.Timestamp("2026-07-09 00:00:00")
    rows = []  # bid decays toward median 5000 with 60-min half-life
    for i, bid in enumerate([5800, 5400, 5200, 5100, 5050, 5025]):
        rows.append(dict(waypoint="W", good="G", ask=3000, bid=bid,
                         trade_volume=80, recorded_at=base + pd.Timedelta(minutes=30 * i)))
    ctrl = pd.DataFrame(rows)
    tiers = pd.DataFrame([dict(waypoint="W", good="G", activity="WEAK")])
    out = fit_recovery(ctrl, tiers)
    assert 40 < out["WEAK"]["half_life_minutes"] < 80

def test_fit_recovery_tolerates_tier_at_time_columns_on_control():
    # Regression: sp-pf60 added supply/activity (tier-at-observation) to
    # extract_control_series; recovery still groups by the market_data tier-now
    # `activity` from `tiers`, so the control's own tier columns must not collide with
    # the merge (real calibration hit KeyError 'activity' via activity_x/activity_y).
    base = pd.Timestamp("2026-07-09 00:00:00")
    rows = []
    for i, bid in enumerate([5800, 5400, 5200, 5100, 5050, 5025]):
        rows.append(dict(waypoint="W", good="G", ask=3000, bid=bid, trade_volume=80,
                         recorded_at=base + pd.Timedelta(minutes=30 * i),
                         supply="ABUNDANT", activity="STRONG"))  # tier-at-time on control
    ctrl = pd.DataFrame(rows)
    tiers = pd.DataFrame([dict(waypoint="W", good="G", activity="WEAK")])  # tier-now
    out = fit_recovery(ctrl, tiers)
    # Grouped by market_data tier-now (WEAK), NOT the control's own tier-at-time (STRONG).
    assert "WEAK" in out and "STRONG" not in out
    assert 40 < out["WEAK"]["half_life_minutes"] < 80

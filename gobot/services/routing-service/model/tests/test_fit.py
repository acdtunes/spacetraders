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
    assert tier["n_obs"] == 3

def test_fit_impact_tranche_normalizes_subtranche_steps():
    # Same tier, two ladders: one sells full tranches (units == tradeVolume) at a
    # 0.95/step raw ratio; the other sells fifth-tranches (units == tv/5) that barely
    # move price at 0.99/step. Per-tranche both are ~0.95 (0.99 ** 5 = 0.951), so the
    # fitted decay must pool to ~0.95 — NOT the raw geometric mean of 0.95 & 0.99
    # (~0.970) that dilutes real depth toward 1.0 and caused the D39 gate miss.
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
    assert 0.945 <= decay <= 0.955           # normalized to per-tranche ~0.95
    assert decay < 0.96                       # NOT the raw pooled ~0.970
    assert out["LIMITED|WEAK"]["n_obs"] == 6

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

"""Versioned market-model artifact + the Phase-0 validation gate.

The validation gate is two checks (revised 2026-07-09, Admiral decision — see spec
Phase 0), both required before the artifact is accepted:

  FORM     — a fixed D39-incident fixture is fed through the real fit machinery
             (fit_impact) and must recover the incident's cumulative decay within
             ±20%. This proves the pipeline on the known incident, INDEPENDENT of
             the live-tier fit.
  COVERAGE — every well-sampled live tier (n_obs >= COVERAGE_MIN_OBS) must fit
             within sane bounds (sell decay in [0.85, 1.0], buy growth in
             [1.0, 1.18]).

Why the split: tier labels in the live fit are tier-NOW (a market_data snapshot),
so a bygone incident cannot anchor a live-tier assertion — the 204 severe historical
sell-steps all carry today's RESTRICTED-era tags (sp-hqrb finding). sp-pf60 (record
supply/activity on market_price_history at capture time) will restore a true
live-incident gate for future fits.
"""
import json
import os

import pandas as pd

from model.fit import fit_impact

FIT_VERSION = 1

# --- FORM check: the known D39 incident, reconstructed as a fixed fixture ---
D39_TIER = "LIMITED|WEAK"
D39_OBSERVED_CUMULATIVE = 1562 / 1844  # 0.847 — the held-out D39 incident (3 decay steps)
D39_STEPS = 3
FORM_TOLERANCE = 0.20
# Four consecutive full-tranche (units == tradeVolume == 20) sells at LIMITED|WEAK, unit
# prices tracing the incident's ~0.949/step decay. Fed through fit_impact so the FORM gate
# exercises the real fitting code on a known incident (units == tv → tranche exponent 1, so
# the tranche-normalization is a no-op here and the fixture stays the incident's shape).
D39_FIXTURE_PRICES = [1844, 1750, 1662, 1578]
D39_FIXTURE_UNITS = 20
D39_FIXTURE_TRADE_VOLUME = 20

# --- COVERAGE check: sane bounds on every fleet-relevant (well-sampled) live tier ---
COVERAGE_MIN_OBS = 30
SELL_DECAY_MIN, SELL_DECAY_MAX = 0.85, 1.0
BUY_GROWTH_MIN, BUY_GROWTH_MAX = 1.0, 1.18


def write_artifact(path, impact, recovery, era, generated_at):
    art = {"fit_version": FIT_VERSION, "era": era, "generated_at": generated_at,
           "impact": impact, "recovery": recovery,
           "diagnostics": {"ladder_count": sum(v.get("n_obs", 0) for v in impact.values()),
                            "control_series_count": sum(v.get("n_series", 0)
                                                        for v in recovery.values())}}
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(art, f, indent=2, sort_keys=True)
    return art


def load_artifact(path):
    with open(path) as f:
        return json.load(f)


def build_d39_fixture() -> pd.DataFrame:
    """The D39 incident as a fixed ladder frame (the FORM check's held-out fixture)."""
    return pd.DataFrame([
        dict(ladder_id=1, step_idx=i, unit_price=p, tx_type="SELL_CARGO",
             supply="LIMITED", activity="WEAK", waypoint="X1-NK36-D39", good="MEDICINE",
             units=D39_FIXTURE_UNITS, ts=pd.Timestamp("2026-07-09"),
             trade_volume=D39_FIXTURE_TRADE_VOLUME)
        for i, p in enumerate(D39_FIXTURE_PRICES)
    ])


def validate_form() -> tuple[bool, str]:
    """FORM gate: the real fit machinery must recover the D39 incident from the fixture.

    Tolerance is applied to the cumulative *drop* (1 - cumulative), not the raw ratio:
    a symmetric band on the ratio (~0.847) reaches ~1.016, which would let a no-decay fit
    slip through as a pass despite predicting zero price movement.
    """
    impact = fit_impact(build_d39_fixture())
    tier = impact.get(D39_TIER)
    if not tier or "sell_decay_per_step" not in tier:
        return False, (f"FORM gate FAIL: fit machinery produced no {D39_TIER} sell decay "
                       f"from the D39 fixture (incident unrecoverable)")
    decay = tier["sell_decay_per_step"]
    predicted = decay ** D39_STEPS
    observed_drop = 1 - D39_OBSERVED_CUMULATIVE
    rel_err = abs((1 - predicted) - observed_drop) / observed_drop
    if rel_err <= FORM_TOLERANCE:
        return True, (f"FORM gate PASS: D39 fixture recovers decay {decay:.4f}/step → "
                      f"cumulative {predicted:.3f} vs observed {D39_OBSERVED_CUMULATIVE:.3f} "
                      f"(drop error {rel_err:.1%})")
    return False, (f"FORM gate FAIL: D39 fixture recovers cumulative {predicted:.3f} vs "
                   f"observed {D39_OBSERVED_CUMULATIVE:.3f} (drop error {rel_err:.1%} exceeds "
                   f"±{FORM_TOLERANCE:.0%})")


def validate_coverage(impact: dict) -> tuple[bool, str]:
    """COVERAGE gate: every live tier with n_obs >= COVERAGE_MIN_OBS fits sane bounds."""
    violations = []
    checked = 0
    for tier, v in sorted(impact.items()):
        if v.get("n_obs", 0) < COVERAGE_MIN_OBS:
            continue
        checked += 1
        sell = v.get("sell_decay_per_step")
        buy = v.get("buy_growth_per_step")
        if sell is not None and not (SELL_DECAY_MIN <= sell <= SELL_DECAY_MAX):
            violations.append(
                f"{tier} sell_decay {sell:.4f}∉[{SELL_DECAY_MIN},{SELL_DECAY_MAX}]")
        if buy is not None and not (BUY_GROWTH_MIN <= buy <= BUY_GROWTH_MAX):
            violations.append(
                f"{tier} buy_growth {buy:.4f}∉[{BUY_GROWTH_MIN},{BUY_GROWTH_MAX}]")
    if violations:
        return False, (f"COVERAGE gate FAIL: {len(violations)} out-of-bounds across "
                       f"{checked} tiers (n_obs≥{COVERAGE_MIN_OBS}): " + "; ".join(violations))
    return True, (f"COVERAGE gate PASS: all {checked} tiers (n_obs≥{COVERAGE_MIN_OBS}) within "
                  f"sell∈[{SELL_DECAY_MIN},{SELL_DECAY_MAX}] buy∈[{BUY_GROWTH_MIN},{BUY_GROWTH_MAX}]")

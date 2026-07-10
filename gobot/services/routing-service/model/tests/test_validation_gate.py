from model.artifact import validate_form, validate_coverage

def test_form_recovers_d39_incident_from_fixture():
    # The fixed D39 fixture, fed through the real fit_impact, must recover the incident's
    # cumulative decay within ±20% — proves the pipeline independently of the live-tier fit.
    ok, msg = validate_form()
    assert ok, msg

def test_coverage_passes_when_all_fleet_tiers_in_range():
    impact = {
        "LIMITED|RESTRICTED": {"sell_decay_per_step": 0.936, "sell_n_obs": 231,
                               "buy_growth_per_step": 1.095, "buy_n_obs": 231},
        "SCARCE|WEAK": {"sell_decay_per_step": 0.959, "sell_n_obs": 169,
                        "buy_growth_per_step": 1.019, "buy_n_obs": 169},
        "HIGH|RESTRICTED": {"buy_growth_per_step": 1.103, "buy_n_obs": 159},
    }
    ok, msg = validate_coverage(impact)
    assert ok, msg

def test_coverage_fails_on_low_sell_decay():
    impact = {"MODERATE|RESTRICTED": {"sell_decay_per_step": 0.80, "sell_n_obs": 1091}}  # 0.80 < 0.85
    ok, msg = validate_coverage(impact)
    assert not ok and "MODERATE|RESTRICTED" in msg and "sell_decay" in msg

def test_coverage_fails_on_high_buy_growth():
    impact = {"HIGH|RESTRICTED": {"buy_growth_per_step": 1.25, "buy_n_obs": 159}}  # 1.25 > 1.18
    ok, msg = validate_coverage(impact)
    assert not ok and "HIGH|RESTRICTED" in msg and "buy_growth" in msg

def test_coverage_ignores_thin_sides():
    # Both sides under the per-side floor (sell_n_obs/buy_n_obs < 30) → not fleet-relevant,
    # not gated, gate passes despite wildly out-of-range coefficients.
    impact = {"LIMITED|WEAK": {"sell_decay_per_step": 0.50, "sell_n_obs": 11,
                               "buy_growth_per_step": 1.9, "buy_n_obs": 11}}
    ok, msg = validate_coverage(impact)
    assert ok, msg

def test_coverage_gates_sides_independently():
    # The HIGH|GROWING gate-level exhibit (sp-bkjz per-side floor): a THIN sell side
    # (23 < 30) that is wildly out of range must NOT be gated, even though the same
    # tier's buy side is well-populated and in range → the gate PASSES. Previously the
    # combined sell+buy count (23+200) cleared the floor and failed the whole gate.
    impact = {"HIGH|GROWING": {"sell_decay_per_step": 0.79, "sell_n_obs": 23,
                               "buy_growth_per_step": 1.10, "buy_n_obs": 200}}
    ok, msg = validate_coverage(impact)
    assert ok, msg
    # And a well-populated bad sell side on the same shape IS still gated.
    bad = {"HIGH|GROWING": {"sell_decay_per_step": 0.79, "sell_n_obs": 200,
                            "buy_growth_per_step": 1.10, "buy_n_obs": 200}}
    ok2, msg2 = validate_coverage(bad)
    assert not ok2 and "HIGH|GROWING" in msg2 and "sell_decay" in msg2

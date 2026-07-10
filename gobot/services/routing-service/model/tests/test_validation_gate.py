from model.artifact import validate_form, validate_coverage

def test_form_recovers_d39_incident_from_fixture():
    # The fixed D39 fixture, fed through the real fit_impact, must recover the incident's
    # cumulative decay within ±20% — proves the pipeline independently of the live-tier fit.
    ok, msg = validate_form()
    assert ok, msg

def test_coverage_passes_when_all_fleet_tiers_in_range():
    impact = {
        "LIMITED|RESTRICTED": {"sell_decay_per_step": 0.936, "buy_growth_per_step": 1.095, "n_obs": 231},
        "SCARCE|WEAK": {"sell_decay_per_step": 0.959, "buy_growth_per_step": 1.019, "n_obs": 169},
        "HIGH|RESTRICTED": {"buy_growth_per_step": 1.103, "n_obs": 159},
    }
    ok, msg = validate_coverage(impact)
    assert ok, msg

def test_coverage_fails_on_low_sell_decay():
    impact = {"MODERATE|RESTRICTED": {"sell_decay_per_step": 0.80, "n_obs": 1091}}  # 0.80 < 0.85
    ok, msg = validate_coverage(impact)
    assert not ok and "MODERATE|RESTRICTED" in msg and "sell_decay" in msg

def test_coverage_fails_on_high_buy_growth():
    impact = {"HIGH|RESTRICTED": {"buy_growth_per_step": 1.25, "n_obs": 159}}  # 1.25 > 1.18
    ok, msg = validate_coverage(impact)
    assert not ok and "HIGH|RESTRICTED" in msg and "buy_growth" in msg

def test_coverage_ignores_thin_tiers():
    # A wildly out-of-range tier with n_obs < 30 is not fleet-relevant → ignored, gate passes.
    impact = {"LIMITED|WEAK": {"sell_decay_per_step": 0.50, "buy_growth_per_step": 1.9, "n_obs": 11}}
    ok, msg = validate_coverage(impact)
    assert ok, msg

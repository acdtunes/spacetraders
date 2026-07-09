from model.artifact import validate_against_incident

def test_gate_passes_on_good_fit():
    ok, msg = validate_against_incident(
        {"LIMITED|WEAK": {"sell_decay_per_step": 0.947, "n_obs": 12}})
    assert ok, msg  # 0.947^3 = 0.849 vs observed 0.847 → well within ±20%

def test_gate_fails_on_no_decay_model():
    ok, msg = validate_against_incident(
        {"LIMITED|WEAK": {"sell_decay_per_step": 1.0, "n_obs": 12}})
    assert not ok and "D39" in msg  # 1.0 predicts no ladder → outside band

def test_gate_fails_when_tier_missing():
    ok, msg = validate_against_incident({})
    assert not ok

import json

FIT_VERSION = 1
D39_OBSERVED_CUMULATIVE = 1562 / 1844  # 0.847, 3 decay steps (spec: held-out incident)
D39_TIER = "LIMITED|WEAK"
D39_STEPS = 3
TOLERANCE = 0.20

def write_artifact(path, impact, recovery, era, generated_at):
    art = {"fit_version": FIT_VERSION, "era": era, "generated_at": generated_at,
           "impact": impact, "recovery": recovery,
           "diagnostics": {"ladder_count": sum(v.get("n_obs", 0) for v in impact.values()),
                            "control_series_count": sum(v.get("n_series", 0)
                                                        for v in recovery.values())}}
    with open(path, "w") as f:
        json.dump(art, f, indent=2, sort_keys=True)
    return art

def load_artifact(path):
    with open(path) as f:
        return json.load(f)

def validate_against_incident(impact):
    tier = impact.get(D39_TIER)
    if not tier or "sell_decay_per_step" not in tier:
        return False, f"validation gate: tier {D39_TIER} missing from fit (D39 incident unverifiable)"
    predicted = tier["sell_decay_per_step"] ** D39_STEPS
    # Tolerance is applied to the magnitude of the predicted cumulative *drop*
    # (1 - ratio) vs the observed drop, not to the raw ratio: a symmetric ±20%
    # band on the ratio itself (~0.847) spans up to ~1.016, which would let a
    # sell_decay_per_step of 1.0 (i.e. no decay/no ladder at all) slip through
    # as a "pass" despite predicting zero price movement.
    observed_drop = 1 - D39_OBSERVED_CUMULATIVE
    predicted_drop = 1 - predicted
    rel_err = abs(predicted_drop - observed_drop) / observed_drop
    if rel_err <= TOLERANCE:
        return True, (f"D39 gate PASS: predicted {predicted:.3f} vs observed "
                      f"{D39_OBSERVED_CUMULATIVE:.3f} (drop error {rel_err:.1%})")
    return False, (f"D39 gate FAIL: predicted cumulative {predicted:.3f} vs observed "
                   f"{D39_OBSERVED_CUMULATIVE:.3f} (drop error {rel_err:.1%} exceeds "
                   f"D39 tolerance of ±{TOLERANCE:.0%})")

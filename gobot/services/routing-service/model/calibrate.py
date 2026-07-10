# gobot/services/routing-service/model/calibrate.py
import os, sys
from datetime import datetime, timezone
from model.extract import db_engine, extract_ladders, extract_control_series
from model.fit import fit_impact, fit_recovery
from model.artifact import write_artifact, validate_form, validate_coverage, mark_thin_sides
import pandas as pd
from sqlalchemy import text

def _tier_at_time_diag(ladders: pd.DataFrame) -> dict:
    """Coverage of ladder steps tagged with a true tier-at-observation vs. the
    market_data tier-now fallback (sp-bkjz). steps_total counts every ladder step;
    steps_true counts those whose tier came from a preceding market_price_history row."""
    if ladders.empty or "tier_at_time" not in ladders.columns:
        return {"steps_true": 0, "steps_total": 0, "coverage": 0.0}
    total = int(len(ladders))
    true_n = int(ladders["tier_at_time"].fillna(False).astype(bool).sum())
    return {"steps_true": true_n, "steps_total": total,
            "coverage": round(true_n / total, 4) if total else 0.0}

def main():
    eng = db_engine()
    ladders = extract_ladders(eng)
    control = extract_control_series(eng)
    tiers = pd.read_sql(text("SELECT waypoint_symbol AS waypoint, good_symbol AS good, activity FROM market_data"), eng)
    impact, recovery = fit_impact(ladders), fit_recovery(control, tiers)
    # Two-check validation gate (spec Phase 0, revised): FORM proves the machinery on the
    # fixed D39 fixture; COVERAGE bounds every well-sampled live tier. Print both verdicts,
    # then fail closed (no artifact) if either check fails.
    form_ok, form_msg = validate_form()
    cov_ok, cov_msg = validate_coverage(impact)
    print(form_msg)
    print(cov_msg)
    if not (form_ok and cov_ok):
        sys.exit(1)
    tat = _tier_at_time_diag(ladders)
    print(f"tier_at_time coverage: {tat['steps_true']}/{tat['steps_total']} steps "
          f"= {tat['coverage']:.1%} (rest fell back to tier-now)")
    era = os.environ.get("ST_ERA", "unknown-era")
    art = write_artifact(os.path.join(os.path.dirname(__file__), "..",
                                      "model_artifacts", "market_model.json"),
                         mark_thin_sides(impact), recovery, era,
                         datetime.now(timezone.utc).isoformat(),
                         extra_diagnostics={"tier_at_time": tat})
    print(f"artifact written: {len(art['impact'])} impact tiers, {len(art['recovery'])} recovery tiers")

if __name__ == "__main__":
    main()

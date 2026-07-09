# gobot/services/routing-service/model/calibrate.py
import os, sys
from datetime import datetime, timezone
from model.extract import db_engine, extract_ladders, extract_control_series
from model.fit import fit_impact, fit_recovery
from model.artifact import write_artifact, validate_against_incident
import pandas as pd
from sqlalchemy import text

def main():
    eng = db_engine()
    ladders = extract_ladders(eng)
    control = extract_control_series(eng)
    tiers = pd.read_sql(text("SELECT waypoint_symbol AS waypoint, good_symbol AS good, activity FROM market_data"), eng)
    impact, recovery = fit_impact(ladders), fit_recovery(control, tiers)
    ok, msg = validate_against_incident(impact)
    print(msg)
    if not ok:
        sys.exit(1)
    era = os.environ.get("ST_ERA", "unknown-era")
    art = write_artifact(os.path.join(os.path.dirname(__file__), "..",
                                      "model_artifacts", "market_model.json"),
                         impact, recovery, era,
                         datetime.now(timezone.utc).isoformat())
    print(f"artifact written: {len(art['impact'])} impact tiers, {len(art['recovery'])} recovery tiers")

if __name__ == "__main__":
    main()

# Multi-Hop Trade-Tour Optimizer Implementation Plan (sp-1ek0)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A depth-aware tour planner (Python, new `OptimizeTradeTour` RPC on the existing routing service) plus a guard-bound one-shot Go executor (`workflow tour-run`) so a heavy freighter flies profitable multi-hop, up-to-2-system trading runs.

**Architecture:** Three phases. P0 fits an empirical market model (price-impact decay per (supply, activity) tier + recovery half-lives) from our own ledger transaction ladders and scout price history, shipping a versioned JSON artifact with a hard validation gate. P1a adds the planner RPC: beam search over hop sequences × tranche-LP over the fitted curves, objective credits/hour, tours scoped to ≤2 gate-adjacent systems. P1b adds the `tour_run` daemon container (arb_run's twin): plan binds routing, prices re-verified live per leg, one stateless re-plan allowance, planned-vs-realized telemetry, stranded-cargo = failure.

**Tech Stack:** Python 3 (pandas, sqlalchemy, ortools GLOP — routing-service venv), gRPC/protobuf, Go (existing gobot patterns: arb_run container chain, GORM, captain-gate).

**Spec:** `docs/superpowers/specs/2026-07-09-trade-tour-optimizer-design.md` — read it first; it is the authority on any ambiguity.

## Global Constraints

- Tours span at most **2 gate-adjacent systems** (`maxTourSystems = 2`, named constant).
- Max hops per tour: **6** (`maxTourHops = 6`).
- Snapshot rows older than **75 minutes** are excluded from planning (share the trading package's existing `maxListingAge` constant on the Go side; mirror the value in Python).
- Graduation gate numbers (used by the report): **10 tours, ≥1.5× single-lane $/hr, ±15% median price error**.
- Model validation gate: predict held-out D39 ladder within **±20%**.
- Executor defaults: `tourPriceTolerancePct = 15`, `tourMaxReplans = 2`, default maxSpend = 25% of live treasury at launch.
- RULINGS.md applies (esp. #2 restart-resilient, #3 single-writer, #4 guards fail closed and are never weakened, #5 parametrize).
- Protocol v2 landing rules: worktree-first, commit before gating (never stage issues.jsonl; `--no-verify` if the hook interferes), gate with `--provision --merge`, verify the merged SHA's numstat.
- Go tests: package-scoped `go test ./internal/application/trading/... ./internal/adapters/...` etc. Python tests: `cd gobot/services/routing-service && ./venv/bin/python -m pytest tests/ model/tests/ -v`.

## File Structure (locked)

```
gobot/services/routing-service/
  model/__init__.py
  model/extract.py          # P0: ledger ladders + price-history control series (sqlalchemy)
  model/fit.py              # P0: decay-per-tranche + recovery half-life fitting (pure)
  model/artifact.py         # P0: versioned JSON artifact write/load + schema
  model/calibrate.py        # P0: CLI entrypoint (extract→fit→validate→write)
  model/tests/test_fit.py, test_artifact.py, test_validation_gate.py
  model_artifacts/market_model.json   # checked-in artifact (P0 output)
  handlers/tour_handler.py  # P1a: OptimizeTradeTour servicer method + solver
  utils/tour_solver.py      # P1a: beam + tranche-LP (pure, unit-testable)
  tests/test_tour_solver.py, tests/test_tour_handler.py
gobot/pkg/proto/routing/routing.proto        # P1a: new RPC + messages (regen Go+Py)
gobot/internal/domain/routing/tour.go        # P1b: Go-side plan types
gobot/internal/adapters/routing/grpc_routing_client.go   # P1b: OptimizeTradeTour method
gobot/internal/application/trading/commands/run_tour_coordinator.go        # P1b executor
gobot/internal/application/trading/commands/run_tour_coordinator_test.go
gobot/internal/application/trading/services/tour_snapshot.go               # P1b assembler
gobot/internal/adapters/persistence/models.go            # P1b: TourLegTelemetryModel
gobot/internal/adapters/persistence/tour_telemetry_repository.go (+_test)
gobot/internal/adapters/grpc/container_ops_tour.go       # P1b: StartTourRun (arb twin)
gobot/internal/adapters/grpc/command_factory_registry.go # P1b: "tour_run" entry
gobot/pkg/proto/daemon/daemon.proto                      # P1b: StartTourRun RPC (regen)
gobot/internal/adapters/cli/workflow_tour_run.go         # P1b: CLI
gobot/internal/adapters/cli/tour_report.go               # P1b: gate-metrics report
```

---

# PHASE P0 — Market model pipeline (bead: file under sp-1ek0, fable tier)

### Task 1: Extraction module — ladder events + control series

**Files:**
- Create: `gobot/services/routing-service/model/__init__.py` (empty)
- Create: `gobot/services/routing-service/model/extract.py`
- Test: `gobot/services/routing-service/model/tests/test_extract.py` (+ empty `tests/__init__.py`)

**Interfaces:**
- Produces: `extract_ladders(engine) -> pd.DataFrame` with columns `[waypoint, good, tx_type, step_idx, units, unit_price, ts, supply, activity, trade_volume]` — consecutive same-(waypoint, good, tx_type) transactions within 30 min form one ladder (`ladder_id` column groups them).
- Produces: `extract_control_series(engine) -> pd.DataFrame` with columns `[waypoint, good, ask, bid, trade_volume, recorded_at]` from `market_price_history` for (waypoint, good) pairs that never appear in our `transactions` (the untouched control set).
- DB env: `ST_DATABASE_HOST/PORT/NAME/USER/PASSWORD` (same as `gobot/analysis/market_dynamics_analysis.py:50`; default host 127.0.0.1, db `spacetraders`, user `spacetraders`).

Ground truth for the SQL (verified 2026-07-09): table `transactions` (TransactionModel, `models.go:287`) — columns include `transaction_type`, `amount` (total credits, negative=buy positive=sell), `timestamp`, `metadata` (JSON text with keys `good_symbol`, `units`, `waypoint` — stamped at `cargo_transaction.go:324-326`). Table `market_data` — per-(waypoint,good) rows with `supply`, `activity`, `trade_volume`, `last_updated`. Table `market_price_history` — `waypoint_symbol`, `good_symbol`, `purchase_price` (bid), `sell_price` (ask), `trade_volume`, `recorded_at`.

- [ ] **Step 1: Write the failing test** (uses an in-memory sqlite engine seeded with 5 transactions forming one 3-step sell ladder + 2 unrelated rows, one market_data row, and 3 price-history rows for an untouched pair)

```python
# gobot/services/routing-service/model/tests/test_extract.py
import json
import pandas as pd
import pytest
from sqlalchemy import create_engine, text
from model.extract import extract_ladders, extract_control_series

@pytest.fixture
def engine():
    eng = create_engine("sqlite://")
    with eng.begin() as c:
        c.execute(text("""CREATE TABLE transactions (
            id TEXT, transaction_type TEXT, amount INTEGER,
            timestamp TIMESTAMP, metadata TEXT)"""))
        c.execute(text("""CREATE TABLE market_data (
            waypoint_symbol TEXT, symbol TEXT, supply TEXT, activity TEXT,
            trade_volume INTEGER, last_updated TIMESTAMP)"""))
        c.execute(text("""CREATE TABLE market_price_history (
            waypoint_symbol TEXT, good_symbol TEXT, purchase_price INTEGER,
            sell_price INTEGER, trade_volume INTEGER, recorded_at TIMESTAMP)"""))
        def tx(i, typ, amount, ts, good, units, wp):
            c.execute(text("INSERT INTO transactions VALUES (:i,:t,:a,:ts,:m)"),
                dict(i=str(i), t=typ, a=amount, ts=ts,
                     m=json.dumps({"good_symbol": good, "units": units, "waypoint": wp})))
        # 3-step sell ladder at D39: unit prices 1844, 1750, 1562
        tx(1, "SELL_CARGO",  36880, "2026-07-09 21:28:00", "MEDICINE", 20, "X1-NK36-D39")
        tx(2, "SELL_CARGO",  35000, "2026-07-09 21:30:00", "MEDICINE", 20, "X1-NK36-D39")
        tx(3, "SELL_CARGO",  31240, "2026-07-09 21:32:00", "MEDICINE", 20, "X1-NK36-D39")
        # unrelated (different good) and out-of-window (2h later) rows
        tx(4, "SELL_CARGO",  10000, "2026-07-09 21:31:00", "FABRICS", 10, "X1-NK36-D39")
        tx(5, "SELL_CARGO",  30000, "2026-07-09 23:40:00", "MEDICINE", 20, "X1-NK36-D39")
        c.execute(text("""INSERT INTO market_data VALUES
            ('X1-NK36-D39','MEDICINE','LIMITED','WEAK',20,'2026-07-09 21:00:00')"""))
        for i, bid in enumerate([5200, 5210, 5190]):
            c.execute(text("""INSERT INTO market_price_history VALUES
                ('X1-GQ92-A1','MEDICINE',:b,3000,80,:ts)"""),
                dict(b=bid, ts=f"2026-07-09 2{i}:00:00"))
    return eng

def test_ladder_grouping_and_unit_prices(engine):
    df = extract_ladders(engine)
    ladder = df[(df.good == "MEDICINE") & (df.tx_type == "SELL_CARGO")]
    ladder = ladder[ladder.ladder_id == ladder.ladder_id.iloc[0]]
    assert list(ladder.step_idx) == [0, 1, 2]
    assert list(ladder.unit_price) == [1844, 1750, 1562]
    assert ladder.supply.iloc[0] == "LIMITED" and ladder.activity.iloc[0] == "WEAK"

def test_out_of_window_tx_starts_new_ladder(engine):
    df = extract_ladders(engine)
    med = df[(df.good == "MEDICINE")]
    assert med.ladder_id.nunique() == 2  # the 23:40 row is its own ladder

def test_control_series_excludes_traded_pairs(engine):
    ctrl = extract_control_series(engine)
    assert set(ctrl.waypoint.unique()) == {"X1-GQ92-A1"}  # D39/MEDICINE traded → excluded
    assert len(ctrl) == 3
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd gobot/services/routing-service && ./venv/bin/python -m pytest model/tests/test_extract.py -v`
Expected: FAIL `ModuleNotFoundError: No module named 'model'` (then, after skeleton, `ImportError: extract_ladders`). If pandas/pytest are missing from the venv: `./venv/bin/pip install pandas sqlalchemy pytest` and append `pandas`, `sqlalchemy`, `pytest` to `requirements.txt`.

- [ ] **Step 3: Implement**

```python
# gobot/services/routing-service/model/extract.py
"""Extract impact-ladder events and untouched control series from the bot DB.

Our own transaction sequences are the treatment events that identify the
price-impact curve (spec: Phase 0). A 'ladder' = consecutive transactions of
the same (waypoint, good, tx_type) within LADDER_WINDOW_MIN minutes.
"""
import json
import os
import pandas as pd
from sqlalchemy import create_engine, text

LADDER_WINDOW_MIN = 30

def db_engine():
    host = os.environ.get("ST_DATABASE_HOST", "127.0.0.1")
    port = os.environ.get("ST_DATABASE_PORT", "5432")
    name = os.environ.get("ST_DATABASE_NAME", "spacetraders")
    user = os.environ.get("ST_DATABASE_USER", "spacetraders")
    pw = os.environ.get("ST_DATABASE_PASSWORD", "dev_password")
    return create_engine(f"postgresql://{user}:{pw}@{host}:{port}/{name}")

def _rows(engine):
    q = text("""SELECT transaction_type, amount, timestamp, metadata
                FROM transactions
                WHERE transaction_type IN ('SELL_CARGO','PURCHASE_CARGO')
                ORDER BY timestamp""")
    with engine.connect() as c:
        return c.execute(q).fetchall()

def extract_ladders(engine) -> pd.DataFrame:
    recs = []
    for tx_type, amount, ts, meta in _rows(engine):
        m = json.loads(meta) if isinstance(meta, str) else (meta or {})
        units = m.get("units") or 0
        if not units or not m.get("good_symbol") or not m.get("waypoint"):
            continue
        recs.append(dict(waypoint=m["waypoint"], good=m["good_symbol"],
                         tx_type=tx_type, units=int(units),
                         unit_price=abs(int(amount)) // int(units),
                         ts=pd.Timestamp(ts)))
    df = pd.DataFrame(recs)
    if df.empty:
        return df
    df = df.sort_values(["waypoint", "good", "tx_type", "ts"]).reset_index(drop=True)
    gap = df.groupby(["waypoint", "good", "tx_type"])["ts"].diff()
    new_ladder = gap.isna() | (gap > pd.Timedelta(minutes=LADDER_WINDOW_MIN))
    df["ladder_id"] = new_ladder.cumsum()
    df["step_idx"] = df.groupby("ladder_id").cumcount()
    tiers = pd.read_sql(text("""SELECT waypoint_symbol AS waypoint, symbol AS good,
                                supply, activity, trade_volume FROM market_data"""),
                        engine)
    return df.merge(tiers, on=["waypoint", "good"], how="left")

def extract_control_series(engine) -> pd.DataFrame:
    return pd.read_sql(text("""
        SELECT h.waypoint_symbol AS waypoint, h.good_symbol AS good,
               h.sell_price AS ask, h.purchase_price AS bid,
               h.trade_volume, h.recorded_at
        FROM market_price_history h
        WHERE NOT EXISTS (
            SELECT 1 FROM transactions t
            WHERE t.transaction_type IN ('SELL_CARGO','PURCHASE_CARGO')
              AND t.metadata LIKE '%%' || h.waypoint_symbol || '%%'
              AND t.metadata LIKE '%%' || h.good_symbol || '%%')
        ORDER BY h.waypoint_symbol, h.good_symbol, h.recorded_at"""), engine)
```

- [ ] **Step 4: Run tests to verify they pass** — same command, Expected: 3 PASS.
- [ ] **Step 5: Commit** — `git add gobot/services/routing-service/model gobot/services/routing-service/requirements.txt && git commit --no-verify -m "feat(model): P0 extraction — ladder events + control series (sp-1ek0)"`

### Task 2: Fitting — decay-per-tranche + recovery half-life

**Files:**
- Create: `gobot/services/routing-service/model/fit.py`
- Test: `gobot/services/routing-service/model/tests/test_fit.py`

**Interfaces:**
- Consumes: the DataFrames from Task 1.
- Produces: `fit_impact(ladders: pd.DataFrame) -> dict` mapping `"{supply}|{activity}"` → `{"sell_decay_per_step": float, "buy_growth_per_step": float, "n_obs": int}` (geometric mean of consecutive-step unit-price ratios per tier; sell ratios < 1, buy ratios > 1; tiers with n_obs < 3 omitted).
- Produces: `fit_recovery(control: pd.DataFrame) -> dict` mapping `activity` → `{"half_life_minutes": float, "n_series": int}` — for each (waypoint, good) series, regress log|price − median| against time on bid; aggregate per activity via median. Series shorter than 4 points skipped. (Control frame lacks activity; join is by trade_volume bucketing proxy — pass `tiers: pd.DataFrame` with columns `[waypoint, good, activity]` as a second argument, from `market_data`.)

- [ ] **Step 1: Failing test**

```python
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
```

- [ ] **Step 2: Run — FAIL** (`ImportError`).
- [ ] **Step 3: Implement**

```python
# gobot/services/routing-service/model/fit.py
"""Fit structural market-model parameters (spec Phase 0).

Curve SHAPES per (supply, activity) are structural and transfer across eras;
equilibrium levels always come from the live snapshot, never from this fit.
"""
import numpy as np
import pandas as pd

MIN_OBS = 3

def fit_impact(ladders: pd.DataFrame) -> dict:
    out = {}
    if ladders.empty:
        return out
    df = ladders.sort_values(["ladder_id", "step_idx"]).copy()
    df["ratio"] = df.groupby("ladder_id")["unit_price"].pct_change() + 1.0
    df = df.dropna(subset=["ratio", "supply", "activity"])
    for (supply, activity, tx_type), g in df.groupby(["supply", "activity", "tx_type"]):
        key = f"{supply}|{activity}"
        ratios = g["ratio"].clip(lower=0.5, upper=2.0)
        if len(ratios) < MIN_OBS:
            continue
        geo = float(np.exp(np.log(ratios).mean()))
        entry = out.setdefault(key, {"n_obs": 0})
        if tx_type == "SELL_CARGO":
            entry["sell_decay_per_step"] = min(geo, 1.0)
        else:
            entry["buy_growth_per_step"] = max(geo, 1.0)
        entry["n_obs"] += len(ratios)
    return {k: v for k, v in out.items()
            if "sell_decay_per_step" in v or "buy_growth_per_step" in v}

def fit_recovery(control: pd.DataFrame, tiers: pd.DataFrame) -> dict:
    if control.empty:
        return {}
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
```

- [ ] **Step 4: Run — 3 PASS.**
- [ ] **Step 5: Commit** — `git commit --no-verify -m "feat(model): P0 fitting — tier decay + recovery half-life (sp-1ek0)"`

### Task 3: Artifact + validation gate + calibrate entrypoint

**Files:**
- Create: `gobot/services/routing-service/model/artifact.py`, `model/calibrate.py`
- Create: `gobot/services/routing-service/model_artifacts/` (artifact written here)
- Test: `model/tests/test_artifact.py`, `model/tests/test_validation_gate.py`
- Modify: `gobot/Makefile` (add `calibrate-market-model` target)

**Interfaces:**
- Produces: `write_artifact(path, impact: dict, recovery: dict, era: str, generated_at: str) -> dict` and `load_artifact(path) -> dict`. Artifact schema (exact):

```json
{"fit_version": 1, "era": "torwind-2026-07-05", "generated_at": "<caller-supplied ISO>",
 "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.947, "n_obs": 12}},
 "recovery": {"WEAK": {"half_life_minutes": 60.0, "n_series": 4}},
 "diagnostics": {"ladder_count": 0, "control_series_count": 0}}
```

- Produces: `validate_against_incident(impact: dict) -> tuple[bool, str]` — the D39 hard gate: tier `LIMITED|WEAK` predicted 3-step cumulative ratio vs observed `1562/1844 = 0.847`, must be within ±20% (i.e. predicted cumulative in `[0.678, 1.016]`). Returns `(ok, message)`.
- `calibrate.py` main: engine → extract → fit → validate (exit 1 on gate failure, artifact NOT written) → `write_artifact("model_artifacts/market_model.json", ..., era=os.environ["ST_ERA"], generated_at=datetime.now(timezone.utc).isoformat())`.
- Makefile target: `calibrate-market-model:` runs `cd services/routing-service && ./venv/bin/python -m model.calibrate`.

- [ ] **Step 1: Failing tests**

```python
# gobot/services/routing-service/model/tests/test_artifact.py
from model.artifact import write_artifact, load_artifact

def test_roundtrip(tmp_path):
    p = tmp_path / "m.json"
    a = write_artifact(str(p), {"LIMITED|WEAK": {"sell_decay_per_step": 0.95, "n_obs": 5}},
                       {"WEAK": {"half_life_minutes": 60, "n_series": 2}},
                       era="torwind-2026-07-05", generated_at="2026-07-09T22:00:00Z")
    b = load_artifact(str(p))
    assert b == a and b["fit_version"] == 1 and b["era"] == "torwind-2026-07-05"
```

```python
# gobot/services/routing-service/model/tests/test_validation_gate.py
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
```

- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement**

```python
# gobot/services/routing-service/model/artifact.py
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
    lo, hi = D39_OBSERVED_CUMULATIVE * (1 - TOLERANCE), D39_OBSERVED_CUMULATIVE * (1 + TOLERANCE)
    if lo <= predicted <= hi:
        return True, f"D39 gate PASS: predicted {predicted:.3f} vs observed {D39_OBSERVED_CUMULATIVE:.3f}"
    return False, (f"D39 gate FAIL: predicted cumulative {predicted:.3f} outside "
                   f"[{lo:.3f}, {hi:.3f}] (observed {D39_OBSERVED_CUMULATIVE:.3f})")
```

```python
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
    tiers = pd.read_sql(text("SELECT waypoint_symbol AS waypoint, symbol AS good, activity FROM market_data"), eng)
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
```

Makefile addition (after the `routing-service` targets):

```makefile
# Calibrate the market model artifact (sp-1ek0 P0)
calibrate-market-model:
	@cd services/routing-service && ./venv/bin/python -m model.calibrate
```

- [ ] **Step 4: Run tests — 4 PASS. Then run the real calibration once** (`make -C gobot calibrate-market-model`) against the live DB; commit the produced `model_artifacts/market_model.json` ONLY if the gate printed PASS. If the gate FAILS on real data (thin ladder sample), report the failure message to harbormaster — do not hand-edit the artifact.
- [ ] **Step 5: Commit** — code + artifact + Makefile.

---

# PHASE P1a — Planner RPC + solver (same fable agent continues; bead under sp-1ek0)

### Task 4: Proto — `OptimizeTradeTour`

**Files:**
- Modify: `gobot/pkg/proto/routing/routing.proto` (append; follow the existing snake_case commented style, e.g. `PlanRouteRequest` at line 25)
- Regenerate: `make -C gobot proto` (generates Go + Python per Makefile:225)

**Interfaces (produces — exact messages later tasks depend on):**

```proto
// --- sp-1ek0: multi-hop trade-tour optimizer ---
message MarketGoodSnapshot {
  string waypoint_symbol = 1;
  string system_symbol = 2;
  string good_symbol = 3;
  int32 ask = 4;            // what we pay to buy (sellPrice in Go domain)
  int32 bid = 5;            // what market pays us (purchasePrice)
  int32 trade_volume = 6;
  string supply = 7;        // SCARCE..ABUNDANT
  string activity = 8;      // WEAK..RESTRICTED
  int64 observed_at_unix = 9;
}
message TourShip {
  string ship_symbol = 1;
  string current_waypoint = 2;
  string current_system = 3;
  int32 hold_capacity = 4;
  int32 fuel_current = 5;
  int32 fuel_capacity = 6;
  int32 engine_speed = 7;
  repeated TourCargoItem cargo = 8;
}
message TourCargoItem { string good_symbol = 1; int32 units = 2; }
message TourConstraints {
  int32 max_hops = 1;              // default 6
  int64 max_spend = 2;
  int32 min_margin_per_unit = 3;
  int64 working_capital_reserve = 4;
  repeated string allowed_systems = 5;   // <=2, gate-adjacent (maxTourSystems)
  int32 max_snapshot_age_minutes = 6;    // 75
  string expected_model_version = 7;     // "1@<era>"; mismatch -> error
}
message TourTrade {
  string good_symbol = 1;
  int32 units = 2;
  bool is_buy = 3;
  int32 expected_unit_price = 4;   // curve-adjusted
}
message TourLeg {
  string waypoint_symbol = 1;
  string system_symbol = 2;
  repeated TourTrade trades = 3;
  int64 projected_leg_profit = 4;
  int32 travel_seconds_from_prev = 5;
}
message RejectedTour { string summary = 1; string reason = 2; }
message OptimizeTradeTourRequest {
  repeated MarketGoodSnapshot snapshot = 1;
  TourShip ship = 2;
  TourConstraints constraints = 3;
}
message OptimizeTradeTourResponse {
  bool feasible = 1;
  string infeasible_reason = 2;
  repeated TourLeg legs = 3;
  int64 projected_profit = 4;
  double projected_credits_per_hour = 5;
  repeated RejectedTour top_rejected = 6;
  string model_version = 7;
}
```

And in `service RoutingService`: `rpc OptimizeTradeTour(OptimizeTradeTourRequest) returns (OptimizeTradeTourResponse);`

- [ ] Steps: append proto → `make -C gobot proto` → `go build ./...` green (generated Go compiles) → Python `generated/` refreshed → commit (`feat(proto): OptimizeTradeTour messages (sp-1ek0 P1a)`).

### Task 5: Solver — tranche pricing + LP + beam (pure module)

**Files:**
- Create: `gobot/services/routing-service/utils/tour_solver.py`
- Test: `gobot/services/routing-service/tests/test_tour_solver.py`

**Interfaces:**
- Consumes: artifact dict (Task 3 schema).
- Produces: `solve_tour(snapshot: list[dict], ship: dict, constraints: dict, model: dict) -> dict` where dicts mirror the proto fields 1:1 (snake_case). Internal pieces, each unit-tested: `tranche_prices(quote, trade_volume, tier, model, is_buy, max_units)` → list of (units, unit_price) with geometric decay per tradeVolume-sized tranche; `score_sequence(seq, ...)` → greedy tranche allocation (buy cheapest tranches, sell into best-bid tranches, hold-capacity flow-conserving, spend-capped) returning (profit, trades-by-leg); `beam_sequences(...)` → up to `BEAM_WIDTH = 50` hop sequences (start at ship's waypoint; neighbors = markets in allowed systems; ≤ `MAX_HOPS`; ≤ 2 systems; travel seconds = euclidean-free simplification: `travel_seconds(a, b)` uses a caller-supplied symmetric matrix built by the handler from the existing routing engine).
- Greedy-then-LP note (spec solver decision A): tranche-greedy IS the LP solution here because tranche marginal profits are independent once buy/sell tranches are enumerated per leg pair and capacity is the only coupling — implement greedy over sorted marginal-profit (buy-tranche, sell-tranche) pairings with capacity/spend bookkeeping; document this equivalence in the module docstring. (If a future case breaks independence, swap in OR-Tools GLOP behind the same function signature.)

- [ ] **Step 1: Failing tests** (three core behaviors)

```python
# gobot/services/routing-service/tests/test_tour_solver.py
from utils.tour_solver import tranche_prices, solve_tour

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}

def snap(wp, sys_, good, ask, bid, tv=20, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=9_999_999_999)

def test_tranche_prices_decay_sell_side():
    t = tranche_prices(quote=1000, trade_volume=20, tier="LIMITED|WEAK",
                       model=MODEL, is_buy=False, max_units=60)
    assert t == [(20, 1000), (20, 900), (20, 810)]

def test_solver_splits_sells_across_two_sinks():
    # 80u to sell; each sink absorbs 40u near-quote before deep decay → split beats dump
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),   # buy here
        snap("B", "S1", "G", ask=999, bid=200, tv=40),  # sink 1
        snap("C", "S1", "G", ask=999, bid=195, tv=40),  # sink 2
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"]
    sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
             for t in l["trades"] if not t["is_buy"]]
    assert ("B", 40) in sells and ("C", 40) in sells  # split, not 80-dump at B

def test_solver_respects_two_system_cap():
    snapshot = [snap("A", "S1", "G", 100, 90), snap("B", "S2", "G", 999, 300),
                snap("C", "S3", "G", 999, 400)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=50_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1", "S2", "S3"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    systems = {l["system_symbol"] for l in out["legs"]}
    assert len(systems) <= 2  # maxTourSystems enforced even when 3 allowed
```

- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement `tour_solver.py`** — module layout (write in this order, each function ≤ ~40 lines): `MAX_TOUR_SYSTEMS = 2`, `MAX_HOPS_DEFAULT = 6`, `BEAM_WIDTH = 50`; `tranche_prices` (geometric decay/growth per tranche from the tier entry, default decay 0.9/growth 1.1 with a logged `"tier-missing"` flag when the tier isn't in the artifact — conservative defaults, never quote-flat); `score_sequence` (walk legs; at each leg enumerate buy tranches from that market and sell tranches for goods held, pair by max marginal profit, apply hold/spend/reserve bookkeeping; returns profit, trades, spend); `beam_sequences` (BFS from start; candidate next hops ranked by best single-good optimistic spread from current holdings/snapshot; prune to BEAM_WIDTH by optimistic bound; enforce ≤2 distinct systems + max_hops); `solve_tour` (filter stale rows; verify `expected_model_version == f"{model['fit_version']}@{model['era']}"` else infeasible with reason `model_version_mismatch`; run beam; LP/greedy-score top 20 sequences fully; pick best by profit ÷ tour seconds; assemble response dict incl. top-3 rejected with reasons; infeasible when best profit ≤ 0 with reason `no_profitable_tour`). Travel seconds: caller passes `travel_seconds(a, b)` via `constraints["_travel_fn"]` if present, else a flat 300s inter-market / 1800s inter-system default (named consts) — the handler supplies the real fn (Task 6).
- [ ] **Step 4: Run — 3 PASS.** Also add + run one property test: over a randomized snapshot (seeded), assert total buys ≤ hold capacity at all times and spend ≤ max_spend (write it in the same file as `test_solver_property_capacity_and_spend`).
- [ ] **Step 5: Commit** — `feat(planner): tour solver — tranche curves + beam + greedy-LP (sp-1ek0 P1a)`.

### Task 6: Handler wiring + golden test

**Files:**
- Create: `gobot/services/routing-service/handlers/tour_handler.py`
- Modify: `gobot/services/routing-service/server/main.py` (register method — same servicer class; follow how `handlers/routing_handler.py` is registered)
- Test: `gobot/services/routing-service/tests/test_tour_handler.py`

**Interfaces:**
- Produces: `RoutingServiceHandler.OptimizeTradeTour(request, context) -> OptimizeTradeTourResponse` (add the method to the existing servicer class in `routing_handler.py` OR a mixin imported there — keep ONE servicer registered). Loads the artifact once at server start from `model_artifacts/market_model.json` (path relative to service root; missing artifact → every call returns `feasible=false, infeasible_reason="model_artifact_missing"` — fail loud, no silent fallback). Converts proto↔dict, supplies `_travel_fn` from the existing `ORToolsRoutingEngine` distance data when waypoint coordinates are present in the request systems (else the flat defaults), calls `solve_tour`, logs one line per call: `tour: feasible=<bool> legs=<n> cph=<x> model=<ver>`.
- Golden test: fixed 6-market snapshot (the Task 5 split-sell scenario extended with a second system) → assert the exact chosen leg sequence and that `top_rejected` has ≤3 entries with non-empty reasons.

- [ ] Steps: failing golden test → implement handler + registration → run full Python suite (`./venv/bin/python -m pytest tests/ model/tests/ -v`, all PASS) → restart the routing service locally (`./run.sh` or the managed binary `bin/routing-service`) and confirm the server boots with the artifact log line → commit (`feat(planner): OptimizeTradeTour handler + golden test (sp-1ek0 P1a)`).

**P1a gate:** land Tasks 4-6 via captain-gate as one branch (`sp-1ek0-p1a`). Full `go build ./...` must pass (regenerated Go proto) and the Python suite is run manually pre-commit (gate doesn't run Python — paste the pytest summary into the merge report).

---

# PHASE P1b — Go executor (opus tier; bead under sp-1ek0; AFTER P1a merges)

### Task 7: Go domain types + routing-client method

**Files:**
- Create: `gobot/internal/domain/routing/tour.go`
- Modify: `gobot/internal/adapters/routing/grpc_routing_client.go` (add method; follow `PartitionFleet` at :194)
- Modify: `gobot/internal/adapters/routing/mock_routing_client.go` (deterministic fake)
- Test: extend `gobot/internal/adapters/routing/` existing test file pattern

**Interfaces (produces):**

```go
// domain/routing/tour.go
type TourGoodSnapshot struct{ Waypoint, System, Good, Supply, Activity string; Ask, Bid, TradeVolume int; ObservedAt time.Time }
type TourShipState struct{ ShipSymbol, CurrentWaypoint, CurrentSystem string; HoldCapacity, FuelCurrent, FuelCapacity, EngineSpeed int; Cargo map[string]int }
type TourConstraints struct{ MaxHops, MinMarginPerUnit, MaxSnapshotAgeMinutes int; MaxSpend, WorkingCapitalReserve int64; AllowedSystems []string; ExpectedModelVersion string }
type TourTrade struct{ Good string; Units, ExpectedUnitPrice int; IsBuy bool }
type TourLeg struct{ Waypoint, System string; Trades []TourTrade; ProjectedLegProfit int64; TravelSecondsFromPrev int }
type TourPlan struct{ Feasible bool; InfeasibleReason string; Legs []TourLeg; ProjectedProfit int64; ProjectedCreditsPerHour float64; ModelVersion string; TopRejected []string }
// RoutingClient interface gains:
OptimizeTradeTour(ctx context.Context, snapshot []TourGoodSnapshot, ship TourShipState, cons TourConstraints) (*TourPlan, error)
```

- [ ] Steps: failing conversion test (Go struct → pb → Go struct roundtrip on one leg) → implement client + mock (mock returns a configurable canned plan) → package tests green → commit.

### Task 8: Snapshot assembler

**Files:**
- Create: `gobot/internal/application/trading/services/tour_snapshot.go` (+ `_test.go`)

**Interfaces:**
- Produces: `BuildTourSnapshot(ctx, marketRepo, systems []string, playerID int) ([]routing.TourGoodSnapshot, error)` — reuses the SAME repository listing call the lane scanner uses (`collectSystemListings`, `run_trade_route_coordinator.go:806` — grep it and call the identical repo method per system), mapping `TradeGoodData{Symbol, Supply, Activity, SellPrice→Ask, PurchasePrice→Bid, TradeVolume}` + market `LastUpdated→ObservedAt`. Excludes rows older than the trading package's existing `maxListingAge` const (import it; do NOT redeclare 75).

- [ ] Steps: failing test with a fake repo (3 rows, one stale → 2 in snapshot, field mapping exact) → implement → green → commit.

### Task 9: Telemetry table + repository

**Files:**
- Modify: `gobot/internal/adapters/persistence/models.go` (add model + AllModels entry — follow SpendReservationModel/w3he idiom)
- Create: `gobot/internal/adapters/persistence/tour_telemetry_repository.go` (+ `_test.go`)

**Interfaces (produces):**

```go
type TourLegTelemetryModel struct {
  ID uint `gorm:"primaryKey;autoIncrement"`
  TourID string `gorm:"index;not null"`   // container id
  ShipSymbol string `gorm:"not null"`
  LegIndex int `gorm:"not null"`
  Waypoint string `gorm:"not null"`
  Good string `gorm:"not null"`
  IsBuy bool
  PlannedUnits, RealizedUnits int
  PlannedUnitPrice, RealizedUnitPrice int
  PlannedAt, RealizedAt time.Time
  PlayerID int `gorm:"index;not null"`
}
func (TourLegTelemetryModel) TableName() string { return "tour_leg_telemetry" }
// Repository: RecordLeg(ctx, model) error ; ListByPlayer(ctx, playerID, since time.Time) ([]TourLegTelemetryModel, error)
```

- [ ] Steps: failing repo test (sqlite, record 2 legs → ListByPlayer returns both ordered) → implement → green → commit.

### Task 10: Executor — `RunTourCoordinatorHandler`

**Files:**
- Create: `gobot/internal/application/trading/commands/run_tour_coordinator.go` (+ `_test.go`)

**Interfaces:**
- Consumes: `RoutingClient.OptimizeTradeTour` (Task 7), `BuildTourSnapshot` (Task 8), telemetry repo (Task 9), and the arb coordinator's proven primitives — grep `run_arb_coordinator.go` and reuse the SAME `travel()`/dock/purchase/sell helpers and `spendFloorBreached` mirror it uses (do not fork them).
- Produces: `RunTourCoordinatorCommand{ShipSymbol string; PlayerID int; MaxHops int; MaxSpend int64; MinMargin int; ReplanLimit int; AgentSymbol string}` and `Handle(ctx, request) (common.Response, error)`.

Core loop (write exactly this control flow; constants `tourPriceTolerancePct = 15`, `tourMaxReplansDefault = 2`):
1. Assemble snapshot (ship's system + gate neighbors with fresh data, capped by allowedSystems) → plan := OptimizeTradeTour. `!plan.Feasible` → return structured no-op ("tour unavailable: <reason>") — success=false is NOT set; it is a clean non-crash exit like vwhi's park (no phantom completion: set result field, return nil error, and ensure the runner's tour completion signal mirrors the vwhi parked pattern).
2. Per leg: `travel()` to leg.Waypoint → dock → live-read the market → for each trade: if `|live − planned|/planned × 100 > tourPriceTolerancePct` → mark leg degraded; else execute (buys: spend-floor live check first, cumulative spend ≤ MaxSpend across ALL retries — count from cargo actuals like 5nqx; sells: tranche ≤ tradeVolume, ladder-breaker semantics) → RecordLeg telemetry (planned + realized).
3. Leg degraded → re-plan once from current position/cargo (decrement replan budget); budget exhausted → sell what's profitably sellable at plan-verified prices, then STOP.
4. Exit: any tour-bought cargo still aboard → return stranded-cargo FAILURE (error → runner signals success=false; message carries good/units/location). Otherwise success with per-leg P&L summary in the log message text.

- [ ] **Step 1: Failing tests** (fake mediator/market/routing-client — reuse the zvMediator-style fakes from `run_arb_coordinator_test.go`; write these four):

```go
func TestTour_ExecutesLegsAndRecordsTelemetry(t *testing.T)      // happy 2-leg plan: buys+sells executed, 4 telemetry rows, success
func TestTour_DegradedLegTriggersSingleReplan(t *testing.T)       // leg-2 live bid 30% under plan → OptimizeTradeTour called exactly twice
func TestTour_PlannerDownFailsOpenCleanly(t *testing.T)           // client err → "tour unavailable", no trades, no crash, no telemetry
func TestTour_StrandedCargoReportsFailure(t *testing.T)           // sell leg absorbs half → err non-nil, message has good+units+waypoint
```

- [ ] **Step 2: FAIL** → **Step 3: implement** → **Step 4: trading package green (incl. all existing tests)** → **Step 5: commit.**

### Task 11: Container ops + registry + daemon RPC + CLI

**Files:**
- Create: `gobot/internal/adapters/grpc/container_ops_tour.go` — copy the SHAPE of `container_ops_arb.go` (StartArbRun): one-shot 0/1 iteration, atomic claim op="trade", claim-release-on-death, recovery-safe launch config carrying all knobs
- Modify: `gobot/internal/adapters/grpc/command_factory_registry.go` — register `"tour_run"`
- Modify: `gobot/pkg/proto/daemon/daemon.proto` — `rpc StartTourRun(StartTourRunRequest) returns (StartTourRunResponse)` with fields mirroring RunTourCoordinatorCommand (+ regen)
- Modify: `gobot/internal/adapters/grpc/daemon_service_impl.go`, `gobot/internal/adapters/cli/daemon_client.go`
- Create: `gobot/internal/adapters/cli/workflow_tour_run.go` — `spacetraders workflow tour-run --ship S [--max-hops 6 --max-spend 0 --min-margin 0 --replan-limit 2] --agent A`; `--max-spend 0` → default 25% of live treasury (GetAgent at launch, computed CLI-side, value persisted into the config); prints container id
- Test: registry recovery test (a persisted `tour_run` config rebuilds the command — follow the arb_run registry test)

- [ ] Steps: registry test RED → implement chain → `go build ./...` + grpc/cli package tests green → commit.

### Task 12: Graduation report

**Files:**
- Create: `gobot/internal/adapters/cli/tour_report.go` (+ test in cli package)

**Interfaces:**
- `spacetraders tour report --agent A [--since 168h]` prints exactly three gate metrics from `tour_leg_telemetry` + ledger: (1) completed tours + guard violations count (violations = stranded-failure exits, from container rows where type=TOUR_RUN and status=FAILED), (2) tour $/hr vs the hull's trailing single-lane $/hr (ledger operation_type comparison — tours stamp `operation_type="tour"` on their ledger writes via the standard cargo-tx path metadata), (3) median |planned−realized|/planned unit-price error %. Output ends with `GATE: PASS/FAIL (need: 10 tours, >=1.5x, <=15%)`.

- [ ] Steps: failing test (seed telemetry rows → exact numbers + FAIL verdict) → implement → green → commit.

**P1b gate:** land Tasks 7-12 as one branch (`sp-1ek0-p1b`) via captain-gate. Do NOT deploy — harbormaster deploys and the captain flies the first supervised tour.

---

## Self-review (done at write time)

- **Spec coverage:** model+validation (T1-3), RPC+solver+2-system cap+age-cap (T4-6), plan-binds/live-verify/replan/fail-open/stranded-failure (T10), telemetry (T9), knobs+25%-default (T11), gate report (T12), era-in-artifact (T3). Absorption reservations + autonomous circuit = P2 (explicitly out of plan, per spec).
- **Placeholders:** none — every step carries code or an exact existing-file template reference with the delta named.
- **Type consistency:** proto snake_case ↔ Python dicts 1:1; Go types in T7 are the single source for T8/T10/T12 names.

# Tour-Path Recovery-Externality Pricing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Charge each planned sell tranche for the recovery burden it imposes on the fleet, so trade hulls stop converging on the same sinks.

**Architecture:** The tour solver's greedy pairing loop selects on raw `margin`. We subtract a recovery-externality cost from the selection key, scaled by the activity-fitted recovery half-life already loaded but never consumed. Capacity netting (`net_absorption`) and past crush (live quote) are untouched — the new term prices only *future* crush, a third disjoint account.

**Tech Stack:** Python (routing service, gRPC :50051), Go (daemon), Postgres, pytest + `go test`.

**Spec:** `docs/superpowers/specs/2026-07-25-tour-path-externality-pricing-design.md`
**Bead:** `sp-ircfy` (P1, shipwright)

## Global Constraints

- **No feature flags** (standing Admiral policy). Ship ARMED at a fitted default. Revert = `externality_weight: 0` + restart.
- **Money guards fail closed and are never weakened** (RULINGS #4). The externality term is an *objective* term and fails OPEN to current behaviour; the A-cap bounds buy commitment and fails CLOSED.
- **One definition of every fitted table.** Do not redeclare the recovery table in Python if it already exists in Go, or vice versa (the `sp-4ki1x` two-cap-tables defect).
- **All code lands via worktree → `captain-gate` → main** (RULINGS #13). Never merge by hand.
- **Protected paths — never modify:** `gobot/internal/captain/**`, `cmd/captain-gate/**`, `city/agents/**`.
- **Deploy order:** routing service FIRST, then daemon (the `sp-4ki1x` precedent for a Go+Python spanning change).
- **Byte-identical baseline:** with `externality_weight = 0`, every plan must be identical to today's. This is the regression fence for Task 2.

---

### Task 1: Re-fit the recovery table for the current era (BLOCKING)

The artifact at `services/routing-service/model_artifacts/market_model.json` is stamped
`era: torwind-2026-07-05`; the live era is `torwind-2026-07-19`. PLAYBOOK §12 rules last era's
coefficients FALSE PRIORS. Sample counts are also thin: `STRONG n_series=1`, `GROWING n_series=3`,
untagged `n_series=3`; only `RESTRICTED` (22) and `WEAK` (20) are meaningfully fitted.

**Pricing on a stale, thinly-fitted table would make the externality charge arbitrary for exactly
the fast-moving activities where it matters most.** This task must complete before Task 2 arms.

**Files:**
- Run: `services/routing-service/model/calibrate.py`
- Modify: `services/routing-service/model_artifacts/market_model.json` (regenerated output)
- Test: `services/routing-service/model/tests/test_fit.py` (existing `test_fit_recovery_half_life`)

**Interfaces:**
- Produces: a `market_model.json` with `era == "torwind-2026-07-19"` and a `recovery` map keyed by activity (`""`, `GROWING`, `RESTRICTED`, `STRONG`, `WEAK`), each `{half_life_minutes, n_series}`.

- [ ] **Step 1: Record the current artifact as the comparison baseline**

```bash
cd gobot/services/routing-service
cp model_artifacts/market_model.json /tmp/market_model.era0705.json
python3 -c "import json;a=json.load(open('/tmp/market_model.era0705.json'));print(a['era'],a['fit_version']);print({k:(round(v['half_life_minutes'],1),v['n_series']) for k,v in a['recovery'].items()})"
```

Expected: `torwind-2026-07-05 2` and the five tiers with the thin `n_series` values above.

- [ ] **Step 2: Re-run the calibration against the current era**

```bash
cd gobot/services/routing-service
python3 model/calibrate.py
```

Expected: `artifact written: N impact tiers, M recovery tiers`.

- [ ] **Step 3: Verify the era stamp advanced and record sample counts**

```bash
python3 -c "import json;a=json.load(open('model_artifacts/market_model.json'));print(a['era']);print({k:(round(v['half_life_minutes'],1),v['n_series']) for k,v in a['recovery'].items()})"
```

Expected: era is `torwind-2026-07-19`.

**Decision gate — record the answer on `sp-ircfy` before continuing:** for every activity tier with
`n_series < 5`, the fitted half-life is not trustworthy. Task 2 Step 3 must fall back to the pooled
half-life for those tiers. Write down which tiers are thin.

- [ ] **Step 4: Run the existing fit tests**

Run: `cd gobot/services/routing-service && python3 -m pytest model/tests/test_fit.py -v`
Expected: PASS, including `test_fit_recovery_half_life`.

- [ ] **Step 5: Commit**

```bash
git add services/routing-service/model_artifacts/market_model.json
git commit -m "fit: re-calibrate market model for era torwind-2026-07-19 (recovery half-lives were era-07-05 false priors)"
```

---

### Task 2: Externality charge in the solver's selection key (CORE)

**Files:**
- Modify: `gobot/services/routing-service/utils/tour_solver.py` (greedy pairing loop, the `key = (margin, ...)` site ~line 825; module docstring ~line 78 documents this as the pending phase-2 work)
- Test: `gobot/services/routing-service/tests/test_tour_solver_externality.py` (create)

**Interfaces:**
- Consumes: the `recovery` map from Task 1's artifact; the existing per-pairing locals `good`, `j`, `units`, `sell_price`, `margin`, and `markets[seq[j]]["goods"][good]["trade_volume"]`.
- Produces: `externality_cost_per_unit(activity, units, trade_volume, sell_price, weight, recovery_tbl) -> float`, and a selection key that ranks on `margin - externality_cost_per_unit(...)`.

**Design note for the implementer:** the charge must be **per unit** so it is commensurable with
`margin` (which is `sell_price - buy_price`, per unit). Ranking on a per-tranche total against a
per-unit margin would silently bias against large tranches.

- [ ] **Step 1: Write the failing tests**

Create `gobot/services/routing-service/tests/test_tour_solver_externality.py`:

```python
import pytest
from utils.tour_solver import externality_cost_per_unit

RECOVERY = {
    "":           {"half_life_minutes": 1074.3, "n_series": 3},
    "GROWING":    {"half_life_minutes": 180.7,  "n_series": 3},
    "RESTRICTED": {"half_life_minutes": 413.7,  "n_series": 22},
    "WEAK":       {"half_life_minutes": 279.1,  "n_series": 20},
}


def test_zero_weight_is_byte_identical_baseline():
    """weight=0 must charge nothing — the regression fence for the whole change."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 0.0, RECOVERY) == 0.0


def test_longer_half_life_costs_more():
    """RESTRICTED (413.7min) must cost more than WEAK (279.1min), all else equal."""
    slow = externality_cost_per_unit("RESTRICTED", 100, 50, 1000, 1.0, RECOVERY)
    fast = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY)
    assert slow > fast > 0


def test_deeper_tranche_costs_more_per_unit():
    """Crush scales with how far past trade_volume the tranche reaches."""
    shallow = externality_cost_per_unit("WEAK", 50, 50, 1000, 1.0, RECOVERY)
    deep = externality_cost_per_unit("WEAK", 200, 50, 1000, 1.0, RECOVERY)
    assert deep > shallow


def test_unknown_activity_falls_back_to_pooled_not_crash():
    """Fail OPEN: an unmapped activity must not raise and must not price as free."""
    cost = externality_cost_per_unit("NOT_A_TIER", 100, 50, 1000, 1.0, RECOVERY)
    assert cost > 0


def test_missing_recovery_table_fails_open_to_zero():
    """Unreadable table degrades to today's behaviour (no charge), never an exception."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, None) == 0.0
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, {}) == 0.0


def test_zero_trade_volume_does_not_divide_by_zero():
    assert externality_cost_per_unit("WEAK", 100, 0, 1000, 1.0, RECOVERY) == 0.0


def test_uses_the_passed_fitted_decay_not_the_fallback():
    """A shallow-decay market (0.98 -> 2% crush) must cost far less than a steep
    one (0.80 -> 20% crush). Pins that the caller's FITTED factor is honoured
    rather than the DEFAULT_SELL_DECAY fallback."""
    shallow = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=0.98)
    steep = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=0.80)
    assert steep > shallow * 5


def test_decay_of_one_means_no_crush_and_no_charge():
    """A market that does not move on a sale imposes no recovery burden."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=1.0) == 0.0
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd gobot/services/routing-service && python3 -m pytest tests/test_tour_solver_externality.py -v`
Expected: FAIL — `ImportError: cannot import name 'externality_cost_per_unit'`.

- [ ] **Step 3: Implement `externality_cost_per_unit`**

Add to `utils/tour_solver.py`, next to `net_absorption` (~line 407) so the two depth-related
helpers sit together:

```python
# Reference window that normalises the recovery half-life into a dimensionless
# multiplier. WEAK (279min) is the best-sampled mid tier, so a WEAK sink prices
# near 1.0x and other tiers scale relative to it.
EXTERNALITY_REFERENCE_MINUTES = 279.0


def externality_cost_per_unit(activity, units, trade_volume, sell_price,
                              weight, recovery_tbl, sell_decay=None):
    """Per-unit charge for the FUTURE recovery burden this sell tranche imposes
    on the fleet (sp-78ai part (b)).

    Three disjoint accounts, so this cannot double-count:
      - PAST crush        -> already in the live quote
      - PLANNED depth     -> netted as capacity by net_absorption()
      - FUTURE crush      -> THIS term

    Fails OPEN (returns 0.0 = today's behaviour) on any unreadable input: this is
    an objective term, not a spend guard (RULINGS #4 governs spend guards).
    """
    if not recovery_tbl or weight <= 0 or trade_volume <= 0 or units <= 0:
        return 0.0

    tier = recovery_tbl.get(activity)
    if tier is None:
        # Unknown/thin tier -> pooled untagged half-life. Never free, never a crash.
        tier = recovery_tbl.get("")
    if not tier:
        return 0.0

    half_life = tier.get("half_life_minutes") or 0.0
    if half_life <= 0:
        return 0.0

    # Crush per tranche = 1 - the fitted sell-decay factor the tranche builder
    # already applies as `price *= factor`. DEFAULT_SELL_DECAY (0.9) is this
    # module's existing fallback for an unfitted tier -> 10% crush per tranche.
    decay = DEFAULT_SELL_DECAY if sell_decay is None else sell_decay
    crush_per_tranche = max(0.0, 1.0 - decay)

    tranches = units / float(trade_volume)
    recovery_multiple = half_life / EXTERNALITY_REFERENCE_MINUTES
    return weight * tranches * recovery_multiple * sell_price * crush_per_tranche
```

**Implementer note — reuse the fitted decay, do not hardcode 0.9.** The caller must pass the SAME
per-tier sell-decay factor the tranche builder resolves from the artifact's `impact` map (keys are
`SUPPLY|ACTIVITY`, e.g. `ABUNDANT|GROWING`); `DEFAULT_SELL_DECAY` is only the unfitted-tier
fallback. Read the tranche-building code around `price *= factor` and thread the same resolved
factor in. Two different decay values for the same market would be the `sp-4ki1x` two-tables defect
in a new place.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd gobot/services/routing-service && python3 -m pytest tests/test_tour_solver_externality.py -v`
Expected: PASS (8 tests).

- [ ] **Step 5: Wire the charge into the greedy selection key**

In the pairing loop in `utils/tour_solver.py`, at the `key = (margin, -j, ...)` site (~line 825),
rank on the externality-adjusted margin. DEPOSIT pairings are synthetic transfers with no market
crush and stay exempt, matching the existing `kind != "deposit"` treatment:

```python
            if kind == "deposit":
                eff_margin = margin
            else:
                eff_margin = margin - externality_cost_per_unit(
                    markets[seq[j]]["goods"][good].get("activity"),
                    units,
                    markets[seq[j]]["goods"][good]["trade_volume"],
                    sell_price,
                    externality_weight,
                    recovery_tbl,
                )
            key = (eff_margin, -j, -(i if i is not None else -1))
```

`externality_weight` and `recovery_tbl` must be threaded in as solver parameters with defaults
`0.0` and `None` so every existing caller and test stays byte-identical.

**Note:** `margin < min_margin` continues to gate on the RAW margin, not `eff_margin` — the
externality changes *preference ordering*, not *eligibility*. Changing eligibility here would
silently tighten a spend gate, which RULINGS #4 forbids as a side effect.

- [ ] **Step 6: Prove the zero-weight path is byte-identical**

Run: `cd gobot/services/routing-service && python3 -m pytest tests/ -v`
Expected: PASS — the entire existing suite, unchanged, with default `externality_weight=0.0`.

- [ ] **Step 7: Commit**

```bash
git add services/routing-service/utils/tour_solver.py services/routing-service/tests/test_tour_solver_externality.py
git commit -m "feat(tour): price the recovery externality of a sell tranche (sp-78ai part b, sp-ircfy)"
```

---

### Task 3: Thread the weight from config through the daemon to the solver

**Files:**
- Modify: `gobot/config.yaml` (`[trade_fleet]` section — sits with `candidate_hop_depth`)
- Modify: `gobot/internal/application/trading/commands/run_tour_coordinator.go` (constraint assembly, near the existing `MaxSnapshotAgeMinutes` set at ~line 1868)
- Modify: `gobot/services/routing-service/handlers/tour_handler.py` (pass `self.tour_model["recovery"]` and the request weight into `solve_tour`)
- Test: `gobot/internal/application/trading/commands/run_tour_coordinator_externality_test.go` (create)

**Interfaces:**
- Consumes: `externality_cost_per_unit` from Task 2; the loaded artifact's `recovery` map.
- Produces: `TourConstraints.ExternalityWeight float64` crossing the wire to `solve_tour`.

- [ ] **Step 1: Write the failing Go test**

Create `run_tour_coordinator_externality_test.go`, mirroring the structure of the existing
`run_tour_coordinator_agecap_backstop_test.go` (read it first for the handler fixture pattern):

```go
func TestExternalityWeightReachesTourConstraints(t *testing.T) {
	h := newTestTourHandler(t)          // same fixture the agecap backstop test uses
	h.externalityWeight = 0.35

	got := h.buildTourConstraints(testPlanState(t))

	if got.ExternalityWeight != 0.35 {
		t.Fatalf("ExternalityWeight = %v, want 0.35", got.ExternalityWeight)
	}
}

func TestExternalityWeightDefaultsToZero(t *testing.T) {
	h := newTestTourHandler(t)          // no weight configured

	got := h.buildTourConstraints(testPlanState(t))

	if got.ExternalityWeight != 0 {
		t.Fatalf("ExternalityWeight = %v, want 0 (byte-identical default)", got.ExternalityWeight)
	}
}
```

**Implementer note:** the exact fixture and constraint-builder names must be taken from the
neighbouring test file. Do not invent them — read `run_tour_coordinator_agecap_backstop_test.go`
and match.

- [ ] **Step 2: Run to verify failure**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestExternalityWeight -v`
Expected: FAIL — compile error, `ExternalityWeight` undefined.

- [ ] **Step 3: Add the field, config plumbing, and proto/constraint pass-through**

Add `ExternalityWeight` to the tour constraints struct, read it from `[trade_fleet]`, and pass it
in the request. Follow the exact path `MaxSnapshotAgeMinutes` already takes (`sp-4ki1x` shipped
that plumbing — mirror it rather than inventing a new route).

Add to `gobot/config.yaml` under `trade_fleet:`:

```yaml
  # sp-ircfy: recovery-externality weight. Charges a sell tranche for the FUTURE
  # recovery burden it imposes on the fleet, so hulls stop converging on one sink.
  # ARMED at the fitted default. Revert = set 0 + restart-daemon (no flag, per
  # standing Admiral policy). Tune DOWN if top-5 sink share falls but net cr/hr
  # also falls (see sp-ircfy validation).
  externality_weight: 0.35
```

**The 0.35 default is a starting estimate, not a fitted value.** Task 5 measures it. If Task 1
found the recovery table thin, start at 0.20 instead and record the choice on `sp-ircfy`.

- [ ] **Step 4: Run to verify pass**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestExternalityWeight -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Run the full Go suite**

Run: `cd gobot && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gobot/config.yaml gobot/internal/application/trading/commands/ gobot/services/routing-service/handlers/tour_handler.py
git commit -m "feat(tour): thread externality_weight config to the solver, armed at fitted default (sp-ircfy)"
```

---

### Task 4: Cross-plan A-cap continuity

Today `sold_this_visit` is rebuilt per plan, so consecutive plans lawfully re-take
`REALIZED_SINK_TRANCHES_PER_VISIT` tranches at the same sink — the D39 ladder rebuilt across plans
(`sp-78ai` §0). The ledger already stores `units_recovering` under decay; this extends its authority
to the cap.

**Files:**
- Modify: `gobot/internal/application/trading/commands/run_tour_coordinator_absorption.go` (`assembleAbsorption`, ~line 366 builds `[]routing.TourMarketAbsorption`)
- Test: `gobot/internal/application/trading/commands/run_tour_coordinator_absorption_test.go` (extend)

**Interfaces:**
- Consumes: the existing `absorption.Ledger` port and `routing.TourMarketAbsorption{}` shape.
- Produces: `units_recovering` that reflects prior plans' realized sink consumption, so `net_absorption` tail-drops those tranches on the next plan.

- [ ] **Step 1: Write the failing test**

```go
func TestPriorPlanConsumptionSuppressesNextPlanDepth(t *testing.T) {
	led := newFakeLedger(t)
	// A prior plan realized 2 tranches at (sink, good) 10 minutes ago.
	led.recordRealized("X1-CD11-A1", "FAB_MATS", 2*tradeVolume, 10*time.Minute)

	view := assembleAbsorptionForTest(t, led)

	got := view.For("X1-CD11-A1", "FAB_MATS")
	if got.UnitsRecovering <= 0 {
		t.Fatalf("UnitsRecovering = %d, want > 0 — prior plan consumption must carry across the plan boundary", got.UnitsRecovering)
	}
}
```

**Implementer note:** the fake-ledger and assemble helpers must match what the existing absorption
test file already provides. Read it first.

- [ ] **Step 2: Run to verify failure**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestPriorPlanConsumption -v`
Expected: FAIL.

- [ ] **Step 3: Carry realized sink consumption into the ledger view under recovery decay**

Record each plan's realized per-sink units into the ledger, and have `assembleAbsorption` include
them in `UnitsRecovering`, decayed by the same half-life Task 2 uses.

**Guard (RULINGS #4): this path FAILS CLOSED.** A ledger read error must degrade to today's
per-plan cap — never to unbounded depth. Assert this explicitly.

- [ ] **Step 4: Run to verify pass, then the full suite**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestPriorPlanConsumption -v && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/application/trading/commands/
git commit -m "feat(tour): carry realized sink consumption across plan boundaries under recovery decay (sp-ircfy)"
```

---

### Task 5: Deploy, arm, and measure

**Files:** none modified — this is the validation gate. Record results on `sp-ircfy`.

- [ ] **Step 1: Land via the gate (RULINGS #13)**

```bash
gobot/bin/captain-gate --repo <root> --worktree <wt> --branch <br> --message "<m>" --provision --merge
```

Then verify the merged SHA's diffstat lists your files (RULINGS #12) — a merge that reports success
with an empty diffstat is the empty-merge incident.

- [ ] **Step 2: Deploy — routing FIRST, then daemon**

This change spans Python and Go. Routing service first, daemon second (the `sp-4ki1x` deploy order).

- [ ] **Step 3: Confirm the model actually loaded**

```bash
grep "market model artifact loaded" gobot/routing.log | tail -2
```

Expected: `recovery_tiers=5` and an era matching `torwind-2026-07-19`.

- [ ] **Step 4: Re-wedge check — construction restart step**

Any daemon restart re-wedges the construction pipeline (`sp-1jpiw`, MECHANICS §8):

```bash
spacetraders construction stop <site> && spacetraders construction start <site> --depth N
```

- [ ] **Step 5: Measure against the pinned baseline after ≥4 clean hours**

Baseline to beat (22:15–01:15Z, 2026-07-25): **34 sink systems, top-3 36.7%, top-5 51.0%.**

```sql
WITH s AS (
  SELECT split_part(metadata->>'waypoint','-',1)||'-'||split_part(metadata->>'waypoint','-',2) AS sys,
         SUM(amount) AS rev
  FROM transactions
  WHERE player_id=4 AND category='TRADING_REVENUE'
    AND timestamp >= '<arm_time>' AND timestamp < '<arm_time + 3h>'
  GROUP BY 1),
r AS (SELECT sys, rev, ROW_NUMBER() OVER (ORDER BY rev DESC) rk, SUM(rev) OVER () tot FROM s)
SELECT COUNT(*) AS sink_systems,
       ROUND(100.0*SUM(rev) FILTER (WHERE rk<=3)/MAX(tot),1) AS top3_pct,
       ROUND(100.0*SUM(rev) FILTER (WHERE rk<=5)/MAX(tot),1) AS top5_pct
FROM r;
```

**Use a 3-hour window to match the baseline exactly.** Unequal windows mechanically distort
concentration and produced a spurious result during design.

**Decision rule:**
- top-5 share **down** AND net cr/hr **≥** baseline → thesis proven, keep armed.
- top-5 share **down** AND net cr/hr **down** → weight too high; halve `externality_weight`, restart, re-measure.
- top-5 share **flat** → weight too low; double it, restart, re-measure.

- [ ] **Step 6: Honesty guard**

Confirm `tour_plan_rate{phase=projected}` vs `{phase=realized}` divergence has not worsened versus
pre-arm. If projected improved while realized did not, the term made the planner optimistic rather
than correct — revert to `externality_weight: 0` and report.

- [ ] **Step 7: Report on the bead and notify (RULINGS #8)**

Record measured numbers on `sp-ircfy`, then mail AND nudge the captain — a deploy the captain does
not know about is a deploy that did not happen.

---

### Task 6: Proactive reposition targeting (spec §4.3) — SEQUENCED AFTER TASK 5

Implements the chosen idle policy: **reposition instead of idle.** Today reposition fires only
reactively — on margin-death, or under 40% of fleet median (`reposition_reach_enabled` /
`reposition_rate_floor_enabled`, both armed in `config.yaml`). A healthy-but-concentrated fleet
never trips either, which is why hulls stay piled on good-but-crowded lanes.

**Why this is sequenced after measurement, not bundled:** it is the largest behavioural change in
the spec (it moves hulls that are currently earning), and bundling it with Tasks 2–4 would make the
Task 5 measurement uninterpretable — if spread improves, we could not attribute it to the pricing
term versus the movement policy. Tasks 2–4 change *preference*; this changes *position*. Measure
the cheap one first.

**Files:**
- Modify: `gobot/internal/application/trading/commands/run_trade_route_coordinator.go` (reposition trigger evaluation)
- Modify: `gobot/config.yaml` (`[trade_fleet]`)
- Test: `gobot/internal/application/trading/commands/run_trade_route_coordinator_reposition_test.go` (extend existing reposition tests)

**Interfaces:**
- Consumes: the absorption ledger view from Task 4 (unreserved depth per `(sink, good)`); the existing `reposition_reach` deadhead-decay (0.85/hop) and per-system anti-herd cap (`reposition_reach_max_hulls_per_system`).
- Produces: a third reposition trigger — *opportunity-relative*, alongside the existing margin-death and rate-floor triggers.

- [ ] **Step 1: Read the existing reposition triggers before changing anything**

Read the two armed triggers and their thrash-safety machinery (dwell 45m, atomic pending-reloc
anti-herd cap, improvement ratchet clamp ≥150, fail-closed on bad median). **The new trigger must
reuse all of it** — a third trigger that bypasses the dwell timer will thrash the fleet.

- [ ] **Step 2: Write the failing test**

```go
func TestConcentratedHullRepositionsTowardUnreservedDepth(t *testing.T) {
	// A hull earning ABOVE the rate floor and with a live margin — neither existing
	// trigger fires — but its best post-externality lane is below fleet median while
	// unreserved depth exists elsewhere.
	h := newTestRepositionHandler(t)
	h.setFleetMedianOpportunity(1000)
	h.setHullBestPostExternalityLane("TORWIND-C0", 400)
	h.setUnreservedDepth("X1-FAR9", 5000)

	got := h.evaluateRepositionTriggers(t.Context(), "TORWIND-C0")

	if !got.ShouldReposition {
		t.Fatal("healthy-but-concentrated hull must reposition on the opportunity trigger")
	}
	if got.Target != "X1-FAR9" {
		t.Fatalf("Target = %q, want X1-FAR9 (highest unreserved depth)", got.Target)
	}
}

func TestOpportunityTriggerRespectsDwellTimer(t *testing.T) {
	h := newTestRepositionHandler(t)
	h.setHullRelocatedAgo("TORWIND-C0", 5*time.Minute) // inside the 45m dwell

	got := h.evaluateRepositionTriggers(t.Context(), "TORWIND-C0")

	if got.ShouldReposition {
		t.Fatal("opportunity trigger must not bypass the 45m dwell timer — thrash guard")
	}
}
```

**Implementer note:** fixture and method names must be taken from the existing reposition test file.
Read it first; do not invent them.

- [ ] **Step 3: Run to verify failure**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestOpportunityTrigger -v`
Expected: FAIL.

- [ ] **Step 4: Implement the opportunity trigger**

Add the third trigger, reusing the existing dwell timer, anti-herd cap, and improvement ratchet.
**Fail closed:** an unreadable median or ledger view must suppress the trigger, never fire it.

Add to `config.yaml` under `trade_fleet:`:

```yaml
  # sp-ircfy Task 6: reposition a hull whose best post-externality lane falls below
  # this fraction of fleet median opportunity, even when it is still earning.
  # Complements the reactive margin-death and rate-floor triggers. 0 = disabled.
  reposition_opportunity_floor_pct: 50
```

- [ ] **Step 5: Run to verify pass, then the full suite**

Run: `cd gobot && go test ./internal/application/trading/commands/ -run TestOpportunityTrigger -v && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/application/trading/commands/ gobot/config.yaml
git commit -m "feat(trade): opportunity-relative reposition trigger — spread healthy-but-concentrated hulls (sp-ircfy)"
```

- [ ] **Step 7: Measure separately from Task 5**

Re-run the Task 5 Step 5 query against a fresh 3-hour window. Attribution requires that Task 5's
measurement has already been recorded — otherwise the two changes are confounded.

---

## Sequencing constraints

- **Task 1 blocks Task 2's arming.** Do not price against era-07-05 half-lives.
- **Do not run the `sp-1cuy6` hop-depth sweep concurrently with Task 5.** Both change lane selection; run serially or the measurement is confounded.
- **Era boundary:** reset 2026-07-26T13:00Z. PLAYBOOK §1 bars arming anything unvalidated inside the final 12h, so Task 5 arming must complete by **~2026-07-26T01:00Z** to leave a measurement window. Past that, land the code disarmed (`externality_weight: 0`) and arm next era.

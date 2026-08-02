"""sp-f1yk W1 — replay-harness structure/wiring tests (seam-free, WAVE-0).

These pin the offline arming gate's PURE, DB-free plumbing that was refactored out of
replay_objective.main():
  * fleet_cph            — the single fleet-$/hr aggregator (one source of truth)
  * summarize            — delegates its per-objective cph to fleet_cph (no dual math)
  * run_case             — threads / omits the sp-syaz max_tour_systems cap key
  * arming_pass          — assembles the two-cap (baseline|candidate) x (profit|rate) matrix
  * arming_verdict       — the deterministic armed:bool gate over that matrix

Most tests are seam-free: the solver is stubbed/monkeypatched, so they depend on neither
sp-y05b (OR-Tools) nor sp-z7ng (placement). The syaz cap-knob (max_tour_systems) is LIVE on
main, so RED#3 is meaningful now.

Two tests drive the REAL merged solver (no stub):
  * test_arming_gate_fires_on_real_replay_solver_data — the end-to-end cap-2-vs-cap-6 x
    profit-vs-rate fleet-$/hr FIRING gate on a reconstructed multi-system replay snapshot.
  * test_closure_ab_reanchors_on_real_replay_solver_data — the sp-g8op closure A/B chained
    open-vs-closed round-trip driven through closure_ab_pass on the real solver (both K-tour
    arms complete; every CLOSED tour re-anchors home while the OPEN arm wanders off it).

The rest of the sp-g8op closure A/B stays unit-pinned WITHOUT the real solver: the
closure_ab_pass ship-advance wiring with a stubbed solve_tour
(test_closure_ab_pass_chains_open_drift_and_closed_reanchor), and the closure_ab_verdict
fleet-$/hr math with hand-built chains — neither exercises the merged solver.
"""
import math
from datetime import datetime
from types import SimpleNamespace

import pytest

# replay_objective reads the ledger through SQLAlchemy, which is a MODEL-PIPELINE
# dependency (requirements-model.txt) and deliberately absent from the pinned service
# venv. Skip rather than error there, so a service-only environment can still run the
# suite to completion instead of failing at collection.
pytest.importorskip("sqlalchemy", reason="model-pipeline dep; see requirements-model.txt")

import replay_objective as ro
from replay_objective import OBJECTIVE_PROFIT, OBJECTIVE_RATE


# --------------------------------------------------------------------------- helpers
def _res(profit, cph, feasible=True):
    """A minimal solver-result dict: exactly the keys plan_seconds / fleet_cph read."""
    return dict(feasible=feasible, projected_profit=profit,
                projected_credits_per_hour=float(cph))


def _case_two_obj(sample, home, hold, profit_res, rate_res):
    """A DEFAULT one_pass-shaped case: objective-keyed `results`."""
    return dict(sample=sample, home=home, hold=hold,
                results={OBJECTIVE_PROFIT: profit_res, OBJECTIVE_RATE: rate_res})


def _case_by_cell(cells):
    """An arming-pass-shaped case: (cap, objective)-keyed `results_by_cell`."""
    return dict(sample="s", home="X1-S1", hold=40, results_by_cell=cells)


# ------------------------------------------------------------- RED: fleet_cph math
@pytest.mark.parametrize("overhead,expected", [
    # two feasible plans; hours = (profit/cph*3600 + overhead)/3600.
    #   r0: 1000cr @ 2000cph -> 1800s -> 0.5h (+oh);  r1: 3000cr @ 1000cph -> 10800s -> 3.0h (+oh)
    (0,    4000.0 / 3.5),          # (1000+3000) / (0.5 + 3.0)
    (3600, 4000.0 / 5.5),          # +1h each: (1000+3000) / (1.5 + 4.0)
])
def test_fleet_cph_known_value(overhead, expected):
    results = [_res(1000, 2000), _res(3000, 1000)]
    assert math.isclose(ro.fleet_cph(results, overhead), expected, rel_tol=1e-9)


def test_fleet_cph_excludes_infeasible_and_zero_time():
    # infeasible and zero-cph rows contribute NOTHING (feasibility / positive-time filter).
    results = [_res(1000, 2000), _res(9999, 0), _res(9999, 500, feasible=False)]
    assert math.isclose(ro.fleet_cph(results, 0), 1000 / 0.5, rel_tol=1e-9)
    assert ro.fleet_cph([], 0) == 0.0
    assert ro.fleet_cph([_res(0, 0)], 0) == 0.0


# ------------------------------------------- RED#1: summarize delegates to fleet_cph
def test_summarize_delegates_to_fleet_cph_and_default_shape():
    cases = [
        _case_two_obj("t1", "H", 40, _res(1000, 2000), _res(1000, 2000)),   # identical
        _case_two_obj("t2", "H", 40, _res(2000, 1000), _res(1500, 3000)),   # diverges
        _case_two_obj("t3", "H", 40, _res(500, 500), _res(400, 800)),       # diverges
    ]
    overhead = 60
    agg, diverged, rate_wins, per_case = ro.summarize(cases, overhead)

    # SINGLE SOURCE OF TRUTH: summarize's per-objective aggregate cph is exactly what
    # fleet_cph computes over the same result lists — the human sanity-check and the
    # machine gate cannot drift (resolves the dual-math finding).
    pp, ph = agg[OBJECTIVE_PROFIT]
    rp, rh = agg[OBJECTIVE_RATE]
    assert pp / ph == ro.fleet_cph([c["results"][OBJECTIVE_PROFIT] for c in cases], overhead)
    assert rp / rh == ro.fleet_cph([c["results"][OBJECTIVE_RATE] for c in cases], overhead)

    # divergence + per-case shape preserved (default profit-vs-rate output invariant).
    assert diverged == 2
    assert rate_wins == 2   # rate choice effectively better in both diverging cases
    assert len(per_case) == 3
    assert set(per_case[0]) == {"sample", "home", "hold", "profit_choice",
                                "rate_choice", "diverged"}
    assert per_case[0]["diverged"] is False
    assert per_case[1]["diverged"] is True
    assert set(per_case[0]["profit_choice"]) == {"profit", "seconds", "cph"}


# --------------------------------------- RED#3: run_case threads/omits the cap key
@pytest.mark.parametrize("cap,expect_present", [(5, True), (2, True), (None, False)])
def test_cap_param_threads_to_constraints(monkeypatch, cap, expect_present):
    captured = {}

    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        captured["cons"] = dict(cons)
        return _res(1, 1)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    snapshot = [dict(waypoint_symbol="X1-S1-A", system_symbol="X1-S1", good_symbol="G",
                     ask=100, bid=90, trade_volume=40, supply="LIMITED", activity="WEAK",
                     observed_at_unix=9_999_999_999)]
    waypoints = [dict(symbol="X1-S1-A", system="X1-S1", x=0, y=0)]

    ro.run_case(snapshot, waypoints, "X1-S1", {"X1-S1"}, 40,
                1_000_000, 0, "1@e", max_tour_systems=cap)

    if expect_present:
        assert captured["cons"]["max_tour_systems"] == cap
    else:
        # None => OMIT the key entirely => byte-identical default DB run (solver
        # resolves absent/0 to MAX_TOUR_SYSTEMS=2 via _effective_tour_systems).
        assert "max_tour_systems" not in captured["cons"]


# --------------------------------- RED#2b: arming_pass assembles the two-cap matrix
def test_arming_pass_assembles_two_cap_matrix(monkeypatch):
    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        cap = cons.get("max_tour_systems")
        # encode the cap it SAW and the objective so the assembly is verifiable.
        return dict(feasible=True, projected_profit=100 * cap,
                    projected_credits_per_hour=1000.0, cap=cap, objective=objective)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [
        SimpleNamespace(waypoint_symbol="X1-S1-A", good_symbol="G", purchase_price=100,
                        sell_price=90, supply="LIMITED", activity="WEAK", trade_volume=40,
                        recorded_at=sample_t),
        SimpleNamespace(waypoint_symbol="X1-S1-B", good_symbol="G", purchase_price=210,
                        sell_price=200, supply="LIMITED", activity="WEAK", trade_volume=40,
                        recorded_at=sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 5, 5)}

    cases = ro.arming_pass([sample_t], rows, {}, coords, [40], "1@e",
                           baseline_cap=2, candidate_cap=6,
                           max_spend=1_000_000, reserve=0)

    assert len(cases) == 1
    cells = cases[0]["results_by_cell"]
    assert set(cells) == {(2, OBJECTIVE_PROFIT), (2, OBJECTIVE_RATE),
                          (6, OBJECTIVE_PROFIT), (6, OBJECTIVE_RATE)}
    for (cap, objective), res in cells.items():
        assert res["cap"] == cap and res["objective"] == objective
    # fleet_cph can read the assembled cells (positive $/hr in every cell).
    assert ro.fleet_cph([cells[(6, OBJECTIVE_RATE)]], 0) > 0


# --------------------------------- RED#2: arming_verdict -- deterministic gate
def _win_cases(n):
    # candidate (6, rate) clearly beats baseline (2, profit); (6, profit) sits between
    # so objective_delta_pct isolates the rate objective at the candidate cap.
    cells = {
        (2, OBJECTIVE_PROFIT): _res(1000, 1000),   # baseline: 1000cr @ 1h -> 1000/hr
        (2, OBJECTIVE_RATE):   _res(1100, 1100),
        (6, OBJECTIVE_PROFIT): _res(1500, 1500),   # candidate-cap profit -> 1500/hr
        (6, OBJECTIVE_RATE):   _res(2000, 2000),   # candidate: 2000/hr
    }
    return [_case_by_cell(dict(cells)) for _ in range(n)]


def test_arming_verdict_win():
    verdict = ro.arming_verdict(_win_cases(30),
                                baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=5.0, min_cases=30)
    assert verdict["armed"] is True
    assert verdict["delta_pct"] > 0
    assert verdict["cases"] == 30
    assert math.isclose(verdict["baseline_cph"], 1000.0, rel_tol=1e-9)
    assert math.isclose(verdict["candidate_cph"], 2000.0, rel_tol=1e-9)
    # sp-db0n: pin the TRUE live-prod baseline the operator must read — cap=2 at the RATE
    # objective (sp-1wp8's launch-path TOUR_SOLVER_OBJECTIVE=rate default), NOT the cap=2
    # PROFIT fail-safe. Traced to the _win_cases (2, RATE)=_res(1100, 1100) cell: at zero
    # overhead that plan is 1100 cr over 1.0 h = 1100 cr/hr. This is the value now surfaced
    # in the --arm console, so ljh5 arms against the config prod ACTUALLY runs today.
    assert math.isclose(verdict["baseline_cap_rate_cph"], 1100.0, rel_tol=1e-9)
    # isolation column present and non-trivial (candidate rate vs candidate-cap profit).
    assert math.isclose(verdict["objective_delta_pct"],
                        (2000.0 - 1500.0) / 1500.0 * 100, rel_tol=1e-9)


@pytest.mark.parametrize("n,min_delta,min_cases,reason", [
    (30, 5.0, 40, "too_few_cases"),        # delta huge, but cases 30 < min 40
    (30, 200.0, 30, "delta_below_min"),    # cases ok, but delta 100% < min 200%
])
def test_arming_verdict_noop(n, min_delta, min_cases, reason):
    verdict = ro.arming_verdict(_win_cases(n),
                                baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=min_delta,
                                min_cases=min_cases)
    assert verdict["armed"] is False, reason


# ------------------------------- sp-ljh5: the arm relies on the TRUE live-prod baseline
def test_arming_verdict_relies_on_true_live_prod_baseline():
    """sp-ljh5 (post sp-db0n): the arm decision must measure the fleet-$/hr win against
    the TRUE live-prod reference — cap=2 at the RATE objective (sp-1wp8's launch default,
    baseline_cap_rate_cph) — NOT the cap=2 PROFIT in-code fail-safe (a config prod does
    NOT run). A candidate that crushes the fail-safe but barely beats what prod ACTUALLY
    runs today must stay DISARMED, so the fleet is never re-objectived on a phantom win."""
    cells = {
        (2, OBJECTIVE_PROFIT): _res(1000, 1000),   # in-code fail-safe (NOT the deployed default)
        (2, OBJECTIVE_RATE):   _res(2000, 2000),   # TRUE live-prod: cap-2 RATE (sp-1wp8 launch)
        (6, OBJECTIVE_PROFIT): _res(1500, 1500),
        (6, OBJECTIVE_RATE):   _res(2050, 2050),   # candidate: +105% vs fail-safe, +2.5% vs true live
    }
    cases = [_case_by_cell(dict(cells)) for _ in range(30)]
    verdict = ro.arming_verdict(cases, baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=5.0, min_cases=30)
    # The fail-safe delta is huge (+105%) — a gate that read it would have armed here.
    assert verdict["delta_pct"] > 100
    # The gating delta is measured against the true live-prod baseline (cap-2 RATE = 2000).
    assert math.isclose(verdict["baseline_cap_rate_cph"], 2000.0, rel_tol=1e-9)
    assert math.isclose(verdict["true_live_delta_pct"],
                        (2050.0 - 2000.0) / 2000.0 * 100, rel_tol=1e-9)
    assert verdict["true_live_delta_pct"] < 5.0
    # ljh5: the arm relies on baseline_cap_rate_cph, so a +2.5% true-live gain stays DISARMED.
    assert verdict["armed"] is False


# ================================== W4: real-solver arming-gate FIRING (sp-f1yk) ======
def _replay_row(waypoint, good, purchase_price, sell_price, sample_t):
    """A market_price_history row shaped for reconstruct_snapshot: ask=purchase_price (what we
    PAY), bid=sell_price (what we RECEIVE) — the live orientation, sp-en5h7/sp-2ehd7. A SOURCE
    (buyable) needs purchase_price>0/sell_price=0 (-> ask>0); a SINK (sellable) needs
    sell_price>0/purchase_price=0 (-> bid>0)."""
    return SimpleNamespace(waypoint_symbol=waypoint, good_symbol=good,
                           purchase_price=purchase_price, sell_price=sell_price,
                           supply="LIMITED", activity="WEAK", trade_volume=40,
                           recorded_at=sample_t)


@pytest.mark.parametrize("sequencer", [None, "ortools"])
def test_arming_gate_fires_on_real_replay_solver_data(monkeypatch, sequencer):
    """sp-f1yk W4 — the end-to-end fleet-$/hr arming gate FIRES on the REAL merged solver
    (no stub). arming_pass drives the actual solve_tour over the cap-2-vs-cap-6 x profit-vs-
    rate matrix on a reconstructed multi-system replay snapshot; arming_verdict emits the
    armed:bool. sp-y05b (OR-Tools) is merged, so the candidate cap-6 cell is solved on the
    real longer-tour path — ortools where installed, and beam (byte-identical default-safe,
    and optimal on this single-visit instance) otherwise. If the merged cap-6 solve ever
    regresses to infeasible, `cases` collapses to 0 and this FAILS, surfacing the integration
    bug instead of hiding it (per the W4 instruction, the assertion is never weakened)."""
    if sequencer == "ortools":
        pytest.importorskip("ortools")
        monkeypatch.setenv("TOUR_SOLVER_SEQUENCER", "ortools")

    # A real fitted single-tier model (resolves to version "1@e") — the harness/closure MODEL.
    monkeypatch.setattr(ro, "MODEL", {
        "fit_version": 1, "era": "e",
        "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                    "buy_growth_per_step": 1.1, "n_obs": 9}},
        "recovery": {}}, raising=False)
    version = "1@e"
    sample_t = datetime(2026, 7, 16, 12, 0, 0)

    # Two reachable systems, each with a profitable single-visit arb. RATE prices a fast
    # small tour far above PROFIT here, so the two cap-2 baselines (rate vs profit) are
    # sharply distinct — the discriminator that catches a profit-baseline regression.
    rows = [
        _replay_row("X1-S1-A", "G", purchase_price=100, sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=0,   sell_price=260, sample_t=sample_t),
        _replay_row("X1-S2-C", "H", purchase_price=90,  sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S2-D", "H", purchase_price=0,   sell_price=240, sample_t=sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 10, 0),
              "X1-S2-C": ("X1-S2", 200, 0), "X1-S2-D": ("X1-S2", 210, 0)}
    neighbors = {"X1-S1": {"X1-S2"}, "X1-S2": {"X1-S1"}}

    cases = ro.arming_pass([sample_t], rows, neighbors, coords, [80], version,
                           baseline_cap=2, candidate_cap=6,
                           max_spend=2_000_000, reserve=0)
    # the REAL merged solver produced the full cap-2 AND cap-6 x profit/rate matrix.
    assert cases, "real solver produced no feasible arming cases at cap 2 and cap 6"
    for case in cases:
        assert set(case["results_by_cell"]) == {(2, OBJECTIVE_PROFIT), (2, OBJECTIVE_RATE),
                                                 (6, OBJECTIVE_PROFIT), (6, OBJECTIVE_RATE)}

    def verdict_at(min_delta_pct, min_cases):
        return ro.arming_verdict(cases, baseline=(2, OBJECTIVE_PROFIT),
                                 candidate=(6, OBJECTIVE_RATE), overhead_seconds=60,
                                 min_delta_pct=min_delta_pct, min_cases=min_cases)

    verdict = verdict_at(5.0, 1)
    assert isinstance(verdict["armed"], bool)
    assert verdict["candidate_cph"] > 0

    # sp-db0n: the GATING baseline is the cap-2 RATE cells (the TRUE live-prod default), not
    # the cap-2 PROFIT in-code fail-safe. Recompute baseline_cap_rate_cph independently from
    # the raw (cap-2, RATE) cells the real solver produced — a non-circular check that the
    # gate exposes and gates on the corrected baseline (never re-introducing profit).
    baseline_cap_rate_cph = ro.fleet_cph(
        [c["results_by_cell"][(2, OBJECTIVE_RATE)] for c in cases], 60)
    assert baseline_cap_rate_cph > 0
    assert math.isclose(verdict["baseline_cap_rate_cph"], baseline_cap_rate_cph, rel_tol=1e-9)

    # `armed` gates on true_live_delta_pct (candidate cap-6 rate vs that cap-2 RATE baseline):
    # the decision flips EXACTLY at the true-live delta, robustly on real numbers under both
    # sequencers — proving the gate measures the candidate against the corrected baseline.
    true_live_delta = verdict["true_live_delta_pct"]
    assert verdict_at(true_live_delta - 1.0, 1)["armed"] is True
    assert verdict_at(true_live_delta + 1.0, 1)["armed"] is False
    # both gate arms actuate on real solver output (falsifiable in BOTH directions):
    assert verdict_at(-1e9, 1)["armed"] is True             # gate CAN arm — not hardwired off
    assert verdict_at(1e9, 1)["armed"] is False             # delta arm blocks an impossible gain
    assert verdict_at(-1e9, 10 ** 9)["armed"] is False      # case-count arm blocks a thin sample


# ============================ sp-g8op: chained open-vs-closed closure A/B gate =========
def _closure_case(open_results, closed_results):
    """A closure-pass-shaped case: an OPEN chain and a CLOSED chain of solver-result dicts."""
    return dict(sample="s", home="X1-S1-A", hold=80,
                open_chain=list(open_results), closed_chain=list(closed_results))


def _closure_win_cases(n, k=4):
    # CLOSED clearly out-earns OPEN over the K-tour horizon: each open tour realizes 1000cr
    # @ 500cph (2h), each closed tour 1000cr @ 1000cph (1h) -> aggregate 500 vs 1000 cr/hr.
    open_chain = [_res(1000, 500) for _ in range(k)]
    closed_chain = [_res(1000, 1000) for _ in range(k)]
    return [_closure_case(open_chain, closed_chain) for _ in range(n)]


def test_closure_ab_pass_chains_open_drift_and_closed_reanchor(monkeypatch):
    """sp-g8op chained-solve wiring: an OPEN chain advances the hull to each plan's tail
    (wander-outward), a CLOSED chain re-anchors it home every replan (floating closure).
    Both solve K tours at the RATE objective (the armed longer-tour objective). Seam-free:
    solve_tour is stubbed to echo the ship position + closed flag it SAW, so the assembly and
    the ship-advance rule are directly verifiable without depending on the real sequencer."""
    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        closed = bool(cons.get("closed"))
        start = ship["current_waypoint"]
        # OPEN wanders one hop further; CLOSED floats back to the current waypoint (home).
        end_waypoint = start if closed else start + ">"
        return dict(feasible=True, projected_profit=1000, projected_credits_per_hour=1000.0,
                    legs=[dict(waypoint_symbol=end_waypoint,
                               system_symbol=ship["current_system"])],
                    start_seen=start, closed_seen=closed, objective_seen=objective)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [
        _replay_row("X1-S1-A", "G", purchase_price=100, sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=0,   sell_price=200, sample_t=sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 5, 5)}

    cases = ro.closure_ab_pass([sample_t], rows, {}, coords, [80], "1@e",
                               cap=6, k=4, max_spend=1_000_000, reserve=0)
    assert len(cases) == 1
    case = cases[0]
    assert len(case["open_chain"]) == 4 and len(case["closed_chain"]) == 4

    home = "X1-S1-A"   # sorted-first market of the home system = the chain's start anchor
    # OPEN drifts: each tour starts where the previous ENDED (home, home>, home>>, home>>>).
    assert [r["start_seen"] for r in case["open_chain"]] == \
           [home, home + ">", home + ">>", home + ">>>"]
    # CLOSED re-anchors: every tour starts from the home anchor (floating closure returns it).
    assert all(r["start_seen"] == home for r in case["closed_chain"])
    assert all(r["closed_seen"] is False for r in case["open_chain"])
    assert all(r["closed_seen"] is True for r in case["closed_chain"])
    # both arms solve at the armed longer-tour objective (rate), never profit.
    assert all(r["objective_seen"] == OBJECTIVE_RATE
               for r in case["open_chain"] + case["closed_chain"])


def test_closure_ab_reanchors_on_real_replay_solver_data(monkeypatch):
    """sp-g8op — the chained open-vs-closed closure A/B on the REAL merged solver (no stub):
    closure_ab_pass drives the ACTUAL solve_tour over K RATE tours per arm on the same two-
    system reconstructed snapshot the arming firing sibling uses. This is the ONLY real-solver
    coverage of the closure path — the stubbed sibling pins the ship-advance WIRING, but only
    here is the floating-closure epilogue proven end-to-end on merged code. Asserts both arms
    complete the full K horizon, every CLOSED tour's realized tail re-anchors to the home
    anchor, and the OPEN arm wanders OFF that anchor (so closed==anchor is FALSIFIABLE — the
    `closed` flag genuinely changes real-solver behavior, not a solver that trivially returns
    home either way). A regression (a chain collapsing < K, or a closed tour failing to return
    home) FAILS here instead of hiding; no assertion is weakened."""
    # A real fitted single-tier model (resolves to version "1@e") — same as the arming sibling.
    monkeypatch.setattr(ro, "MODEL", {
        "fit_version": 1, "era": "e",
        "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                    "buy_growth_per_step": 1.1, "n_obs": 9}},
        "recovery": {}}, raising=False)
    version = "1@e"
    sample_t = datetime(2026, 7, 16, 12, 0, 0)

    # The same two-system profitable-arb fixture the real-solver arming gate uses; it reaches
    # the full K=4 horizon for BOTH the open and the closed arm.
    rows = [
        _replay_row("X1-S1-A", "G", purchase_price=100, sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=0,   sell_price=260, sample_t=sample_t),
        _replay_row("X1-S2-C", "H", purchase_price=90,  sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S2-D", "H", purchase_price=0,   sell_price=240, sample_t=sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 10, 0),
              "X1-S2-C": ("X1-S2", 200, 0), "X1-S2-D": ("X1-S2", 210, 0)}
    neighbors = {"X1-S1": {"X1-S2"}, "X1-S2": {"X1-S1"}}

    k = 4
    cases = ro.closure_ab_pass([sample_t], rows, neighbors, coords, [80], version,
                               cap=6, k=k, max_spend=2_000_000, reserve=0)
    # the REAL merged solver completed BOTH K-tour chains for at least one (home, hull) case.
    assert cases, "real solver produced no full-K closure A/B cases at cap 6"

    for case in cases:
        # both arms reached the full K horizon (closure_ab_pass emits only complete chains).
        assert len(case["open_chain"]) == k
        assert len(case["closed_chain"]) == k
        # the anchor is the home system's sorted-first market — the chain's start waypoint.
        anchor = min(wp for wp, (system, _x, _y) in coords.items()
                     if system == case["home"])
        # CLOSED arm: every tour's realized tail re-anchors home (floating-closure epilogue).
        assert all(result["legs"][-1]["waypoint_symbol"] == anchor
                   for result in case["closed_chain"])
        # OPEN arm: the hull wanders — after K tours it is NOT back at the anchor. This is what
        # makes the closed==anchor assertion meaningful: the `closed` flag drives the real
        # solver's tail placement, it is not a no-op that returns home either way.
        assert case["open_chain"][-1]["legs"][-1]["waypoint_symbol"] != anchor


def test_closure_ab_verdict_closed_wins():
    cases = _closure_win_cases(30)
    verdict = ro.closure_ab_verdict(cases, overhead_seconds=0,
                                    min_delta_pct=5.0, min_cases=30)
    # SINGLE SOURCE OF TRUTH: each arm's realized cph is exactly fleet_cph over its pooled
    # chains — the closure gate shares the one fleet-$/hr definition (no drift from arming).
    open_pool = [r for c in cases for r in c["open_chain"]]
    closed_pool = [r for c in cases for r in c["closed_chain"]]
    assert verdict["open_cph"] == ro.fleet_cph(open_pool, 0)
    assert verdict["closed_cph"] == ro.fleet_cph(closed_pool, 0)
    assert math.isclose(verdict["open_cph"], 500.0, rel_tol=1e-9)
    assert math.isclose(verdict["closed_cph"], 1000.0, rel_tol=1e-9)
    assert math.isclose(verdict["closure_delta_pct"], 100.0, rel_tol=1e-9)
    assert verdict["cases"] == 30
    assert verdict["armed"] is True


@pytest.mark.parametrize("n,min_delta,min_cases,reason", [
    (30, 5.0, 40, "too_few_cases"),        # closed wins big, but 30 cases < min 40
    (30, 200.0, 30, "delta_below_min"),    # cases ok, but +100% < min 200%
    (0, 5.0, 0, "empty_fails_safe"),       # no chained cases -> NaN delta -> never armed
])
def test_closure_ab_verdict_noop(n, min_delta, min_cases, reason):
    verdict = ro.closure_ab_verdict(_closure_win_cases(n), overhead_seconds=0,
                                    min_delta_pct=min_delta, min_cases=min_cases)
    assert verdict["armed"] is False, reason


# ======= sp-2ehd7: bid/ask orientation + the gate-hop feed the harness could not see =====
def _market_row(waypoint, good, purchase_price, sell_price, sample_t, volume=40):
    """A market_price_history row with the columns EXACTLY as the game writes them."""
    return SimpleNamespace(waypoint_symbol=waypoint, good_symbol=good,
                           purchase_price=purchase_price, sell_price=sell_price,
                           supply="LIMITED", activity="WEAK", trade_volume=volume,
                           recorded_at=sample_t)


def test_reconstructed_snapshot_is_never_crossed_at_a_single_waypoint():
    """THE STRUCTURAL FALSIFIER (sp-2ehd7). No waypoint may quote bid >= ask for the same
    good: a market that pays more to take a good than it charges to hand the same good over is
    free money at a standstill, and the solver has no cross-check of its own — it reads `ask`
    to buy and `bid` to sell. Every such tour is in-system, so the distortion is DIRECTIONAL:
    in-system rates inflate without bound and no cross-system haul is ever worth planning,
    which destroys any lane-selection verdict the harness produces.

    This is a property over the whole quote space, not an example: it sweeps a grid of real
    market shapes (purchase_price > sell_price > 0 — the shape ALL 273,183 rows in
    market_price_history have; zero crossed, zero one-sided) at spreads from 1 credit to 10x,
    and asserts the invariant on every reconstructed row. It also asserts nothing VANISHED and
    pins the orientation itself, so a transposition cannot pass by emptying the snapshot.
    """
    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows, expected = [], {}
    for i, (purchase_price, sell_price) in enumerate([
            (101, 100),      # 1-credit spread — the tightest possible real market
            (120, 100),
            (260, 100),
            (1000, 100),     # 10x spread
            (5, 1),
            (99_999, 50_000),
    ]):
        waypoint = f"X1-S1-{chr(ord('A') + i)}"
        rows.append(_market_row(waypoint, "G", purchase_price, sell_price, sample_t))
        expected[waypoint] = (sell_price, purchase_price)   # (bid, ask)

    snapshot = ro.reconstruct_snapshot(rows, sample_t)

    # nothing vanished: a transposition that got silently dropped instead of refused would
    # otherwise satisfy the invariant below vacuously.
    assert len(snapshot) == len(rows)
    for row in snapshot:
        bid, ask = row["bid"], row["ask"]
        assert not (bid > 0 and ask > 0 and bid >= ask), (
            f"{row['waypoint_symbol']}/{row['good_symbol']} is crossed at a single waypoint: "
            f"bid {bid} >= ask {ask} — bid/ask are transposed (sp-2ehd7)")
        # ask is what WE PAY (purchase_price); bid is what WE RECEIVE (sell_price).
        assert (bid, ask) == expected[row["waypoint_symbol"]]


def test_reconstruct_snapshot_refuses_a_crossed_quote():
    """The refusal is real, and it names the offender. A row whose columns are crossed under
    the live orientation (purchase 90 / sell 200 -> ask 90, bid 200) cannot be market data, so
    the harness must REFUSE rather than drop it: dropping would let a wholesale transposition
    present itself as "the window had no opportunities" — silent, and indistinguishable from a
    thin sample. Fails if the orientation is transposed, because then this row reads clean."""
    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [_market_row("X1-S1-A", "G", purchase_price=90, sell_price=200, sample_t=sample_t)]

    with pytest.raises(ValueError, match="CROSSED"):
        ro.reconstruct_snapshot(rows, sample_t)

    # and the message identifies which waypoint/good, so the refusal is actionable.
    try:
        ro.reconstruct_snapshot(rows, sample_t)
    except ValueError as err:
        assert "X1-S1-A" in str(err) and "G" in str(err)


def test_one_sided_quotes_are_not_treated_as_crossed():
    """A pure EXPORT (only a purchase price) or IMPORT (only a sell price) leaves one side at
    0. That is not a crossed market and must survive — the guard tests bid>0 AND ask>0, so it
    can never silently thin a snapshot of legitimate one-sided listings."""
    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [
        _market_row("X1-S1-A", "G", purchase_price=100, sell_price=0, sample_t=sample_t),
        _market_row("X1-S1-B", "G", purchase_price=0, sell_price=260, sample_t=sample_t),
    ]
    snapshot = {r["waypoint_symbol"]: r for r in ro.reconstruct_snapshot(rows, sample_t)}
    assert (snapshot["X1-S1-A"]["ask"], snapshot["X1-S1-A"]["bid"]) == (100, 0)
    assert (snapshot["X1-S1-B"]["ask"], snapshot["X1-S1-B"]["bid"]) == (0, 260)


# ---------------------------------------------- the inter_system_hops feed (sp-2ehd7 #2)
# A line graph: S1 - S2 - S3. S1..S3 are 2 gate hops apart; S4 is disconnected.
_LINE_NEIGHBORS = {"X1-S1": {"X1-S2"}, "X1-S2": {"X1-S1", "X1-S3"}, "X1-S3": {"X1-S2"}}


def test_inter_system_hops_emits_real_depth_for_a_two_hop_pair():
    """Without this feed the solver's _build_inter_system_hop_index sees {} and prices EVERY
    crossing at gate_hops=1 — the cheapest possible — so a hop-depth A/B reads as inert past
    depth 1 and the harness cannot see the variable under test (sp-2ehd7). Two neighbors of a
    common home are 1 hop from home but 2 from EACH OTHER, which a cap>2 tour can cross."""
    hops = ro.inter_system_hop_distances(_LINE_NEIGHBORS,
                                         {"X1-S1", "X1-S2", "X1-S3"}, max_tour_systems=4)
    assert hops == [dict(from_system="X1-S1", to_system="X1-S3", gate_hops=2)]
    # the 1-hop pairs are OMITTED, not emitted as 1 — a proven single hop already prices at
    # the base charge (tourInterSystemHops' contract).
    assert all(h["gate_hops"] > 1 for h in hops)


def test_inter_system_hops_is_empty_at_the_default_cap():
    """Mirror of tourInterSystemHops' arming gate: at cap<=2 a tour touches start + one gate
    neighbor, so every crossing is a single hop the flat charge prices exactly."""
    for cap in (None, 0, 1, 2):
        assert ro.inter_system_hop_distances(_LINE_NEIGHBORS,
                                             {"X1-S1", "X1-S2", "X1-S3"}, cap) == []


def test_inter_system_hops_refuses_an_unprovable_pair_instead_of_pricing_it_cheap():
    """An unproven crossing is charged bound+1 (unprovenCrossingHops), never dropped into the
    solver's 1-hop default: the harness must not quote a crossing the walk could not even
    reach at the cheapest price. bound = max(2 x candidate depth, MaxJumpPath) = 5, so an
    unreachable pair costs 6 hops."""
    hops = ro.inter_system_hop_distances(_LINE_NEIGHBORS,
                                         {"X1-S1", "X1-S4"}, max_tour_systems=4)
    assert hops == [dict(from_system="X1-S1", to_system="X1-S4", gate_hops=6)]


def test_inter_system_hops_needs_the_gate_graph_to_price_a_widened_tour():
    """A missing graph makes EVERY pair unprovable, which would silently charge the refusal
    distance on every crossing and bias the comparison toward in-system tours. Refuse loudly
    instead. (A sub-2 system set needs no graph and short-circuits before this.)"""
    with pytest.raises(ValueError, match="gate adjacency"):
        ro.inter_system_hop_distances(None, {"X1-S1", "X1-S2"}, max_tour_systems=4)
    assert ro.inter_system_hop_distances(None, {"X1-S1"}, max_tour_systems=4) == []


@pytest.mark.parametrize("cap,expect_hops", [(4, True), (2, False), (None, False)])
def test_run_case_threads_the_hop_matrix_into_the_constraints(monkeypatch, cap, expect_hops):
    """The feed reaches the solver. Absent at cap<=2 / no cap, so the default DB run stays
    byte-identical; present with the real depth once the horizon is widened."""
    captured = {}

    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        captured["cons"] = dict(cons)
        return _res(1, 1)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    snapshot = [dict(waypoint_symbol="X1-S1-A", system_symbol="X1-S1", good_symbol="G",
                     ask=100, bid=90, trade_volume=40, supply="LIMITED", activity="WEAK",
                     observed_at_unix=9_999_999_999)]
    waypoints = [dict(symbol="X1-S1-A", system="X1-S1", x=0, y=0)]

    ro.run_case(snapshot, waypoints, "X1-S1", {"X1-S1", "X1-S2", "X1-S3"}, 40,
                1_000_000, 0, "1@e", max_tour_systems=cap, neighbors=_LINE_NEIGHBORS)

    if expect_hops:
        assert captured["cons"]["inter_system_hops"] == \
               [dict(from_system="X1-S1", to_system="X1-S3", gate_hops=2)]
    else:
        assert "inter_system_hops" not in captured["cons"]


# ------------------------- the depth<->cap coupling and the RATE-only cap sweep (sp-2ehd7)
@pytest.mark.parametrize("configured,cap,expected,why", [
    (0, 4, 1, "absent depth resolves to candidateHopDepthDefault=1"),
    (-5, 4, 1, "negative depth resolves to the default"),
    (1, 4, 1, "depth 1 passes through"),
    (3, 4, 3, "a widened cap unlocks the configured depth"),
    (3, 2, 1, "cap<=2 clamps depth BACK to 1 — the live coupling"),
    (3, None, 1, "no cap resolves to the default cap 2, so depth clamps to 1"),
    (30, 4, 3, "a fat-fingered depth is clamped to maxCandidateHopDepth=3"),
])
def test_effective_candidate_hop_depth_mirrors_the_live_coupling(configured, cap, expected, why):
    """The depth knob and the cap knob are COUPLED live (resolveCandidateHopDepth +
    effectiveCandidateHopDepth). Pinning one depth across both arms of a cap A/B measures the
    wrong pair: depth 1 everywhere starves the widened arm of the systems it actually reaches,
    depth 3 everywhere hands the cap-2 arm reach prod does not give it."""
    assert ro.effective_candidate_hop_depth(configured, cap) == expected, why


def test_compute_allowed_at_depth_one_is_the_historical_one_hop_set():
    """Depth-1 equivalence: the widened walk must REDUCE to the set every call site used
    before, or the un-widened arm is no longer measuring today's behaviour."""
    by_system = {"X1-S1": {"a"}, "X1-S2": {"b"}, "X1-S3": {"c"}}
    historical = {"X1-S1"} | (_LINE_NEIGHBORS.get("X1-S1", set()) & set(by_system))
    assert ro.compute_allowed("X1-S1", _LINE_NEIGHBORS, by_system, 1) == historical
    # and the walk grows monotonically with depth: S3 is 2 hops out on the line graph.
    assert ro.compute_allowed("X1-S1", _LINE_NEIGHBORS, by_system, 2) == \
           historical | {"X1-S3"}
    assert ro.compute_allowed("X1-S1", _LINE_NEIGHBORS, by_system, 0) == {"X1-S1"}


def test_widen_verdict_reports_each_cap_against_the_first():
    """The sweep's arithmetic: fleet-$/hr per cap over the JOINT-feasible windows only, each
    delta measured against the FIRST cap. Delegates to fleet_cph so a widen number can sit
    next to an arming number without drift."""
    cases = [dict(sample="s", home="X1-S1", hold=80,
                  results_by_cap={2: _res(1000, 1000), 4: _res(3000, 1500)})
             for _ in range(4)]
    # one case is infeasible at cap 4 -> excluded from BOTH caps' aggregates.
    cases.append(dict(sample="s", home="X1-S2", hold=80,
                      results_by_cap={2: _res(9_000_000, 9_000_000),
                                      4: _res(0, 0, feasible=False)}))

    verdict = ro.widen_verdict(cases, [2, 4], overhead_seconds=0)

    assert verdict["cases"] == 4
    assert verdict["baseline_cap"] == 2
    assert verdict["cph_by_cap"][2] == ro.fleet_cph([_res(1000, 1000)] * 4, 0)
    assert math.isclose(verdict["cph_by_cap"][2], 1000.0, rel_tol=1e-9)
    assert math.isclose(verdict["cph_by_cap"][4], 1500.0, rel_tol=1e-9)
    assert math.isclose(verdict["delta_by_cap"][2], 0.0, abs_tol=1e-9)
    assert math.isclose(verdict["delta_by_cap"][4], 50.0, rel_tol=1e-9)


def test_widen_pass_gives_each_cap_its_own_reach_and_hop_matrix(monkeypatch):
    """The sweep must not hand both caps the same candidate set: at cap 2 live sees a 1-hop
    set with NO hop matrix, at cap 4 the configured depth and the real distances. A sweep that
    shares one reach across caps cannot answer the cap question at all."""
    seen = {}

    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        seen[cons["max_tour_systems"]] = dict(
            allowed=tuple(cons["allowed_systems"]),
            hops=tuple((h["from_system"], h["to_system"], h["gate_hops"])
                       for h in cons.get("inter_system_hops", [])))
        return _res(1000, 1000)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [
        _market_row("X1-S1-A", "G", 100, 90, sample_t),
        _market_row("X1-S1-B", "G", 210, 200, sample_t),
        _market_row("X1-S2-C", "H", 100, 90, sample_t),
        _market_row("X1-S3-D", "H", 210, 200, sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 5, 5),
              "X1-S2-C": ("X1-S2", 100, 0), "X1-S3-D": ("X1-S3", 200, 0)}

    cases = ro.widen_pass([sample_t], rows, _LINE_NEIGHBORS, coords, [80], "1@e",
                          caps=[2, 4], max_spend=1_000_000, reserve=0,
                          candidate_hop_depth=3)

    assert cases, "widen_pass produced no joint-feasible cases"
    # cap 2: depth clamped to 1 -> S1 + S2 only, and NO hop matrix (every crossing is 1 hop).
    assert seen[2]["allowed"] == ("X1-S1", "X1-S2")
    assert seen[2]["hops"] == ()
    # cap 4: depth 3 -> S3 joins, and S1<->S3 (the only >1-hop pair on the line) prices at its
    # real 2-hop distance instead of the 1-hop default the un-fed harness quoted.
    assert seen[4]["allowed"] == ("X1-S1", "X1-S2", "X1-S3")
    assert seen[4]["hops"] == (("X1-S1", "X1-S3", 2),)

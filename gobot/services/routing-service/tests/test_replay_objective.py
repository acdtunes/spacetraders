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
        SimpleNamespace(waypoint_symbol="X1-S1-B", good_symbol="G", purchase_price=90,
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
    """A market_price_history row shaped for reconstruct_snapshot: bid=purchase_price,
    ask=sell_price. A SOURCE (buyable) needs sell_price>0/purchase_price=0 (-> ask>0); a
    SINK (sellable) needs purchase_price>0/sell_price=0 (-> bid>0)."""
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
        _replay_row("X1-S1-A", "G", purchase_price=0,   sell_price=100, sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=260, sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S2-C", "H", purchase_price=0,   sell_price=90,  sample_t=sample_t),
        _replay_row("X1-S2-D", "H", purchase_price=240, sell_price=0,   sample_t=sample_t),
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
        _replay_row("X1-S1-A", "G", purchase_price=0,   sell_price=100, sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=200, sell_price=0,   sample_t=sample_t),
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
        _replay_row("X1-S1-A", "G", purchase_price=0,   sell_price=100, sample_t=sample_t),
        _replay_row("X1-S1-B", "G", purchase_price=260, sell_price=0,   sample_t=sample_t),
        _replay_row("X1-S2-C", "H", purchase_price=0,   sell_price=90,  sample_t=sample_t),
        _replay_row("X1-S2-D", "H", purchase_price=240, sell_price=0,   sample_t=sample_t),
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

# gobot/services/routing-service/tests/test_tour_solver_home_union.py
#
# sp-97ine: the home-scoped stage-1 union.
#
# THE DEFECT. Stage 1 is a truncated search (BEAM_WIDTH per level, then the
# full_score_top_n cut), so a wide candidate set can crowd out the in-system
# tours a home-only solve finds — even though the wide solve's solution space
# strictly CONTAINS the home-only one. Economy-analyst A/B (2026-07-31, 51 joint
# cases, hull 220): the intra-only solve BEAT live on 14 of them.
#
# THE INVARIANT UNDER TEST is therefore a strict-superset property, not a
# strategy claim: a solve over systems {home, ...} must NEVER return less profit
# than the same solve restricted to {home}. Anything else means the wide search
# is diluting out winners it already contains.
#
# The dilution fixture below is deterministic (no RNG). Verified against the
# pre-fix solver at git HEAD 880df6f5: intra 70,440 vs live 65,320 — a 7.3%
# loss to its own subset, stable across foreign-field sizes 30/45/60/80.
import os

import pytest

from utils import tour_solver as ts
from utils.tour_solver import solve_tour

MODEL = {"fit_version": 1, "era": "e",
         "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                     "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def _row(wp, sys_, good, ask, bid, tv):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good,
                ask=ask, bid=bid, trade_volume=tv, supply="LIMITED",
                activity="WEAK", observed_at_unix=9_999_999_999)


def dilution_fixture(n_far=60):
    """Rich 4-stop HOME cluster + a wide shallow FOREIGN field that crowds the
    beam. Returns (snapshot, waypoints)."""
    snapshot, waypoints = [], []

    def add(wp, sys_, x, y, goods):
        waypoints.append(dict(symbol=wp, x=x, y=y))
        for good, ask, bid, tv in goods:
            snapshot.append(_row(wp, sys_, good, ask, bid, tv))

    # HOME S1: cheap deep sources (H0/H1) feeding deep rich sinks (H2/H3).
    add("H0", "S1", 0, 0, [("G1", 100, 10, 60), ("G2", 120, 12, 60)])
    add("H1", "S1", 10, 0, [("G1", 110, 9, 60), ("G2", 130, 11, 60)])
    add("H2", "S1", 0, 10, [("G1", 9999, 400, 60), ("G2", 9999, 30, 60)])
    add("H3", "S1", 10, 10, [("G1", 9999, 30, 60), ("G2", 9999, 460, 60)])

    # FOREIGN S2: many shallow markets. Each pair looks fine to the optimistic
    # per-hop pack bound but realizes little against a 220 hold.
    for i in range(n_far):
        add(f"F{i:02d}", "S2", 300 + (i % 10), 300 + (i // 10),
            [("G3", 100, 40, 12), ("G4", 90, 250 + (i % 7), 12)])
    return snapshot, waypoints


def _ship(wp="H0"):
    return dict(ship_symbol="HULL", current_waypoint=wp, current_system="S1",
                hold_capacity=220, fuel_current=1000, fuel_capacity=1000,
                engine_speed=30, cargo=[])


def _cons(allowed, **over):
    base = dict(max_hops=6, max_spend=1_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, max_snapshot_age_minutes=75,
                expected_model_version="1@e", max_tour_systems=2,
                allowed_systems=list(allowed))
    base.update(over)
    return base


def _profit(snapshot, waypoints, allowed, ship=None, **over):
    out = solve_tour(snapshot, ship or _ship(), _cons(allowed, **over), MODEL,
                     waypoints=waypoints)
    return out["projected_profit"] if out["feasible"] else 0


# --------------------------------------------------------------------------
# 1. The named regression: the diluted in-system winner comes back.
# --------------------------------------------------------------------------

def test_union_recovers_the_diluted_in_system_winner():
    """PRE-FIX this fixture returned 65,320 for live against 70,440 for intra —
    the wide search losing to a subset of itself. The union must restore it."""
    snapshot, waypoints = dilution_fixture()
    intra = _profit(snapshot, waypoints, ["S1"])
    live = _profit(snapshot, waypoints, ["S1", "S2"])
    assert intra > 0, "fixture must have a real in-system tour to be diluted"
    assert live >= intra, (
        f"wide solve {live:,} lost to its own home-only subset {intra:,} — "
        "shortlist dilution is back")
    # Pin the recovered value so a future regression that merely *narrows* the
    # gap instead of closing it still fails.
    assert live == 70_440


# --------------------------------------------------------------------------
# 2. The property itself, across fixture shapes and both objectives.
# --------------------------------------------------------------------------

@pytest.mark.parametrize("n_far", [30, 45, 60, 80])
def test_wide_solve_never_loses_to_its_home_only_subset(n_far):
    snapshot, waypoints = dilution_fixture(n_far)
    intra = _profit(snapshot, waypoints, ["S1"])
    live = _profit(snapshot, waypoints, ["S1", "S2"])
    assert live >= intra, f"n_far={n_far}: live {live:,} < intra {intra:,}"


def test_superset_property_holds_with_launch_cargo():
    """Held cargo changes the seeding (liquidation prizes), so re-assert the
    invariant on that path rather than assuming it carries over."""
    snapshot, waypoints = dilution_fixture()
    ship = _ship()
    ship["cargo"] = [dict(good_symbol="G1", units=40)]
    intra = _profit(snapshot, waypoints, ["S1"], ship=ship)
    live = _profit(snapshot, waypoints, ["S1", "S2"], ship=ship)
    assert live >= intra


def test_superset_property_holds_under_rate_objective(monkeypatch):
    """cph-primary ordering picks a different winner; the invariant is about the
    pool, not the objective, so it must hold there too."""
    monkeypatch.setenv(ts.OBJECTIVE_ENV_VAR, ts.OBJECTIVE_RATE)
    snapshot, waypoints = dilution_fixture()
    ship = _ship()
    intra = solve_tour(snapshot, ship, _cons(["S1"]), MODEL, waypoints=waypoints)
    live = solve_tour(snapshot, ship, _cons(["S1", "S2"]), MODEL, waypoints=waypoints)
    assert live["feasible"] and intra["feasible"]
    assert live["projected_credits_per_hour"] >= intra["projected_credits_per_hour"]


# --------------------------------------------------------------------------
# 3. Additive-only: the common path pays nothing and changes nothing.
# --------------------------------------------------------------------------

def _count_stage1(monkeypatch):
    calls = []
    real = ts.beam_sequences

    def spy(markets, *a, **kw):
        calls.append(dict(markets))
        return real(markets, *a, **kw)

    monkeypatch.setattr(ts, "beam_sequences", spy)
    return calls


def test_single_system_solve_never_runs_the_second_stage_one(monkeypatch):
    """When the home subset IS the whole market set the union would reproduce
    the wide candidates exactly, so it is skipped outright — no extra solve, no
    added latency, and the response is unchanged by construction."""
    snapshot, waypoints = dilution_fixture()
    home_only = [r for r in snapshot if r["system_symbol"] == "S1"]
    calls = _count_stage1(monkeypatch)
    out = solve_tour(snapshot, _ship(), _cons(["S1"]), MODEL, waypoints=waypoints)
    assert out["feasible"]
    assert len(calls) == 1, "single-system solve must run stage 1 exactly once"
    # And the same holds when the snapshot itself carries only the home system.
    calls.clear()
    solve_tour(home_only, _ship(), _cons(["S1", "S2"]), MODEL, waypoints=waypoints)
    assert len(calls) == 1


def test_multi_system_solve_runs_a_second_home_scoped_stage_one(monkeypatch):
    """The mirror of the above: a genuinely multi-system solve pays for exactly
    one extra stage-1, and it is scoped to the home system."""
    snapshot, waypoints = dilution_fixture()
    calls = _count_stage1(monkeypatch)
    solve_tour(snapshot, _ship(), _cons(["S1", "S2"]), MODEL, waypoints=waypoints)
    assert len(calls) == 2, "expected wide stage 1 + home-scoped stage 1"
    wide, home = calls
    assert any(m["system"] == "S2" for m in wide.values())
    assert home, "home-scoped market set must not be empty"
    assert set(home) < set(wide), "home set must be a STRICT subset of the wide set"
    assert {m["system"] for m in home.values()} == {"S1"}


def test_union_appends_and_never_drops_a_wide_candidate(monkeypatch):
    """The can-only-ADD contract: whatever the wide stage 1 produced still gets
    scored. Captured at the stage-2 boundary."""
    snapshot, waypoints = dilution_fixture()
    seen = []
    real_score = ts.score_sequence

    def spy(seq, *a, **kw):
        seen.append(seq)
        return real_score(seq, *a, **kw)

    monkeypatch.setattr(ts, "score_sequence", spy)
    wide_pool = []
    real_beam = ts.beam_sequences

    def beam_spy(markets, *a, **kw):
        out = real_beam(markets, *a, **kw)
        if not wide_pool:
            wide_pool.extend(out[:ts._resolve_full_score_top_n()])
        return out

    monkeypatch.setattr(ts, "beam_sequences", beam_spy)
    solve_tour(snapshot, _ship(), _cons(["S1", "S2"]), MODEL, waypoints=waypoints)
    assert wide_pool, "wide stage 1 produced nothing — fixture is broken"
    missing = [s for s in wide_pool if s not in seen]
    assert not missing, f"union dropped {len(missing)} wide candidate(s)"
    # Appended, not prepended: the wide pool leads the scoring order so a home
    # candidate has to win outright rather than inherit a tie.
    assert seen[0] == wide_pool[0]


# --------------------------------------------------------------------------
# 4. The scoping predicate.
# --------------------------------------------------------------------------

def test_home_scope_keeps_a_storage_node_with_no_declared_system(monkeypatch):
    """`_build_deposit_sinks` setdefaults a storage node with system "" when the
    candidate declares no storage_system, and an allowed_systems={home} solve
    KEEPS it (its guard is `if system and system not in allowed`). The home
    scope must keep it too, or the superset property has a hole exactly where a
    warehouse deposit is the only profitable sink."""
    snapshot, waypoints = dilution_fixture()
    waypoints.append(dict(symbol="WAREHOUSE", x=5, y=5))
    calls = _count_stage1(monkeypatch)
    solve_tour(snapshot, _ship(), _cons(["S1", "S2"]), MODEL, waypoints=waypoints,
               deposit_candidates=[dict(storage_waypoint="WAREHOUSE",
                                        good_symbol="G1", units_wanted=50,
                                        synthetic_bid=900, storage_system="")])
    assert len(calls) == 2
    home = calls[1]
    assert "WAREHOUSE" in home, "undeclared-system storage node dropped from home scope"
    assert not any(m["system"] == "S2" for m in home.values())


def test_home_scope_excludes_a_foreign_storage_node(monkeypatch):
    """The mirror: a storage node that DOES declare a foreign system is a
    foreign node and must stay out of the home-scoped solve."""
    snapshot, waypoints = dilution_fixture()
    waypoints.append(dict(symbol="FARDEPOT", x=305, y=305))
    calls = _count_stage1(monkeypatch)
    solve_tour(snapshot, _ship(), _cons(["S1", "S2"]), MODEL, waypoints=waypoints,
               deposit_candidates=[dict(storage_waypoint="FARDEPOT",
                                        good_symbol="G1", units_wanted=50,
                                        synthetic_bid=900, storage_system="S2")])
    assert len(calls) == 2
    assert "FARDEPOT" not in calls[1]
    assert "FARDEPOT" in calls[0], "foreign depot must still reach the WIDE solve"


# --------------------------------------------------------------------------
# 5. The ortools mirror.
# --------------------------------------------------------------------------

@pytest.mark.skipif("ortools" in os.environ.get("PYTEST_SKIP", ""),
                    reason="ortools opt-out")
def test_superset_property_holds_under_the_ortools_sequencer(monkeypatch):
    """The home union mirrors the wide path's ortools-then-beam shape, so the
    invariant must survive with the OR-Tools stage-1 armed."""
    pytest.importorskip("ortools")
    monkeypatch.setenv(ts.SEQUENCER_ENV_VAR, ts.SEQUENCER_ORTOOLS)
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS", "2")
    snapshot, waypoints = dilution_fixture(n_far=20)
    intra = _profit(snapshot, waypoints, ["S1"])
    live = _profit(snapshot, waypoints, ["S1", "S2"])
    assert live >= intra, f"ortools: live {live:,} < intra {intra:,}"


def test_broken_home_ortools_never_kills_the_solve(monkeypatch):
    """Same never-die contract as the wide ortools call: a raising sequencer
    degrades to the home beam, it does not fail the plan."""
    monkeypatch.setenv(ts.SEQUENCER_ENV_VAR, ts.SEQUENCER_ORTOOLS)
    snapshot, waypoints = dilution_fixture(n_far=15)
    calls = {"n": 0}

    def boom(*a, **kw):
        calls["n"] += 1
        raise RuntimeError("ortools wheel is broken")

    monkeypatch.setattr(ts, "ortools_sequences", boom)
    ts._logged_sequencer.discard("ortools_error")
    ts._logged_sequencer.discard("ortools_error_home")
    out = solve_tour(snapshot, _ship(), _cons(["S1", "S2"]), MODEL,
                     waypoints=waypoints)
    assert out["feasible"], "a broken ortools must not fail the solve"
    assert calls["n"] == 2, "both the wide and the home call must be attempted"
    intra = _profit(snapshot, waypoints, ["S1"])
    assert out["projected_profit"] >= intra

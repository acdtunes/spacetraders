# gobot/services/routing-service/tests/test_tour_solver_ortools.py
#
# sp-y05b: OR-Tools prize-collecting sequencer behind TOUR_SOLVER_SEQUENCER.
# Test plan T1-T16 from the finding-cleared brief. Test budget: brief-mandated
# 16 tests covering 14 distinct behaviors (dispatch/dormancy, fallback x2,
# source-as-intermediate F1, ordering F2, brute-force optimality, stop cap F5,
# time value, request cap F4, pruning F6/F11, liquidation F6 x2, deposit/stock
# +virtual-end F10, latency F3/F7/F8, parity, system cap) — within 2x budget.
#
# GLS is anytime => assertions are profit-equality/dominance and set
# membership, NEVER leg order. Brute-force fixtures stay <= 6 nodes so the
# optimum is reached within the 250 ms per-model floor. Every value-assertion
# test pins TOUR_SOLVER_ORTOOLS_TIME_VALUE=0 (pure value model) except T9,
# which sets a high lambda explicitly to prove travel participates.
import itertools
import logging
import os
import subprocess
import sys
import time as time_mod

import utils.tour_solver as ts
from utils.tour_solver import solve_tour, score_sequence

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def snap(wp, sys_, good, ask, bid, tv=20, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=9_999_999_999)


def _ship(wp="A", system="S1", hold=80, cargo=None):
    return dict(ship_symbol="H", current_waypoint=wp, current_system=system,
                hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=cargo or [])


def _cons(**over):
    base = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    base.update(over)
    return base


def _two_sinks_fixture():
    # Mirror of tests/test_tour_solver.py:20 (the golden-pin board).
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S1", "G", ask=999, bid=200, tv=40),
        snap("C", "S1", "G", ask=999, bid=195, tv=40),
    ]
    return snapshot, _ship(hold=80), _cons()


def _ortools_env(monkeypatch, lam="0"):
    monkeypatch.setenv("TOUR_SOLVER_SEQUENCER", "ortools")
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", lam)


def _beam_env(monkeypatch):
    monkeypatch.delenv("TOUR_SOLVER_SEQUENCER", raising=False)


def _setup(snapshot, ship, cons, deposit_candidates=None, stock_sources=None):
    """Test-side mirror of solve_tour's stage-1 input assembly (markets, built
    deposit/stock indices, travel fn) for DIRECT ortools_sequences calls and
    brute-force scoring with the real score_sequence."""
    allowed = set(cons.get("allowed_systems") or [ship["current_system"]])
    rows = [r for r in snapshot
            if r["system_symbol"] in allowed and (r["ask"] > 0 or r["bid"] > 0)]
    markets = ts._build_markets(rows)
    deposit_sinks = ts._build_deposit_sinks(deposit_candidates, markets, allowed)
    stock_idx = ts._build_stock_sources(stock_sources, markets, allowed)
    travel_fn = ts._make_travel_fn(cons, markets, ship)
    return markets, deposit_sinks, stock_idx, travel_fn


def _brute_force_optimum(snapshot, ship, cons,
                         deposit_candidates=None, stock_sources=None):
    """Max profit over ALL waypoint permutations up to max_hops, priced by the
    real score_sequence. Fixtures keep hold >= the A-cap depth (2*tv) so
    revisit ladders add nothing and permutations are the true optimum."""
    markets, dep, stock, travel = _setup(snapshot, ship, cons,
                                         deposit_candidates, stock_sources)
    max_hops = min(cons.get("max_hops") or ts.MAX_HOPS_DEFAULT, ts.MAX_HOPS_DEFAULT)
    best = 0
    wps = sorted(markets)
    for r in range(1, min(max_hops, len(wps)) + 1):
        for perm in itertools.permutations(wps, r):
            res = score_sequence(perm, markets, ship, cons, MODEL, travel,
                                 dep, None, stock)
            if res["profit"] > best:
                best = res["profit"]
    return best


def _direct_candidates(snapshot, ship, cons, deposit_candidates=None,
                       stock_sources=None, stats_out=None):
    """Call ortools_sequences DIRECTLY (bypassing the solve_tour union seam so
    beam can never mask a weak model — F2/F6 pins)."""
    markets, dep, stock, travel = _setup(snapshot, ship, cons,
                                         deposit_candidates, stock_sources)
    if stats_out is not None:
        cands = ts.ortools_sequences(markets, ship, cons, travel, dep, stock,
                                     stats_out=stats_out)
    else:
        cands = ts.ortools_sequences(markets, ship, cons, travel, dep, stock)
    return cands, markets, dep, stock, travel


def _best_direct_profit(snapshot, ship, cons, deposit_candidates=None,
                        stock_sources=None):
    cands, markets, dep, stock, travel = _direct_candidates(
        snapshot, ship, cons, deposit_candidates, stock_sources)
    best = 0
    for seq in cands[:ts.FULL_SCORE_TOP_N]:
        res = score_sequence(seq, markets, ship, cons, MODEL, travel,
                             dep, None, stock)
        if res["profit"] > best:
            best = res["profit"]
    return best, cands


# --- T1: default dormancy + golden pin --------------------------------------

def test_default_sequencer_is_beam_and_never_touches_ortools(monkeypatch):
    _beam_env(monkeypatch)
    snapshot, ship, cons = _two_sinks_fixture()
    golden = solve_tour(snapshot, ship, cons, MODEL)
    assert golden["feasible"], golden

    # Lazy-import pin: a beam-mode call in a FRESH process must never import
    # the ortools package (a broken wheel cannot affect default mode).
    code = (
        "import sys\n"
        "sys.path.insert(0, %r)\n"
        "from utils.tour_solver import solve_tour\n"
        "MODEL = {'fit_version': 1, 'era': 'e', 'impact':\n"
        "         {'LIMITED|WEAK': {'sell_decay_per_step': 0.9,\n"
        "                           'buy_growth_per_step': 1.1, 'n_obs': 9}},\n"
        "         'recovery': {}}\n"
        "def snap(wp, sys_, good, ask, bid, tv=20):\n"
        "    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good,\n"
        "                ask=ask, bid=bid, trade_volume=tv, supply='LIMITED',\n"
        "                activity='WEAK', observed_at_unix=9_999_999_999)\n"
        "snapshot = [snap('A', 'S1', 'G', 100, 90, 40),\n"
        "            snap('B', 'S1', 'G', 999, 200, 40),\n"
        "            snap('C', 'S1', 'G', 999, 195, 40)]\n"
        "ship = dict(ship_symbol='H', current_waypoint='A', current_system='S1',\n"
        "            hold_capacity=80, fuel_current=400, fuel_capacity=400,\n"
        "            engine_speed=30, cargo=[])\n"
        "cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,\n"
        "            working_capital_reserve=0, allowed_systems=['S1'],\n"
        "            max_snapshot_age_minutes=75, expected_model_version='1@e')\n"
        "out = solve_tour(snapshot, ship, cons, MODEL)\n"
        "assert out['feasible'], out\n"
        "bad = [m for m in sys.modules if m == 'ortools' or m.startswith('ortools.')]\n"
        "assert not bad, 'beam mode imported %%s' %% bad\n"
        "print('LAZY_OK')\n"
    ) % ROOT
    env = {k: v for k, v in os.environ.items() if k != "TOUR_SOLVER_SEQUENCER"}
    proc = subprocess.run([sys.executable, "-c", code], capture_output=True,
                          text=True, env=env, cwd=ROOT)
    assert proc.returncode == 0, proc.stderr
    assert "LAZY_OK" in proc.stdout

    # Booby trap: with the sequencer env unset, ortools_sequences must never
    # be called and the output must be byte-identical to the golden.
    def _boom(*args, **kwargs):
        raise AssertionError("ortools_sequences must not be called in beam mode")

    monkeypatch.setattr(ts, "ortools_sequences", _boom)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out == golden


# --- T2: env/kwarg dispatch + built-index contract (F9) ----------------------

def test_sequencer_env_dispatch_and_explicit_argument_wins(monkeypatch):
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S1", "G", ask=999, bid=200, tv=40),
    ]
    ship = _ship(hold=40)
    cons = _cons()
    deposits = [dict(good_symbol="G", units_wanted=40, synthetic_bid=600,
                     storage_waypoint="W", storage_system="S1")]
    stocks = [dict(storage_waypoint="V", good_symbol="MEDICINE",
                   units_available=40, unit_ask=100, storage_system="S1")]

    calls = []

    def spy(*args, **kwargs):
        calls.append((args, kwargs))
        return [("A", "B")]

    monkeypatch.setattr(ts, "ortools_sequences", spy)
    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL,
                     deposit_candidates=deposits, stock_sources=stocks)
    assert out["feasible"], out
    assert len(calls) == 1
    args, _ = calls[0]
    # F9 pin: 6 positional args, byte-mirroring the beam call — the BUILT
    # deposit/stock indices, not the raw request lists and not None.
    assert len(args) == 6, f"expected 6 positional args, got {len(args)}"
    markets_arg, ship_arg, cons_arg, travel_arg = args[0], args[1], args[2], args[3]
    assert "A" in markets_arg and "W" in markets_arg and "V" in markets_arg
    assert ship_arg is ship and cons_arg is cons and callable(travel_arg)
    assert ("W", "G") in args[4], args[4]
    assert ("V", "MEDICINE") in args[5], args[5]

    # Explicit sequencer="beam" overrides the env (the replay harness lever).
    out_beam = solve_tour(snapshot, ship, cons, MODEL,
                          deposit_candidates=deposits, stock_sources=stocks,
                          sequencer="beam")
    assert out_beam["feasible"], out_beam
    assert len(calls) == 1, "explicit sequencer='beam' must not call ortools"

    # An unrecognized env value fails toward the proven default (beam).
    monkeypatch.setenv("TOUR_SOLVER_SEQUENCER", "bogus")
    out_bogus = solve_tour(snapshot, ship, cons, MODEL,
                           deposit_candidates=deposits, stock_sources=stocks)
    assert out_bogus["feasible"], out_bogus
    assert len(calls) == 1, "bogus sequencer env must fall back to beam"


# --- T3: empty-pool fallback --------------------------------------------------

def test_ortools_empty_pool_falls_back_to_beam_with_warning(monkeypatch, caplog):
    snapshot, ship, cons = _two_sinks_fixture()
    _beam_env(monkeypatch)
    golden = solve_tour(snapshot, ship, cons, MODEL)
    assert golden["feasible"], golden

    monkeypatch.setattr(ts, "ortools_sequences", lambda *a, **k: [])
    monkeypatch.setattr(ts, "_logged_sequencer", set())  # reset once-per-process log
    _ortools_env(monkeypatch)
    with caplog.at_level(logging.WARNING, logger="utils.tour_solver"):
        out = solve_tour(snapshot, ship, cons, MODEL)
    assert out == golden
    assert any("no candidates" in rec.message for rec in caplog.records), \
        [rec.message for rec in caplog.records]


# --- T4: exception fallback ---------------------------------------------------

def test_ortools_exception_falls_back_to_beam(monkeypatch):
    snapshot, ship, cons = _two_sinks_fixture()
    _beam_env(monkeypatch)
    golden = solve_tour(snapshot, ship, cons, MODEL)
    assert golden["feasible"], golden

    def _explode(*args, **kwargs):
        raise RuntimeError("solver wheel is broken")

    monkeypatch.setattr(ts, "ortools_sequences", _explode)
    monkeypatch.setattr(ts, "_logged_sequencer", set())
    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)  # nothing escapes
    assert out == golden


# --- T5 (F1 blocker): neutral start, source strictly intermediate ------------

def test_ortools_visits_pure_buy_source_from_neutral_start(monkeypatch):
    # Ship parked at N, a market with one junk good (no profitable pair touches
    # N). The FUEL source A is strictly intermediate: its value exists ONLY on
    # the arc that LEAVES it toward sink B. A sell-side-only prize model skips
    # A (strictly-dominated skip) and never books the buy.
    # hold=80 equals the A-cap depth (2*tv) so a single visit lifts the whole
    # profitable depth and beam's revisit ladder adds nothing (same rationale
    # as the T6 crossing fixture) — DIRECT single-visit parity is then exact.
    snapshot = [
        snap("N", "S1", "JUNK", ask=999, bid=1, tv=40),
        snap("A", "S1", "FUEL", ask=50, bid=40, tv=40),
        snap("B", "S1", "FUEL", ask=999, bid=150, tv=40),
    ]
    ship = _ship(wp="N", hold=80)
    cons = _cons(max_spend=10_000)
    _beam_env(monkeypatch)
    beam_out = solve_tour(snapshot, ship, cons, MODEL)
    assert beam_out["feasible"], beam_out

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    buys = [(l["waypoint_symbol"], t["good_symbol"]) for l in out["legs"]
            for t in l["trades"] if t["is_buy"]]
    assert ("A", "FUEL") in buys, out
    assert out["projected_profit"] > 0
    assert out["projected_profit"] == beam_out["projected_profit"]

    # DIRECT pin (union cannot mask a sell-side-only model): the ortools
    # candidate set alone must realize the same profit.
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "0")
    best, cands = _best_direct_profit(snapshot, ship, cons)
    assert cands, "ortools produced no candidates"
    assert best == beam_out["projected_profit"], (best, cands)


# --- T6 (F2 blocker): crossing-ladder ordering --------------------------------

def _crossing_ladder_fixture():
    # A (S1): G source. D (S2): G sink AND H source. E (S1): H sink. The
    # travel-cheapest order A->E->D (300+1800s) books only the G leg; only the
    # value order A->D->E (1800+1800s) crosses buy-before-sell on H. hold=80
    # equals the A-cap depth (2*tv) so revisits add nothing and permutation
    # brute force is the true optimum.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("D", "S2", "G", ask=360, bid=320, tv=40),
        snap("D", "S2", "H", ask=50, bid=40, tv=40),
        snap("E", "S1", "H", ask=999, bid=160, tv=40),
    ]
    ship = _ship(hold=80)
    cons = _cons(allowed_systems=["S1", "S2"])
    return snapshot, ship, cons


def test_ortools_orders_buy_before_sell_across_the_crossing(monkeypatch):
    snapshot, ship, cons = _crossing_ladder_fixture()
    optimum = _brute_force_optimum(snapshot, ship, cons)
    assert optimum > 0

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    assert out["projected_profit"] == optimum, out

    # DIRECT pin: a travel-only/position-independent model picks the cheap
    # A->E->D order and under-credits H; the beam union must not mask it.
    best, cands = _best_direct_profit(snapshot, ship, cons)
    assert best == optimum, (best, optimum, cands[:5])


# --- T7: brute-force equality, single system ----------------------------------

def test_ortools_matches_brute_force_on_single_system_chain(monkeypatch):
    # 5 markets, hold >= A-cap depth (2*tv) so permutations are the optimum.
    # The value chain M1(FUEL)->M2(sell FUEL, buy ORE)->M3(sell ORE, buy GAS)
    # ->M4(sell GAS) must be discovered by ordering, not luck.
    snapshot = [
        snap("M1", "S1", "FUEL", ask=50, bid=45, tv=20),
        snap("M2", "S1", "FUEL", ask=999, bid=150, tv=20),
        snap("M2", "S1", "ORE", ask=60, bid=50, tv=20),
        snap("M3", "S1", "ORE", ask=999, bid=200, tv=20),
        snap("M3", "S1", "GAS", ask=30, bid=20, tv=20),
        snap("M4", "S1", "GAS", ask=999, bid=90, tv=20),
        snap("M5", "S1", "FUEL", ask=70, bid=60, tv=20),
    ]
    ship = _ship(wp="M1", hold=40)
    cons = _cons()
    optimum = _brute_force_optimum(snapshot, ship, cons)
    assert optimum > 0

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    assert out["projected_profit"] == optimum, out

    best, cands = _best_direct_profit(snapshot, ship, cons)
    assert best == optimum, (best, optimum, cands[:5])


# --- T8 (F5): value-dense selection under scarcity -----------------------------

def test_ortools_respects_max_hops_in_model_under_scarcity(monkeypatch):
    # 6 profitable markets but only 3 hops: the stop cap must live IN the
    # model (visit the best 3), not in post-hoc truncation of a 6-stop route.
    snapshot = [
        snap("A", "S1", "FUEL", ask=50, bid=45, tv=20),
        snap("B", "S1", "FUEL", ask=999, bid=150, tv=20),
        snap("C", "S1", "ORE", ask=60, bid=50, tv=20),
        snap("D", "S1", "ORE", ask=999, bid=300, tv=20),
        snap("E", "S1", "GAS", ask=30, bid=25, tv=20),
        snap("F", "S1", "GAS", ask=999, bid=60, tv=20),
    ]
    ship = _ship(wp="A", hold=40)
    cons = _cons(max_hops=3)
    optimum = _brute_force_optimum(snapshot, ship, cons)
    assert optimum > 0

    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "0")
    cands, markets, dep, stock, travel = _direct_candidates(snapshot, ship, cons)
    assert cands, "ortools produced no candidates"
    too_long = [seq for seq in cands if len(seq) > 3]
    assert not too_long, too_long

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    assert out["projected_profit"] == optimum, out


# --- T9: inter-system arc cost carried (travel seam) ---------------------------

def test_ortools_arcs_price_travel_through_the_shared_travel_fn(monkeypatch):
    # Marginal S2 sink: gain 4,000 cr (spread 100 x 40u). At lambda=50 the
    # flat 1,800s crossing costs (1800+60)*50 = 93,000 cr-equivalent — B is
    # dropped. With the documented constraints["_travel_fn"] hook returning
    # 1s hops, the same arc costs (1+60)*50 = 3,050 < 4,000 — B appears.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S2", "G", ask=999, bid=200, tv=40),
    ]
    ship = _ship(hold=40)
    cons = _cons(allowed_systems=["S1", "S2"], max_spend=10_000)
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "50")

    cands_flat, *_ = _direct_candidates(snapshot, ship, cons)
    assert not any("B" in seq for seq in cands_flat), cands_flat

    cons_fast = _cons(allowed_systems=["S1", "S2"], max_spend=10_000,
                      _travel_fn=lambda a, b: 1)
    cands_fast, *_ = _direct_candidates(snapshot, ship, cons_fast)
    assert any("B" in seq for seq in cands_fast), cands_fast


# --- T9b (F4): request-driven system cap through the shared accessor -----------

def _three_system_chain_fixture():
    # A(S1) sources G -> B(S2) sinks G and sources H -> C(S3) sinks H: the
    # in-model AND stage-2 optimum strictly needs all 3 systems.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S2", "G", ask=999, bid=300, tv=40),
        snap("B", "S2", "H", ask=50, bid=40, tv=40),
        snap("C", "S3", "H", ask=999, bid=160, tv=40),
    ]
    ship = _ship(hold=80)

    def cons(**over):
        return _cons(allowed_systems=["S1", "S2", "S3"], **over)

    return snapshot, ship, cons


def test_ortools_honors_request_raised_system_cap(monkeypatch):
    snapshot, ship, cons = _three_system_chain_fixture()
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "0")

    # Raised cap: subset enumeration must honor max_tour_systems=3 through
    # _effective_tour_systems — a 3-system candidate emerges.
    cands_raised, *_ = _direct_candidates(snapshot, ship, cons(max_tour_systems=3))
    assert any({"A", "B", "C"} <= set(seq) for seq in cands_raised), cands_raised[:8]

    # Default (key omitted): capped at 2 distinct systems by construction.
    cands_default, *_ = _direct_candidates(snapshot, ship, cons())
    assert not any({"A", "B", "C"} <= set(seq) for seq in cands_default), \
        cands_default[:8]

    _ortools_env(monkeypatch)
    raised = solve_tour(snapshot, ship, cons(max_tour_systems=3), MODEL,
                        objective="profit")
    assert raised["feasible"], raised
    assert {l["system_symbol"] for l in raised["legs"]} == {"S1", "S2", "S3"}, raised

    default = solve_tour(snapshot, ship, cons(), MODEL, objective="profit")
    assert default["feasible"], default
    assert len({l["system_symbol"] for l in default["legs"]}) <= 2, default


# --- T10 (F11/F6): node pruning ------------------------------------------------

def _grouped_stock(stock_idx):
    grouped = {}
    for (wp, good), src in stock_idx.items():
        grouped.setdefault(wp, {})[good] = src
    return grouped


def test_prune_keeps_pair_participants_and_drops_zero_margin_and_junk():
    pairs = [
        snap("P1", "S1", "FUEL", ask=50, bid=45, tv=20),
        snap("P2", "S1", "FUEL", ask=999, bid=150, tv=20),
        snap("P3", "S1", "ORE", ask=60, bid=50, tv=20),
        snap("P4", "S1", "ORE", ask=999, bid=200, tv=20),
        snap("P5", "S1", "GAS", ask=30, bid=20, tv=20),
        snap("P6", "S1", "GAS", ask=999, bid=90, tv=20),
    ]
    # Zero-margin pair: best ZM spread is exactly 0 (bid 100 vs ask 100). Even
    # at min_margin_per_unit=0 the pruner's floor is max(1, 0)=1 — the same
    # floor score_sequence applies — so both ZM markets must be DROPPED (F11).
    zero_margin = [
        snap("Z1", "S1", "ZM", ask=100, bid=95, tv=20),
        snap("Z2", "S1", "ZM", ask=999, bid=100, tv=20),
    ]
    junk = [snap(f"J{i:02d}", "S1", "DIRT", ask=100, bid=90, tv=20)
            for i in range(32)]
    snapshot = pairs + zero_margin + junk  # 40 markets total
    ship = _ship(wp="P1", hold=40)
    cons = _cons(min_margin_per_unit=0)
    deposits = [dict(good_symbol="FUEL", units_wanted=40, synthetic_bid=600,
                     storage_waypoint="W", storage_system="S1")]
    stocks = [dict(storage_waypoint="V", good_symbol="MEDICINE",
                   units_available=40, unit_ask=100, storage_system="S1")]
    markets, dep, stock_idx, _ = _setup(snapshot, ship, cons, deposits, stocks)
    pruned = ts._prune_nodes(markets, ship, cons, dep, _grouped_stock(stock_idx))
    assert set(pruned) == {"P1", "P2", "P3", "P4", "P5", "P6", "W", "V"}, pruned

    # Truncation: 200 profitable markets collapse to ORTOOLS_MAX_NODES with
    # start/deposit/stock waypoints EXEMPT from the cut.
    big = []
    for i in range(100):
        big.append(snap(f"S{i:03d}", "S1", f"GD{i:02d}", ask=50, bid=45, tv=20))
        big.append(snap(f"K{i:03d}", "S1", f"GD{i:02d}", ask=999, bid=150, tv=20))
    ship_big = _ship(wp="S000", hold=40)
    markets_b, dep_b, stock_b, _ = _setup(big, ship_big, cons, deposits, stocks)
    pruned_b = ts._prune_nodes(markets_b, ship_big, cons, dep_b,
                               _grouped_stock(stock_b))
    assert len(pruned_b) <= ts.ORTOOLS_MAX_NODES, len(pruned_b)
    assert {"S000", "W", "V"} <= set(pruned_b), pruned_b


# --- T11 (F6): held-cargo liquidation under ortools -----------------------------

def test_ortools_held_cargo_liquidates_without_buy_leg(monkeypatch):
    # Mirror of tests/test_tour_solver.py:224 under the ortools sequencer: a
    # laden hull with max_spend=0 must still plan the liquidation sell.
    snapshot = [snap("B", "S1", "MEDICINE", ask=999, bid=1800, tv=40)]
    ship = _ship(wp="A", hold=80, cargo=[dict(good_symbol="MEDICINE", units=40)])
    cons = _cons(max_spend=0)
    _beam_env(monkeypatch)
    beam_out = solve_tour(snapshot, ship, cons, MODEL)
    assert beam_out["feasible"], beam_out

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
             for t in l["trades"] if not t["is_buy"] and t["good_symbol"] == "MEDICINE"]
    buys = [t for l in out["legs"] for t in l["trades"] if t["is_buy"]]
    assert ("B", 40) in sells, out
    assert not buys, buys
    assert out["projected_profit"] == beam_out["projected_profit"]

    # DIRECT pin: the liquidation-only sink must survive pruning and be
    # emitted by the model itself (ship's waypoint is NOT a market here).
    cands, *_ = _direct_candidates(snapshot, ship, cons)
    assert ("B",) in cands, cands


# --- T12 (F6): pair value and held-cargo liquidation coexist --------------------

def test_ortools_books_pair_and_liquidation_together(monkeypatch):
    snapshot = [
        snap("A", "S1", "FUEL", ask=50, bid=45, tv=20),
        snap("B", "S1", "FUEL", ask=999, bid=150, tv=20),
        snap("C", "S1", "MEDICINE", ask=999, bid=500, tv=40),  # liq-only sink
    ]
    ship = _ship(wp="A", hold=40, cargo=[dict(good_symbol="MEDICINE", units=20)])
    cons = _cons(max_spend=10_000)
    _beam_env(monkeypatch)
    beam_out = solve_tour(snapshot, ship, cons, MODEL)
    assert beam_out["feasible"], beam_out

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    med_sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
                 for t in l["trades"]
                 if not t["is_buy"] and t["good_symbol"] == "MEDICINE"]
    fuel_buys = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
                 for t in l["trades"] if t["is_buy"] and t["good_symbol"] == "FUEL"]
    assert ("C", 20) in med_sells, out
    assert fuel_buys, out
    assert out["projected_profit"] >= beam_out["projected_profit"]

    # DIRECT pin: C carries zero pair value — only its liquidation prize can
    # keep it in the model.
    cands, *_ = _direct_candidates(snapshot, ship, cons)
    assert any("C" in seq for seq in cands), cands[:8]


# --- T13 (F10): deposit/stock survive + virtual end never leaks into travel ----

def test_ortools_deposit_and_stock_survive_and_virtual_end_stays_out_of_travel(
        monkeypatch):
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=20),    # scarce foreign source
        snap("B", "S1", "G", ask=999, bid=150, tv=40),   # weak arb sink
        snap("M", "S1", "MEDICINE", ask=100, bid=90, tv=40),
        snap("D", "S1", "MEDICINE", ask=999, bid=300, tv=40),
    ]
    deposits = [dict(good_symbol="G", units_wanted=40, synthetic_bid=600,
                     storage_waypoint="W", storage_system="S1")]
    stocks = [dict(storage_waypoint="V", good_symbol="MEDICINE",
                   units_available=40, unit_ask=100, storage_system="S1")]
    seen_syms = set()

    def counting_travel(a, b):
        seen_syms.add(a)
        seen_syms.add(b)
        return 300

    ship = _ship(wp="A", hold=40)
    cons = _cons(max_spend=1_000_000, _travel_fn=counting_travel)
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "0")

    cands, *_ = _direct_candidates(snapshot, ship, cons, deposits, stocks)
    assert any("W" in seq for seq in cands), cands[:8]   # deposit wp survives
    assert any("V" in seq for seq in cands), cands[:8]   # stock wp survives
    real = {"A", "B", "M", "D", "W", "V"}
    assert seen_syms and seen_syms <= real, seen_syms    # no virtual-end leak

    # The plan books the high-margin deposit (mirror of
    # test_deposit_beats_weak_arb_sell under the ortools sequencer).
    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL, deposit_candidates=deposits,
                     stock_sources=stocks)
    assert out["feasible"], out
    booked = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
              for t in l["trades"] if t.get("is_deposit")]
    assert ("W", 40) in booked, out
    assert out["deposit_value"] == 40 * 600, out


# --- T14 (F3/F7/F8): aggregate latency under the global budget ------------------

def test_ortools_multi_subset_wall_clock_stays_within_global_budget(monkeypatch):
    # Start system + 4 gate neighbors, ALL with profitable internal pairs:
    # >= 5 eligible subsets at the default cap ({S0}, {S0,S1}..{S0,S4}), 80
    # market nodes total. The budget is per CALL, not per model (F3/F7).
    snapshot = []
    for i in range(5):
        for k in range(8):
            good = f"G{i}_{k}"
            snapshot.append(snap(f"S{i}A{k}", f"S{i}", good, ask=50, bid=45, tv=20))
            snapshot.append(snap(f"S{i}B{k}", f"S{i}", good, ask=999, bid=150, tv=20))
    ship = _ship(wp="S0A0", system="S0", hold=40)
    cons = _cons(max_hops=6, max_spend=1_000_000,
                 allowed_systems=[f"S{i}" for i in range(5)])
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "0")
    monkeypatch.delenv("TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS", raising=False)

    stats = {}
    t0 = time_mod.monotonic()
    cands, *_ = _direct_candidates(snapshot, ship, cons, stats_out=stats)
    wall = time_mod.monotonic() - t0
    assert cands, "ortools produced no candidates"
    assert wall <= ts.ORTOOLS_TIME_BUDGET_SECONDS + 2, wall
    assert stats["subsets_solved"] <= ts.ORTOOLS_MAX_SUBSETS, stats
    assert stats["subsets_eligible"] >= 5, stats


# --- T15: parity on the two-sinks golden board ----------------------------------

def test_ortools_matches_beam_profit_on_two_sinks_board(monkeypatch):
    snapshot, ship, cons = _two_sinks_fixture()
    _beam_env(monkeypatch)
    beam_out = solve_tour(snapshot, ship, cons, MODEL)
    assert beam_out["feasible"], beam_out

    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    assert out["projected_profit"] == beam_out["projected_profit"]


# --- T16: default system cap by construction -------------------------------------

def test_ortools_respects_two_system_cap(monkeypatch):
    # Mirror of tests/test_tour_solver.py:39 under the ortools sequencer.
    snapshot = [snap("A", "S1", "G", 100, 90), snap("B", "S2", "G", 999, 300),
                snap("C", "S3", "G", 999, 400)]
    ship = _ship(hold=40)
    cons = _cons(max_spend=50_000, allowed_systems=["S1", "S2", "S3"])
    _ortools_env(monkeypatch)
    out = solve_tour(snapshot, ship, cons, MODEL)
    systems = {l["system_symbol"] for l in out["legs"]}
    assert len(systems) <= 2, out

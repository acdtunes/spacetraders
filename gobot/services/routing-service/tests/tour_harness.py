"""sp-f1yk W1 — reusable verification harness (seam-free scaffold).

Shared by the WAVE-1 (W4) gated tests (optimality / closed-mode / latency) but depending on
NOTHING that sp-y05b (OR-Tools) or sp-z7ng (placement) add. It provides:

  * bruteforce_best(...)          — exhaustive single-visit REFERENCE optimum. It rebuilds
    solve_tour's exact setup (row filter, markets, travel fn, sink/source/absorption
    indexing) and prices EVERY single-visit sequence with the REAL score_sequence, ordering
    with the REAL _sort_scored — so it differs from prod solve_tour ONLY in the stage-1
    sequencer (brute enumeration vs beam). Because it enumerates the SAME single-visit
    prize-collecting space OR-Tools' AddDisjunction is defined over (no revisits), an `==`
    against OR-Tools' objective is well-posed on constructed single-visit-optimal instances.

  * scenario generators           — small, well-formed single-visit market instances.
  * latency rig                   — time_solve + env-tunable ceiling/target/bench toggles.

W4 imports this module to assert OR-Tools == bruteforce_best and to wall-clock cap-6 solves.
"""
import itertools
import os
import time
from collections import namedtuple

from utils import tour_solver
from utils.tour_solver import (
    OBJECTIVE_PROFIT, OBJECTIVE_RATE, MAX_HOPS_DEFAULT,
    MAX_SNAPSHOT_AGE_MINUTES_DEFAULT,
    _build_markets, _build_deposit_sinks, _build_stock_sources, _index_absorption,
    _make_travel_fn, _effective_tour_systems, score_sequence, _sort_scored,
)

# A fully-fitted single-tier model: expected_model_version resolves to "1@e".
MODEL = {
    "fit_version": 1, "era": "e",
    "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                "buy_growth_per_step": 1.1, "n_obs": 9}},
    "recovery": {},
}
MODEL_VERSION = f"{MODEL['fit_version']}@{MODEL['era']}"
_FRESH = 9_999_999_999   # observed_at_unix far enough forward to always pass the age gate

Scenario = namedtuple("Scenario", "snapshot ship cons waypoints model")


# ------------------------------------------------------------------- scenario builders
def _row(waypoint, system, good, ask, bid, tv=40, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=waypoint, system_symbol=system, good_symbol=good,
                ask=ask, bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=_FRESH)


def _cons(allowed, max_hops=4, max_spend=5_000_000, reserve=0, max_tour_systems=None):
    cons = dict(max_hops=max_hops, min_margin_per_unit=1, max_spend=max_spend,
                working_capital_reserve=reserve,
                max_snapshot_age_minutes=MAX_SNAPSHOT_AGE_MINUTES_DEFAULT,
                allowed_systems=sorted(allowed), expected_model_version=MODEL_VERSION)
    if max_tour_systems is not None:
        cons["max_tour_systems"] = max_tour_systems
    return cons


def _ship(current_waypoint, current_system, hold=40, speed=30):
    return dict(ship_symbol=f"HARNESS-{hold}", current_waypoint=current_waypoint,
                current_system=current_system, hold_capacity=hold,
                fuel_current=2000, fuel_capacity=2000, engine_speed=speed, cargo=[])


def _waypoints(rows):
    seen, out = set(), []
    for i, row in enumerate(rows):
        wp = row["waypoint_symbol"]
        if wp in seen:
            continue
        seen.add(wp)
        out.append(dict(symbol=wp, system=row["system_symbol"], x=i * 5, y=i * 5))
    return out


def single_good_two_market(system="X1-S1", source_wp="X1-S1-A", sink_wp="X1-S1-B",
                           good="FUEL", ask=100, bid=200, hold=40, tv=40, max_hops=4):
    """The tight single-visit fixture: ONE good buyable only at the source (ask>0, bid=0)
    and sellable only at the sink (ask=0, bid>0). At max_hops=2 revisits are IMPOSSIBLE, so
    the single-visit A->B tour is the global optimum and equals the prod beam. NOTE: at
    max_hops>=4 the beam can revisit (A->B->A->B) to unlock a 2nd A-cap tranche and BEAT
    the single-visit optimum — that revisit advantage is real (see the RED#5b behavioral
    difference), not a reference bug."""
    snapshot = [
        _row(source_wp, system, good, ask=ask, bid=0, tv=tv),
        _row(sink_wp, system, good, ask=0, bid=bid, tv=tv),
    ]
    return Scenario(snapshot, _ship(source_wp, system, hold=hold),
                    _cons([system], max_hops=max_hops), _waypoints(snapshot), MODEL)


def random_single_visit_instance(seed, n_markets=4, system="X1-S1", hold=40, tv=40):
    """A deterministic small single-visit instance (one system). A guaranteed-profitable
    (source ask=low, sink bid=high) pair is ALWAYS planted so a feasible tour exists; the
    remaining markets carry seeded-random asks/bids for discriminating fuzz coverage."""
    import random
    rng = random.Random(seed)
    source, sink = f"{system}-S", f"{system}-K"
    snapshot = [
        _row(source, system, "G0", ask=rng.randint(40, 120), bid=0, tv=tv),
        _row(sink, system, "G0", ask=0, bid=rng.randint(240, 400), tv=tv),
    ]
    for i in range(max(0, n_markets - 2)):
        good = f"G{rng.randint(0, 3)}"
        snapshot.append(_row(f"{system}-M{i}", system, good,
                             ask=rng.randint(50, 500), bid=rng.randint(0, 400), tv=tv))
    return Scenario(snapshot, _ship(source, system, hold=hold), _cons([system]),
                    _waypoints(snapshot), MODEL)


def effective_cap(cons):
    """The clamped distinct-system cap the solver resolves for these constraints."""
    return _effective_tour_systems(cons)


# ------------------------------------------------------ brute-force reference optimum
def _prepared(snapshot, ship, constraints, waypoints, deposit_candidates,
              absorption, stock_sources):
    """Replicate solve_tour's setup EXACTLY so the ONLY difference from prod is the
    stage-1 sequencer (brute enumeration vs beam)."""
    age_cap = constraints.get("max_snapshot_age_minutes") or MAX_SNAPSHOT_AGE_MINUTES_DEFAULT
    cutoff = time.time() - age_cap * 60
    allowed = set(constraints.get("allowed_systems") or [ship["current_system"]])
    rows = [r for r in snapshot
            if r["observed_at_unix"] >= cutoff
            and r["system_symbol"] in allowed
            and (r["ask"] > 0 or r["bid"] > 0)]
    markets = _build_markets(rows)
    deposit_sinks = _build_deposit_sinks(deposit_candidates, markets, allowed)
    stock_idx = _build_stock_sources(stock_sources, markets, allowed)
    absorption_index = _index_absorption(absorption)
    travel_fn = _make_travel_fn(constraints, markets, ship, waypoints)
    return markets, travel_fn, deposit_sinks, stock_idx, absorption_index


def _single_visit_sequences(markets, ship, constraints):
    """EVERY single-visit hop sequence within max_hops and the distinct-system cap — the
    same single-visit space OR-Tools' AddDisjunction is defined over (no revisits)."""
    max_hops = min(constraints.get("max_hops") or MAX_HOPS_DEFAULT, MAX_HOPS_DEFAULT)
    max_systems = _effective_tour_systems(constraints)
    start_system = ship["current_system"]
    waypoints = sorted(markets)
    for length in range(1, max_hops + 1):
        for combo in itertools.permutations(waypoints, length):
            systems = frozenset([start_system] + [markets[wp]["system"] for wp in combo])
            if len(systems) <= max_systems:
                yield combo


def bruteforce_best(snapshot, ship, constraints, model, objective, waypoints=None,
                    deposit_candidates=None, absorption=None, stock_sources=None):
    """Exhaustive single-visit reference optimum. Returns (best_objective_value, best),
    where `best` is the REAL score_sequence result dict for the winning sequence (carrying
    its `legs`) and best_objective_value is profit under OBJECTIVE_PROFIT / cph under
    OBJECTIVE_RATE — the same quantity _sort_scored optimizes. (0, None) if nothing is
    profitable. NOT a general optimality proof: it holds on constructed single-visit
    instances where revisits cannot help."""
    markets, travel_fn, deposit_sinks, stock_idx, absorption_index = _prepared(
        snapshot, ship, constraints, waypoints, deposit_candidates, absorption, stock_sources)
    if not markets:
        return 0, None
    scored, seen = [], set()
    for seq in _single_visit_sequences(markets, ship, constraints):
        result = score_sequence(seq, markets, ship, constraints, model, travel_fn,
                                deposit_sinks, absorption_index, stock_idx)
        signature = tuple((leg["waypoint_symbol"],
                           tuple((t["good_symbol"], t["units"], t["is_buy"],
                                  t["is_deposit"], t["is_stock"], t["expected_unit_price"])
                                 for t in leg["trades"]))
                          for leg in result["legs"])
        if signature in seen:
            continue
        seen.add(signature)
        summary = "->".join(leg["waypoint_symbol"] for leg in result["legs"]) or "->".join(seq)
        scored.append((result, summary))
    if not scored:
        return 0, None
    _sort_scored(scored, objective)
    best, _ = scored[0]
    if best["profit"] <= 0:
        return 0, None
    value = best["cph"] if objective == OBJECTIVE_RATE else best["profit"]
    return value, best


# -------------------------------------------------------------------- latency rig
def time_solve(callable_fn):
    """Wall-clock a zero-arg callable. Returns (result, elapsed_seconds)."""
    start = time.perf_counter()
    result = callable_fn()
    return result, time.perf_counter() - start


def latency_hard_ceiling_seconds():
    """HARD contract ceiling (bead (d) / spec-Risk routing.timeout.tsp). Env-overridable."""
    return float(os.environ.get("LATENCY_HARD_CEILING_SECONDS", "60"))


def latency_soft_target_seconds():
    """SOFT regression target — a small multiple of the 2-5s anytime cap. Env-overridable;
    recalibrated in W4 once sp-y05b's real cap-6 solve times are measured."""
    return float(os.environ.get("LATENCY_SOFT_TARGET_SECONDS", "15"))


def latency_bench_enabled():
    """The RUN_LATENCY_BENCH opt-in for the informational cap-2 vs cap-6 benchmark."""
    return os.environ.get("RUN_LATENCY_BENCH", "").strip() not in ("", "0", "false", "False")

"""Model-artifact A/B replay — the offline adoption gate for a refitted market model.

For each (sample time, home system, hull) case the solver plans the same
reconstructed snapshot twice, once under the incumbent artifact and once under
the candidate, at the deployed objective. The two chosen ROUTES are then valued
under ONE common evaluator — the candidate model — by re-running the tranche
allocator (score_sequence) over the incumbent's route with candidate prices.

Why routes, and why a single evaluator: each model's own projection is its
promise, and a stale model's promise is exactly the over-statement a refit
exists to remove — promise-vs-promise would reward whichever model flatters
itself hardest. The route (which markets, in what order) is the decision the
model actually owns per solve; the executor re-decides units at dock time, so
re-allocating trades along the frozen route under the common evaluator mirrors
what executing that route would earn if the candidate's prices are the truth.

Verdict per case:
  win  — the candidate's route out-values the incumbent's route under the
         common evaluator (including finding a worthwhile route where the
         incumbent found none),
  tie  — both models choose the same route, or values are equal (including
         both refusing an unprofitable window),
  loss — the incumbent's route out-values the candidate's; an unprofitable
         plan values at 0 (a hull idles rather than flying a losing route).

Adoption bar: losses must stay a small minority of cases. A loss epidemic
means the candidate interacts badly with the solver's search heuristics
(beam cuts, sequencer pruning) and must not be adopted on aggregate charm.

Usage (from gobot/services/routing-service, with the model venv):
  python replay_model_ab.py --incumbent old_model.json [--candidate new.json]
      [--hours 48] [--samples 12] [--hulls 80,220] [--max-spend 2000000]
      [--reserve 50000] [--tour-overhead-seconds 60] [--max-loss-share 0.10]
      [--json out.json]
"""
import argparse
import json
import os
import sys
from collections import defaultdict
from datetime import timedelta

from model.extract import db_engine
from replay_objective import (STALENESS_MINUTES, ENGINE_SPEED, compute_allowed,
                              fetch_gate_neighbors, fetch_history,
                              fetch_waypoint_coords, open_era, reconstruct_snapshot)
from utils import tour_solver
from utils.tour_solver import OBJECTIVE_RATE, solve_tour

EPS_REL = 1e-9


def load_artifact(path):
    with open(path) as f:
        model = json.load(f)
    return model, f"{model['fit_version']}@{model['era']}"


def route_of(result):
    """The chosen route as an ordered waypoint tuple; empty when infeasible."""
    if not result or not result.get("feasible"):
        return ()
    return tuple(l["waypoint_symbol"] for l in result.get("legs") or [])


def plan_value(profit, cph, objective):
    """The metric the deployed objective ranks plans by. An absent or
    unprofitable plan values at 0: the hull idles rather than flying a loser."""
    if profit is None or profit <= 0:
        return 0.0
    return float(cph) if objective == OBJECTIVE_RATE else float(profit)


def classify(cand_value, inc_value, same_route):
    if same_route:
        return "tie"
    scale = max(abs(cand_value), abs(inc_value), 1.0)
    if cand_value - inc_value > EPS_REL * scale:
        return "win"
    if inc_value - cand_value > EPS_REL * scale:
        return "loss"
    return "tie"


def rescore_route(route, scoped, ship, cons, model, waypoints):
    """Value an already-chosen route under `model`: rebuild the market/travel
    context exactly as solve_tour does, then run the real tranche allocator over
    the frozen hop sequence. Returns score_sequence's result dict, or None when
    the route is empty or leaves the priced market set."""
    if not route:
        return None
    rows = [r for r in scoped if r["ask"] > 0 or r["bid"] > 0]
    if not rows:
        return None
    markets = tour_solver._build_markets(rows)
    if any(wp not in markets for wp in route):
        return None
    travel_fn = tour_solver._make_travel_fn(cons, markets, ship, waypoints)
    gate_fee_fn = tour_solver._make_gate_fee_fn(cons, markets, ship)
    return tour_solver.score_sequence(tuple(route), markets, ship, cons, model,
                                      travel_fn, gate_fee_fn=gate_fee_fn)


def run_ab_case(snapshot, waypoints, home, allowed, hold, max_spend, reserve,
                incumbent, inc_version, candidate, cand_version, objective):
    home_markets = sorted({s["waypoint_symbol"] for s in snapshot
                           if s["system_symbol"] == home})
    if not home_markets:
        return None
    ship = dict(ship_symbol=f"REPLAY-{hold}", current_waypoint=home_markets[0],
                current_system=home, hold_capacity=hold,
                fuel_current=400, fuel_capacity=400,
                engine_speed=ENGINE_SPEED, cargo=[])
    base_cons = dict(max_hops=6, min_margin_per_unit=1,
                     max_snapshot_age_minutes=STALENESS_MINUTES,
                     max_spend=max_spend, working_capital_reserve=reserve,
                     allowed_systems=sorted(allowed))
    scoped = [s for s in snapshot if s["system_symbol"] in allowed]
    wps = [w for w in waypoints if w["system"] in allowed]

    inc_cons = dict(base_cons, expected_model_version=inc_version)
    cand_cons = dict(base_cons, expected_model_version=cand_version)
    inc_res = solve_tour(scoped, dict(ship), inc_cons, incumbent, waypoints=wps,
                         objective=objective)
    cand_res = solve_tour(scoped, dict(ship), cand_cons, candidate, waypoints=wps,
                          objective=objective)
    if not inc_res.get("feasible") and not cand_res.get("feasible"):
        return None

    inc_route, cand_route = route_of(inc_res), route_of(cand_res)
    cand_value = plan_value(cand_res.get("projected_profit"),
                            cand_res.get("projected_credits_per_hour"), objective)
    rescored = rescore_route(inc_route, scoped, dict(ship), dict(cand_cons),
                             candidate, wps)
    inc_value = (plan_value(rescored["profit"], rescored["cph"], objective)
                 if rescored else 0.0)
    verdict = classify(cand_value, inc_value, cand_route == inc_route)
    return dict(home=home, hold=hold,
                verdict=verdict,
                cand_value=cand_value, inc_value=inc_value,
                cand_route=cand_route, inc_route=inc_route,
                cand_promise=cand_res.get("projected_profit") or 0,
                inc_promise=inc_res.get("projected_profit") or 0)


def ab_verdict(cases, max_loss_share):
    """Deterministic adoption verdict over the classified cases. Empty input
    fails safe (never adopt on no evidence)."""
    wins = sum(1 for c in cases if c["verdict"] == "win")
    losses = sum(1 for c in cases if c["verdict"] == "loss")
    ties = sum(1 for c in cases if c["verdict"] == "tie")
    n = len(cases)
    loss_share = losses / n if n else 1.0
    return dict(cases=n, wins=wins, losses=losses, ties=ties,
                loss_share=loss_share,
                adopt=bool(n > 0 and loss_share <= max_loss_share))


def mirror_deployed_solver_env():
    """Mirror the deployed routing-service knobs (run.sh) so the replayed solves
    take the same search paths production does. Called from main() only — the
    solver resolves these per solve, and importing this module must never mutate
    the process env (a leaked default would reshape unrelated callers' solves).
    Every value stays operator-overridable."""
    os.environ.setdefault("TOUR_SOLVER_OBJECTIVE", "rate")
    os.environ.setdefault("TOUR_SOLVER_RATE_ARMED_LONG", "1")
    os.environ.setdefault("TOUR_SOLVER_SEQUENCER", "ortools")
    os.environ.setdefault("TOUR_SOLVER_MAX_PLANNED_TRANCHES", "3")
    os.environ.setdefault("TOUR_SOLVER_FULL_SCORE_TOP_N", "150")
    os.environ.setdefault("TOUR_SOLVER_ORTOOLS_MAX_NODES", "160")
    os.environ.setdefault("TOUR_SOLVER_INTER_SYSTEM_TRAVEL_BASE_SECONDS", "750")
    os.environ.setdefault("TOUR_SOLVER_INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS", "1030")


def main():
    mirror_deployed_solver_env()
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--incumbent", required=True,
                    help="path to the artifact being replaced")
    ap.add_argument("--candidate", default="model_artifacts/market_model.json",
                    help="path to the refitted artifact under adoption review")
    ap.add_argument("--hours", type=int, default=48)
    ap.add_argument("--samples", type=int, default=12)
    ap.add_argument("--hulls", default="80,220")
    ap.add_argument("--max-spend", type=int, default=2_000_000)
    ap.add_argument("--reserve", type=int, default=50_000)
    ap.add_argument("--max-loss-share", type=float, default=0.10,
                    help="losses above this share of cases block adoption")
    ap.add_argument("--max-homes", type=int, default=0,
                    help="bound the home systems per sample (0 = all); homes are "
                         "picked by an even stride over the sorted set, so a bounded "
                         "run is deterministic and spread across the map")
    ap.add_argument("--json", default="", help="write per-case results to this file")
    args = ap.parse_args()

    incumbent, inc_version = load_artifact(args.incumbent)
    candidate, cand_version = load_artifact(args.candidate)
    objective = tour_solver._resolve_objective(None)
    engine = db_engine()
    rows = fetch_history(engine, args.hours)
    if not rows:
        print("no market_price_history rows in the window; nothing to replay")
        return 1
    neighbors = fetch_gate_neighbors(engine, open_era(engine))
    coords = fetch_waypoint_coords(engine)
    hulls = [int(h) for h in args.hulls.split(",") if h]

    newest = max(r.recorded_at for r in rows)
    window_start = newest - timedelta(hours=args.hours)
    step = (newest - window_start) / max(1, args.samples)
    samples = [window_start + step * (i + 1) for i in range(args.samples)]

    cases = []
    for sample_t in samples:
        snapshot = reconstruct_snapshot(rows, sample_t)
        waypoints = [dict(symbol=wp, system=sys_, x=int(x), y=int(y))
                     for wp, (sys_, x, y) in coords.items()]
        by_system = defaultdict(set)
        for s in snapshot:
            by_system[s["system_symbol"]].add(s["waypoint_symbol"])
        homes = [h for h, markets in sorted(by_system.items()) if len(markets) >= 2]
        if args.max_homes and len(homes) > args.max_homes:
            stride = len(homes) / args.max_homes
            homes = [homes[int(i * stride)] for i in range(args.max_homes)]
        for home in homes:
            allowed = compute_allowed(home, neighbors, by_system, 1)
            for hold in hulls:
                case = run_ab_case(snapshot, waypoints, home, allowed, hold,
                                   args.max_spend, args.reserve,
                                   incumbent, inc_version, candidate, cand_version,
                                   objective)
                if case:
                    case["sample"] = str(sample_t)
                    cases.append(case)

    verdict = ab_verdict(cases, args.max_loss_share)
    tot_cand = sum(c["cand_value"] for c in cases)
    tot_inc = sum(c["inc_value"] for c in cases)
    promise_deltas = [c["cand_promise"] - c["inc_promise"] for c in cases]
    print(f"\n=== MODEL A/B ({inc_version} -> {cand_version}, objective {objective}) ===")
    print("legend: both routes valued under the CANDIDATE model (common evaluator); "
          "`promise delta` is candidate self-projection minus incumbent "
          "self-projection — the honesty correction, diagnostic only")
    print(f"cases: {verdict['cases']}  win {verdict['wins']} / loss {verdict['losses']} "
          f"/ tie {verdict['ties']}  (loss share {verdict['loss_share']:.1%}, "
          f"max {args.max_loss_share:.0%})")
    print(f"summed route value under candidate eval: candidate {tot_cand:>14,.0f}")
    print(f"                                         incumbent {tot_inc:>14,.0f}")
    if promise_deltas:
        promise_deltas.sort()
        print(f"promise delta per case: median {promise_deltas[len(promise_deltas)//2]:,.0f} cr")
    for c in sorted((c for c in cases if c["verdict"] == "loss"),
                    key=lambda c: c["cand_value"] - c["inc_value"])[:10]:
        print(f"  loss: {c['sample']} {c['home']} h{c['hold']}  candidate "
              f"{c['cand_value']:,.0f} vs incumbent-route {c['inc_value']:,.0f}")
    print(f"ADOPT: {verdict['adopt']}")
    if args.json:
        with open(args.json, "w") as f:
            json.dump(dict(incumbent=inc_version, candidate=cand_version,
                           verdict=verdict, cases=cases), f, indent=1, default=str)
        print(f"per-case detail written to {args.json}")
    return 0 if verdict["adopt"] else 1


if __name__ == "__main__":
    sys.exit(main())

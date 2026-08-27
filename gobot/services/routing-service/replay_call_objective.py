"""Objective A/B replay for the API-call cost term — the adoption gate for saturation-aware
selection.

For each (sample time, home system, hull) case the SAME model plans the SAME reconstructed
snapshot twice: once with no saturation reading (the incumbent — selection on credits/hour)
and once at the measured limiter saturation (the candidate). Only the request's saturation
permille differs, so the two arms share candidate generation, tranche pricing and every
guard; the ONLY thing that can move is which candidate selection picks.

Both chosen ROUTES are then valued under ONE common evaluator: the tranche allocator re-run
over each frozen route with a saturation-free constraints dict. Each arm's own projection
would already be commensurable here (identical model, identical pricing), but routing both
through one code path removes the question entirely.

WHY BOTH AXES ARE REPORTED. The natural metric is credits/hour, and it is the wrong axis for
the change under test: the whole finding is that at the request ceiling the fleet is bound by
calls, not by wall clock, so a candidate that trades hourly rate for per-call rate is doing
exactly what it was built to do. Reporting only credits/hour would score the change against
the objective it is replacing. Reporting only credits-per-call would flatter it. So both are
reported per case, and the adoption call is made on a THIRD metric that is neither arm's
objective:

  FLEET RATE UNDER A SHARED CEILING. N hulls each fly their chosen tour repeatedly. Hull h
  earns cph_h and demands d_h = calls_h x 3600 / seconds_h requests per hour. If the fleet's
  total demand D = sum(d_h) sits under the account ceiling C = ceiling_req_per_sec x 3600,
  every hull cycles at its own pace and the fleet earns sum(cph_h). Once D exceeds C every
  hull's cycle stretches by D/C, so the fleet earns sum(cph_h) x C/D. That is the number a
  fleet actually banks, it uses only measured quantities (the ceiling, the hull count), and
  it is not the objective either arm optimises.

Verdict per case, on each axis:
  win  — the candidate's route out-values the incumbent's under the common evaluator,
  tie  — both arms choose the same route, or the values are equal,
  loss — the incumbent's route out-values the candidate's.

Adoption bar: losses must stay a small minority of cases on the DECIDING axis, and the fleet
rate must improve. A loss epidemic means the term interacts badly with the solver's search
heuristics and must not be adopted on aggregate charm.

Usage (from gobot/services/routing-service, with the model venv):
  python replay_call_objective.py [--saturation-permille 1000] [--hours 48] [--samples 12]
      [--hulls 80,220] [--max-spend 2000000] [--reserve 50000] [--fleet-hulls 60]
      [--ceiling-req-per-sec 2.0] [--max-loss-share 0.10] [--calibrate] [--json out.json]
"""
import argparse
import concurrent.futures as futures
import json
import os
import statistics
import sys
from collections import defaultdict
from datetime import timedelta

from model.extract import db_engine
from replay_model_ab import load_artifact, mirror_deployed_solver_env, route_of
from replay_objective import (STALENESS_MINUTES, ENGINE_SPEED, compute_allowed,
                              fetch_gate_neighbors, fetch_history,
                              fetch_waypoint_coords, open_era, reconstruct_snapshot)
from utils import tour_solver
from utils.tour_solver import OBJECTIVE_RATE, solve_tour

EPS_REL = 1e-9


def rescore_route(route, scoped, ship, cons, model, waypoints):
    """Value an already-chosen route under one common evaluator: rebuild the market/travel
    context exactly as solve_tour does, then run the real tranche allocator over the frozen
    hop sequence. Returns score_sequence's result dict, or None when the route is empty or
    leaves the priced market set."""
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


def axes(scored):
    """(credits/hour, credits/call, calls/hour) for a common-evaluator result. An absent or
    unprofitable plan values at 0 on every axis: the hull idles rather than flying a loser."""
    if not scored or scored["profit"] <= 0 or scored["seconds"] <= 0:
        return 0.0, 0.0, 0.0
    calls = scored.get("api_calls") or 0.0
    cpc = scored["profit"] / calls if calls > 0 else 0.0
    return scored["cph"], cpc, calls * 3600.0 / scored["seconds"]


def depth_stats(scored, scoped):
    """(units moved, units-weighted mean tradeVolume, own-volume share) for a plan.

    OWN-VOLUME SHARE is units/tradeVolume summed over the plan's trades — the same ratio
    sp-as4k4 regressed realized margin on, and identically the plan's transaction-request
    count. So the call term and the crowding term are not merely correlated: minimising
    requests IS minimising the plan's own footprint in the markets it touches, and a
    replay where the candidate lifts weighted depth is one where both improve together."""
    if not scored:
        return 0.0, 0.0, 0.0
    tv = {(r["waypoint_symbol"], r["good_symbol"]): r.get("trade_volume") or 0
          for r in scoped}
    units = weighted = share = 0.0
    for leg in scored["legs"]:
        for trade in leg["trades"]:
            volume = tv.get((leg["waypoint_symbol"], trade["good_symbol"]), 0)
            if volume <= 0:
                continue
            units += trade["units"]
            weighted += trade["units"] * volume
            share += trade["units"] / volume
    return units, (weighted / units if units else 0.0), share


def classify(cand, inc, same_route):
    if same_route:
        return "tie"
    scale = max(abs(cand), abs(inc), 1.0)
    if cand - inc > EPS_REL * scale:
        return "win"
    if inc - cand > EPS_REL * scale:
        return "loss"
    return "tie"


def run_case(snapshot, waypoints, home, allowed, hold, max_spend, reserve, version,
             model, saturation_permille):
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
                     allowed_systems=sorted(allowed),
                     expected_model_version=version)
    scoped = [s for s in snapshot if s["system_symbol"] in allowed]
    wps = [w for w in waypoints if w["system"] in allowed]

    inc_res = solve_tour(scoped, dict(ship), dict(base_cons), model, waypoints=wps,
                         objective=OBJECTIVE_RATE)
    cand_res = solve_tour(scoped, dict(ship),
                          dict(base_cons, api_saturation_permille=saturation_permille),
                          model, waypoints=wps, objective=OBJECTIVE_RATE)
    if not inc_res.get("feasible") and not cand_res.get("feasible"):
        return None

    inc_route, cand_route = route_of(inc_res), route_of(cand_res)
    # ONE evaluator, and it carries NO saturation: the scorer must not be able to see which
    # arm produced the route it is pricing.
    inc_scored = rescore_route(inc_route, scoped, dict(ship), dict(base_cons), model, wps)
    cand_scored = rescore_route(cand_route, scoped, dict(ship), dict(base_cons), model, wps)
    inc_cph, inc_cpc, inc_demand = axes(inc_scored)
    cand_cph, cand_cpc, cand_demand = axes(cand_scored)
    inc_units, inc_depth, inc_share = depth_stats(inc_scored, scoped)
    cand_units, cand_depth, cand_share = depth_stats(cand_scored, scoped)
    same = cand_route == inc_route
    return dict(home=home, hold=hold, same_route=same,
                inc_units=inc_units, cand_units=cand_units,
                inc_depth=inc_depth, cand_depth=cand_depth,
                inc_share=inc_share, cand_share=cand_share,
                inc_cph=inc_cph, cand_cph=cand_cph,
                inc_cpc=inc_cpc, cand_cpc=cand_cpc,
                inc_demand=inc_demand, cand_demand=cand_demand,
                inc_stops=len(inc_route), cand_stops=len(cand_route),
                inc_calls=(inc_scored or {}).get("api_calls", 0.0),
                cand_calls=(cand_scored or {}).get("api_calls", 0.0),
                inc_seconds=(inc_scored or {}).get("seconds", 0),
                cand_seconds=(cand_scored or {}).get("seconds", 0),
                cph_verdict=classify(cand_cph, inc_cph, same),
                cpc_verdict=classify(cand_cpc, inc_cpc, same),
                inc_route=inc_route, cand_route=cand_route)


_WORKER = {}


def _init_worker(model_path, hours, max_homes, hulls, max_spend, reserve,
                 saturation_permille, solver_env):
    """One DB read and one artifact load per worker process, reused across samples. The
    parent's mirrored solver env is re-applied explicitly because a spawned child does not
    inherit a mutation made after import."""
    os.environ.update(solver_env)
    engine = db_engine()
    rows = fetch_history(engine, hours)
    model, version = load_artifact(model_path)
    _WORKER.update(rows=rows, model=model, version=version,
                   neighbors=fetch_gate_neighbors(engine, open_era(engine)),
                   coords=fetch_waypoint_coords(engine), max_homes=max_homes,
                   hulls=hulls, max_spend=max_spend, reserve=reserve,
                   saturation_permille=saturation_permille)


def _run_sample(sample_t):
    """Every (home, hull) case at one sample time. Returns the classified cases."""
    w = _WORKER
    snapshot = reconstruct_snapshot(w["rows"], sample_t)
    waypoints = [dict(symbol=wp, system=sys_, x=int(x), y=int(y))
                 for wp, (sys_, x, y) in w["coords"].items()]
    by_system = defaultdict(set)
    for s in snapshot:
        by_system[s["system_symbol"]].add(s["waypoint_symbol"])
    homes = [h for h, markets in sorted(by_system.items()) if len(markets) >= 2]
    if w["max_homes"] and len(homes) > w["max_homes"]:
        stride = len(homes) / w["max_homes"]
        homes = [homes[int(i * stride)] for i in range(w["max_homes"])]
    out = []
    for home in homes:
        allowed = compute_allowed(home, w["neighbors"], by_system, 1)
        for hold in w["hulls"]:
            case = run_case(snapshot, waypoints, home, allowed, hold, w["max_spend"],
                            w["reserve"], w["version"], w["model"],
                            w["saturation_permille"])
            if case:
                case["sample"] = str(sample_t)
                out.append(case)
    return out


def tally(cases, key):
    wins = sum(1 for c in cases if c[key] == "win")
    losses = sum(1 for c in cases if c[key] == "loss")
    ties = sum(1 for c in cases if c[key] == "tie")
    n = len(cases)
    return dict(cases=n, wins=wins, losses=losses, ties=ties,
                loss_share=(losses / n if n else 1.0))


def fleet_rate(cases, arm, fleet_hulls, ceiling_req_per_sec):
    """Credits/hour a fleet of `fleet_hulls` hulls banks flying these plans under the shared
    request ceiling. Below the ceiling it is the plain sum of hourly rates; above it, every
    hull's cycle stretches by the overdraft factor and the whole fleet is scaled down by it.
    Neither arm optimises this number, which is why it can decide between them."""
    if not cases:
        return 0.0, 0.0
    per_hull_cph = statistics.fmean(c[f"{arm}_cph"] for c in cases)
    per_hull_demand = statistics.fmean(c[f"{arm}_demand"] for c in cases)
    demand = per_hull_demand * fleet_hulls
    ceiling = ceiling_req_per_sec * 3600.0
    gross = per_hull_cph * fleet_hulls
    if demand <= ceiling or demand <= 0:
        return gross, demand
    return gross * ceiling / demand, demand


def ceiling_sweep(cases, fleet_hulls, full_ceiling_req_per_sec):
    """Fleet rate for both arms across the share of the request ceiling the TOUR fleet
    actually gets, plus the share at which they tie.

    The sweep exists because the verdict turns on that share and nothing else. The account
    ceiling is shared: market scanning, fleet polls, contract and construction hauling all
    draw on it, so the budget left for tours is strictly less than the account's. Below the
    tie the fleet is not tour-call-bound and the call term only costs hourly rate; above it
    every hull's cycle is already stretched by the queue and per-call efficiency is the
    whole game. Reporting one number for one assumed ceiling would hide that."""
    rows, tie = [], None
    prev = None
    for step in range(4, 41):
        ceiling = full_ceiling_req_per_sec * step / 40.0
        inc, _ = fleet_rate(cases, "inc", fleet_hulls, ceiling)
        cand, _ = fleet_rate(cases, "cand", fleet_hulls, ceiling)
        rows.append((ceiling, inc, cand))
        sign = cand > inc
        if prev is not None and sign != prev and tie is None:
            tie = ceiling
        prev = sign
    return rows, tie


def main():
    mirror_deployed_solver_env()
    # The live per-visit sink depth, which the shared mirror does not carry. It sets how
    # many tranches a sell visit realizes, so it moves the very unit counts the call term
    # chunks — replaying at the in-code default would price a plan shape production does
    # not fly.
    os.environ.setdefault("TOUR_SOLVER_REALIZED_SINK_TRANCHES", "3.0")
    # The replay decides what the saturation reading DOES; it must never inherit one.
    os.environ.pop(tour_solver.API_SATURATION_ENV_VAR, None)
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", default="model_artifacts/market_model.json")
    ap.add_argument("--saturation-permille", type=int, default=1000,
                    help="the candidate arm's limiter reading; 1000 = pinned at the ceiling")
    ap.add_argument("--hours", type=int, default=48)
    ap.add_argument("--samples", type=int, default=12)
    ap.add_argument("--hulls", default="80,220")
    ap.add_argument("--max-spend", type=int, default=2_000_000)
    ap.add_argument("--reserve", type=int, default=50_000)
    ap.add_argument("--fleet-hulls", type=int, default=60)
    ap.add_argument("--ceiling-req-per-sec", type=float, default=2.0,
                    help="the ACCOUNT request ceiling")
    ap.add_argument("--tour-ceiling-share", type=float, default=0.693,
                    help="share of the account ceiling left for tour traffic after "
                         "scanning, polls, contract and construction draw; measured at "
                         "0.693 from a 180s live endpoint-mix sample at the ceiling")
    ap.add_argument("--max-loss-share", type=float, default=0.10)
    ap.add_argument("--max-homes", type=int, default=0)
    ap.add_argument("--calibrate", action="store_true",
                    help="report the incumbent's tour seconds-per-call ratio and stop")
    ap.add_argument("--workers", type=int, default=8,
                    help="sample-level process parallelism; each worker holds its own "
                         "history read, so the wall cost is one solve pass, not N")
    ap.add_argument("--json", default="")
    args = ap.parse_args()

    model, version = load_artifact(args.model)
    engine = db_engine()
    rows = fetch_history(engine, args.hours)
    if not rows:
        print("no market_price_history rows in the window; nothing to replay")
        return 1
    hulls = [int(h) for h in args.hulls.split(",") if h]

    newest = max(r.recorded_at for r in rows)
    window_start = newest - timedelta(hours=args.hours)
    step = (newest - window_start) / max(1, args.samples)
    samples = [window_start + step * (i + 1) for i in range(args.samples)]
    del rows

    solver_env = {k: v for k, v in os.environ.items() if k.startswith("TOUR_SOLVER_")}
    init_args = (args.model, args.hours, args.max_homes, hulls, args.max_spend,
                 args.reserve, args.saturation_permille, solver_env)
    cases = []
    with futures.ProcessPoolExecutor(max_workers=args.workers,
                                     initializer=_init_worker,
                                     initargs=init_args) as pool:
        for done, batch in enumerate(pool.map(_run_sample, samples), start=1):
            cases.extend(batch)
            print(f"  sample {done}/{len(samples)}: {len(batch)} cases "
                  f"({len(cases)} total)", flush=True)

    if not cases:
        print("no joint-feasible cases; nothing to decide on")
        return 1

    if args.calibrate:
        ratios = [c["inc_seconds"] / c["inc_calls"] for c in cases if c["inc_calls"] > 0]
        ratios.sort()
        print(f"=== CALL-SECONDS CALIBRATION ({len(ratios)} incumbent plans) ===")
        print(f"tour seconds per planned call: "
              f"p25 {ratios[len(ratios)//4]:.1f}  median {statistics.median(ratios):.1f}  "
              f"p75 {ratios[3*len(ratios)//4]:.1f}  mean {statistics.fmean(ratios):.1f}")
        print(f"median plan: {statistics.median(c['inc_seconds'] for c in cases):,.0f}s, "
              f"{statistics.median(c['inc_calls'] for c in cases):.1f} calls, "
              f"{statistics.median(c['inc_stops'] for c in cases):.0f} stops")
        return 0

    cph = tally(cases, "cph_verdict")
    cpc = tally(cases, "cpc_verdict")
    inc_fleet, inc_demand = fleet_rate(cases, "inc", args.fleet_hulls, args.ceiling_req_per_sec)
    cand_fleet, cand_demand = fleet_rate(cases, "cand", args.fleet_hulls, args.ceiling_req_per_sec)
    fleet_delta = (cand_fleet / inc_fleet - 1) if inc_fleet else 0.0
    tour_ceiling = args.ceiling_req_per_sec * args.tour_ceiling_share
    inc_tour, _ = fleet_rate(cases, "inc", args.fleet_hulls, tour_ceiling)
    cand_tour, _ = fleet_rate(cases, "cand", args.fleet_hulls, tour_ceiling)
    # The verdict rests on the budget the TOUR fleet actually gets, not on the account's:
    # scanning, polls, contract and construction traffic take their share first, so judging
    # at the full ceiling would credit tours with a budget they never see.
    adopt = bool(cand_tour > inc_tour and cpc["loss_share"] <= args.max_loss_share)

    print(f"\n=== CALL-COST OBJECTIVE A/B (model {version}, "
          f"candidate at {args.saturation_permille/10:.1f}% limiter load) ===")
    print("legend: both routes valued by ONE saturation-free evaluator; credits/hour is the "
          "incumbent objective's axis, credits/call the candidate's, and the fleet rate is "
          "neither — it is what a fleet banks under the shared ceiling.")
    print(f"cases: {len(cases)}   route changed in {sum(1 for c in cases if not c['same_route'])}")
    for name, t in (("credits/HOUR", cph), ("credits/CALL", cpc)):
        print(f"  {name:>13}: win {t['wins']:>4} / loss {t['losses']:>4} / tie {t['ties']:>4}"
              f"   (loss share {t['loss_share']:.1%})")
    print(f"per-hull mean: cph {statistics.fmean(c['inc_cph'] for c in cases):>12,.0f} -> "
          f"{statistics.fmean(c['cand_cph'] for c in cases):>12,.0f}   "
          f"cpc {statistics.fmean(c['inc_cpc'] for c in cases):>9,.0f} -> "
          f"{statistics.fmean(c['cand_cpc'] for c in cases):>9,.0f}")
    print(f"median stops: {statistics.median(c['inc_stops'] for c in cases):.0f} -> "
          f"{statistics.median(c['cand_stops'] for c in cases):.0f}   "
          f"median calls: {statistics.median(c['inc_calls'] for c in cases):.1f} -> "
          f"{statistics.median(c['cand_calls'] for c in cases):.1f}")
    moved = [c for c in cases if c["inc_units"] > 0 and c["cand_units"] > 0]
    if moved:
        print(f"units-weighted market depth (tradeVolume) of chosen legs: "
              f"{statistics.fmean(c['inc_depth'] for c in moved):.0f} -> "
              f"{statistics.fmean(c['cand_depth'] for c in moved):.0f}   "
              f"own-volume share created: "
              f"{statistics.fmean(c['inc_share'] for c in moved):.2f} -> "
              f"{statistics.fmean(c['cand_share'] for c in moved):.2f}   "
              f"(units {statistics.fmean(c['inc_units'] for c in moved):.0f} -> "
              f"{statistics.fmean(c['cand_units'] for c in moved):.0f})")
    print(f"fleet demand at {args.fleet_hulls} hulls: {inc_demand:,.0f} -> {cand_demand:,.0f} "
          f"req/hr against a {args.ceiling_req_per_sec*3600:,.0f} ceiling")
    print(f"FLEET RATE at the full account ceiling: {inc_fleet:>14,.0f} -> "
          f"{cand_fleet:>14,.0f} cr/hr  ({fleet_delta:+.2%})")
    print(f"FLEET RATE at the measured tour share ({args.tour_ceiling_share:.0%} = "
          f"{tour_ceiling:.2f} req/s): {inc_tour:>14,.0f} -> {cand_tour:>14,.0f} cr/hr  "
          f"({(cand_tour/inc_tour - 1) if inc_tour else 0:+.2%})")

    sweep, tie = ceiling_sweep(cases, args.fleet_hulls, args.ceiling_req_per_sec)
    print(f"\nfleet rate vs the share of the {args.ceiling_req_per_sec:.2f} req/s ceiling "
          f"the TOUR fleet gets (the rest goes to scanning, polls, contract and "
          f"construction traffic):")
    for ceiling, inc, cand in sweep:
        if round(ceiling / args.ceiling_req_per_sec * 40) % 4:
            continue
        print(f"  {ceiling:5.2f} req/s ({ceiling/args.ceiling_req_per_sec:4.0%})  "
              f"incumbent {inc:>13,.0f}  candidate {cand:>13,.0f}  "
              f"{(cand/inc - 1) if inc else 0:+7.2%}")
    print(f"break-even tour ceiling: "
          + (f"{tie:.2f} req/s ({tie/args.ceiling_req_per_sec:.0%} of the account "
             f"ceiling) — the candidate wins BELOW it, where the tour fleet's "
             f"own demand outruns its budget" if tie else
             "none in range (one arm wins throughout)"))

    for axis, key in (("credits/hour", "cph"), ("credits/call", "cpc")):
        losses = sorted((c for c in cases if c[f"{key}_verdict"] == "loss"),
                        key=lambda c: c[f"cand_{key}"] - c[f"inc_{key}"])
        if not losses:
            continue
        deltas = [c[f"cand_{key}"] - c[f"inc_{key}"] for c in losses]
        print(f"\n{axis} loss distribution ({len(losses)} cases): "
              f"worst {deltas[0]:,.0f}  p25 {deltas[len(deltas)//4]:,.0f}  "
              f"median {statistics.median(deltas):,.0f}  p90 {deltas[9*len(deltas)//10]:,.0f}")
        for c in losses[:5]:
            print(f"  loss: {c['sample']} {c['home']} h{c['hold']}  "
                  f"{c[f'cand_{key}']:,.0f} vs {c[f'inc_{key}']:,.0f} "
                  f"(stops {c['inc_stops']}->{c['cand_stops']}, "
                  f"calls {c['inc_calls']:.0f}->{c['cand_calls']:.0f})")

    print(f"\nADOPT: {adopt}")
    if args.json:
        with open(args.json, "w") as f:
            json.dump(dict(model=version, saturation_permille=args.saturation_permille,
                           cph=cph, cpc=cpc, inc_fleet=inc_fleet, cand_fleet=cand_fleet,
                           fleet_delta=fleet_delta, adopt=adopt, cases=cases),
                      f, indent=1, default=str)
        print(f"per-case detail written to {args.json}")
    return 0 if adopt else 1


if __name__ == "__main__":
    sys.exit(main())

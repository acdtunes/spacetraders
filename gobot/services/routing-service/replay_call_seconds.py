"""API-call exchange-rate sweep — the adoption gate for retuning TOUR_SOLVER_API_CALL_SECONDS.

The knob is the rate objective's exchange rate between a tour's two spent resources:
seconds of the hull's own clock per API request in the selection denominator
(profit / (seconds + saturation x calls x cs)). Its fitted default is the fleet-typical
seconds-per-call ratio, which scores a typical plan alike on either axis. That neutrality
is only right when neither resource dominates; a fleet pinned to the account request
ceiling with slack hull-time banks budget x credits-per-call, so the candidate arms pay
MORE hull-seconds per request saved and this sweep measures what that buys.

SELECTION-ONLY, the sp-1wp8 invariant: candidate generation, tranche pricing, guards and
every projection are cs-independent, so each arm's chosen plan is valued by its own
solve_tour output and the comparison is one-evaluator by construction. The single
difference between arms is which candidate selection picks.

Verdict axes, per window and arm, against the cs-default baseline:
  * fleet rate at the tour call-budget share (fleet_rate: at a bound budget the fleet
    banks budget x credits-per-call, so cpc gains convert 1:1)
  * per-case credits-per-call loss share (the loss-distribution bar)
Mechanism checks that must move the right way before any arm is adopted:
  * mean tour seconds — a call saving bought with far longer tours is the failure mode
    the surcharge form exists to bound; it must stay bounded
  * deep-tranche unit share (planned units past ordinal 3 per (market, good, side)) —
    realized margin decays far harder by tranche ordinal than the fitted per-step factors,
    so a cpc win bought by deeper tranches is projection-flattered and does not arm
  * goods per trading leg and stops — names the channel (route choice vs bundling)

Usage (from gobot/services/routing-service, with the model venv):
  python replay_call_seconds.py [--hours 48] [--samples-per-window 5] [--homes 10]
      [--cs 29,60,120,240] [--saturation-permille 352] [--hulls 75]
      [--budget-share 0.54] [--hold 490] [--max-spend 4000000] [--json out.json]
"""
import argparse
import json
import os
import sys
from collections import Counter, defaultdict
from datetime import timedelta

from model.extract import db_engine
from replay_call_objective import fleet_rate
from replay_objective import (STALENESS_MINUTES, ENGINE_SPEED, compute_allowed,
                              fetch_gate_neighbors, fetch_history,
                              fetch_waypoint_coords, open_era, reconstruct_snapshot,
                              load_model)
from utils import tour_solver
from utils.tour_solver import solve_tour

DEFAULT_CS_ARMS = "29,60,120,240"
DEEP_TRANCHE_ORDINAL = 3  # realized margin falls off past this many tranches per pool
TOUR_OVERHEAD_SECONDS = 60.0
FULL_CEILING_REQ_PER_SEC = 2.0


def mirror_deployed_solver_env():
    """Mirror the deployed routing-service knobs (run.sh) so the replayed solves take the
    same search paths production does. Called from main() only — importing this module must
    never mutate the process env. Every value stays operator-overridable."""
    os.environ.setdefault("TOUR_SOLVER_OBJECTIVE", "rate")
    os.environ.setdefault("TOUR_SOLVER_RATE_ARMED_LONG", "1")
    os.environ.setdefault("TOUR_SOLVER_SEQUENCER", "ortools")
    os.environ.setdefault("TOUR_SOLVER_MAX_PLANNED_TRANCHES", "4")
    os.environ.setdefault("TOUR_SOLVER_REALIZED_SINK_TRANCHES", "4.0")
    os.environ.setdefault("TOUR_SOLVER_FULL_SCORE_TOP_N", "150")
    os.environ.setdefault("TOUR_SOLVER_ORTOOLS_MAX_NODES", "160")


def solve_at_cs(cs, scoped, ship, cons, model, wps):
    """Plan with the exchange rate armed at `cs`. The knob is env-resolved per solve, so
    setting it immediately before the call is exact; clearing it for a non-positive arm is
    what makes that arm the code-default solver rather than a variant."""
    if cs and cs > 0:
        os.environ[tour_solver.API_CALL_SECONDS_ENV_VAR] = repr(float(cs))
    else:
        os.environ.pop(tour_solver.API_CALL_SECONDS_ENV_VAR, None)
    try:
        return solve_tour(scoped, dict(ship), dict(cons), model, waypoints=wps)
    finally:
        os.environ.pop(tour_solver.API_CALL_SECONDS_ENV_VAR, None)


def plan_shape(result, trade_volumes):
    """Every judged quantity of one chosen plan, off its own legs.

    `trade_volumes` maps (waypoint, good) to the snapshot tradeVolume so deep-tranche
    units can be counted per (market, good, side) pool — the same pool the ladder caps."""
    legs = result["legs"]
    goods_per_leg = [len({t["good_symbol"] for t in l["trades"]}) for l in legs]
    trading = [g for g in goods_per_leg if g > 0]
    seconds = (sum(l["travel_seconds_from_prev"] for l in legs)
               + tour_solver.DWELL_SECONDS_PER_LEG * len(legs))
    per_pool = Counter()
    units_total = 0
    for l in legs:
        for t in l["trades"]:
            units_total += t["units"]
            per_pool[(l["waypoint_symbol"], t["good_symbol"], t["is_buy"])] += t["units"]
    deep_units = 0
    for (wp, good, _is_buy), units in per_pool.items():
        tv = trade_volumes.get((wp, good), 0)
        if tv > 0:
            deep_units += max(0, units - DEEP_TRANCHE_ORDINAL * tv)
    crossings = 0
    prev = None
    for l in legs:
        if prev is not None and l["system_symbol"] != prev:
            crossings += 1
        prev = l["system_symbol"]
    return dict(profit=result["projected_profit"],
                calls=result.get("projected_api_calls", 0.0),
                seconds=seconds,
                legs=len(legs),
                goods=(sum(trading) / len(trading)) if trading else 0.0,
                units=units_total, deep_units=deep_units, crossings=crossings)


def arm_cases(shapes):
    """fleet_rate input rows for one arm: each case as {arm}_cph / {arm}_demand."""
    out = []
    for s in shapes:
        hours = (s["seconds"] + TOUR_OVERHEAD_SECONDS) / 3600.0
        out.append(dict(arm_cph=s["profit"] / hours, arm_demand=s["calls"] / hours))
    return out


def loss_share(baseline, candidate):
    """Share of joint cases where the candidate's chosen plan banks fewer credits per call
    than the baseline's — the per-case loss bar, paired by case."""
    losses = joint = 0
    for b, c in zip(baseline, candidate):
        if b["calls"] <= 0 or c["calls"] <= 0:
            continue
        joint += 1
        if c["profit"] / c["calls"] < b["profit"] / b["calls"]:
            losses += 1
    return (losses / joint) if joint else 0.0


def fleet_homes(telemetry_csv, top_n):
    """The fleet's own working systems, weighted by where it actually docked."""
    import csv
    count = Counter()
    with open(telemetry_csv) as f:
        for row in csv.DictReader(f):
            wp = row.get("waypoint") or ""
            parts = wp.split("-")
            if len(parts) >= 2:
                count["-".join(parts[:2])] += 1
    return [h for h, _ in count.most_common(top_n)]


def db_homes(engine, top_n, hours):
    """Homes from tour_leg_telemetry directly when no CSV is supplied."""
    from sqlalchemy import text
    q = text("""SELECT waypoint FROM tour_leg_telemetry
                WHERE realized_at >= now() - make_interval(hours => :hours)""")
    count = Counter()
    with engine.connect() as c:
        for (wp,) in c.execute(q, {"hours": hours}):
            parts = (wp or "").split("-")
            if len(parts) >= 2:
                count["-".join(parts[:2])] += 1
    return [h for h, _ in count.most_common(top_n)]


def main():
    mirror_deployed_solver_env()
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--hours", type=int, default=48)
    ap.add_argument("--samples-per-window", type=int, default=5)
    ap.add_argument("--homes", type=int, default=10)
    ap.add_argument("--cs", default=DEFAULT_CS_ARMS)
    ap.add_argument("--saturation-permille", type=int, default=352)
    ap.add_argument("--hulls", type=int, default=75)
    ap.add_argument("--budget-share", type=float, default=0.54)
    ap.add_argument("--hold", type=int, default=490)
    ap.add_argument("--max-spend", type=int, default=4_000_000)
    ap.add_argument("--homes-csv", default="",
                    help="optional telemetry CSV to weight homes from (default: live table)")
    ap.add_argument("--json", default="")
    args = ap.parse_args()

    cs_arms = [float(x) for x in args.cs.split(",") if x.strip()]
    base_cs = cs_arms[0]

    model, version = load_model()
    engine = db_engine()
    rows = fetch_history(engine, args.hours)
    era = open_era(engine)
    neighbors = fetch_gate_neighbors(engine, era)
    coords = fetch_waypoint_coords(engine)
    homes = (fleet_homes(args.homes_csv, args.homes) if args.homes_csv
             else db_homes(engine, args.homes, args.hours))

    latest = max(r[-1] for r in rows)
    half = args.hours // 2
    span = max(1, half - 2)
    windows = {}
    for name, offset in (("recent", 0), ("prior", half)):
        windows[name] = [latest - timedelta(hours=offset)
                         - timedelta(hours=span * i / max(1, args.samples_per_window - 1))
                         for i in range(args.samples_per_window)]
    waypoint_rows = [dict(symbol=wp, system=s, x=x, y=y)
                     for wp, (s, x, y) in coords.items()]

    results = defaultdict(list)
    for wname, samples in windows.items():
        for sample_t in samples:
            snapshot = reconstruct_snapshot(rows, sample_t)
            by_system = defaultdict(set)
            for s_row in snapshot:
                by_system[s_row["system_symbol"]].add(s_row["waypoint_symbol"])
            for home in homes:
                allowed = compute_allowed(home, neighbors, by_system, 1)
                scoped = [s for s in snapshot if s["system_symbol"] in allowed]
                home_markets = sorted({s["waypoint_symbol"] for s in scoped
                                       if s["system_symbol"] == home})
                if not home_markets:
                    continue
                trade_volumes = {(s["waypoint_symbol"], s["good_symbol"]): s["trade_volume"]
                                 for s in scoped}
                ship = dict(ship_symbol="REPLAY", current_waypoint=home_markets[0],
                            current_system=home, hold_capacity=args.hold,
                            fuel_current=400, fuel_capacity=400,
                            engine_speed=ENGINE_SPEED, cargo=[])
                cons = dict(max_hops=6, min_margin_per_unit=1,
                            max_snapshot_age_minutes=STALENESS_MINUTES,
                            max_spend=args.max_spend, working_capital_reserve=0,
                            allowed_systems=sorted(allowed),
                            expected_model_version=version,
                            api_saturation_permille=args.saturation_permille)
                wps = [w for w in waypoint_rows if w["system"] in allowed]
                case = {}
                for cs in cs_arms:
                    res = solve_at_cs(cs, scoped, ship, cons, model, wps)
                    if not res["feasible"]:
                        case = None
                        break
                    case[cs] = plan_shape(res, trade_volumes)
                if case:
                    for cs, sh in case.items():
                        results[(wname, cs)].append(sh)

    if args.json:
        with open(args.json, "w") as f:
            json.dump({f"{w}|{cs:g}": v for (w, cs), v in results.items()}, f)

    import statistics as st
    budget = FULL_CEILING_REQ_PER_SEC * 3600 * args.budget_share
    print(f"saturation={args.saturation_permille} hulls={args.hulls} "
          f"tour-budget={budget:.0f} req/hr")
    for wname in windows:
        baseline = results.get((wname, base_cs)) or []
        print(f"=== window {wname} (n={len(baseline)}) ===")
        print("%-6s %8s %10s %7s %7s %6s %6s %7s %8s %7s" % (
            "cs", "cpc", "fleet", "d%", "sec", "legs", "goods", "deep%", "demand", "loss%"))
        base_rate = None
        for cs in cs_arms:
            arm = results.get((wname, cs)) or []
            if not arm:
                continue
            rate, demand = fleet_rate(arm_cases(arm), "arm", args.hulls,
                                      budget / 3600.0)
            if cs == base_cs:
                base_rate = rate
            cpc = sum(a["profit"] for a in arm) / max(1e-9, sum(a["calls"] for a in arm))
            deep = sum(a["deep_units"] for a in arm) / max(1, sum(a["units"] for a in arm))
            print("%-6g %8.0f %9.2fM %+6.1f%% %7.0f %6.2f %6.2f %6.1f%% %8.0f %6.1f%%" % (
                cs, cpc, rate / 1e6,
                (100 * (rate / base_rate - 1)) if base_rate else 0.0,
                st.fmean(a["seconds"] for a in arm),
                st.fmean(a["legs"] for a in arm),
                st.fmean(a["goods"] for a in arm),
                100 * deep, demand,
                100 * loss_share(baseline, arm)))


if __name__ == "__main__":
    main()

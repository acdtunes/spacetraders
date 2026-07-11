"""sp-1wp8 offline objective replay: profit-primary vs rate-primary tour selection
on reconstructed REAL market snapshots — the analyst's Q3 method bar ("the objective
function of a live engine is replay-validated, never A/B-tested on a hunch").

What it measures
----------------
For each (sample time, home system, hull shape) case, the solver plans the same
snapshot twice — objective="profit" (the live default) and objective="rate" — and
the harness compares the CHOSEN plans. The aggregate per objective is
sum(projected profit) / sum(projected hours + per-tour overhead) over the feasible
cases: the long-run $/hr of a hull that repeatedly makes that objective's choice,
under the model's own price/time projections. This isolates the CHOICE RULE on real
market shapes; estimator honesty itself is validated separately in the field by the
tour_plan_rate{phase=projected|realized} pair.

Reconstruction fidelity
-----------------------
- Snapshots come from market_price_history (prices, tier, trade_volume all captured
  at write time), freshest row per (waypoint, good) within the SAME 75-minute
  staleness window the live snapshot builder enforces. observed_at is stamped "now"
  because solve_tour ages rows against the wall clock — a replay row is fresh as of
  its sample time by construction.
- allowed_systems = home + gate_edges neighbors (the live tourSystemsFrom shape).
- Waypoint coordinates come from the waypoints table, so travel times use the same
  CRUISE formula the live request carries.
- Deposits and absorption are deliberately empty: the comparison is pure-arb
  planning on equal footing (both objectives see identical candidates either way).

Usage (from gobot/services/routing-service, with the model venv):
  python replay_objective.py [--hours 48] [--samples 12] [--hulls 80,220]
      [--max-spend 2000000] [--reserve 50000] [--tour-overhead-seconds 60]
      [--deep] [--json out.json]
"""
import argparse
import json
import math
import sys
import time
from collections import defaultdict
from datetime import timedelta

from sqlalchemy import text

from model.extract import db_engine
from utils import tour_solver
from utils.tour_solver import solve_tour, OBJECTIVE_PROFIT, OBJECTIVE_RATE

STALENESS_MINUTES = 75  # mirrors BuildTourSnapshot / MAX_SNAPSHOT_AGE_MINUTES_DEFAULT
ENGINE_SPEED = 30       # the hauler class the CRUISE mirror is calibrated against


def load_model(path="model_artifacts/market_model.json"):
    with open(path) as f:
        model = json.load(f)
    return model, f"{model['fit_version']}@{model['era']}"


def fetch_history(engine, hours):
    q = text("""
        SELECT waypoint_symbol, good_symbol, purchase_price, sell_price,
               supply, activity, trade_volume, recorded_at
        FROM market_price_history
        WHERE recorded_at >= (SELECT max(recorded_at) FROM market_price_history)
                             - make_interval(hours => :hours + 2)
        ORDER BY recorded_at""")
    with engine.connect() as c:
        rows = c.execute(q, {"hours": hours}).fetchall()
    return rows


def fetch_gate_neighbors(engine):
    q = text("SELECT system_symbol, connected_system FROM gate_edges")
    neighbors = defaultdict(set)
    with engine.connect() as c:
        for sys_, conn in c.execute(q):
            neighbors[sys_].add(conn)
    return neighbors


def fetch_waypoint_coords(engine):
    q = text("SELECT waypoint_symbol, system_symbol, x, y FROM waypoints")
    coords = {}
    with engine.connect() as c:
        for wp, sys_, x, y in c.execute(q):
            coords[wp] = (sys_, float(x), float(y))
    return coords


def system_of(waypoint):
    parts = waypoint.split("-")
    return "-".join(parts[:2]) if len(parts) >= 2 else waypoint


def reconstruct_snapshot(rows, sample_t):
    """Freshest row per (waypoint, good) with recorded_at in (T-75min, T]."""
    cutoff = sample_t - timedelta(minutes=STALENESS_MINUTES)
    latest = {}
    for r in rows:
        if r.recorded_at <= sample_t and r.recorded_at > cutoff:
            latest[(r.waypoint_symbol, r.good_symbol)] = r
    snapshot = []
    now_unix = time.time()
    for (wp, good), r in latest.items():
        snapshot.append(dict(
            waypoint_symbol=wp, system_symbol=system_of(wp), good_symbol=good,
            # GoodListing orientation: Bid = market BUY column (purchase_price),
            # Ask = market SELL column (sell_price) — same as collectSystemListings.
            bid=int(r.purchase_price), ask=int(r.sell_price),
            trade_volume=int(r.trade_volume),
            supply=r.supply or "", activity=r.activity or "",
            observed_at_unix=now_unix,
        ))
    return snapshot


def plan_seconds(result):
    """Recover the chosen plan's projected wall-clock from its own cph."""
    if not result["feasible"] or result["projected_credits_per_hour"] <= 0:
        return None
    return result["projected_profit"] / result["projected_credits_per_hour"] * 3600


def run_case(snapshot, waypoints, home, allowed, hold, max_spend, reserve, version):
    home_markets = sorted({s["waypoint_symbol"] for s in snapshot
                           if s["system_symbol"] == home})
    if not home_markets:
        return None
    ship = dict(ship_symbol=f"REPLAY-{hold}", current_waypoint=home_markets[0],
                current_system=home, hold_capacity=hold,
                fuel_current=400, fuel_capacity=400,
                engine_speed=ENGINE_SPEED, cargo=[])
    cons = dict(max_hops=6, min_margin_per_unit=1, max_snapshot_age_minutes=STALENESS_MINUTES,
                max_spend=max_spend, working_capital_reserve=reserve,
                allowed_systems=sorted(allowed), expected_model_version=version)
    scoped = [s for s in snapshot if s["system_symbol"] in allowed]
    wps = [w for w in waypoints if w["system"] in allowed]
    out = {}
    for objective in (OBJECTIVE_PROFIT, OBJECTIVE_RATE):
        res = solve_tour(scoped, dict(ship), dict(cons), MODEL, waypoints=wps,
                         objective=objective)
        out[objective] = res
    return ship, out


def summarize(cases, overhead_seconds):
    agg = {OBJECTIVE_PROFIT: [0.0, 0.0], OBJECTIVE_RATE: [0.0, 0.0]}  # [profit, hours]
    diverged = 0
    diverged_rate_wins = 0
    per_case = []
    for c in cases:
        p, r = c["results"][OBJECTIVE_PROFIT], c["results"][OBJECTIVE_RATE]
        sp, sr = plan_seconds(p), plan_seconds(r)
        if sp is None or sr is None:
            continue
        agg[OBJECTIVE_PROFIT][0] += p["projected_profit"]
        agg[OBJECTIVE_PROFIT][1] += (sp + overhead_seconds) / 3600
        agg[OBJECTIVE_RATE][0] += r["projected_profit"]
        agg[OBJECTIVE_RATE][1] += (sr + overhead_seconds) / 3600
        differs = (p["projected_profit"], round(plan_seconds(p))) != \
                  (r["projected_profit"], round(plan_seconds(r)))
        if differs:
            diverged += 1
            eff_p = p["projected_profit"] / ((sp + overhead_seconds) / 3600)
            eff_r = r["projected_profit"] / ((sr + overhead_seconds) / 3600)
            if eff_r > eff_p:
                diverged_rate_wins += 1
        per_case.append(dict(
            sample=c["sample"], home=c["home"], hold=c["hold"],
            profit_choice=dict(profit=p["projected_profit"], seconds=round(sp),
                               cph=round(p["projected_credits_per_hour"])),
            rate_choice=dict(profit=r["projected_profit"], seconds=round(sr),
                             cph=round(r["projected_credits_per_hour"])),
            diverged=differs,
        ))
    return agg, diverged, diverged_rate_wins, per_case


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--hours", type=int, default=48)
    ap.add_argument("--samples", type=int, default=12)
    ap.add_argument("--hulls", default="80,220")
    ap.add_argument("--max-spend", type=int, default=2_000_000)
    ap.add_argument("--reserve", type=int, default=50_000)
    ap.add_argument("--tour-overhead-seconds", type=int, default=60,
                    help="per-tour replan overhead added to BOTH objectives' hours")
    ap.add_argument("--deep", action="store_true",
                    help="also replay with FULL_SCORE_TOP_N=100 (beam-cut ceiling)")
    ap.add_argument("--json", default="", help="write per-case results to this file")
    args = ap.parse_args()

    global MODEL
    MODEL, version = load_model()
    engine = db_engine()
    rows = fetch_history(engine, args.hours)
    if not rows:
        print("no market_price_history rows in the window; nothing to replay")
        return 1
    neighbors = fetch_gate_neighbors(engine)
    coords = fetch_waypoint_coords(engine)
    hulls = [int(h) for h in args.hulls.split(",") if h]

    newest = max(r.recorded_at for r in rows)
    window_start = newest - timedelta(hours=args.hours)
    step = (newest - window_start) / max(1, args.samples)
    samples = [window_start + step * (i + 1) for i in range(args.samples)]

    def one_pass(label):
        cases = []
        for sample_t in samples:
            snapshot = reconstruct_snapshot(rows, sample_t)
            waypoints = [dict(symbol=wp, system=sys_, x=int(x), y=int(y))
                         for wp, (sys_, x, y) in coords.items()]
            by_system = defaultdict(set)
            for s in snapshot:
                by_system[s["system_symbol"]].add(s["waypoint_symbol"])
            for home, markets in sorted(by_system.items()):
                if len(markets) < 2:
                    continue
                allowed = {home} | (neighbors.get(home, set()) & set(by_system))
                for hold in hulls:
                    res = run_case(snapshot, waypoints, home, allowed, hold,
                                   args.max_spend, args.reserve, version)
                    if res is None:
                        continue
                    _, out = res
                    p, r = out[OBJECTIVE_PROFIT], out[OBJECTIVE_RATE]
                    if p["feasible"] != r["feasible"]:
                        print(f"!! feasibility diverged at {sample_t} {home} h{hold} "
                              f"(profit={p['feasible']} rate={r['feasible']}) — investigate",
                              file=sys.stderr)
                        continue
                    if not p["feasible"]:
                        continue
                    cases.append(dict(sample=str(sample_t), home=home, hold=hold,
                                      results=out))
        agg, diverged, rate_wins, per_case = summarize(cases, args.tour_overhead_seconds)
        pp, ph = agg[OBJECTIVE_PROFIT]
        rp, rh = agg[OBJECTIVE_RATE]
        cph_p = pp / ph if ph > 0 else 0.0
        cph_r = rp / rh if rh > 0 else 0.0
        delta = (cph_r - cph_p) / cph_p * 100 if cph_p > 0 else float("nan")
        print(f"\n=== {label} ===")
        print(f"cases (feasible both): {len(per_case)}  |  choice diverged: {diverged} "
              f"(rate-choice effectively better in {rate_wins})")
        print(f"profit-primary : {pp:>12,.0f} cr over {ph:8.2f} h  =  {cph_p:>10,.0f} cr/hr")
        print(f"rate-primary   : {rp:>12,.0f} cr over {rh:8.2f} h  =  {cph_r:>10,.0f} cr/hr")
        print(f"fleet-$/hr delta (rate vs profit): {delta:+.2f}%")
        return dict(label=label, cases=len(per_case), diverged=diverged,
                    rate_wins=rate_wins, profit_cph=cph_p, rate_cph=cph_r,
                    delta_pct=delta, per_case=per_case)

    results = [one_pass(f"top-{tour_solver.FULL_SCORE_TOP_N} (production cut)")]
    if args.deep:
        saved = tour_solver.FULL_SCORE_TOP_N
        try:
            tour_solver.FULL_SCORE_TOP_N = 100
            results.append(one_pass("top-100 (beam-cut ceiling, measurement only)"))
        finally:
            tour_solver.FULL_SCORE_TOP_N = saved

    if args.json:
        with open(args.json, "w") as f:
            json.dump(results, f, indent=1, default=str)
        print(f"\nper-case detail written to {args.json}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

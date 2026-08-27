"""Stage-1 depth A/B replay — the adoption gate for charging market depth during candidate
GENERATION rather than only during selection.

Both arms run at the SAME measured limiter saturation, which is the deployed reality: the
call surcharge on selection is live, so the incumbent here is production, not a time-only
solver. The single difference is TOUR_SOLVER_STAGE1_CALL_CREDITS — the price stage 1 pays per
request its packed units would spend. The incumbent leaves it unarmed and its stage 1 is
depth-blind; the candidate arms it and every stage-1 cut (beam width, OR-Tools node prune and
subset choice, the FULL_SCORE_TOP_N shortlist) sees depth before it cuts.

WHY THIS IS A DIFFERENT QUESTION FROM THE SURCHARGE'S OWN REPLAY. That one asked whether
ranking a fixed shortlist by credits-per-request beats ranking it by credits-per-hour. It won
+11.55%, and its own mechanism metrics said the win came from cheaper ROUTES: units-weighted
depth of the chosen legs moved 92 -> 89 and own-volume share 6.09 -> 6.08, both flat. A term
that only reorders a shortlist cannot reach a candidate the shortlist never contained. This
replay asks whether reaching them is worth anything, so the mechanism metrics are part of the
verdict rather than colour: if units-weighted DEPTH does not move, the change did not do what
it was built to do, whatever the credits say.

Both chosen ROUTES are valued under ONE common evaluator — the tranche allocator re-run over
each frozen route with a saturation-free constraints dict — so the scorer cannot see which
arm produced the route it is pricing.

THE KNOWN FAILURE MODE, tested explicitly. A blend that let the hull's own clock leave the
objective bought a 3% call saving with 5.04x longer tours. The analogous trap here is a depth
preference that flies a hull past a near shallow market to reach a far deep one, so tour
seconds and CROSSINGS are reported per arm alongside the credits.

Usage (from gobot/services/routing-service, with the model venv):
  python replay_stage1_depth.py [--credits 290,1000,3000,10000] [--saturation-permille 1000]
      [--hours 48] [--samples 12] [--hulls 80,220] [--max-spend 2000000] [--reserve 50000]
      [--fleet-hulls 60] [--ceiling-req-per-sec 2.0] [--max-loss-share 0.10] [--json out.json]
"""
import argparse
import concurrent.futures as futures
import json
import os
import statistics
import sys
import time
from collections import defaultdict
from datetime import timedelta

from model.extract import db_engine
# The verdict vocabulary is IMPORTED, not transcribed: this replay's adoption call has to be
# the same call the call-cost replay made, and two copies of a classifier drift into two
# different bars without either one looking wrong.
from replay_call_objective import (axes, ceiling_sweep, classify, depth_stats, fleet_rate,
                                   rescore_route, tally)
from replay_model_ab import load_artifact, mirror_deployed_solver_env, route_of
from replay_objective import (STALENESS_MINUTES, ENGINE_SPEED, compute_allowed,
                              fetch_gate_neighbors, fetch_history,
                              fetch_waypoint_coords, open_era, reconstruct_snapshot)
from utils import tour_solver
from utils.tour_solver import OBJECTIVE_RATE, solve_tour


def crossings(scored, start_system):
    """System changes along a valued plan, counted from the hull's own system — the second
    half of the far-away-deep-market check, because a plan can add travel seconds without
    crossing and cross without adding many."""
    if not scored:
        return 0
    count, system = 0, start_system
    for leg in scored["legs"]:
        leg_system = leg.get("system_symbol")
        if leg_system and leg_system != system:
            count += 1
            system = leg_system
    return count


def solve_at(credits, scoped, ship, cons, model, wps):
    """Plan with stage 1 charged at `credits` per request. The knob is resolved per solve
    from the environment, so setting it immediately before the call is exact — and clearing
    it for the incumbent is what makes that arm the deployed solver rather than a variant."""
    if credits > 0:
        os.environ[tour_solver.STAGE1_CALL_CREDITS_ENV_VAR] = repr(float(credits))
    else:
        os.environ.pop(tour_solver.STAGE1_CALL_CREDITS_ENV_VAR, None)
    started = time.monotonic()
    result = solve_tour(scoped, dict(ship), dict(cons), model, waypoints=wps,
                        objective=OBJECTIVE_RATE)
    return result, (time.monotonic() - started) * 1000.0


def measure(route, scoped, ship, base_cons, model, wps, home):
    """Every reported quantity for one chosen route, all off the common evaluator."""
    scored = rescore_route(route, scoped, dict(ship), dict(base_cons), model, wps)
    cph, cpc, demand = axes(scored)
    units, depth, share = depth_stats(scored, scoped)
    return dict(cph=cph, cpc=cpc, demand=demand, units=units, depth=depth, share=share,
                calls=(scored or {}).get("api_calls", 0.0),
                seconds=(scored or {}).get("seconds", 0),
                stops=len(route), crossings=crossings(scored, home))


def run_case(snapshot, waypoints, home, allowed, hold, max_spend, reserve, version,
             model, saturation_permille, credit_prices):
    """One (home, hull) case: the incumbent once, then each swept price against it."""
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
    # BOTH arms carry the measured saturation: the selection surcharge is deployed, so the
    # thing under test is stage 1's blindness, not the surcharge itself.
    live_cons = dict(base_cons, api_saturation_permille=saturation_permille)
    scoped = [s for s in snapshot if s["system_symbol"] in allowed]
    wps = [w for w in waypoints if w["system"] in allowed]

    inc_res, inc_ms = solve_at(0, scoped, ship, live_cons, model, wps)
    inc_route = route_of(inc_res)
    inc = measure(inc_route, scoped, ship, base_cons, model, wps, home)

    out = {}
    for price in credit_prices:
        cand_res, cand_ms = solve_at(price, scoped, ship, live_cons, model, wps)
        if not inc_res.get("feasible") and not cand_res.get("feasible"):
            continue
        cand_route = route_of(cand_res)
        cand = measure(cand_route, scoped, ship, base_cons, model, wps, home)
        same = cand_route == inc_route
        case = dict(home=home, hold=hold, same_route=same,
                    inc_route=inc_route, cand_route=cand_route,
                    inc_ms=inc_ms, cand_ms=cand_ms,
                    cph_verdict=classify(cand["cph"], inc["cph"], same),
                    cpc_verdict=classify(cand["cpc"], inc["cpc"], same))
        case.update({f"inc_{k}": v for k, v in inc.items()})
        case.update({f"cand_{k}": v for k, v in cand.items()})
        out[price] = case
    return out or None


_WORKER = {}


def _init_worker(model_path, hours, max_homes, hulls, max_spend, reserve,
                 saturation_permille, credit_prices, solver_env):
    """One DB read and one artifact load per worker process, reused across samples. The
    parent's mirrored solver env is re-applied explicitly because a spawned child does not
    inherit a mutation made after import."""
    os.environ.update(solver_env)
    os.environ.pop(tour_solver.STAGE1_CALL_CREDITS_ENV_VAR, None)
    engine = db_engine()
    rows = fetch_history(engine, hours)
    model, version = load_artifact(model_path)
    _WORKER.update(rows=rows, model=model, version=version,
                   neighbors=fetch_gate_neighbors(engine, open_era(engine)),
                   coords=fetch_waypoint_coords(engine), max_homes=max_homes,
                   hulls=hulls, max_spend=max_spend, reserve=reserve,
                   saturation_permille=saturation_permille, credit_prices=credit_prices)


def _run_sample(sample_t):
    """Every (home, hull) case at one sample time, for every swept price."""
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
    out = defaultdict(list)
    for home in homes:
        allowed = compute_allowed(home, w["neighbors"], by_system, 1)
        for hold in w["hulls"]:
            byprice = run_case(snapshot, waypoints, home, allowed, hold, w["max_spend"],
                               w["reserve"], w["version"], w["model"],
                               w["saturation_permille"], w["credit_prices"])
            if not byprice:
                continue
            for price, case in byprice.items():
                case["sample"] = str(sample_t)
                out[price].append(case)
    return dict(out)


def mean(cases, key):
    return statistics.fmean(c[key] for c in cases) if cases else 0.0


def report_price(price, cases, args):
    """Everything the adoption call rests on, for one swept price."""
    cph = tally(cases, "cph_verdict")
    cpc = tally(cases, "cpc_verdict")
    tour_ceiling = args.ceiling_req_per_sec * args.tour_ceiling_share
    inc_tour, _ = fleet_rate(cases, "inc", args.fleet_hulls, tour_ceiling)
    cand_tour, _ = fleet_rate(cases, "cand", args.fleet_hulls, tour_ceiling)
    inc_full, inc_demand = fleet_rate(cases, "inc", args.fleet_hulls,
                                      args.ceiling_req_per_sec)
    cand_full, cand_demand = fleet_rate(cases, "cand", args.fleet_hulls,
                                        args.ceiling_req_per_sec)
    tour_delta = (cand_tour / inc_tour - 1) if inc_tour else 0.0
    moved = [c for c in cases if c["inc_units"] > 0 and c["cand_units"] > 0]
    adopt = bool(cand_tour > inc_tour and cpc["loss_share"] <= args.max_loss_share)

    print(f"\n=== STAGE-1 DEPTH CHARGE at {price:,.0f} credits/request "
          f"({len(cases)} cases, route changed in "
          f"{sum(1 for c in cases if not c['same_route'])}) ===")
    for name, t in (("credits/HOUR", cph), ("credits/CALL", cpc)):
        print(f"  {name:>13}: win {t['wins']:>4} / loss {t['losses']:>4} / tie {t['ties']:>4}"
              f"   (loss share {t['loss_share']:.1%})")
    print(f"per-hull mean: cph {mean(cases, 'inc_cph'):>12,.0f} -> "
          f"{mean(cases, 'cand_cph'):>12,.0f}   "
          f"cpc {mean(cases, 'inc_cpc'):>9,.0f} -> {mean(cases, 'cand_cpc'):>9,.0f}")
    print(f"MECHANISM  units-weighted depth {mean(moved, 'inc_depth'):>6.1f} -> "
          f"{mean(moved, 'cand_depth'):>6.1f}   "
          f"own-volume share {mean(moved, 'inc_share'):>5.2f} -> "
          f"{mean(moved, 'cand_share'):>5.2f}   "
          f"units {mean(moved, 'inc_units'):>5.0f} -> {mean(moved, 'cand_units'):>5.0f}")
    # THE SAME MECHANISM, UNDILUTED. The line above averages over every case including the
    # ties, so a change that fires on a tenth of the board reads as a tenth of its own effect
    # — which is how a real mechanism and an inert one come to look alike. These are the
    # cases where the change actually chose something different.
    changed = [c for c in cases if not c["same_route"]]
    changed_moved = [c for c in changed if c["inc_units"] > 0 and c["cand_units"] > 0]
    longer = [c for c in changed if c["cand_seconds"] > c["inc_seconds"]]
    deeper = [c for c in changed_moved if c["cand_depth"] > c["inc_depth"]]
    if changed:
        print(f"CHANGED    n={len(changed)}   depth {mean(changed_moved, 'inc_depth'):.1f} -> "
              f"{mean(changed_moved, 'cand_depth'):.1f} "
              f"(deeper in {len(deeper)}/{len(changed_moved)})   "
              f"calls {mean(changed, 'inc_calls'):.1f} -> {mean(changed, 'cand_calls'):.1f}   "
              f"seconds {mean(changed, 'inc_seconds'):,.0f} -> "
              f"{mean(changed, 'cand_seconds'):,.0f} "
              f"(longer in {len(longer)}/{len(changed)})   "
              f"crossings {mean(changed, 'inc_crossings'):.2f} -> "
              f"{mean(changed, 'cand_crossings'):.2f}")
    print(f"SHAPE      tour seconds {mean(cases, 'inc_seconds'):>7,.0f} -> "
          f"{mean(cases, 'cand_seconds'):>7,.0f} "
          f"({(mean(cases, 'cand_seconds')/mean(cases, 'inc_seconds') - 1) if mean(cases, 'inc_seconds') else 0:+.1%})"
          f"   crossings {mean(cases, 'inc_crossings'):.2f} -> "
          f"{mean(cases, 'cand_crossings'):.2f}   "
          f"stops {mean(cases, 'inc_stops'):.2f} -> {mean(cases, 'cand_stops'):.2f}   "
          f"calls {mean(cases, 'inc_calls'):.1f} -> {mean(cases, 'cand_calls'):.1f}")
    print(f"SOLVE MS   {mean(cases, 'inc_ms'):>7,.0f} -> {mean(cases, 'cand_ms'):>7,.0f}")
    print(f"fleet demand at {args.fleet_hulls} hulls: {inc_demand:,.0f} -> "
          f"{cand_demand:,.0f} req/hr against a {args.ceiling_req_per_sec*3600:,.0f} ceiling")
    print(f"FLEET RATE at the full account ceiling: {inc_full:>14,.0f} -> "
          f"{cand_full:>14,.0f} cr/hr  "
          f"({(cand_full/inc_full - 1) if inc_full else 0:+.2%})")
    print(f"FLEET RATE at the measured tour share ({args.tour_ceiling_share:.0%} = "
          f"{tour_ceiling:.2f} req/s): {inc_tour:>14,.0f} -> {cand_tour:>14,.0f} cr/hr  "
          f"({tour_delta:+.2%})")

    sweep, tie = ceiling_sweep(cases, args.fleet_hulls, args.ceiling_req_per_sec)
    print("fleet rate vs the share of the ceiling the TOUR fleet gets:")
    for ceiling, inc, cand in sweep:
        if round(ceiling / args.ceiling_req_per_sec * 40) % 4:
            continue
        print(f"  {ceiling:5.2f} req/s ({ceiling/args.ceiling_req_per_sec:4.0%})  "
              f"incumbent {inc:>13,.0f}  candidate {cand:>13,.0f}  "
              f"{(cand/inc - 1) if inc else 0:+7.2%}")
    print("break-even tour ceiling: "
          + (f"{tie:.2f} req/s ({tie/args.ceiling_req_per_sec:.0%})" if tie
             else "none in range (one arm wins throughout)"))

    for axis, key in (("credits/hour", "cph"), ("credits/call", "cpc")):
        losses = sorted((c for c in cases if c[f"{key}_verdict"] == "loss"),
                        key=lambda c: c[f"cand_{key}"] - c[f"inc_{key}"])
        if not losses:
            print(f"{axis} loss distribution: none")
            continue
        deltas = [c[f"cand_{key}"] - c[f"inc_{key}"] for c in losses]
        print(f"{axis} loss distribution ({len(losses)} cases): "
              f"worst {deltas[0]:,.0f}  p25 {deltas[len(deltas)//4]:,.0f}  "
              f"median {statistics.median(deltas):,.0f}  "
              f"p90 {deltas[9*len(deltas)//10]:,.0f}")
        for c in losses[:5]:
            print(f"  loss: {c['sample']} {c['home']} h{c['hold']}  "
                  f"{c[f'cand_{key}']:,.0f} vs {c[f'inc_{key}']:,.0f} "
                  f"(stops {c['inc_stops']}->{c['cand_stops']}, "
                  f"calls {c['inc_calls']:.0f}->{c['cand_calls']:.0f})")
    print(f"ADOPT at {price:,.0f}: {adopt}")
    return dict(price=price, cases=len(cases), cph=cph, cpc=cpc,
                inc_tour=inc_tour, cand_tour=cand_tour, tour_delta=tour_delta,
                inc_depth=mean(moved, "inc_depth"), cand_depth=mean(moved, "cand_depth"),
                inc_share=mean(moved, "inc_share"), cand_share=mean(moved, "cand_share"),
                inc_seconds=mean(cases, "inc_seconds"),
                cand_seconds=mean(cases, "cand_seconds"),
                inc_crossings=mean(cases, "inc_crossings"),
                cand_crossings=mean(cases, "cand_crossings"),
                adopt=adopt)


def main():
    mirror_deployed_solver_env()
    # The live per-visit sink depth, which the shared mirror does not carry. It sets how many
    # tranches a sell visit realizes, so it moves the very unit counts the charge chunks —
    # replaying at the in-code default would price a plan shape production does not fly.
    os.environ.setdefault("TOUR_SOLVER_REALIZED_SINK_TRANCHES", "3.0")
    # The replay decides what both knobs under test DO; it must never inherit either.
    os.environ.pop(tour_solver.API_SATURATION_ENV_VAR, None)
    os.environ.pop(tour_solver.STAGE1_CALL_CREDITS_ENV_VAR, None)
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", default="model_artifacts/market_model.json")
    ap.add_argument("--credits", default="290,1000,3000,10000",
                    help="stage-1 credit prices per request to sweep; the incumbent is 0")
    ap.add_argument("--saturation-permille", type=int, default=1000,
                    help="the limiter reading BOTH arms carry; 1000 = pinned at the ceiling")
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
                         "scanning, polls, contract and construction draw")
    ap.add_argument("--max-loss-share", type=float, default=0.10)
    ap.add_argument("--max-homes", type=int, default=0)
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--json", default="")
    args = ap.parse_args()

    model, version = load_artifact(args.model)
    engine = db_engine()
    rows = fetch_history(engine, args.hours)
    if not rows:
        print("no market_price_history rows in the window; nothing to replay")
        return 1
    hulls = [int(h) for h in args.hulls.split(",") if h]
    prices = [float(p) for p in args.credits.split(",") if p]

    newest = max(r.recorded_at for r in rows)
    window_start = newest - timedelta(hours=args.hours)
    step = (newest - window_start) / max(1, args.samples)
    samples = [window_start + step * (i + 1) for i in range(args.samples)]
    del rows

    solver_env = {k: v for k, v in os.environ.items() if k.startswith("TOUR_SOLVER_")}
    init_args = (args.model, args.hours, args.max_homes, hulls, args.max_spend,
                 args.reserve, args.saturation_permille, prices, solver_env)
    by_price = defaultdict(list)
    with futures.ProcessPoolExecutor(max_workers=args.workers,
                                     initializer=_init_worker,
                                     initargs=init_args) as pool:
        for done, batch in enumerate(pool.map(_run_sample, samples), start=1):
            for price, cases in batch.items():
                by_price[price].extend(cases)
            n = sum(len(v) for v in by_price.values())
            print(f"  sample {done}/{len(samples)}: {n} case-prices total", flush=True)

    if not by_price:
        print("no joint-feasible cases; nothing to decide on")
        return 1

    print(f"\nmodel {version}   both arms at {args.saturation_permille/10:.1f}% limiter load")
    print("legend: both routes valued by ONE saturation-free evaluator; the incumbent is the "
          "DEPLOYED solver (selection surcharge on, stage 1 depth-blind).")
    summaries = [report_price(price, by_price[price], args) for price in sorted(by_price)]

    print("\n=== SWEEP SUMMARY (deciding metric: fleet rate at the measured tour share) ===")
    print(f"{'price':>8}  {'cph W/L/T':>14}  {'cpc W/L/T':>14}  {'tour-share':>10}  "
          f"{'depth':>13}  {'seconds':>9}  {'adopt':>5}")
    for s in summaries:
        print(f"{s['price']:>8,.0f}  "
              f"{s['cph']['wins']:>4}/{s['cph']['losses']:>4}/{s['cph']['ties']:>4}  "
              f"{s['cpc']['wins']:>4}/{s['cpc']['losses']:>4}/{s['cpc']['ties']:>4}  "
              f"{s['tour_delta']:>+9.2%}  "
              f"{s['inc_depth']:>5.0f}->{s['cand_depth']:<6.0f}  "
              f"{(s['cand_seconds']/s['inc_seconds'] - 1) if s['inc_seconds'] else 0:>+8.1%}  "
              f"{str(s['adopt']):>5}")
    best = max(summaries, key=lambda s: s["tour_delta"])
    print(f"best price {best['price']:,.0f} at {best['tour_delta']:+.2%}, "
          f"adopt {best['adopt']}")
    if args.json:
        with open(args.json, "w") as f:
            json.dump(dict(model=version, saturation_permille=args.saturation_permille,
                           summaries=summaries,
                           cases={str(p): c for p, c in by_price.items()}),
                      f, indent=1, default=str)
        print(f"per-case detail written to {args.json}")
    return 0 if best["adopt"] else 1


if __name__ == "__main__":
    sys.exit(main())

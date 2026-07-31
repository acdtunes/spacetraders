#!/usr/bin/env python3
"""sp-o2dzb — fixture demonstration.

Relax ONE candidate bounding term at a time on the SAME live snapshot and the SAME
hull states, holding every other term constant, and measure total planned market-buy
units and projected profit. A term that binds moves the number; a term that does not
is inert.

The snapshot is loaded ONCE and reused for every arm, so the only thing that differs
between arms is the term under test.

NEVER COMPARE ARMS ACROSS PROCESSES. load() re-pulls the LIVE market, which moves
minutes to minutes: the same 14-hull baseline measured 2111 units in one session
and 701 in the next. Every arm main() runs shares the one snapshot loaded at the
top, which is the only thing that makes them comparable.

Both sequencers repeat EXACTLY within a process on a fixed snapshot — ortools was
measured bit-identical across back-to-back baselines despite its wall-clock budget
(ORTOOLS_TIME_BUDGET_SECONDS), so a cross-session disagreement is the snapshot
moving, not the sequencer wobbling. An earlier reading of this file blamed ortools
for a sign flip that was really two different snapshots; that was wrong.

They do NOT agree with each other, though, and the difference is real signal rather
than noise: on one snapshot MAX_PLANNED_TRANCHES 3->4 raised BOTH units and profit
under beam, and raised profit while LOWERING units under ortools. Sweep under
ortools when you want production's answer — that is what run.sh arms — and under
beam only to isolate the allocator from the sequencer.
"""
import collections
import copy
import json
import os
import sys

SCRATCH = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SCRATCH)
import replay_binding_term as R  # noqa: E402  (sets sys.path + env, mirrors live)

from utils import tour_solver  # noqa: E402


def total_buys(out):
    return sum(t["units"] for l in out["legs"] for t in l["trades"]
               if t["is_buy"] and not t.get("is_stock"))


def run_arm(name, snapshot, ships, wps, edges, model, version, *,
            hold_mult=1, tv_mult=1, spend=10_750_000, sink_tranches=None,
            planned_tranches=None, min_margin=0):
    if sink_tranches is not None:
        os.environ["TOUR_SOLVER_REALIZED_SINK_TRANCHES"] = str(sink_tranches)
    else:
        os.environ.pop("TOUR_SOLVER_REALIZED_SINK_TRANCHES", None)
    os.environ["TOUR_SOLVER_MAX_PLANNED_TRANCHES"] = str(planned_tranches or 3)

    snap = snapshot
    if tv_mult != 1:
        snap = [dict(r, trade_volume=r["trade_volume"] * tv_mult) for r in snapshot]
    by_system = collections.defaultdict(list)
    for r in snap:
        by_system[r["system_symbol"]].append(r)

    units = profit = 0
    fills, nplans = [], 0
    for sym, ssys, loc, cap, inv, speed in ships:
        cap = int(cap) * hold_mult
        cargo = [dict(good_symbol=c["symbol"], units=c["units"])
                 for c in (json.loads(inv) or []) if c.get("units", 0) > 0]
        ship = dict(ship_symbol=sym, current_waypoint=loc, current_system=ssys,
                    hold_capacity=cap, fuel_current=4000, fuel_capacity=4000,
                    engine_speed=30, cargo=cargo)
        neighbours = [n for n in sorted(edges.get(ssys, ())) if by_system.get(n)]
        best = None
        for nb in [None] + neighbours[:6]:
            allowed = [ssys] + ([nb] if nb else [])
            rows = [r for s in allowed for r in by_system.get(s, ())]
            if not rows:
                continue
            cons = dict(max_hops=6, min_margin_per_unit=min_margin, max_spend=spend,
                        working_capital_reserve=0, allowed_systems=allowed,
                        max_snapshot_age_minutes=75, expected_model_version=version,
                        externality_weight=0.35)
            try:
                out = tour_solver.solve_tour(
                    rows, ship, cons, model,
                    waypoints=[wps[r["waypoint_symbol"]] for r in rows
                               if r["waypoint_symbol"] in wps])
            except Exception:  # noqa: BLE001
                continue
            if out.get("legs") and (best is None
                                    or out["projected_profit"] > best["projected_profit"]):
                best = out
        if best is None:
            continue
        nplans += 1
        b = total_buys(best)
        units += b
        profit += best["projected_profit"]
        fills.append(b)
    print(f"  {name:44s} plans {nplans:3d}  buy_units {units:6d}  "
          f"profit {profit:12,d}  mean/plan {units/max(1,nplans):6.1f}")
    return units, profit


def main():
    # Production runs ortools (run.sh + the live process env), so that is the default
    # here. Set TOUR_SOLVER_SEQUENCER=beam to isolate the allocator instead.
    os.environ.setdefault("TOUR_SOLVER_SEQUENCER", "ortools")
    os.environ.setdefault("TOUR_SOLVER_FULL_SCORE_TOP_N", "150")   # current live value
    snapshot, ships, wps, edges = R.load()
    ships = ships[::2][:20]      # deterministic stratified sample; the full fleet is slow
    with open(os.path.join(R.SVC, "model_artifacts", "market_model.json")) as f:
        model = json.load(f)
    version = f"{model['fit_version']}@{model['era']}"
    # Trace off: this sweep measures outcomes, not argmins.
    tour_solver.score_sequence = R._ORIG
    args = (snapshot, ships, wps, edges, model, version)

    print(f"\n=== {len(ships)} hulls, beam, ONE snapshot, one term relaxed at a time ===")
    run_arm("BASELINE (live knobs)", *args)
    # max_spend: the x10/x100 arms must come out IDENTICAL to baseline and the /100 arm
    # must collapse. Without the /100 arm a flat result would be indistinguishable from a
    # sweep that cannot see the term at all.
    run_arm("max_spend x10   (10.75M -> 107.5M)", *args, spend=107_500_000)
    run_arm("max_spend x100  (10.75M -> 1.075B)", *args, spend=1_075_000_000)
    run_arm("max_spend /100  (10.75M -> 107.5k)", *args, spend=107_500)
    # Same two-sided discipline for the per-visit sink cap.
    run_arm("REALIZED_SINK_TRANCHES 2.5 -> 6.0", *args, sink_tranches=6.0)
    run_arm("REALIZED_SINK_TRANCHES 2.5 -> 1.0", *args, sink_tranches=1.0)
    for n in (2, 4, 5, 6):
        run_arm(f"MAX_PLANNED_TRANCHES 3 -> {n}", *args, planned_tranches=n)
    run_arm("hold capacity x2 (225 -> 450)", *args, hold_mult=2)
    run_arm("trade_volume x2  (buy AND sell depth)", *args, tv_mult=2)


if __name__ == "__main__":
    main()

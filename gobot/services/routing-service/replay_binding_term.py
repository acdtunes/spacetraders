#!/usr/bin/env python3
"""sp-o2dzb — which term bounds planned units on LIVE plans?

Replays solve_tour against the live Postgres market snapshot with the real hull
states, then reads tour_solver._ALLOC_TRACE for the WINNING sequence and reports
the argmin distribution over every term that can bound an allocation.
"""
import collections
import json
import os
import subprocess
import sys

SVC = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SVC)

# Mirror the LIVE routing-service process env (ps eww 97811), so the replay
# resolves the same knobs production does.
os.environ.setdefault("TOUR_SOLVER_MAX_PLANNED_TRANCHES", "3")
os.environ.setdefault("TOUR_SOLVER_OBJECTIVE", "rate")
os.environ.setdefault("TOUR_SOLVER_RATE_ARMED_LONG", "1")
os.environ.setdefault("TOUR_SOLVER_SEQUENCER", "ortools")
os.environ.setdefault("TOUR_SOLVER_FULL_SCORE_TOP_N", "150")
os.environ.setdefault("TOUR_SOLVER_ORTOOLS_MAX_NODES", "160")
os.environ.setdefault("TOUR_SOLVER_INTER_SYSTEM_TRAVEL_BASE_SECONDS", "750")
os.environ.setdefault("TOUR_SOLVER_INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS", "650")

from utils import tour_solver  # noqa: E402


def q(sql):
    out = subprocess.run(
        ["docker", "exec", "spacetraders-postgres", "psql", "-U", "spacetraders",
         "-d", "spacetraders", "-t", "-A", "-F", "\x1f", "-c", sql],
        capture_output=True, text=True, check=True).stdout
    return [line.split("\x1f") for line in out.splitlines() if line.strip()]


def load():
    rows = q("""SELECT waypoint_symbol, good_symbol, purchase_price, sell_price,
                       trade_volume, coalesce(supply,''), coalesce(activity,''),
                       extract(epoch FROM last_updated)::bigint
                FROM market_data WHERE last_updated > now() - interval '75 minutes'""")
    snapshot = [dict(waypoint_symbol=r[0], system_symbol="-".join(r[0].split("-")[:2]),
                     good_symbol=r[1], ask=int(r[2]), bid=int(r[3]),
                     trade_volume=int(r[4]), supply=r[5], activity=r[6],
                     observed_at_unix=int(r[7])) for r in rows]

    ships = q("""SELECT ship_symbol, system_symbol, location_symbol, cargo_capacity,
                        cargo_inventory::text, 30
                 FROM ships WHERE cargo_capacity >= 120 ORDER BY ship_symbol""")

    wps = {}
    for sym, blob in q("SELECT system_symbol, (graph_data->'waypoints')::text FROM system_graphs"):
        for w in json.loads(blob).values():
            wps[w["symbol"]] = dict(symbol=w["symbol"], x=w["x"], y=w["y"],
                                    system_symbol=sym)

    edges = collections.defaultdict(set)
    for a, b in q("SELECT system_symbol, connected_system FROM gate_edges "
                  "WHERE NOT under_construction"):
        edges[a].add(b)
        edges[b].add(a)
    return snapshot, ships, wps, edges


# --- trace capture -----------------------------------------------------------
_ORIG = tour_solver.score_sequence
_CAPTURED = []


def _traced(seq, *a, **kw):
    tour_solver._ALLOC_TRACE = []
    try:
        res = _ORIG(seq, *a, **kw)
    finally:
        trace, tour_solver._ALLOC_TRACE = tour_solver._ALLOC_TRACE, None
    _CAPTURED.append((tuple(seq), res, trace))
    return res


tour_solver.score_sequence = _traced


def plan_key(legs):
    return tuple((l["waypoint_symbol"], t["good_symbol"], t["is_buy"], t["units"])
                 for l in legs for t in l["trades"])


def main():
    snapshot, ships, wps, edges = load()
    with open(os.path.join(SVC, "model_artifacts", "market_model.json")) as f:
        model = json.load(f)
    version = f"{model['fit_version']}@{model['era']}"
    by_system = collections.defaultdict(list)
    for r in snapshot:
        by_system[r["system_symbol"]].append(r)

    binding = collections.Counter()
    term_dist = collections.Counter()
    census = collections.Counter()
    fills, solves, alloc_rows = [], 0, []

    for sym, ssys, loc, cap, inv, speed in ships:
        cap = int(cap)
        cargo = [dict(good_symbol=c["symbol"], units=c["units"])
                 for c in (json.loads(inv) or []) if c.get("units", 0) > 0]
        ship = dict(ship_symbol=sym, current_waypoint=loc, current_system=ssys,
                    hold_capacity=cap, fuel_current=4000, fuel_capacity=4000,
                    engine_speed=int(speed or 30), cargo=cargo)
        # Production plans over the anchor system + 1 neighbour (MAX_TOUR_SYSTEMS=2).
        # Try each gate neighbour that has market data and keep the best solve, which
        # is what the Go candidate walk effectively selects.
        neighbours = [n for n in sorted(edges.get(ssys, ())) if by_system.get(n)]
        best = None
        for nb in [None] + neighbours[:6]:
            allowed = [ssys] + ([nb] if nb else [])
            rows = [r for s in allowed for r in by_system.get(s, ())]
            if not rows:
                continue
            cons = dict(max_hops=6, min_margin_per_unit=0, max_spend=10_750_000,
                        working_capital_reserve=0, allowed_systems=allowed,
                        max_snapshot_age_minutes=75, expected_model_version=version,
                        externality_weight=0.35)
            _CAPTURED.clear()
            try:
                out = tour_solver.solve_tour(
                    rows, ship, cons, model,
                    waypoints=[wps[r["waypoint_symbol"]] for r in rows
                               if r["waypoint_symbol"] in wps])
            except Exception as e:  # noqa: BLE001
                print(f"  {sym} {allowed}: solve error {e!r}")
                continue
            if not out.get("legs"):
                continue
            key = plan_key(out["legs"])
            trace = next((t for s, r, t in _CAPTURED if plan_key(r["legs"]) == key), None)
            if trace and (best is None or out["projected_profit"] > best[0]):
                best = (out["projected_profit"], sym, allowed, out, trace)
        if best is None:
            print(f"  {sym}: no feasible tour")
            continue

        _profit, _s, allowed, out, trace = best
        solves += 1
        peak = 0
        for rec in trace:
            if rec["event"] == "terminated":
                for k, v in rec["census"].items():
                    census[k] += v
                peak = rec["peak_occupancy"]
                continue
            if rec["buy_leg"] is None:
                continue  # launch-cargo liquidation: no acquisition, not a "fill" decision
            binding["+".join(rec["binding"]) or "none"] += 1
            for t in rec["binding"]:
                term_dist[t] += 1
            alloc_rows.append(rec)
        fills.append((sym, cap, peak, 100.0 * peak / cap, len(out["legs"]),
                      sum(c["units"] for c in cargo), allowed))

    print(f"\n=== {solves} live solves, {len(alloc_rows)} market-buy allocations ===")
    print("\n--- ARGMIN over every bounding term (per allocation) ---")
    tot = sum(binding.values()) or 1
    for k, v in binding.most_common():
        print(f"  {k:38s} {v:5d}  {100.0*v/tot:5.1f}%")
    print("\n--- term appears in the argmin set ---")
    for k, v in term_dist.most_common():
        print(f"  {k:20s} {v:5d}  {100.0*v/tot:5.1f}%")
    print("\n--- why the greedy loop TERMINATED (pairings by first blocking gate) ---")
    ctot = sum(census.values()) or 1
    for k, v in census.most_common(10):
        print(f"  {k:38s} {v:6d}  {100.0*v/ctot:5.1f}%")
    print("\n--- peak hold occupancy of the WINNING plan ---")
    print(f"  {'ship':14s} {'cap':>5s} {'peak':>6s} {'fill%':>7s} {'legs':>5s} {'launch':>7s}  systems")
    for sym, cap, peak, pct, legs, launch, allowed in sorted(fills, key=lambda r: -r[3]):
        print(f"  {sym:14s} {cap:5d} {peak:6d} {pct:7.1f} {legs:5d} {launch:7d}  {allowed}")
    if fills:
        print(f"\n  MEAN fill {sum(f[3] for f in fills)/len(fills):.1f}%  "
              f"median {sorted(f[3] for f in fills)[len(fills)//2]:.1f}%")
    if alloc_rows:
        print(f"  MEAN units/market-buy-allocation {sum(r['units'] for r in alloc_rows)/len(alloc_rows):.1f}")


if __name__ == "__main__":
    main()

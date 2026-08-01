"""Throwaway timing probe: measure per-solve ortools cost at candidate-hop-depth 1/2/3
so a full arming run can be sized under the 20-min budget BEFORE launching it.
Reuses the harness functions from the working copy. Not production; deleted after use.
"""
import sys, time
from collections import defaultdict

import replay_hopwiden_TEMP as H
from utils.tour_solver import solve_tour, OBJECTIVE_RATE

H.MODEL, version = H.load_model()
engine = H.db_engine()
rows = H.fetch_history(engine, 3)
neighbors = H.fetch_gate_neighbors(engine)
coords = H.fetch_waypoint_coords(engine)

newest = max(r.recorded_at for r in rows)
from datetime import timedelta
sample_t = newest  # newest edge, representative live shape
snap = H.reconstruct_snapshot(rows, sample_t)
by_system = defaultdict(set)
for s in snap:
    by_system[s["system_symbol"]].add(s["waypoint_symbol"])
waypoints = [dict(symbol=wp, system=sys_, x=int(x), y=int(y))
             for wp, (sys_, x, y) in coords.items()]

homes = sorted((h for h, m in by_system.items() if len(m) >= 2),
               key=lambda h: len(neighbors.get(h, set())), reverse=True)
probe_homes = homes[:3]  # most-connected -> worst-case growth

print(f"per-solve cap-4 RATE ortools timing (snapshot @ {sample_t})")
print(f"{'home':<12} {'depth':>5} {'|allowed|':>9} {'scoped_mkts':>11} {'solve_s':>8}")
depth_solve_totals = defaultdict(float)
for home in probe_homes:
    home_markets = sorted(by_system[home])
    for depth in (1, 2, 3):
        allowed = H.compute_allowed(home, neighbors, by_system, depth)
        scoped = [s for s in snap if s["system_symbol"] in allowed]
        wps = [w for w in waypoints if w["system"] in allowed]
        ship = dict(ship_symbol="PROBE-220", current_waypoint=home_markets[0],
                    current_system=home, hold_capacity=220,
                    fuel_current=400, fuel_capacity=400,
                    engine_speed=H.ENGINE_SPEED, cargo=[])
        cons = dict(max_hops=6, min_margin_per_unit=1,
                    max_snapshot_age_minutes=H.STALENESS_MINUTES,
                    max_spend=2_000_000, working_capital_reserve=50_000,
                    allowed_systems=sorted(allowed), expected_model_version=version,
                    max_tour_systems=4)
        t0 = time.time()
        solve_tour(scoped, dict(ship), dict(cons), H.MODEL, waypoints=wps,
                   objective=OBJECTIVE_RATE)
        dt = time.time() - t0
        depth_solve_totals[depth] += dt
        print(f"{home:<12} {depth:>5} {len(allowed):>9} {len(scoped):>11} {dt:>8.3f}")

print("\navg per-solve seconds by depth (over probe homes):")
for depth in (1, 2, 3):
    print(f"  depth {depth}: {depth_solve_totals[depth]/len(probe_homes):.3f}s")

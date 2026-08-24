#!/usr/bin/env python3
"""Regenerate route_goldens.json from the OR-Tools service's fuel-state Dijkstra.

The goldens pin the Go in-process planner to the Python engine that defined the
route contract: utils.routing_engine.ORToolsRoutingEngine.find_optimal_path, which
the routing service still runs underneath its VRP solvers. Running this script is
how you re-confirm that the two engines have not drifted apart.

    services/routing-service/venv/bin/python \
        internal/domain/routing/testdata/generate_route_goldens.py            # rewrite
    services/routing-service/venv/bin/python \
        internal/domain/routing/testdata/generate_route_goldens.py --check    # diff only

Waypoint graphs are read back out of the existing goldens file, so the script needs
no database and no running service. The case list lives below in CASES. A case carries
its own waypoint list only when it hands them over in something other than sorted
order; otherwise the reader sorts the named graph.

A case's tie_class says how strictly the Go planner must match:
  exact      - waypoints are handed over in lexicographic order, which is also the
               order the Go planner scans neighbours in, so both engines generate
               candidates in the same order and every step must agree.
  cost_only  - waypoints are handed over shuffled, so the two engines break
               equal-cost ties differently. Total time and total fuel must still
               agree exactly; the chosen path need not.
"""

import argparse
import json
import os
import random
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
GOLDENS = os.path.join(HERE, "route_goldens.json")
SERVICE = os.path.abspath(os.path.join(HERE, "..", "..", "..", "..", "services", "routing-service"))

sys.path.insert(0, SERVICE)

from utils.routing_engine import ORToolsRoutingEngine, Waypoint  # noqa: E402

MG48 = "X1-MG48"
CORRIDOR = "corridor"
STARVED = "starved"
TIE = "tie"

# Ship shapes are the ones the fleet actually flies: probe (no tank), frigate,
# light freighter.
FRIGATE = dict(fuel_capacity=400, engine_speed=36)
FREIGHTER = dict(fuel_capacity=600, engine_speed=15)
PROBE = dict(fuel_capacity=0, engine_speed=9)

CASES = [
    dict(name="mg48_full_tank_long_haul", graph=MG48,
         start="X1-MG48-J70", goal="X1-MG48-J86", current_fuel=400, **FRIGATE),
    dict(name="mg48_full_tank_freighter", graph=MG48,
         start="X1-MG48-J70", goal="X1-MG48-J86", current_fuel=600, **FREIGHTER),
    dict(name="mg48_mid_range_partial_tank", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-K90", current_fuel=210, **FRIGATE),
    dict(name="mg48_orbital_cluster_hop", graph=MG48,
         start="X1-MG48-A1", goal="X1-MG48-A4", current_fuel=400, **FRIGATE),
    dict(name="mg48_same_waypoint", graph=MG48,
         start="X1-MG48-A1", goal="X1-MG48-A1", current_fuel=400, **FRIGATE),
    dict(name="mg48_start_below_ninety_percent", graph=MG48,
         start="X1-MG48-A1", goal="X1-MG48-E47", current_fuel=300, **FRIGATE),
    dict(name="mg48_start_above_ninety_percent", graph=MG48,
         start="X1-MG48-A1", goal="X1-MG48-AX5C", current_fuel=399, **FRIGATE),
    dict(name="mg48_long_refuel_chain", graph=MG48,
         start="X1-MG48-J74", goal="X1-MG48-B39", current_fuel=400, **FRIGATE),
    dict(name="mg48_long_refuel_chain_prefer_cruise", graph=MG48,
         start="X1-MG48-J74", goal="X1-MG48-B39", current_fuel=400,
         prefer_cruise=True, **FRIGATE),
    dict(name="mg48_long_refuel_chain_freighter", graph=MG48,
         start="X1-MG48-J74", goal="X1-MG48-B39", current_fuel=600, **FREIGHTER),
    dict(name="mg48_refuel_chain_from_dry_station", graph=MG48,
         start="X1-MG48-J61", goal="X1-MG48-B38", current_fuel=60, **FRIGATE),
    dict(name="mg48_refuel_chain_partial_tank_no_station", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-J78", current_fuel=150, **FRIGATE),
    dict(name="mg48_refuel_chain_fuel_efficient", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-J78", current_fuel=400,
         fuel_efficient=True, **FRIGATE),
    dict(name="mg48_unreachable_hop_drifts", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-J86", current_fuel=120, **FRIGATE),
    dict(name="mg48_starved_drift", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-B11", current_fuel=3, **FRIGATE),
    dict(name="mg48_starved_drift_fuel_efficient", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-B11", current_fuel=3,
         fuel_efficient=True, **FRIGATE),
    dict(name="mg48_prefer_cruise", graph=MG48,
         start="X1-MG48-J70", goal="X1-MG48-J86", current_fuel=400,
         prefer_cruise=True, **FRIGATE),
    dict(name="mg48_probe_zero_capacity", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-J86", current_fuel=0, **PROBE),
    dict(name="mg48_probe_zero_capacity_orbital", graph=MG48,
         start="X1-MG48-A1", goal="X1-MG48-A4", current_fuel=0, **PROBE),
    dict(name="mg48_unknown_goal", graph=MG48,
         start="X1-MG48-B10", goal="X1-MG48-NOWHERE", current_fuel=400, **FRIGATE),
    dict(name="mg48_shuffled_waypoint_order", graph=MG48, order="shuffled",
         tie_class="cost_only",
         start="X1-MG48-J74", goal="X1-MG48-B39", current_fuel=400, **FRIGATE),
    dict(name="corridor_multi_refuel", graph=CORRIDOR,
         start="COR-A", goal="COR-E", current_fuel=220,
         fuel_capacity=220, engine_speed=30),
    dict(name="corridor_empty_tank_at_fuelled_start", graph=CORRIDOR,
         start="COR-A", goal="COR-E", current_fuel=0,
         fuel_capacity=220, engine_speed=30),
    dict(name="corridor_no_refuel_needed", graph=CORRIDOR,
         start="COR-A", goal="COR-B", current_fuel=220,
         fuel_capacity=220, engine_speed=30),
    dict(name="starved_unreachable", graph=STARVED,
         start="STV-P", goal="STV-Q", current_fuel=0,
         fuel_capacity=100, engine_speed=30),
    dict(name="starved_drift_to_goal", graph=STARVED,
         start="STV-P", goal="STV-Q", current_fuel=2,
         fuel_capacity=100, engine_speed=30),
    dict(name="tie_takes_first_of_two_equal_detours", graph=TIE,
         start="TIE-A", goal="TIE-G", current_fuel=150,
         fuel_capacity=150, engine_speed=30),
]

# Graphs the goldens file does not already carry. The real X1-MG48 graph is only
# ever read back from the goldens file - it was captured from the daemon's
# waypoints table and is not reproducible from code.
SYNTHETIC_GRAPHS = {
    CORRIDOR: [
        dict(symbol="COR-A", x=0.0, y=0.0, has_fuel=True),
        dict(symbol="COR-B", x=100.0, y=0.0, has_fuel=False),
        dict(symbol="COR-C", x=200.0, y=0.0, has_fuel=True),
        dict(symbol="COR-D", x=300.0, y=0.0, has_fuel=False),
        dict(symbol="COR-E", x=400.0, y=0.0, has_fuel=False),
    ],
    STARVED: [
        dict(symbol="STV-P", x=0.0, y=0.0, has_fuel=False),
        dict(symbol="STV-Q", x=500.0, y=0.0, has_fuel=False),
    ],
    # Two mirror-image refuelling detours of identical cost, so the route taken
    # is decided purely by the order candidates are generated in.
    TIE: [
        dict(symbol="TIE-A", x=0.0, y=0.0, has_fuel=True),
        dict(symbol="TIE-G", x=200.0, y=0.0, has_fuel=False),
        dict(symbol="TIE-N", x=100.0, y=100.0, has_fuel=True),
        dict(symbol="TIE-S", x=100.0, y=-100.0, has_fuel=True),
    ],
}

ACTION_NAMES = {"TRAVEL": "TRAVEL", "REFUEL": "REFUEL"}


def load_graphs():
    graphs = dict(SYNTHETIC_GRAPHS)
    if os.path.exists(GOLDENS):
        with open(GOLDENS) as handle:
            graphs.update(json.load(handle).get("graphs", {}))
    missing = {case["graph"] for case in CASES} - set(graphs)
    if missing:
        raise SystemExit(f"no waypoint data for graph(s): {sorted(missing)}")
    return graphs


def order_waypoints(rows, order, seed):
    ordered = sorted(rows, key=lambda row: row["symbol"])
    if order == "shuffled":
        random.Random(seed).shuffle(ordered)
    return ordered


def plan(engine, rows, case):
    graph = {
        row["symbol"]: Waypoint(
            symbol=row["symbol"], x=row["x"], y=row["y"], has_fuel=row["has_fuel"]
        )
        for row in rows
    }
    result = engine.find_optimal_path(
        graph=graph,
        start=case["start"],
        goal=case["goal"],
        current_fuel=case["current_fuel"],
        fuel_capacity=case["fuel_capacity"],
        engine_speed=case["engine_speed"],
        fuel_efficient=case.get("fuel_efficient", False),
        prefer_cruise=case.get("prefer_cruise", False),
    )
    if result is None:
        return dict(
            success=False,
            error_message=f"No path found from {case['start']} to {case['goal']}",
            steps=[],
            total_fuel_cost=0,
            total_time_seconds=0,
            total_distance=0.0,
        )
    steps = []
    for step in result["steps"]:
        steps.append(dict(
            action=ACTION_NAMES[step["action"]],
            waypoint=step["waypoint"],
            fuel_cost=step["fuel_cost"],
            time_seconds=step["time"],
            distance=float(step.get("distance", 0.0)),
            # The handler stamps CRUISE on any step the engine left unmarked,
            # which is every refuel step.
            mode=step.get("mode", "CRUISE"),
            refuel_amount=step.get("refuel_amount", 0),
        ))
    return dict(
        success=True,
        error_message="",
        steps=steps,
        total_fuel_cost=result["total_fuel_cost"],
        total_time_seconds=result["total_time"],
        total_distance=float(result["total_distance"]),
    )


def build():
    graphs = load_graphs()
    engine = ORToolsRoutingEngine()
    cases = []
    for index, case in enumerate(CASES):
        order = case.get("order", "sorted")
        rows = order_waypoints(graphs[case["graph"]], order, index)
        request = dict(
            system_symbol=case["graph"],
            start_waypoint=case["start"],
            goal_waypoint=case["goal"],
            current_fuel=case["current_fuel"],
            fuel_capacity=case["fuel_capacity"],
            engine_speed=case["engine_speed"],
            fuel_efficient=case.get("fuel_efficient", False),
            prefer_cruise=case.get("prefer_cruise", False),
        )
        if order != "sorted":
            request["waypoints"] = rows
        cases.append(dict(
            name=case["name"],
            graph=case["graph"],
            waypoint_order=order,
            tie_class=case.get("tie_class", "exact"),
            request=request,
            response=plan(engine, rows, case),
        ))
    used = sorted({case["graph"] for case in CASES})
    return dict(
        engine="services/routing-service utils.routing_engine.find_optimal_path",
        regenerate="internal/domain/routing/testdata/generate_route_goldens.py",
        graphs={name: sorted(graphs[name], key=lambda row: row["symbol"]) for name in used},
        cases=cases,
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true",
                        help="compare against the committed goldens instead of rewriting them")
    args = parser.parse_args()

    payload = build()
    rendered = json.dumps(payload, indent=2, sort_keys=True) + "\n"
    if args.check:
        with open(GOLDENS) as handle:
            if handle.read() == rendered:
                print(f"{len(payload['cases'])} case(s) match the committed goldens")
                return 0
        print("goldens differ from the reference engine", file=sys.stderr)
        return 1
    with open(GOLDENS, "w") as handle:
        handle.write(rendered)
    print(f"wrote {len(payload['cases'])} case(s) to {GOLDENS}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

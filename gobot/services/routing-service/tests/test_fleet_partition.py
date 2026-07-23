# gobot/services/routing-service/tests/test_fleet_partition.py
"""VRP fleet partitioning (PartitionFleet) — sp-t73c.

The live routing service partitions a system's markets across 2+ scout hulls via
ORToolsRoutingEngine.optimize_fleet_tour. A single-ship scout bypasses the VRP and
keeps ALL its markets; a 2+ ship partition MUST keep parity — it must never return
an empty partition (0 tours) nor silently omit a market the VRP could not place.

Reproduction geometry (KM70 5E+5F shape): two hulls share a fuelled start and scout
5 MOON + 5 ASTEROID markets. Two of the market symbols are ABSENT from the system
graph — the real sp-8k9m failure, where destination waypoints were resolved against
the wrong system's waypoint cache and never made it into the graph. Such a market
has only unreachable (1,000,000) arcs, so the VRP drops it; the live engine used to
log-and-omit it (a silently shrunk book), and on solution=None returned an empty
partition (0 tours) — which for 2+ ships collapsed the whole reset. The fix keeps
parity: every market is partitioned, none dropped, no ship left empty.
"""
from utils.routing_engine import ORToolsRoutingEngine, Waypoint


# Eight in-graph markets in a tight, fully-reachable cluster around a fuelled yard.
_IN_GRAPH = {
    "X1-KM70-ZY1": (0, 0, True),      # shared start, has fuel
    "X1-KM70-E1": (100, 0, False),
    "X1-KM70-E2": (0, 100, False),
    "X1-KM70-E3": (-100, 0, False),
    "X1-KM70-E4": (0, -100, False),
    "X1-KM70-E5": (80, 80, False),
    "X1-KM70-F1": (-80, -80, False),
    "X1-KM70-F2": (120, -40, False),
    "X1-KM70-F3": (-40, 120, False),
}
# Two market symbols the caller asks for but which never made it into the graph
# (the sp-8k9m cache-scope miss). They are unreachable and get dropped by the VRP.
_MISSING_FROM_GRAPH = ["X1-KM70-F4", "X1-KM70-F5"]

_MARKETS = [
    "X1-KM70-E1", "X1-KM70-E2", "X1-KM70-E3", "X1-KM70-E4", "X1-KM70-E5",
    "X1-KM70-F1", "X1-KM70-F2", "X1-KM70-F3",
    *_MISSING_FROM_GRAPH,
]


def _graph():
    return {
        sym: Waypoint(symbol=sym, x=x, y=y, has_fuel=fuel)
        for sym, (x, y, fuel) in _IN_GRAPH.items()
    }


def _assigned(result):
    seen = set()
    for markets in result.values():
        seen.update(markets)
    return seen


def test_two_ships_sharing_start_keep_every_market_including_ungraphed_ones():
    """Two scouts sharing a start partition all markets; none dropped, none empty."""
    engine = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=2)
    result = engine.optimize_fleet_tour(
        graph=_graph(),
        markets=list(_MARKETS),
        ship_locations={"TORWIND-5E": "X1-KM70-ZY1", "TORWIND-5F": "X1-KM70-ZY1"},
        fuel_capacity=400,
        engine_speed=30,
    )

    assert result is not None, "partition must never be None"
    assigned = _assigned(result)
    dropped = set(_MARKETS) - assigned
    assert not dropped, f"no market may be silently dropped, but the VRP omitted: {sorted(dropped)}"
    assert len(assigned) == len(_MARKETS), f"all {len(_MARKETS)} markets must be assigned"

    loads = [len(m) for m in result.values()]
    assert min(loads) >= 1, f"no ship may be left empty (0 tours): loads={loads}"
    assert max(loads) <= min(loads) * 2 + 1, f"load must be reasonably balanced: loads={loads}"


def test_multi_slot_partition_materializes_every_slot():
    """sp-enry: N synthetic slot-hulls at one waypoint each get a non-empty tour."""
    engine = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=2)
    result = engine.optimize_fleet_tour(
        graph=_graph(),
        markets=list(_MARKETS),
        ship_locations={
            "slot-0": "X1-KM70-ZY1",
            "slot-1": "X1-KM70-ZY1",
            "slot-2": "X1-KM70-ZY1",
        },
        fuel_capacity=400,
        engine_speed=30,
    )

    assert result is not None
    assigned = _assigned(result)
    assert set(_MARKETS) == assigned, (
        f"every market must land on a slot; dropped={sorted(set(_MARKETS) - assigned)}"
    )
    loads = [len(m) for m in result.values()]
    assert min(loads) >= 1, f"every slot must be materialized (non-empty): loads={loads}"


# ---------------------------------------------------------------------------
# sp-cc2na — min-makespan partition, every probe used.
#
# The scout-post coordinator floors the home post to N slots and asks PartitionFleet
# to split a system's markets into N disjoint per-probe tours. The VRP minimizes total
# tour cost with a global-span (makespan) term; but when using an extra probe would NOT
# lower the max tour time — a tight cluster far from the shared start (splitting it barely
# changes the depot-leg-dominated time), or an outlier market that pins the makespan —
# the secondary arc-cost sum packs the markets onto FEWER vehicles and leaves probe(s)
# idle. Live symptom (DEV11 @ X1-CH36): "3 disjoint tours over 27 markets" logged, yet
# only 2 probes move, so coverage crawls at 2-probe cadence.
#
# These cases use ALL-REACHABLE markets so the _distribute_evenly fallback (which round-
# robins UNPLACEABLE markets and would mask an idle probe) never fires — the partition
# itself must materialize every probe.
# ---------------------------------------------------------------------------

_FUEL_CAPACITY = 400   # partitionAnchorFuelCapacity (Go side)
_ENGINE_SPEED = 30     # partitionAnchorEngineSpeed (Go side)
_DEPOT = "X1-CC2-HOME"


def _graph_from(points):
    return {sym: Waypoint(symbol=sym, x=x, y=y, has_fuel=fuel) for sym, (x, y, fuel) in points.items()}


def _tour_time(engine, graph, depot, markets):
    """Closed-circuit time depot -> markets (in the given order) -> depot.

    Freshness per market is its probe's circuit time, so this is the quantity the
    partition must balance across probes. Each leg is priced by the same fuel-aware
    pathfinder the engine uses to build its VRP matrix.
    """
    if not markets:
        return 0
    total = 0
    cur = depot
    for nxt in list(markets) + [depot]:
        leg = engine.find_optimal_path(graph, cur, nxt, _FUEL_CAPACITY, _FUEL_CAPACITY, _ENGINE_SPEED)
        total += leg["total_time"] if leg else 10 ** 6
        cur = nxt
    return total


# A tight 8-market cluster ~350 units from the fuelled depot: once a probe pays the
# ~350-out/~350-back depot legs, visiting many of these adds almost nothing, so tour
# time is depot-leg-dominated and nearly flat in market count.
_FAR_CLUSTER = {
    f"X1-CC2-C{i}": (x, y, False)
    for i, (x, y) in enumerate(
        [(350, 0), (356, 0), (350, 6), (356, 6), (353, 3), (347, 3), (359, 3), (353, 9)]
    )
}
# One isolated market a similar distance away on a different bearing: a full ~722s
# circuit on its own, and ~495 units (an un-bridged hop) from the cluster.
_ISOLATED = {"X1-CC2-I0": (0, 350, False)}


def _hybrid_points():
    return {_DEPOT: (0, 0, True), **_FAR_CLUSTER, **_ISOLATED}


def test_every_probe_used_and_partition_balanced_by_time_not_market_count():
    """N=3 probes share a start; 8-market far cluster + 1 isolated market, all reachable.

    A min-makespan partition gives the isolated market its own probe and splits the
    cluster across the other two (~1 / 4 / 4) so the three CIRCUIT TIMES match — even
    though the market COUNTS are deliberately lopsided. The pre-fix engine instead packs
    the whole cluster onto one probe and leaves the third idle (~1 / 0 / 8).
    """
    engine = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=2)
    graph = _hybrid_points()
    g = _graph_from(graph)
    markets = list(_FAR_CLUSTER) + list(_ISOLATED)
    result = engine.optimize_fleet_tour(
        graph=g,
        markets=markets,
        ship_locations={"PROBE-A": _DEPOT, "PROBE-B": _DEPOT, "PROBE-C": _DEPOT},
        fuel_capacity=_FUEL_CAPACITY,
        engine_speed=_ENGINE_SPEED,
    )

    assert result is not None
    assert _assigned(result) == set(markets), "every market must be assigned"

    loads = sorted(len(m) for m in result.values())
    # (a) NO probe left idle — the load-bearing sp-cc2na guarantee (M >= N).
    assert loads[0] >= 1, f"every probe must get a non-empty tour: loads={loads}"

    times = [_tour_time(engine, g, _DEPOT, m) for m in result.values()]
    # (b) balanced by TIME: circuit times within a generous 25% band...
    assert max(times) <= min(times) * 1.25, f"probe circuit times must be balanced: times={sorted(times)}"
    # ...but explicitly NOT by market count: a count-balanced split would be ~3/3/3, and
    # (crucially) could not achieve the time band because pairing the isolated market with
    # cluster markets forces a ~2x circuit. A >=2 spread proves time, not count, drove it.
    assert max(loads) - min(loads) >= 2, (
        f"balance must be by time, not count — counts stay lopsided: loads={loads}"
    )


def test_no_probe_idle_on_tight_far_cluster():
    """N=3 probes, one tight cluster of 9 reachable markets far from the shared start.

    Splitting the cluster barely moves the makespan (depot legs dominate), so the pre-fix
    sum term packs all 9 onto ONE probe (~9 / 0 / 0), idling two. Every probe must scout.
    """
    engine = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=2)
    points = {_DEPOT: (0, 0, True)}
    for i, (x, y) in enumerate(
        [(360, 0), (368, 0), (360, 8), (368, 8), (364, 4), (356, 4), (372, 4), (364, 12), (356, 12)]
    ):
        points[f"X1-CC2-M{i}"] = (x, y, False)
    g = _graph_from(points)
    markets = [f"X1-CC2-M{i}" for i in range(9)]

    result = engine.optimize_fleet_tour(
        graph=g,
        markets=markets,
        ship_locations={"PROBE-A": _DEPOT, "PROBE-B": _DEPOT, "PROBE-C": _DEPOT},
        fuel_capacity=_FUEL_CAPACITY,
        engine_speed=_ENGINE_SPEED,
    )

    assert result is not None
    assert _assigned(result) == set(markets), "every market must be assigned"
    loads = sorted(len(m) for m in result.values())
    assert loads[0] >= 1, f"no probe may sit idle (all 3 must scout): loads={loads}"


def test_partition_is_deterministic_across_repeated_solves():
    """Identical inputs must yield an identical partition — the Go side freezes the cut
    and re-solving must not thrash it (sp-cc2na req #3)."""
    graph = _hybrid_points()
    g = _graph_from(graph)
    markets = list(_FAR_CLUSTER) + list(_ISOLATED)
    locs = {"PROBE-A": _DEPOT, "PROBE-B": _DEPOT, "PROBE-C": _DEPOT}

    signatures = set()
    for _ in range(3):
        engine = ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=2)
        result = engine.optimize_fleet_tour(
            graph=g, markets=list(markets), ship_locations=dict(locs),
            fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED,
        )
        signatures.add(tuple(sorted((ship, tuple(wps)) for ship, wps in result.items())))

    assert len(signatures) == 1, f"partition must be deterministic; got {len(signatures)} distinct results"

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

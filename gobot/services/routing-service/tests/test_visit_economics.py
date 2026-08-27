"""Visit-economics measurement — the grouping and ceiling arithmetic, no DB.

Every number this script reports rests on two judgements that are easy to get quietly wrong:
what counts as ONE visit, and what counts as a good a hop could have bundled. These pin both.
"""
import pytest

import visit_economics as ve
from utils.tour_solver import API_CALLS_PER_VISIT


def _rows(*triples):
    """(ship, waypoint, good, epoch, engine) rows, already ship/time ordered."""
    return [(s, w, g, t, e) for s, w, g, t, e in triples]


def test_consecutive_rows_at_one_waypoint_are_one_visit():
    # The dock is the unit of movement cost: three chunks of two goods at one waypoint cost
    # ONE navigate/dock/orbit bundle, and counting them as three visits would triple the very
    # overhead the report exists to size.
    sessions = ve.dock_sessions(_rows(
        ("H", "A", "G", 0, "solver"), ("H", "A", "G", 10, "solver"),
        ("H", "A", "F", 20, "solver")), gap_seconds=600)
    assert len(sessions) == 1
    assert sessions[0]["chunks"] == 3
    assert sessions[0]["goods"] == {"G", "F"}


def test_a_return_to_the_same_waypoint_is_a_second_visit():
    sessions = ve.dock_sessions(_rows(
        ("H", "A", "G", 0, "solver"), ("H", "B", "G", 10, "solver"),
        ("H", "A", "G", 20, "solver")), gap_seconds=600)
    assert [s["waypoint"] for s in sessions] == ["A", "B", "A"]


def test_an_idle_gap_at_one_waypoint_splits_the_visit():
    # A hull that traded, left, and came back without trading anywhere in between shows no
    # waypoint change — only the gap distinguishes two docks from one.
    sessions = ve.dock_sessions(_rows(
        ("H", "A", "G", 0, "solver"), ("H", "A", "G", 5_000, "solver")),
        gap_seconds=600)
    assert len(sessions) == 2


def test_two_hulls_never_share_a_visit():
    sessions = ve.dock_sessions(_rows(
        ("H1", "A", "G", 0, "solver"), ("H2", "A", "G", 1, "solver")),
        gap_seconds=600)
    assert len(sessions) == 2


def test_a_dock_two_engines_traded_at_pays_one_movement_bundle():
    # A look-back load filled at a waypoint the solver plan also trades at shares the bundle,
    # and the shared saving is only visible if the session carries both engine labels.
    sessions = ve.dock_sessions(_rows(
        ("H", "A", "G", 0, "lookback"), ("H", "A", "F", 5, "solver")),
        gap_seconds=600)
    report = {r["engine"]: r for r in ve.session_report(sessions)}
    assert "lookback+solver" in report
    assert report["lookback+solver"]["visits"] == 1


def test_the_request_bill_splits_into_movement_and_transactions():
    sessions = ve.dock_sessions(_rows(
        ("H", "A", "G", 0, "solver"), ("H", "A", "G", 5, "solver"),
        ("H", "B", "F", 50, "solver")), gap_seconds=600)
    report = {r["engine"]: r for r in ve.session_report(sessions)}
    row = report["ALL"]
    assert row["visits"] == 2 and row["chunks"] == 3
    assert row["goods_per_visit"] == pytest.approx(1.0)
    assert row["single_share"] == pytest.approx(1.0)
    movement = 2 * API_CALLS_PER_VISIT
    assert row["calls_per_transaction"] == pytest.approx((movement + 3) / 3)
    assert row["movement_share"] == pytest.approx(movement / (movement + 3))


def test_an_empty_window_degrades_to_zeros_and_never_divides():
    """THE DEGRADE-SAFELY GATE. Whoever next asks whether the bundling lever exists will point
    this at whatever window they have — a quiet hour, a fleet between eras, a fresh database.
    Every ratio it reports carries a count in its denominator, so an empty read has to come
    back as zeros rather than as a traceback that only reads as "no data" to someone who
    already knows the script."""
    assert ve.dock_sessions([], gap_seconds=600) == []
    empty = ve.session_report([])
    assert len(empty) == 1, "the ALL row is always reported, so this check is never vacuous"
    for row in empty:
        assert row["visits"] == 0
        assert row["goods_per_visit"] == 0.0
        assert row["chunks_per_visit"] == 0.0
        assert row["single_share"] == 0.0
        assert row["calls_per_transaction"] == 0.0
        assert row["movement_share"] == 0.0
    assert ve.pack_ceiling({}, hold=490, tranches=3, min_margin=1) == ({}, [])


def _market(ask, bid, volume):
    return (ask, bid, volume)


def test_the_ceiling_counts_only_goods_the_hop_can_actually_carry():
    # THE NUMBER THE WHOLE QUESTION TURNS ON. A pair shares three goods but only two clear the
    # margin floor, so the hop's bundling ceiling is two however rich the third looks.
    markets = {
        "X1-S1-A": {"G": _market(100, 0, 60), "H": _market(100, 0, 60),
                    "J": _market(600, 0, 60)},
        "X1-S1-B": {"G": _market(0, 600, 60), "H": _market(0, 600, 60),
                    "J": _market(0, 601, 60)},
    }
    histogram, fills = ve.pack_ceiling(markets, hold=490, tranches=3, min_margin=33)
    # Only A->B is a hop (B quotes no asks), and J's 1-credit spread is under the floor.
    assert histogram == {2: 1}
    assert fills == [pytest.approx(360 / 490)]   # 2 goods x 3 tranches x 60 units


def test_a_pair_with_nothing_profitable_is_not_counted_as_an_unbundled_hop():
    # Pairs that cannot trade at all are not a bundling failure; folding them in as "1 good"
    # or "0 goods" would move the mean for a reason that has nothing to do with bundling.
    markets = {"X1-S1-A": {"G": _market(600, 0, 300)},
               "X1-S1-B": {"G": _market(0, 100, 300)}}
    histogram, fills = ve.pack_ceiling(markets, hold=490, tranches=3, min_margin=1)
    assert histogram == {} and fills == []


def test_a_deep_good_fills_the_hold_alone_and_a_shallow_one_needs_company():
    # WHY THE CEILING IS NOT A GOODS COUNT. A good deep enough to fill the hold on its own
    # ends the pack at one however many others the pair shares, so the ceiling reads the
    # bundling a hop NEEDS, not the co-location it merely has.
    deep = {"X1-S1-A": {"G": _market(100, 0, 300), "H": _market(100, 0, 300)},
            "X1-S1-B": {"G": _market(0, 600, 300), "H": _market(0, 600, 300)}}
    histogram, fills = ve.pack_ceiling(deep, hold=490, tranches=3, min_margin=1)
    assert histogram == {1: 1}
    assert fills == [pytest.approx(1.0)]

    shallow = {"X1-S1-A": {g: _market(100, 0, 20) for g in "GHJ"},
               "X1-S1-B": {g: _market(0, 600, 20) for g in "GHJ"}}
    histogram, fills = ve.pack_ceiling(shallow, hold=490, tranches=3, min_margin=1)
    assert histogram == {3: 1}
    assert fills[0] == pytest.approx(180 / 490)   # 3 goods x 3 tranches x 20 units


def test_markets_in_different_systems_are_never_paired():
    # A tour crossing a gate pays a jump on top of the visit; the ceiling this script reports
    # is the IN-SYSTEM hop's, which is the one a bundling change would move.
    markets = {"X1-S1-A": {"G": _market(100, 0, 300)},
               "X1-S2-B": {"G": _market(0, 600, 300)}}
    assert ve.pack_ceiling(markets, hold=490, tranches=3, min_margin=1)[0] == {}

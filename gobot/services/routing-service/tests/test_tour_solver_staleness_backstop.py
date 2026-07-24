# gobot/services/routing-service/tests/test_tour_solver_staleness_backstop.py
"""The solver's max_snapshot_age_minutes filter is a BACKSTOP behind the caller's
per-activity freshness pass. Rows it drops are rows the caller believed were rankable, so
the drop must be reported — an unmetered second filter can silently void the upstream model
and only ever surfaces in the all-rows-gone case (no_fresh_market_data)."""
import logging
import re
import time

from utils.tour_solver import solve_tour

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def snap(wp, good, ask, bid, age_minutes, tv=20, system="S1"):
    return dict(waypoint_symbol=wp, system_symbol=system, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply="LIMITED", activity="WEAK",
                observed_at_unix=time.time() - age_minutes * 60)


SHIP = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
            hold_capacity=40, fuel_current=400, fuel_capacity=400,
            engine_speed=30, cargo=[])


def cons(age_cap):
    return dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=age_cap, expected_model_version="1@e")


def test_backstop_reports_how_many_in_scope_rows_it_dropped(caplog):
    """The report must count rows the backstop ALONE cost the plan: rows the solver would
    otherwise have routed over. Rows dropped for being out of the allowed systems (or for
    carrying no price) were never candidates, so counting them would inflate the signal
    into noise."""
    snapshot = [
        snap("A", "G", ask=100, bid=90, age_minutes=1),
        snap("B", "G", ask=999, bid=200, age_minutes=1),
        snap("C", "G", ask=999, bid=300, age_minutes=200),           # past the cap
        snap("D", "G", ask=999, bid=300, age_minutes=400),           # past the cap
        snap("E", "G", ask=999, bid=300, age_minutes=400, system="S2"),  # out of scope
        snap("F", "G", ask=0, bid=0, age_minutes=400),               # unpriced
    ]
    with caplog.at_level(logging.INFO, logger="utils.tour_solver"):
        out = solve_tour(snapshot, SHIP, cons(120), MODEL)

    assert out["feasible"]
    reported = [r.getMessage() for r in caplog.records if "staleness backstop" in r.getMessage()]
    assert len(reported) == 1, f"expected one backstop report, got {reported}"
    counts = re.search(r"dropped (\d+) of (\d+)", reported[0])
    assert counts, f"expected a 'dropped N of M' count: {reported[0]}"
    assert counts.groups() == ("2", "4"), (
        f"expected 2 stale rows out of the 4 in scope, got {counts.groups()}: {reported[0]}")


def test_backstop_stays_silent_when_it_drops_nothing(caplog):
    snapshot = [
        snap("A", "G", ask=100, bid=90, age_minutes=1),
        snap("B", "G", ask=999, bid=200, age_minutes=100),  # inside the cap
    ]
    with caplog.at_level(logging.INFO, logger="utils.tour_solver"):
        out = solve_tour(snapshot, SHIP, cons(120), MODEL)

    assert out["feasible"]
    assert not [r for r in caplog.records if "staleness backstop" in r.getMessage()]

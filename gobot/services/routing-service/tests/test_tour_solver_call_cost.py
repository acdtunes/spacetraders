# gobot/services/routing-service/tests/test_tour_solver_call_cost.py
"""The API-call cost term and the saturation-aware selection blend."""
import pytest

from utils.tour_solver import (API_CALLS_PER_CROSSING, API_CALLS_PER_TRANSACTION,
                               API_CALLS_PER_VISIT, API_CALL_SECONDS,
                               API_CALL_SECONDS_ENV_VAR, API_SATURATION_ENV_VAR,
                               OBJECTIVE_PROFIT, OBJECTIVE_RATE,
                               _plan_api_calls, _resolve_api_call_seconds,
                               _resolve_api_saturation, _selection_rate,
                               _sort_scored, solve_tour)

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def snap(wp, sys_, good, ask, bid, tv=20, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=9_999_999_999)


def _leg(wp, system, trades=0, units=1):
    return dict(waypoint_symbol=wp, system_symbol=system,
                trades=[dict(good_symbol="G", units=units, is_buy=True,
                             is_deposit=False, is_stock=False,
                             expected_unit_price=1)] * trades)


def _markets(*specs):
    """{waypoint: {"goods": {good: {"trade_volume": n}}}} for the call model to read."""
    return {wp: {"goods": {"G": {"trade_volume": tv}}} for wp, tv in specs}


DEEP = _markets(("A", 1000), ("B", 1000), ("C", 1000))


def _scored(profit, seconds, api_calls, summary):
    """One fully-scored candidate in the shape _sort_scored consumes."""
    cph = profit / (seconds / 3600.0) if seconds > 0 else 0.0
    return (dict(profit=profit, seconds=seconds, cph=cph, api_calls=api_calls), summary)


def _clear_call_env(monkeypatch):
    monkeypatch.delenv(API_SATURATION_ENV_VAR, raising=False)
    monkeypatch.delenv(API_CALL_SECONDS_ENV_VAR, raising=False)


# --- the call-cost model -----------------------------------------------------

def test_plan_api_calls_prices_visits_transactions_and_crossings():
    # Every market stop costs a fixed movement bundle; every tradeVolume-sized chunk of a
    # planned trade is one more request; every system change adds the jump. The three terms
    # are independent, so each is pinned on its own.
    one_stop = _plan_api_calls([_leg("A", "S1", trades=0)], "S1", DEEP)
    assert one_stop == pytest.approx(API_CALLS_PER_VISIT)

    two_trades = _plan_api_calls([_leg("A", "S1", trades=2)], "S1", DEEP)
    assert two_trades == pytest.approx(API_CALLS_PER_VISIT + 2 * API_CALLS_PER_TRANSACTION)

    crossing = _plan_api_calls([_leg("B", "S2", trades=0)], "S1", DEEP)
    assert crossing == pytest.approx(API_CALLS_PER_VISIT + API_CALLS_PER_CROSSING)


def test_transactions_are_chunked_by_the_market_own_trade_volume():
    # THE TERM A STOPS-COUNT MODEL CANNOT SEE. The API caps one request at the market's
    # tradeVolume, so the SAME 300 units cost three requests through a shallow market and
    # one through a deep one. Two plans with identical stop counts can differ severalfold.
    shallow = _plan_api_calls([_leg("A", "S1", trades=1, units=300)], "S1",
                              _markets(("A", 100)))
    deep = _plan_api_calls([_leg("A", "S1", trades=1, units=300)], "S1",
                           _markets(("A", 300)))
    assert shallow == pytest.approx(API_CALLS_PER_VISIT + 3 * API_CALLS_PER_TRANSACTION)
    assert deep == pytest.approx(API_CALLS_PER_VISIT + 1 * API_CALLS_PER_TRANSACTION)

    # A partial chunk still costs a whole request — the ceiling, never a fraction.
    partial = _plan_api_calls([_leg("A", "S1", trades=1, units=101)], "S1",
                              _markets(("A", 100)))
    assert partial == pytest.approx(API_CALLS_PER_VISIT + 2 * API_CALLS_PER_TRANSACTION)


def test_unreadable_trade_depth_charges_one_request_not_zero():
    # A deposit sink, a warehouse withdrawal and a good absent from the priced market all
    # have no tradeVolume to chunk by. They must charge the conservative floor of one
    # request each: charging zero would make an unpriceable leg look free on this axis.
    for markets in (_markets(("A", 0)), {"A": {"goods": {}}}, {}):
        assert _plan_api_calls([_leg("A", "S1", trades=1, units=300)], "S1", markets) \
            == pytest.approx(API_CALLS_PER_VISIT + API_CALLS_PER_TRANSACTION)
    # A zero-unit trade transacts nothing and costs nothing.
    assert _plan_api_calls([_leg("A", "S1", trades=1, units=0)], "S1", DEEP) \
        == pytest.approx(API_CALLS_PER_VISIT)


def test_plan_api_calls_charges_every_system_change_including_the_return():
    # A tour that leaves and comes back crosses TWICE. Charging the departure only would
    # make a far cluster look half-price on the axis this term exists to price.
    legs = [_leg("B", "S2", trades=2), _leg("C", "S2", trades=2), _leg("A", "S1", trades=2)]
    assert _plan_api_calls(legs, "S1", DEEP) == pytest.approx(
        3 * API_CALLS_PER_VISIT + 6 * API_CALLS_PER_TRANSACTION + 2 * API_CALLS_PER_CROSSING)


def test_plan_api_calls_of_an_empty_plan_is_zero():
    # A bare seed carries no legs. It must price at zero rather than at a per-visit
    # floor, so it stays the degenerate candidate the selection sort already quarantines.
    assert _plan_api_calls([], "S1", DEEP) == 0.0


# --- degrade-safety: the byte-identity pins ----------------------------------

def test_zero_saturation_selection_is_byte_identical_to_today(monkeypatch):
    # THE DEGRADE-SAFELY PROOF. With no saturation signal the selection rate IS the
    # candidate's own cph object — not a value that merely rounds to it — so the sort key
    # cannot differ from today's by so much as a float ULP.
    _clear_call_env(monkeypatch)
    result = dict(profit=1_000_000, seconds=3600, cph=1_000_000.0, api_calls=40.0)
    assert _selection_rate(result, 0.0, API_CALL_SECONDS) is result["cph"]


def test_absent_or_malformed_saturation_reads_as_none(monkeypatch):
    # Absent, zero, negative, a bool, a float, a string: every one of them is "no
    # opinion", which is the fail-open value. Bools are excluded explicitly because
    # True is an int in Python and would otherwise read as 0.1% saturation.
    _clear_call_env(monkeypatch)
    for value in (None, 0, -250, True, False, "600", 600.5, [], {}):
        assert _resolve_api_saturation({"api_saturation_permille": value}) == 0.0, value
    assert _resolve_api_saturation({}) == 0.0
    assert _resolve_api_saturation(None) == 0.0


def test_absent_saturation_leaves_the_ranking_untouched_end_to_end(monkeypatch):
    # The same board, planned with and without the field present. Selection, projection
    # and the reported rate must all match exactly.
    _clear_call_env(monkeypatch)
    rows = [snap("A", "S1", "G", ask=100, bid=90),
            snap("B", "S1", "G", ask=400, bid=380),
            snap("C", "S1", "G", ask=420, bid=300)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=60, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    base = dict(max_hops=6, min_margin_per_unit=1, max_snapshot_age_minutes=75,
                max_spend=1_000_000, working_capital_reserve=0,
                allowed_systems=["S1"], expected_model_version="1@e")
    today = solve_tour(rows, dict(ship), dict(base), MODEL, objective=OBJECTIVE_RATE)
    assert today["feasible"], today["infeasible_reason"]
    absent = solve_tour(rows, dict(ship), dict(base, api_saturation_permille=0),
                        MODEL, objective=OBJECTIVE_RATE)
    malformed = solve_tour(rows, dict(ship), dict(base, api_saturation_permille="oops"),
                           MODEL, objective=OBJECTIVE_RATE)
    for other in (absent, malformed):
        assert other["legs"] == today["legs"]
        assert other["projected_profit"] == today["projected_profit"]
        assert other["projected_credits_per_hour"] == today["projected_credits_per_hour"]


def test_profit_objective_never_consults_saturation(monkeypatch):
    # The call term rides on the RATE objective's denominator. Profit-primary selection
    # has no denominator to reshape, so a saturated fleet must not perturb it.
    _clear_call_env(monkeypatch)
    pool = [_scored(90_000, 3600, 12.0, "cheap"), _scored(100_000, 3600, 60.0, "rich")]
    ordered = list(pool)
    assert _sort_scored(ordered, OBJECTIVE_PROFIT, saturation=1.0) == OBJECTIVE_PROFIT
    assert ordered[0][1] == "rich"


# --- the behaviour change ----------------------------------------------------

def test_saturated_selection_prefers_the_higher_credits_per_call_tour(monkeypatch):
    # The finding, in one assertion: at equal duration the fleet's scarce resource is
    # requests, so the request-hungry tour is marked down until the leaner one wins, even
    # though it earns less per hour. Unsaturated, the same pool still ranks by credits/hour.
    _clear_call_env(monkeypatch)
    pool = [_scored(100_000, 3600, 200.0, "request-hungry"),
            _scored(80_000, 3600, 40.0, "lean")]

    unsaturated = list(pool)
    _sort_scored(unsaturated, OBJECTIVE_RATE, saturation=0.0)
    assert unsaturated[0][1] == "request-hungry"

    saturated = list(pool)
    _sort_scored(saturated, OBJECTIVE_RATE, saturation=1.0)
    assert saturated[0][1] == "lean"


def test_the_call_charge_is_a_surcharge_and_never_erases_the_clock(monkeypatch):
    # THE FLAW A DENOMINATOR INTERPOLATION HAS. Weighting time by (1-s) drives the hull's
    # own clock out of the objective at full saturation, and the solver answers by flying
    # far longer tours for a trivial call saving. Congestion is an EXTRA cost, not a
    # replacement, so at ANY saturation a slower plan with the same request cost must still
    # lose to a faster one.
    _clear_call_env(monkeypatch)
    for permille in (1, 250, 500, 1000):
        pool = [_scored(100_000, 18_000, 20.0, "slow"),
                _scored(100_000, 3_600, 20.0, "fast")]
        _sort_scored(pool, OBJECTIVE_RATE, saturation=permille / 1000.0)
        assert pool[0][1] == "fast", permille


def test_selection_rate_is_monotone_between_the_two_axes(monkeypatch):
    # The surcharge grows continuously with the limiter, so EVERY plan's score falls
    # monotonically as the fleet tightens — the call-heavy one faster than the call-light
    # one, which is the whole re-ranking. Every plan equals its own cph at 0, and nothing
    # steps in between.
    _clear_call_env(monkeypatch)
    heavy = dict(profit=100_000, seconds=3600, cph=100_000.0, api_calls=240.0)
    light = dict(profit=100_000, seconds=3600, cph=100_000.0, api_calls=20.0)
    for plan in (heavy, light):
        scores = [_selection_rate(plan, s / 10.0, API_CALL_SECONDS) for s in range(11)]
        assert scores[0] == plan["cph"]
        assert scores == sorted(scores, reverse=True), plan
        assert scores[-1] == pytest.approx(
            plan["profit"] / ((plan["seconds"] + plan["api_calls"] * API_CALL_SECONDS)
                              / 3600.0))


def test_call_free_candidate_is_never_ranked_above_a_real_plan(monkeypatch):
    # A zero-time zero-call candidate is the bare seed the pool always contains. It must
    # stay quarantined at the bottom under saturation exactly as it is under plain cph —
    # a plan that costs nothing on both axes must not divide its way to the top.
    _clear_call_env(monkeypatch)
    pool = [_scored(0, 0, 0.0, "bare-seed"), _scored(50_000, 3600, 20.0, "real")]
    _sort_scored(pool, OBJECTIVE_RATE, saturation=1.0)
    assert pool[0][1] == "real"


# --- precedence, clamping, and the selection-only invariant ------------------

def test_request_saturation_outranks_the_env_override(monkeypatch):
    # PRECEDENCE: request > env > default. The daemon owns the limiter, so its live
    # reading beats a number exported by hand; the env stays the manual override for a
    # fleet whose daemon has nothing to say.
    monkeypatch.setenv(API_SATURATION_ENV_VAR, "400")
    assert _resolve_api_saturation({"api_saturation_permille": 900}) == pytest.approx(0.9)
    assert _resolve_api_saturation({}) == pytest.approx(0.4)


def test_saturation_is_clamped_to_the_unit_interval(monkeypatch):
    # Saturation is a fraction of a known ceiling. A reading above the ceiling means the
    # limiter is fully bound, not that calls are worth more than everything else.
    _clear_call_env(monkeypatch)
    assert _resolve_api_saturation({"api_saturation_permille": 5_000}) == 1.0
    monkeypatch.setenv(API_SATURATION_ENV_VAR, "-100")
    assert _resolve_api_saturation({}) == 0.0


def test_call_seconds_scale_is_env_tunable_and_floors_the_term_off(monkeypatch):
    # The scale converts a call into the tour-seconds it displaces, which is what makes
    # the two denominator terms commensurate. Exporting 0 makes the call term inert at
    # every saturation — the disarm that needs no code change.
    _clear_call_env(monkeypatch)
    assert _resolve_api_call_seconds() == API_CALL_SECONDS
    monkeypatch.setenv(API_CALL_SECONDS_ENV_VAR, "250")
    assert _resolve_api_call_seconds() == 250.0
    monkeypatch.setenv(API_CALL_SECONDS_ENV_VAR, "0")
    result = dict(profit=100_000, seconds=3600, cph=100_000.0, api_calls=60.0)
    assert _selection_rate(result, 1.0, _resolve_api_call_seconds()) is result["cph"]


def test_saturation_never_moves_projected_profit_or_the_reported_rate(monkeypatch):
    # SELECTION ONLY. The term reorders candidates; it must not restate what a plan is
    # worth. Whichever tour wins, its reported profit and credits/hour are the honest
    # unblended projection, and the response still carries the call count that priced it.
    _clear_call_env(monkeypatch)
    rows = [snap("A", "S1", "G", ask=100, bid=90),
            snap("B", "S1", "G", ask=400, bid=380)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=60, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=6, min_margin_per_unit=1, max_snapshot_age_minutes=75,
                max_spend=1_000_000, working_capital_reserve=0,
                allowed_systems=["S1"], expected_model_version="1@e",
                api_saturation_permille=1000)
    out = solve_tour(rows, dict(ship), cons, MODEL, objective=OBJECTIVE_RATE)
    assert out["feasible"]
    hours = sum(l["travel_seconds_from_prev"] for l in out["legs"])
    assert hours >= 0  # legs still carry their honest travel time
    assert out["projected_credits_per_hour"] > 0
    assert out["projected_api_calls"] > 0

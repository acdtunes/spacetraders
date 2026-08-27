# gobot/services/routing-service/tests/test_tour_solver_stage1_depth.py
"""Market depth in STAGE-1 candidate generation: the congestion charge on the packing bound.

The API caps one buy or sell at the market's own tradeVolume, so the requests a plan spends
moving a load are ceil(units / tradeVolume) per side. Stage 2 already prices that exactly.
Stage 1 could not see it at all, so a call-cheap candidate the beam or the OR-Tools sequencer
pruned was unreachable by any amount of stage-2 reordering. These tests pin the charge that
makes the packing bound pay for the requests its own units imply, the saturation gate that
keeps it inert while the limiter has headroom, and the byte-identity that gate guarantees.
"""
import pytest

from utils import tour_solver as ts
from utils.tour_solver import (API_SATURATION_ENV_VAR, STAGE1_CALL_CREDITS,
                               STAGE1_CALL_CREDITS_ENV_VAR, _pair_gain, _prune_nodes,
                               _request_chunks, _resolve_stage1_call_charge,
                               beam_sequences, ortools_sequences, solve_tour)

MODEL = {"fit_version": 1, "era": "e",
         "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                     "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def _row(wp, sys_, good, ask, bid, tv):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply="LIMITED", activity="WEAK",
                observed_at_unix=9_999_999_999)


def _ship(wp, system="S1", hold=220):
    return dict(ship_symbol="HULL", current_waypoint=wp, current_system=system,
                hold_capacity=hold, fuel_current=1000, fuel_capacity=1000,
                engine_speed=30, cargo=[])


def _cons(allowed=("S1",), **over):
    # max_hops 2 keeps every board below a source and ONE sink, so a candidate's rank is its
    # own pair's bound rather than an accumulation over a chain of them — the quantity these
    # tests are about.
    base = dict(max_hops=2, max_spend=100_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, max_snapshot_age_minutes=75,
                expected_model_version="1@e", allowed_systems=list(allowed))
    base.update(over)
    return base


def _clear_env(monkeypatch):
    monkeypatch.delenv(API_SATURATION_ENV_VAR, raising=False)
    monkeypatch.delenv(STAGE1_CALL_CREDITS_ENV_VAR, raising=False)


# --- the charge's arithmetic -------------------------------------------------

def test_request_chunks_mirror_the_stage_two_transaction_rule():
    # ONE VOCABULARY. Stage 2 charges ceil(units / tradeVolume) requests per trade
    # (_transaction_chunks); stage 1 must charge the same quantity off its own units, or the
    # two stages would disagree about what a market costs.
    assert _request_chunks(300, 100) == 3
    assert _request_chunks(300, 300) == 1
    assert _request_chunks(101, 100) == 2      # a partial chunk is still a whole request
    assert _request_chunks(0, 100) == 0        # nothing moved costs nothing


def test_unreadable_depth_charges_the_one_request_floor():
    # sp-c6rx2's conservative floor, reproduced: a good with no readable tradeVolume is
    # charged ONE request, never zero (which would make an unpriceable market look free) and
    # never a crash on the divide.
    assert _request_chunks(300, 0) == 1
    assert _request_chunks(300, None) == 1


def test_pair_gain_charges_the_requests_its_packed_units_imply():
    # A hold filled through a SHALLOW market costs several requests per good where a deep one
    # costs one, and the packing bound is where stage 1 can first see it.
    deep = {"A": {"system": "S1", "goods": {"G": _row("A", "S1", "G", 100, 0, 300)}},
            "B": {"system": "S1", "goods": {"G": _row("B", "S1", "G", 0, 600, 300)}}}
    raw = _pair_gain("A", "B", deep, 220, {}, {}, max_planned_tranches=2)
    assert raw == 220 * 500

    # 220 units through a tradeVolume-300 market on both sides is 1 + 1 requests.
    charged = _pair_gain("A", "B", deep, 220, {}, {}, max_planned_tranches=2,
                         call_charge=1000.0)
    assert charged == pytest.approx(raw - 2 * 1000.0)


def test_a_shallow_pair_pays_more_than_a_deep_pair_moving_the_same_load():
    # THE 21x SPREAD, at candidate-scoring time. Eleven tradeVolume-10 goods fill the same
    # 220-unit hold as one tradeVolume-300 good, for the same credits — and cost 33 requests
    # against 2. Only the charged bound can tell them apart.
    goods_src = {f"G{i}": _row("A", "S1", f"G{i}", 100, 0, 10) for i in range(11)}
    goods_sink = {f"G{i}": _row("B", "S1", f"G{i}", 0, 600, 10) for i in range(11)}
    shallow = {"A": {"system": "S1", "goods": goods_src},
               "B": {"system": "S1", "goods": goods_sink}}
    deep = {"A": {"system": "S1", "goods": {"G": _row("A", "S1", "G", 100, 0, 300)}},
            "B": {"system": "S1", "goods": {"G": _row("B", "S1", "G", 0, 600, 300)}}}

    assert _pair_gain("A", "B", shallow, 220, {}, {}, max_planned_tranches=2) == 220 * 500
    assert _pair_gain("A", "B", deep, 220, {}, {}, max_planned_tranches=2) == 220 * 500

    charged_shallow = _pair_gain("A", "B", shallow, 220, {}, {}, max_planned_tranches=2,
                                 call_charge=100.0)
    charged_deep = _pair_gain("A", "B", deep, 220, {}, {}, max_planned_tranches=2,
                              call_charge=100.0)
    # 11 goods x 20 units, two requests a side each; against one good's single request a side.
    assert charged_shallow == pytest.approx(220 * 500 - 44 * 100.0)
    assert charged_deep == pytest.approx(220 * 500 - 2 * 100.0)
    assert charged_deep > charged_shallow


# --- the saturation gate -----------------------------------------------------

def test_the_charge_is_the_measured_saturation_times_the_armed_credit_price(monkeypatch):
    # ONE saturation notion, sp-c6rx2's: the reading the daemon measures scales the charge
    # linearly, so a fleet with headroom pays nothing and a pinned one pays the full price.
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "1000")
    assert _resolve_stage1_call_charge({"api_saturation_permille": 1000}) == pytest.approx(1000.0)
    assert _resolve_stage1_call_charge({"api_saturation_permille": 500}) == pytest.approx(500.0)


def test_the_charge_is_inert_without_a_saturation_reading(monkeypatch):
    # Absent, zero, negative, a bool, a float and a string all read as NO OPINION — the same
    # fail-open surface sp-c6rx2 plumbed, and every one of them must charge exactly nothing.
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "1000")
    for cons in ({}, {"api_saturation_permille": 0}, {"api_saturation_permille": -5},
                 {"api_saturation_permille": True}, {"api_saturation_permille": 0.8},
                 {"api_saturation_permille": "900"}, None):
        assert _resolve_stage1_call_charge(cons) == 0


def test_the_env_saturation_override_reaches_stage_one_too(monkeypatch):
    # The manual override underneath the daemon's measurement is what an operator has when
    # the daemon is silent, and it has to govern BOTH stages or the two would disagree about
    # how loaded the fleet is inside a single solve.
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "1000")
    monkeypatch.setenv(API_SATURATION_ENV_VAR, "400")
    assert _resolve_stage1_call_charge({}) == pytest.approx(400.0)


def test_the_fitted_price_is_active_and_one_export_disarms_it(monkeypatch):
    # The replay-fitted price is LIVE with no env set — absent means charged, not off. The
    # disarm is a single export, and it has to reach zero exactly: a floor that merely got
    # close would leave a term an operator believes they have switched off still ordering
    # candidates.
    _clear_env(monkeypatch)
    assert _resolve_stage1_call_charge({"api_saturation_permille": 1000}) == pytest.approx(
        STAGE1_CALL_CREDITS)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "0")
    assert _resolve_stage1_call_charge({"api_saturation_permille": 1000}) == 0


# --- byte-identity: the degrade-safely gate ----------------------------------

def _crowded_board():
    """A rich DEEP pair buried under a wide field of shallow pairs the bound over-values.

    Each shallow pair fills the hold for slightly more optimistic credits than the deep pair
    does, so an uncharged stage 1 ranks the whole shallow field above the deep one; each also
    needs 22 requests where the deep pair needs 2."""
    snapshot, waypoints = [], []

    def add(wp, x, y, goods):
        waypoints.append(dict(symbol=wp, x=x, y=y))
        for good, ask, bid, tv in goods:
            snapshot.append(_row(wp, "S1", good, ask, bid, tv))

    add("SRC", 0, 0, [("DEEP", 100, 0, 300)]
        + [(f"THIN{i}", 100, 0, 20) for i in range(11)])
    add("DEEPSINK", 5, 0, [("DEEP", 0, 700, 300)])
    for k in range(12):
        add(f"THINSINK{k:02d}", 10 + k, 4,
            [(f"THIN{i}", 0, 720 + k, 20) for i in range(11)])
    return snapshot, waypoints


def test_stage_one_is_byte_identical_without_a_saturation_reading(monkeypatch):
    """THE DEGRADE-SAFELY GATE.

    A fleet with headroom, a window too thin to read, a caller that predates the field, or a
    malformed reading must all plan EXACTLY as they do today — same candidate lists out of
    both sequencers, same chosen route, same projection. The charge is a strict addition
    behind a reading that is zero in every one of those cases."""
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "5000")
    snapshot, waypoints = _crowded_board()
    ship, markets = _ship("SRC"), ts._build_markets(snapshot)
    travel = ts._make_travel_fn(_cons(), markets, ship, waypoints)

    baseline_beam = beam_sequences(markets, dict(ship), _cons(), travel)
    baseline_ortools = ortools_sequences(markets, dict(ship), _cons(), travel)
    baseline_plan = solve_tour(snapshot, dict(ship), _cons(), MODEL, waypoints=waypoints,
                               sequencer="ortools")

    for cons in (_cons(), _cons(api_saturation_permille=0),
                 _cons(api_saturation_permille=-1),
                 _cons(api_saturation_permille="1000"),
                 _cons(api_saturation_permille=1.0)):
        assert beam_sequences(markets, dict(ship), cons, travel) == baseline_beam
        assert ortools_sequences(markets, dict(ship), cons, travel) == baseline_ortools
        assert solve_tour(snapshot, dict(ship), dict(cons), MODEL, waypoints=waypoints,
                          sequencer="ortools") == baseline_plan


def test_a_board_with_no_readable_depth_charges_the_floor_and_never_divides(monkeypatch):
    # A row whose tradeVolume is absent or zero is the case that could crash the charge or,
    # worse, price an unpriceable market at nothing. It charges the floor — one request a
    # side — and the whole solve runs to its ordinary verdict.
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "500")
    blind = {"A": {"system": "S1", "goods": {"G": _row("A", "S1", "G", 100, 0, 0)}},
             "B": {"system": "S1", "goods": {"G": _row("B", "S1", "G", 0, 600, 0)}}}
    raw = _pair_gain("A", "B", blind, 220, {}, {}, max_planned_tranches=2)
    charged = _pair_gain("A", "B", blind, 220, {}, {}, max_planned_tranches=2,
                         call_charge=100.0)
    assert charged == pytest.approx(raw - 2 * 100.0)

    snapshot = [_row("A", "S1", "G", 100, 0, 0), _row("B", "S1", "G", 0, 600, 0)]
    waypoints = [dict(symbol="A", x=0, y=0), dict(symbol="B", x=5, y=0)]
    plan = solve_tour(snapshot, _ship("A"), _cons(api_saturation_permille=1000), MODEL,
                      waypoints=waypoints, sequencer="ortools")
    blind_plan = solve_tour(snapshot, _ship("A"), _cons(), MODEL, waypoints=waypoints,
                            sequencer="ortools")
    assert (plan["feasible"], plan["infeasible_reason"]) == (
        blind_plan["feasible"], blind_plan["infeasible_reason"])


# --- the mechanism the bead exists for ---------------------------------------

def test_the_charge_lifts_a_deep_candidate_the_beam_had_ranked_below_the_shallow_field(monkeypatch):
    _clear_env(monkeypatch)
    snapshot, waypoints = _crowded_board()
    ship, markets = _ship("SRC"), ts._build_markets(snapshot)
    travel = ts._make_travel_fn(_cons(), markets, ship, waypoints)

    uncharged = beam_sequences(markets, dict(ship), _cons(), travel)
    charged = beam_sequences(markets, dict(ship), _cons(), travel, call_charge=5000.0)
    deep = ("SRC", "DEEPSINK")
    assert uncharged.index(deep) > charged.index(deep)
    assert charged.index(deep) == 0


def test_the_charge_reaches_the_ortools_node_prune_and_emission_order(monkeypatch):
    # STAGE 1 PRUNES BEFORE STAGE 2 EVER SEES A CANDIDATE. The OR-Tools path cuts twice —
    # the per-model node cap and the emission ranking — and the charge has to reach both or a
    # deep market that lost the node cut is unrecoverable.
    _clear_env(monkeypatch)
    snapshot, waypoints = _crowded_board()
    ship, markets = _ship("SRC"), ts._build_markets(snapshot)
    travel = ts._make_travel_fn(_cons(), markets, ship, waypoints)

    kept = _prune_nodes(markets, dict(ship), _cons(), {}, {}, ortools_max_nodes=3)
    kept_charged = _prune_nodes(markets, dict(ship), _cons(), {}, {}, ortools_max_nodes=3,
                                call_charge=5000.0)
    assert "DEEPSINK" not in kept
    assert "DEEPSINK" in kept_charged

    emitted = ortools_sequences(markets, dict(ship), _cons(), travel)
    emitted_charged = ortools_sequences(markets, dict(ship), _cons(), travel,
                                        call_charge=5000.0)
    assert emitted_charged[0] == ("SRC", "DEEPSINK")
    assert emitted[0] != ("SRC", "DEEPSINK")


def test_a_pinned_limiter_selects_the_call_cheap_route_the_shortlist_had_cut(monkeypatch):
    """THE WHOLE POINT. With the shortlist cut where production cuts it, the deep candidate
    never reaches stage 2 under a depth-blind stage 1 — so the stage-2 call surcharge, which
    can only REORDER what it is given, cannot recover it. Charging stage 1 does."""
    _clear_env(monkeypatch)
    monkeypatch.setenv(STAGE1_CALL_CREDITS_ENV_VAR, "5000")
    monkeypatch.setenv("TOUR_SOLVER_FULL_SCORE_TOP_N", "10")
    snapshot, waypoints = _crowded_board()

    blind = solve_tour(snapshot, _ship("SRC"), _cons(), MODEL, waypoints=waypoints,
                       objective="rate")
    charged = solve_tour(snapshot, _ship("SRC"),
                         _cons(api_saturation_permille=1000), MODEL,
                         waypoints=waypoints, objective="rate")
    blind_route = tuple(l["waypoint_symbol"] for l in blind["legs"])
    charged_route = tuple(l["waypoint_symbol"] for l in charged["legs"])
    assert "DEEPSINK" not in blind_route
    assert charged_route == ("SRC", "DEEPSINK")
    assert charged["projected_api_calls"] < blind["projected_api_calls"]


def test_the_charge_prices_transactions_only_never_the_stop_itself(monkeypatch):
    # DELIBERATE SCOPE. Visits and crossings are already priced in seconds by stage 1's
    # travel terms and exactly by stage 2's call model; only the transaction term carries
    # market depth, and it is the only one stage 1 was blind to. Charging stops here would
    # add a second, unmeasured pressure on tour LENGTH — the shape sp-c6rx2 found ruinous.
    _clear_env(monkeypatch)
    board = {"A": {"system": "S1", "goods": {"G": _row("A", "S1", "G", 100, 0, 300)}},
             "B": {"system": "S2", "goods": {"G": _row("B", "S2", "G", 0, 600, 300)}}}
    same_system = {"A": board["A"],
                   "B": {"system": "S1", "goods": board["B"]["goods"]}}
    crossing = _pair_gain("A", "B", board, 220, {}, {}, max_planned_tranches=2,
                          call_charge=1000.0)
    local = _pair_gain("A", "B", same_system, 220, {}, {}, max_planned_tranches=2,
                       call_charge=1000.0)
    assert crossing == local

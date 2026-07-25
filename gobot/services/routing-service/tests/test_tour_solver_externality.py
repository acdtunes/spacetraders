# gobot/services/routing-service/tests/test_tour_solver_externality.py
"""Recovery-externality pricing on the tour path.

Two levels, deliberately:

  * the pure per-unit charge (a standalone pricing algorithm with a stable
    signature — the one place a direct unit test is warranted), and
  * the observable PLAN a solve produces, through solve_tour — the driving
    port. Preference-order, eligibility, deposit exemption and the fail-open
    degradation are only meaningful as plan outcomes, so they are asserted
    there and nowhere else.
"""
import pytest

from tests.tour_harness import _prepared
from utils.tour_solver import externality_cost_per_unit, score_sequence, solve_tour

# The era-07-19 fit, verbatim (activity -> {half_life_minutes, n_series}). Kept in step
# with the checked-in artifact so the charges these tests reason about are the ones
# production computes. STRONG is the one tier still under the fitted-series floor.
RECOVERY = {
    "":           {"half_life_minutes": 2848.9, "n_series": 51},
    "GROWING":    {"half_life_minutes": 1942.1, "n_series": 49},
    "RESTRICTED": {"half_life_minutes": 2366.5, "n_series": 754},
    "STRONG":     {"half_life_minutes": 1577.7, "n_series": 4},
    "WEAK":       {"half_life_minutes": 1599.2, "n_series": 416},
}

_FRESH = 9_999_999_999


def snap(wp, sys_, good, ask, bid, tv=20, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=_FRESH)


def model(impact, recovery=RECOVERY):
    return {"fit_version": 1, "era": "e", "impact": impact, "recovery": recovery}


def cons(weight=None, min_margin=1, **kw):
    c = dict(max_hops=4, max_spend=5_000_000, min_margin_per_unit=min_margin,
             working_capital_reserve=0, allowed_systems=["S1"],
             max_snapshot_age_minutes=75, expected_model_version="1@e")
    if weight is not None:
        c["externality_weight"] = weight
    c.update(kw)
    return c


def ship(hold, wp="A", cargo=()):
    return dict(ship_symbol="H", current_waypoint=wp, current_system="S1",
                hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=list(cargo))


def sells(out):
    """(waypoint, units) per sell tranche of a plan, in leg order."""
    return [(leg["waypoint_symbol"], t["units"])
            for leg in out["legs"] for t in leg["trades"] if not t["is_buy"]]


# --------------------------------------------------------------- the per-unit charge

def test_zero_weight_charges_nothing():
    """weight=0 is the revert path and the regression fence for the whole change."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 0.0, RECOVERY) == 0.0


def test_longer_half_life_costs_more():
    """RESTRICTED (2366min) burdens the fleet longer than WEAK (1599min)."""
    slow = externality_cost_per_unit("RESTRICTED", 100, 50, 1000, 1.0, RECOVERY)
    fast = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY)
    assert slow > fast > 0


def test_deeper_tranche_costs_more_per_unit():
    """Crush scales with how far past trade_volume the tranche reaches."""
    shallow = externality_cost_per_unit("WEAK", 50, 50, 1000, 1.0, RECOVERY)
    deep = externality_cost_per_unit("WEAK", 200, 50, 1000, 1.0, RECOVERY)
    assert deep > shallow > 0


def test_unknown_activity_falls_back_to_pooled_not_free():
    """An unmapped activity prices on the pooled half-life — never a crash, never free."""
    pooled = externality_cost_per_unit("", 100, 50, 1000, 1.0, RECOVERY)
    assert externality_cost_per_unit("NOT_A_TIER", 100, 50, 1000, 1.0, RECOVERY) == pooled


def test_thinly_fitted_tier_falls_back_to_the_pooled_half_life():
    """PLAYBOOK §12: a tier fitted on n_series < 5 is not a trustworthy prior, so it
    prices on the pooled untagged half-life instead of its own thin fit."""
    thin = {"": {"half_life_minutes": 1000.0, "n_series": 9},
            "STRONG": {"half_life_minutes": 10.0, "n_series": 1}}
    pooled = externality_cost_per_unit("", 100, 50, 1000, 1.0, thin)
    assert externality_cost_per_unit("STRONG", 100, 50, 1000, 1.0, thin) == pooled


def test_well_fitted_tier_uses_its_own_half_life():
    """The converse of the thin-tier fallback: a well-sampled tier keeps its own fit."""
    tbl = {"": {"half_life_minutes": 1000.0, "n_series": 9},
           "WEAK": {"half_life_minutes": 100.0, "n_series": 20}}
    pooled = externality_cost_per_unit("", 100, 50, 1000, 1.0, tbl)
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, tbl) < pooled


def test_missing_recovery_table_fails_open_to_zero():
    """Unreadable table degrades to today's behaviour (no charge), never an exception."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, None) == 0.0
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, {}) == 0.0


def test_zero_trade_volume_does_not_divide_by_zero():
    assert externality_cost_per_unit("WEAK", 100, 0, 1000, 1.0, RECOVERY) == 0.0


def test_uses_the_passed_fitted_decay_not_the_fallback():
    """A shallow-decay market (0.98 -> 2% crush) costs far less than a steep one
    (0.80 -> 20% crush): the caller's FITTED factor is honoured, not DEFAULT_SELL_DECAY."""
    shallow = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=0.98)
    steep = externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=0.80)
    assert steep > shallow * 5


def test_decay_of_one_means_no_crush_and_no_charge():
    """A market that does not move on a sale imposes no recovery burden."""
    assert externality_cost_per_unit("WEAK", 100, 50, 1000, 1.0, RECOVERY, sell_decay=1.0) == 0.0


# ------------------------------------------------------------------ observable plans
#
# The charge moves which pairing the allocator takes FIRST. It is deliberately
# invisible to projected profit (it prices an externality the hull does not pay), so
# it can only change a PLAN where the competing allocations earn the SAME profit —
# otherwise sequence selection, which ranks on profit, decides and the term is inert.
# Every plan-level test below is therefore built profit-invariant on purpose: the
# split is the only thing free to move, which is exactly what makes the assertion
# falsifiable rather than an accident of tie-breaking.

def split(out):
    """units sold per sink waypoint, summed across legs."""
    totals = {}
    for wp, units in sells(out):
        totals[wp] = totals.get(wp, 0) + units
    return totals


def _two_sink_board():
    """A laden hull at SLOW with 60u; two equal-bid sinks differing ONLY in recovery
    tier, each able to absorb 40u. 60u of cargo into 80u of depth means one sink takes
    40 and the other 20 — and revenue is 29,000 either way, so profit cannot decide it."""
    snapshot = [snap("SLOW", "S1", "G", ask=9_999, bid=500, tv=20, activity="RESTRICTED"),
                snap("FAST", "S1", "G", ask=9_999, bid=500, tv=20, activity="WEAK")]
    m = model({"LIMITED|RESTRICTED": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1},
               "LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1}})
    laden = ship(60, wp="SLOW", cargo=[dict(good_symbol="G", units=60)])
    return snapshot, laden, m


def test_externality_shifts_depth_onto_the_faster_recovering_sink():
    """The whole point. RESTRICTED recovers in ~2366min, WEAK in ~1599min; both quote
    500. Unpriced the hull dumps its bulk into the slower sink (it is standing on it);
    priced, the bulk moves to the sink the fleet gets back sooner — at identical
    projected profit, so nothing but the charge can have moved it."""
    snapshot, laden, m = _two_sink_board()

    unpriced = solve_tour(snapshot, laden, cons(weight=0.0), m)
    priced = solve_tour(snapshot, laden, cons(weight=1.0), m)

    assert split(unpriced) == {"SLOW": 40, "FAST": 20}
    assert split(priced) == {"SLOW": 20, "FAST": 40}
    assert priced["projected_profit"] == unpriced["projected_profit"]


def test_externality_never_gates_eligibility_only_preference():
    """RULINGS #4: the charge reorders preference, it does not tighten a spend gate.
    A lane whose RAW margin clears min_margin stays eligible even when the charge
    exceeds the margin outright — moving eligibility would silently tighten a guard."""
    # margin = 500-100 = 400 raw; the charge at weight 50 is far larger than 400.
    snapshot = [snap("A", "S1", "G", ask=100, bid=1, tv=20),
                snap("B", "S1", "G", ask=9_999, bid=500, tv=20, activity="RESTRICTED")]
    m = model({"LIMITED|RESTRICTED": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1},
               "LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1}})

    out = solve_tour(snapshot, ship(20), cons(weight=50.0, min_margin=400), m)

    assert out["feasible"], "raw margin 400 clears min_margin 400 — the charge must not veto it"
    assert ("B", 20) in sells(out)


def test_unreadable_recovery_table_degrades_to_todays_ordering():
    """Fail OPEN: this is an objective term, not a spend guard, so an unreadable
    recovery map must reproduce today's plan exactly — never a wilder one. Asserted
    on the board where the charge demonstrably DOES move the split, so 'identical to
    baseline' is a real claim and not a board too insensitive to show a difference."""
    snapshot, laden, m = _two_sink_board()

    baseline = solve_tour(snapshot, laden, cons(weight=0.0), m)
    no_table = solve_tour(snapshot, laden, cons(weight=1.0),
                          model(m["impact"], recovery={}))

    assert split(no_table) == split(baseline) == {"SLOW": 40, "FAST": 20}
    assert no_table["legs"] == baseline["legs"]


def test_charge_uses_the_same_fitted_decay_the_tranche_builder_applies(monkeypatch):
    """One resolved decay per market, not two.

    Both sinks recover on the SAME curve (WEAK) and quote 500; they differ only in
    FITTED sell-decay — STEEP collapses 40% a tranche, FLAT does not move on a sale.
    The ladder cap is pinned to ONE tranche per pool, so no second tranche exists
    anywhere and the decay cannot touch a price: its one route to the outcome is the
    charge. 60u of cargo into 40+40 of depth, revenue 30,000 either way.

    Fitted: STEEP is charged ~200cr/u and FLAT exactly 0, so the bulk moves to FLAT.
    Reading DEFAULT_SELL_DECAY (0.9) instead would charge both ~50cr/u — an exact tie
    that leaves the split where weight 0 put it. The move is the proof.
    """
    monkeypatch.setenv("TOUR_SOLVER_MAX_PLANNED_TRANCHES", "1")
    snapshot = [snap("STEEP", "S1", "G", ask=9_999, bid=500, tv=40, supply="HIGH"),
                snap("FLAT", "S1", "G", ask=9_999, bid=500, tv=40, supply="SCARCE")]
    m = model({"HIGH|WEAK": {"sell_decay_per_step": 0.6, "buy_growth_per_step": 1.1},
               "SCARCE|WEAK": {"sell_decay_per_step": 1.0, "buy_growth_per_step": 1.1}})
    laden = ship(60, wp="STEEP", cargo=[dict(good_symbol="G", units=60)])

    unpriced = solve_tour(snapshot, laden, cons(weight=0.0), m)
    priced = solve_tour(snapshot, laden, cons(weight=1.0), m)

    assert split(unpriced) == {"STEEP": 40, "FLAT": 20}
    assert split(priced) == {"FLAT": 40, "STEEP": 20}
    assert priced["projected_profit"] == unpriced["projected_profit"]


def test_deposit_pairings_are_exempt_from_the_externality():
    """A deposit is an inventory transfer, not a market sale: no crush, no charge —
    matching the existing `kind != 'deposit'` treatment.

    Asserted at the allocator rather than through solve_tour on purpose. A deposit
    can only WIN a pairing race the charge has re-ordered, but adding the deposit leg
    never raises projected profit, so sequence selection would always prefer the
    shorter market-only tour and the exemption would never surface in a plan. Fixing
    the sequence is what makes the exemption observable at all.
    """
    snapshot = [snap("A", "S1", "G", ask=100, bid=1, tv=60),
                snap("SINK", "S1", "G", ask=9_999, bid=500, tv=60, activity="RESTRICTED")]
    deposits = [dict(good_symbol="G", units_wanted=40, synthetic_bid=500,
                     storage_waypoint="HOME", storage_system="S1")]
    m = model({"LIMITED|RESTRICTED": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1},
               "LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1}})
    hull = ship(60)

    def allocate(weight):
        c = cons(weight=weight)
        markets, travel_fn, sinks, stock, absorb = _prepared(
            snapshot, hull, c, None, deposits, None, None)
        out = score_sequence(["A", "SINK", "HOME"], markets, hull, c, m, travel_fn,
                             sinks, absorb, stock)
        totals = {}
        for leg in out["legs"]:
            for t in leg["trades"]:
                if not t["is_buy"]:
                    totals[leg["waypoint_symbol"]] = (
                        totals.get(leg["waypoint_symbol"], 0) + t["units"])
        return totals

    # Unpriced the market sink wins the tie and swallows the hold; priced, only the
    # market sink is charged, so the uncharged deposit takes what it can absorb.
    assert allocate(0.0) == {"SINK": 60}
    assert allocate(1.0) == {"HOME": 40, "SINK": 20}

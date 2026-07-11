# gobot/services/routing-service/tests/test_tour_solver_stock.py
# C1 (sp-64je): planner-visible stock — the tour solver treats warehouse stock as a
# zero-ask-at-basis WITHDRAWAL source (the buy-side mirror of the sp-dchv deposit sink),
# so it draws factory output from stock at basis instead of buying our own output at the
# laddered market ask.
from utils.tour_solver import solve_tour

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def snap(wp, sys_, good, ask, bid, tv=40, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=9_999_999_999)


def cons(allowed=("S1",), max_hops=4):
    return dict(max_hops=max_hops, max_spend=5_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=list(allowed),
                max_snapshot_age_minutes=75, expected_model_version="1@e")


def _buys(out):
    return [t for l in out["legs"] for t in l["trades"] if t["is_buy"]]


# A warehouse stock source is withdrawn at basis and sold at a market sink; the buy
# leg is marked is_stock and priced at basis, and stock_value reports units*basis.
def test_stock_source_withdrawn_and_sold():
    snapshot = [snap("SINK", "S1", "CLOTHING", ask=9999, bid=2000, tv=40)]
    ship = dict(ship_symbol="H", current_waypoint="WH", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    stock = [dict(good_symbol="CLOTHING", units_available=40, unit_ask=500,
                  storage_waypoint="WH", storage_system="S1")]

    out = solve_tour(snapshot, ship, cons(), MODEL, stock_sources=stock)
    assert out["feasible"]

    stock_buys = [t for t in _buys(out) if t["is_stock"]]
    assert len(stock_buys) == 1, out["legs"]
    b = stock_buys[0]
    assert b["good_symbol"] == "CLOTHING" and b["units"] == 40
    assert b["expected_unit_price"] == 500  # basis, not a market ask
    assert out["stock_value"] == 40 * 500


# When the same good is available both as cheap stock (basis 500) and at a pricier
# market source (ask 1000), a single buy+sell tour WITHDRAWS from stock — the
# export-ask-subsidy inversion (buying our own output at market) is structurally
# beaten on margin (max_hops=2 isolates the single acquisition choice).
def test_stock_preferred_over_pricier_market_buy():
    snapshot = [
        snap("MKT", "S1", "CLOTHING", ask=1000, bid=0, tv=40),      # market source (pricier)
        snap("SINK", "S1", "CLOTHING", ask=9999, bid=2000, tv=40),  # sink
    ]
    ship = dict(ship_symbol="H", current_waypoint="WH", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    stock = [dict(good_symbol="CLOTHING", units_available=40, unit_ask=500,
                  storage_waypoint="WH", storage_system="S1")]

    out = solve_tour(snapshot, ship, cons(max_hops=2), MODEL, stock_sources=stock)
    assert out["feasible"]

    stock_units = sum(t["units"] for t in _buys(out) if t["is_stock"])
    market_units = sum(t["units"] for t in _buys(out)
                       if t["good_symbol"] == "CLOTHING" and not t["is_stock"])
    assert stock_units == 40, out["legs"]  # all stock withdrawn at basis
    assert market_units == 0, f"must not buy our own output at market when stock is cheaper: {out['legs']}"


# Finite stock: at a source waypoint that carries BOTH cheap stock and a market row,
# the allocator draws the whole finite stock first (higher margin) then spills the
# overflow to a market buy of the same good — the stock pool is bounded, never infinite.
def test_stock_overflow_spills_to_market():
    snapshot = [
        # WH carries a real market row AND stock (they coexist, priced independently).
        snap("WH", "S1", "CLOTHING", ask=1000, bid=0, tv=40),       # market source at WH (overflow)
        snap("SINK", "S1", "CLOTHING", ask=9999, bid=2000, tv=40),  # sink absorbs 40 near quote
    ]
    ship = dict(ship_symbol="H", current_waypoint="WH", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    stock = [dict(good_symbol="CLOTHING", units_available=20, unit_ask=500,
                  storage_waypoint="WH", storage_system="S1")]

    out = solve_tour(snapshot, ship, cons(max_hops=2), MODEL, stock_sources=stock)
    assert out["feasible"]

    stock_units = sum(t["units"] for t in _buys(out) if t["is_stock"])
    market_units = sum(t["units"] for t in _buys(out)
                       if t["good_symbol"] == "CLOTHING" and not t["is_stock"])
    assert stock_units == 20, out["legs"]           # the whole finite stock drawn first
    assert market_units >= 1, out["legs"]           # overflow bought at market


# A stock source in a system outside the tour graph is dropped (unreachable, fail
# closed) — no stock legs are planned.
def test_unreachable_stock_system_dropped():
    snapshot = [snap("SINK", "S1", "CLOTHING", ask=9999, bid=2000, tv=40)]
    ship = dict(ship_symbol="H", current_waypoint="SINK", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    stock = [dict(good_symbol="CLOTHING", units_available=40, unit_ask=500,
                  storage_waypoint="WH", storage_system="S9")]  # S9 not in allowed

    out = solve_tour(snapshot, ship, cons(allowed=("S1",)), MODEL, stock_sources=stock)
    assert not [t for l in out["legs"] for t in l["trades"] if t.get("is_stock")]
    assert out["stock_value"] == 0


# The pre-C1 shape (no stock_sources) plans byte-identically — no is_stock trades.
def test_no_stock_sources_is_inert():
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S1", "G", ask=9999, bid=200, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    out = solve_tour(snapshot, ship, cons(), MODEL)
    assert out["stock_value"] == 0
    assert not [t for l in out["legs"] for t in l["trades"] if t.get("is_stock")]

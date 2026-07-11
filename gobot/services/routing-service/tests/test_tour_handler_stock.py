# gobot/services/routing-service/tests/test_tour_handler_stock.py
# sp-qzej / C1 (sp-64je): the OptimizeTradeTour HANDLER must resolve a request that
# carries planner-visible stock (stock_sources) into a plan that DRAWS the stock at
# basis — end to end through the proto, the seam the Go coordinator actually calls.
#
# On 2026-07-11 this exact seam failed: the request field access `request.stock_sources`
# raised AttributeError('stock_sources') because the deployed routing_pb2 was STALE (a
# redeploy reused a pre-C1 generated/ that predated the field). The handler caught it and
# returned infeasible_reason="internal_error: stock_sources", aborting the tour. The
# existing test_tour_solver_stock.py exercised solve_tour() DIRECTLY and so never covered
# the handler+proto round-trip that broke. This test closes that gap: with a proto that
# carries the field (run.sh now regenerates it when routing.proto is newer), the handler
# round-trips stock_sources into an is_stock withdrawal priced at basis, no internal_error.
import json

from generated import routing_pb2
from handlers.tour_handler import TourHandlerMixin, load_model_artifact


class _StockHandler(TourHandlerMixin):
    # OptimizeTradeTour needs only self.tour_model — drive the mixin in isolation from
    # RoutingServiceHandler (whose routing_engine import pulls in ortools).
    def __init__(self, model):
        self.tour_model = model


def _artifact(tmp_path):
    art = {"fit_version": 1, "era": "goldene",
           "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                       "buy_growth_per_step": 1.1, "n_obs": 9}},
           "recovery": {}}
    p = tmp_path / "market_model.json"
    p.write_text(json.dumps(art))
    return load_model_artifact(str(p))


def snap(wp, sys_, good, ask, bid, tv=40):
    return routing_pb2.MarketGoodSnapshot(
        waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask, bid=bid,
        trade_volume=tv, supply="LIMITED", activity="WEAK", observed_at_unix=9_999_999_999)


def test_stock_source_round_trips_to_stock_withdrawal(tmp_path):
    # A warehouse (WH) stocks CLOTHING at basis 500; a foreign SINK buys it at 2000.
    # The tour must WITHDRAW the stock at basis and sell it at the sink — never a market
    # buy (there is no CLOTHING buy-market in the snapshot at all).
    handler = _StockHandler(_artifact(tmp_path))
    req = routing_pb2.OptimizeTradeTourRequest(
        snapshot=[snap("SINK", "S1", "CLOTHING", ask=9999, bid=2000, tv=40)],
        ship=routing_pb2.TourShip(
            ship_symbol="HULL-1", current_waypoint="WH", current_system="S1",
            hold_capacity=40, fuel_current=400, fuel_capacity=400, engine_speed=30),
        constraints=routing_pb2.TourConstraints(
            max_hops=4, max_spend=5_000_000, min_margin_per_unit=1,
            working_capital_reserve=0, allowed_systems=["S1"],
            max_snapshot_age_minutes=75, expected_model_version="1@goldene"),
        waypoints=[routing_pb2.TourWaypoint(symbol="SINK", system_symbol="S1", x=0, y=0)],
        stock_sources=[routing_pb2.StockSource(
            good_symbol="CLOTHING", units_available=40, unit_ask=500,
            storage_waypoint="WH", storage_system="S1")])

    resp = handler.OptimizeTradeTour(req, None)

    # The seam that broke: a stock-bearing request must NOT surface an internal_error.
    assert not resp.infeasible_reason.startswith("internal_error"), resp.infeasible_reason
    assert resp.feasible, resp.infeasible_reason

    # The stock is DRAWN at basis: an is_stock buy priced at 500 (not a market ask) —
    # the is_stock trade plumbing the Go executor reads to withdraw at basis.
    stock_buys = [(t.good_symbol, t.units, t.expected_unit_price)
                  for l in resp.legs for t in l.trades if t.is_buy and t.is_stock]
    assert ("CLOTHING", 40, 500) in stock_buys, [
        (l.waypoint_symbol,
         [(t.good_symbol, t.units, t.is_buy, t.is_stock, t.expected_unit_price)
          for t in l.trades])
        for l in resp.legs]

    # No CLOTHING buy-market exists, so a correct resolution buys ZERO at market.
    market_buys = sum(t.units for l in resp.legs for t in l.trades
                      if t.is_buy and not t.is_stock)
    assert market_buys == 0, "must draw from stock, never buy our own output at market"

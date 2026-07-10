"""OptimizeTradeTour servicer method (sp-1ek0 P1a).

Mixin for the single RoutingServiceHandler servicer (one servicer stays
registered in server/main.py). Owns:

- market-model artifact loading at server start (`load_model_artifact`):
  missing/corrupt artifact -> the server still boots, but every
  OptimizeTradeTour call returns feasible=false with
  infeasible_reason="model_artifact_missing" — fail loud, no silent
  fallback (spec error-handling table).
- proto <-> dict conversion for `utils.tour_solver.solve_tour`, which does
  the actual beam + tranche solve on proto-shaped snake_case dicts.

Travel times: OptimizeTradeTourRequest.waypoints carries per-market coords
(harbormaster amendment 2026-07-09); the solver prices intra-system hops
with the routing engine's CRUISE formula and gate crossings with named
cooldown/hop allowances. An empty waypoints list degrades to flat named
defaults with a logged warning — never silently. The `_travel_fn`
constraint hook remains the seam for a future engine-backed matrix.
"""
import json
import logging
import os

from generated import routing_pb2
from utils.tour_solver import solve_tour

logger = logging.getLogger(__name__)

MODEL_ARTIFACT_PATH = os.path.join(
    os.path.dirname(__file__), "..", "model_artifacts", "market_model.json")


def load_model_artifact(path=None):
    """Load the fitted market model once at server start. Returns None when
    missing/unreadable — solve_tour then fails loud per call."""
    path = path or MODEL_ARTIFACT_PATH
    try:
        with open(path) as f:
            artifact = json.load(f)
        version = f"{artifact['fit_version']}@{artifact['era']}"
        logger.info(
            "tour: market model artifact loaded version=%s impact_tiers=%d "
            "recovery_tiers=%d path=%s",
            version, len(artifact.get("impact") or {}),
            len(artifact.get("recovery") or {}), os.path.abspath(path))
        return artifact
    except (OSError, ValueError, KeyError) as exc:
        logger.error(
            "tour: market model artifact MISSING/UNREADABLE at %s (%s) — "
            "every OptimizeTradeTour call will return model_artifact_missing",
            os.path.abspath(path), exc)
        return None


class TourHandlerMixin:
    """Adds OptimizeTradeTour to RoutingServiceHandler.

    Expects `self.tour_model` (dict or None) set at construction via
    load_model_artifact().
    """

    tour_model = None

    def OptimizeTradeTour(self, request: "routing_pb2.OptimizeTradeTourRequest",
                          context) -> "routing_pb2.OptimizeTradeTourResponse":
        try:
            snapshot = [dict(waypoint_symbol=r.waypoint_symbol,
                             system_symbol=r.system_symbol,
                             good_symbol=r.good_symbol,
                             ask=r.ask, bid=r.bid,
                             trade_volume=r.trade_volume,
                             supply=r.supply, activity=r.activity,
                             observed_at_unix=r.observed_at_unix)
                        for r in request.snapshot]
            ship = dict(ship_symbol=request.ship.ship_symbol,
                        current_waypoint=request.ship.current_waypoint,
                        current_system=request.ship.current_system,
                        hold_capacity=request.ship.hold_capacity,
                        fuel_current=request.ship.fuel_current,
                        fuel_capacity=request.ship.fuel_capacity,
                        engine_speed=request.ship.engine_speed,
                        cargo=[dict(good_symbol=c.good_symbol, units=c.units)
                               for c in request.ship.cargo])
            constraints = dict(
                max_hops=request.constraints.max_hops,
                max_spend=request.constraints.max_spend,
                min_margin_per_unit=request.constraints.min_margin_per_unit,
                working_capital_reserve=request.constraints.working_capital_reserve,
                allowed_systems=list(request.constraints.allowed_systems),
                max_snapshot_age_minutes=request.constraints.max_snapshot_age_minutes,
                expected_model_version=request.constraints.expected_model_version)
            waypoints = [dict(symbol=w.symbol, system_symbol=w.system_symbol,
                              x=w.x, y=w.y)
                         for w in request.waypoints]
            # sp-dchv Lane C: haul-to-storage deposit sinks the Go daemon assembled
            # and capped. Absent (pre-sp-dchv shape) -> [] -> pure-arb planning.
            deposit_candidates = [dict(good_symbol=d.good_symbol,
                                       units_wanted=d.units_wanted,
                                       synthetic_bid=d.synthetic_bid,
                                       storage_waypoint=d.storage_waypoint,
                                       storage_system=d.storage_system)
                                  for d in request.deposit_candidates]

            result = solve_tour(snapshot, ship, constraints, self.tour_model,
                                waypoints=waypoints,
                                deposit_candidates=deposit_candidates)

            response = routing_pb2.OptimizeTradeTourResponse(
                feasible=result["feasible"],
                infeasible_reason=result["infeasible_reason"],
                legs=[routing_pb2.TradeTourLeg(
                    waypoint_symbol=leg["waypoint_symbol"],
                    system_symbol=leg["system_symbol"],
                    trades=[routing_pb2.TourTrade(
                        good_symbol=t["good_symbol"], units=t["units"],
                        is_buy=t["is_buy"], is_deposit=t["is_deposit"],
                        expected_unit_price=t["expected_unit_price"])
                        for t in leg["trades"]],
                    projected_leg_profit=leg["projected_leg_profit"],
                    travel_seconds_from_prev=leg["travel_seconds_from_prev"])
                    for leg in result["legs"]],
                projected_profit=result["projected_profit"],
                projected_credits_per_hour=result["projected_credits_per_hour"],
                held_liquidation=result["held_liquidation"],
                deposit_value=result["deposit_value"],
                top_rejected=[routing_pb2.RejectedTour(
                    summary=r["summary"], reason=r["reason"])
                    for r in result["top_rejected"]],
                model_version=result["model_version"])

            logger.info(
                "tour: feasible=%s legs=%d cph=%.0f model=%s%s",
                result["feasible"], len(result["legs"]),
                result["projected_credits_per_hour"], result["model_version"],
                "" if result["feasible"]
                else f" reason={result['infeasible_reason']}")
            return response

        except Exception as exc:  # fail structured, never crash the servicer
            logger.error("tour: OptimizeTradeTour error: %s", exc, exc_info=True)
            return routing_pb2.OptimizeTradeTourResponse(
                feasible=False,
                infeasible_reason=f"internal_error: {exc}",
                model_version="")

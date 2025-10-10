#!/usr/bin/env python3
from __future__ import annotations

"""
Routing validation utilities to compare OR-Tools predictions against live API behaviour.
"""

import logging
import time
from dataclasses import dataclass
from typing import Dict, Optional

from .routing_config import RoutingConfig
from .routing_pause import pause as pause_routing, resume as resume_routing
from .smart_navigator import SmartNavigator
from .ship_controller import ShipController

logger = logging.getLogger(__name__)


@dataclass
class ValidationResult:
    """Summary of a routing validation run."""

    ship_symbol: str
    start_waypoint: str
    destination: str
    predicted_time: float
    predicted_fuel_cost: int
    actual_time: float
    actual_fuel_cost: int
    time_deviation_pct: float
    fuel_deviation_pct: float
    passed: bool

    def as_dict(self) -> Dict:
        return {
            "ship": self.ship_symbol,
            "start": self.start_waypoint,
            "destination": self.destination,
            "predicted_time": self.predicted_time,
            "predicted_fuel_cost": self.predicted_fuel_cost,
            "actual_time": self.actual_time,
            "actual_fuel_cost": self.actual_fuel_cost,
            "time_deviation_pct": self.time_deviation_pct,
            "fuel_deviation_pct": self.fuel_deviation_pct,
            "passed": self.passed,
        }


class RoutingValidator:
    """Validate routing predictions against actual API navigation."""

    def __init__(self, api_client, system_symbol: str, config: Optional[RoutingConfig] = None):
        self.api = api_client
        self.system = system_symbol
        self.config = config or RoutingConfig()
        validation_cfg = self.config.get_validation_config()
        self.max_deviation_pct = validation_cfg["max_deviation_percent"]
        self.pause_on_failure = validation_cfg["pause_on_failure"]

    def validate_route(
        self,
        ship_symbol: str,
        destination: str,
        prefer_cruise: bool = True,
        execute: bool = True,
    ) -> Optional[ValidationResult]:
        """
        Validate routing predictions for a specific ship and destination.

        Args:
            ship_symbol: Ship to validate
            destination: Destination waypoint
            prefer_cruise: Deprecated. Cruise is always preferred.
            execute: If False, only compute predictions without navigation
        """
        prefer_cruise = True  # Cruise preference enforced globally
        ship_data = self.api.get_ship(ship_symbol)
        if not ship_data:
            logger.error("Unable to fetch ship %s", ship_symbol)
            return None

        navigator = SmartNavigator(self.api, self.system, graph=None)
        predicted_route = navigator.plan_route(ship_data, destination, prefer_cruise=prefer_cruise)
        if not predicted_route:
            logger.error("No route found for %s → %s", ship_data['nav']['waypointSymbol'], destination)
            return None

        predicted_time = float(predicted_route["total_time"])
        predicted_fuel_cost = self._calc_predicted_fuel(predicted_route)

        if not execute:
            resume_routing()
            return ValidationResult(
                ship_symbol=ship_symbol,
                start_waypoint=ship_data['nav']['waypointSymbol'],
                destination=destination,
                predicted_time=predicted_time,
                predicted_fuel_cost=predicted_fuel_cost,
                actual_time=0.0,
                actual_fuel_cost=0,
                time_deviation_pct=0.0,
                fuel_deviation_pct=0.0,
                passed=True,
            )

        ship_controller = ShipController(self.api, ship_symbol)
        start_fuel = ship_data["fuel"]["current"]
        start_location = ship_data["nav"]["waypointSymbol"]

        refuel_added_units = 0
        original_refuel = ship_controller.refuel

        def tracking_refuel(*args, **kwargs):
            nonlocal refuel_added_units
            status_before = ship_controller.get_status()
            before = status_before["fuel"]["current"] if status_before else 0
            success = original_refuel(*args, **kwargs)
            status_after = ship_controller.get_status()
            if success and status_before and status_after:
                added = max(0, status_after["fuel"]["current"] - before)
                refuel_added_units += added
            return success

        ship_controller.refuel = tracking_refuel  # type: ignore[assignment]

        final_fuel = start_fuel
        try:
            start_time = time.time()
            success = navigator.execute_route(ship_controller, destination, prefer_cruise=prefer_cruise)
            actual_time = time.time() - start_time

            if not success:
                logger.error("Navigation execution failed for validation run")
                return None

            final_status = ship_controller.get_status()
            if not final_status:
                logger.error("Failed to retrieve final ship status after navigation")
                return None

            final_fuel = final_status["fuel"]["current"]
        finally:
            ship_controller.refuel = original_refuel  # type: ignore[assignment]

        actual_fuel_cost = max(0, start_fuel + refuel_added_units - final_fuel)

        time_deviation = self._compute_deviation(predicted_time, actual_time)
        fuel_deviation = self._compute_deviation(predicted_fuel_cost, actual_fuel_cost)
        passed = time_deviation <= self.max_deviation_pct and fuel_deviation <= self.max_deviation_pct

        if not passed and self.pause_on_failure:
            pause_routing(
                "Routing validation deviation exceeded threshold",
                {
                    "time_deviation_pct": f"{time_deviation:.2f}",
                    "fuel_deviation_pct": f"{fuel_deviation:.2f}",
                    "ship": ship_symbol,
                    "destination": destination,
                },
            )
            logger.critical(
                "Routing validation failed (time deviation %.2f%%, fuel deviation %.2f%%); operations paused.",
                time_deviation,
                fuel_deviation,
            )
        elif passed:
            resume_routing()

        return ValidationResult(
            ship_symbol=ship_symbol,
            start_waypoint=start_location,
            destination=destination,
            predicted_time=predicted_time,
            predicted_fuel_cost=predicted_fuel_cost,
            actual_time=actual_time,
            actual_fuel_cost=actual_fuel_cost,
            time_deviation_pct=time_deviation,
            fuel_deviation_pct=fuel_deviation,
            passed=passed,
        )

    @staticmethod
    def _calc_predicted_fuel(route: Dict) -> int:
        fuel_used = 0
        fuel_added = 0
        for step in route.get("steps", []):
            if step["action"] == "navigate":
                fuel_used += int(step.get("fuel_cost", 0))
            elif step["action"] == "refuel":
                fuel_added += int(step.get("fuel_added", 0))
        net = fuel_used - fuel_added
        return max(0, net)

    @staticmethod
    def _compute_deviation(predicted: float, actual: float) -> float:
        if actual <= 0:
            return 0.0
        return abs(predicted - actual) / actual * 100.0

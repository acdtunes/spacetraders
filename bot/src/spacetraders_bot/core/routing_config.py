#!/usr/bin/env python3
from __future__ import annotations

"""
Routing configuration loader for OR-Tools routing system.

Responsibilities:
- Load routing constants from YAML configuration file
- Validate schema and provide accessor helpers
- Support hot-reloading for runtime updates
"""

import logging
from pathlib import Path
from typing import Any, Dict

import yaml

logger = logging.getLogger(__name__)


class RoutingConfigError(RuntimeError):
    """Raised when routing configuration fails validation."""


class RoutingConfig:
    """Load and expose routing configuration constants."""

    DEFAULT_PATH = Path("config") / "routing_constants.yaml"

    def __init__(self, config_path: str | Path | None = None):
        self._path = Path(config_path) if config_path else self.DEFAULT_PATH
        self._config: Dict[str, Any] = {}
        self.reload()

    # --------------------------------------------------------------------- #
    # Public API
    # --------------------------------------------------------------------- #

    @property
    def path(self) -> Path:
        return self._path

    def get(self, key: str, default: Any = None) -> Any:
        return self._config.get(key, default)

    def get_flight_mode_config(self, mode: str) -> Dict[str, Any]:
        mode_upper = mode.upper()
        flight_modes = self._config.get("flight_modes", {})
        if mode_upper not in flight_modes:
            raise RoutingConfigError(f"Flight mode '{mode}' not defined in routing config")
        return dict(flight_modes[mode_upper])

    def get_validation_config(self) -> Dict[str, Any]:
        validation = self._config.get("validation", {})
        return {
            "max_deviation_percent": float(validation.get("max_deviation_percent", 5.0)),
            "check_interval_hours": int(validation.get("check_interval_hours", 24)),
            "pause_on_failure": bool(validation.get("pause_on_failure", True)),
        }

    def get_solver_config(self) -> Dict[str, Any]:
        solver = self._config.get("solver", {})
        return {
            "time_limit_ms": int(solver.get("time_limit_ms", 5000)),
            "first_solution_strategy": solver.get("first_solution_strategy", "PATH_CHEAPEST_ARC"),
            "local_search_metaheuristic": solver.get("local_search_metaheuristic", "GUIDED_LOCAL_SEARCH"),
            "guided_local_search_lambda": float(solver.get("guided_local_search_lambda", 0.1)),
        }

    def fuel_safety_margin(self) -> float:
        return float(self._config.get("fuel_safety_margin", 0.1))

    def refuel_time_seconds(self) -> int:
        return int(self._config.get("refuel_time_seconds", 5))

    def reload(self) -> None:
        """Hot-reload configuration from YAML file."""
        if not self._path.exists():
            raise RoutingConfigError(f"Routing config not found: {self._path}")

        with self._path.open("r", encoding="utf-8") as handle:
            loaded = yaml.safe_load(handle) or {}

        self._config = loaded
        self.validate()
        logger.info("Routing configuration loaded from %s", self._path)

    # --------------------------------------------------------------------- #
    # Validation
    # --------------------------------------------------------------------- #

    def validate(self) -> None:
        """Validate minimal routing configuration schema."""
        flight_modes = self._config.get("flight_modes")
        if not isinstance(flight_modes, dict) or not flight_modes:
            raise RoutingConfigError("routing config missing 'flight_modes' section")

        for mode, values in flight_modes.items():
            if not isinstance(values, dict):
                raise RoutingConfigError(f"Flight mode '{mode}' must be a mapping")
            if "time_multiplier" not in values:
                raise RoutingConfigError(f"Flight mode '{mode}' missing time_multiplier")
            if "fuel_rate" not in values:
                raise RoutingConfigError(f"Flight mode '{mode}' missing fuel_rate")

            try:
                float(values["time_multiplier"])
                float(values["fuel_rate"])
            except (TypeError, ValueError) as exc:
                raise RoutingConfigError(
                    f"Flight mode '{mode}' must have numeric time_multiplier and fuel_rate"
                ) from exc

        if "fuel_safety_margin" in self._config:
            margin = self._config["fuel_safety_margin"]
            if not isinstance(margin, (float, int)) or margin < 0:
                raise RoutingConfigError("fuel_safety_margin must be a non-negative number")

        if "refuel_time_seconds" in self._config:
            refuel_time = self._config["refuel_time_seconds"]
            if not isinstance(refuel_time, (float, int)) or refuel_time < 0:
                raise RoutingConfigError("refuel_time_seconds must be non-negative")

        validation = self._config.get("validation", {})
        if validation:
            if "max_deviation_percent" in validation:
                max_deviation = validation["max_deviation_percent"]
                if not isinstance(max_deviation, (float, int)) or max_deviation <= 0:
                    raise RoutingConfigError("validation.max_deviation_percent must be > 0")
            if "check_interval_hours" in validation:
                interval = validation["check_interval_hours"]
                if not isinstance(interval, (float, int)) or interval <= 0:
                    raise RoutingConfigError("validation.check_interval_hours must be > 0")
            if "pause_on_failure" in validation and not isinstance(
                validation["pause_on_failure"], bool
            ):
                raise RoutingConfigError("validation.pause_on_failure must be boolean")

        solver = self._config.get("solver", {})
        if solver:
            for key in ("time_limit_ms", "guided_local_search_lambda"):
                if key in solver and not isinstance(solver[key], (float, int)):
                    raise RoutingConfigError(f"solver.{key} must be numeric")
            if "first_solution_strategy" in solver and not isinstance(
                solver["first_solution_strategy"], str
            ):
                raise RoutingConfigError("solver.first_solution_strategy must be string")
            if "local_search_metaheuristic" in solver and not isinstance(
                solver["local_search_metaheuristic"], str
            ):
                raise RoutingConfigError("solver.local_search_metaheuristic must be string")

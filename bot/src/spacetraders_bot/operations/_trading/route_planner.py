"""
Route Planning - Legacy Compatibility Module

⚠️ DEPRECATED: This module provides backward compatibility wrappers.
New code should use: operations._trading.route_planning

This module will be removed in a future version.
All functionality has been refactored into:
- route_planning.route_generator (GreedyRoutePlanner, MultiLegRouteCoordinator)
- route_planning.fixed_route_builder (FixedRouteBuilder, create_fixed_route)
- route_planning.market_validator (MarketValidator)
- route_planning.opportunity_finder (OpportunityFinder)
"""

import logging
from typing import Callable, Dict, List, Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.operations._trading.models import MultiLegRoute
from spacetraders_bot.operations._trading.evaluation_strategies import TradeEvaluationStrategy
from spacetraders_bot.operations._trading.route_planning import (
    GreedyRoutePlanner as _GreedyRoutePlanner,
    MultiLegRouteCoordinator as _MultiLegRouteCoordinator,
)


# Legacy compatibility wrapper
class GreedyRoutePlanner(_GreedyRoutePlanner):
    """
    DEPRECATED: Use operations._trading.route_planning.GreedyRoutePlanner instead

    Legacy wrapper for backward compatibility.
    """
    pass


# Legacy compatibility wrapper
class MultiLegTradeOptimizer:
    """
    DEPRECATED: Use operations._trading.route_planning.MultiLegRouteCoordinator instead

    Legacy wrapper for backward compatibility.
    This class delegates all calls to MultiLegRouteCoordinator.
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
        strategy_factory: Optional[Callable[[logging.Logger], TradeEvaluationStrategy]] = None,
    ):
        """Initialize by delegating to MultiLegRouteCoordinator"""
        self._coordinator = _MultiLegRouteCoordinator(
            api=api,
            db=db,
            player_id=player_id,
            logger=logger,
            strategy_factory=strategy_factory,
        )
        # Expose coordinator attributes for backward compatibility
        self.api = self._coordinator.api
        self.db = self._coordinator.db
        self.player_id = self._coordinator.player_id
        self.logger = self._coordinator.logger

    def find_optimal_route(
        self,
        start_waypoint: str,
        system: str,
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        fuel_capacity: int,
        current_fuel: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        """Delegate to coordinator"""
        return self._coordinator.find_optimal_route(
            start_waypoint=start_waypoint,
            system=system,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
            fuel_capacity=fuel_capacity,
            current_fuel=current_fuel,
            starting_cargo=starting_cargo,
        )

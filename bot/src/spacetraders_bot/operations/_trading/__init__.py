"""
Trading Operations Module

Modular trading system following SOLID principles.

Architecture:
- models.py: Domain models (TradeAction, RouteSegment, MultiLegRoute, etc.)
- market_service.py: Market data operations (pricing, validation, updates)
- market_repository.py: Database access layer for market queries
- circuit_breaker.py: Profitability validation and circuit breaker logic
- trade_executor.py: Buy/sell action execution
- segment_executor.py: Single route segment execution
- route_executor.py: Full route orchestration
- dependency_analyzer.py: Segment dependency analysis for smart skip logic
- evaluation_strategies.py: Market evaluation strategies for route planning
- route_planner.py: Multi-leg route optimization (GreedyRoutePlanner, MultiLegTradeOptimizer)
- route_planning/: Route planning modules (FixedRouteBuilder, future: MarketValidator, OpportunityFinder)
- cargo_salvage.py: Emergency cargo cleanup service

Public API:
- Data models
- Execution classes (RouteExecutor is primary entry point)
- Market service functions
- Route planning classes
- Cargo salvage service
"""

# Domain Models
from .models import (
    TradeAction,
    RouteSegment,
    MultiLegRoute,
    MarketEvaluation,
    SegmentDependency,
)

# Market Services
from .market_service import (
    estimate_sell_price_with_degradation,
    find_planned_sell_price,
    find_planned_sell_destination,
    update_market_price_from_transaction,
    validate_market_data_freshness,
)

# Circuit Breaker
from .circuit_breaker import (
    ProfitabilityValidator,
    calculate_batch_size,
)

# Executors
from .trade_executor import TradeExecutor
from .segment_executor import SegmentExecutor
from .route_executor import RouteExecutor

# Dependency Analysis
from .dependency_analyzer import (
    analyze_route_dependencies,
    should_skip_segment,
    cargo_blocks_future_segments,
)

# Route Planning
from .evaluation_strategies import (
    TradeEvaluationStrategy,
    ProfitFirstStrategy,
)
from .route_planner import (
    GreedyRoutePlanner,
    MultiLegTradeOptimizer,
)
from .route_planning import (
    create_fixed_route,
    FixedRouteBuilder,
    MarketValidator,
    OpportunityFinder,
    GreedyRoutePlanner as _GreedyRoutePlanner,
    MultiLegRouteCoordinator,
)
from .market_repository import (
    MarketRepository,
    NearbyMarketBuyer,
)

# Cargo Salvage
from .cargo_salvage import CargoSalvageService

# Fleet Optimization
from .fleet_optimizer import FleetTradeOptimizer


__all__ = [
    # Models
    'TradeAction',
    'RouteSegment',
    'MultiLegRoute',
    'MarketEvaluation',
    'SegmentDependency',
    # Market Services
    'estimate_sell_price_with_degradation',
    'find_planned_sell_price',
    'find_planned_sell_destination',
    'update_market_price_from_transaction',
    'validate_market_data_freshness',
    # Circuit Breaker
    'ProfitabilityValidator',
    'calculate_batch_size',
    # Executors
    'TradeExecutor',
    'SegmentExecutor',
    'RouteExecutor',
    # Dependency Analysis
    'analyze_route_dependencies',
    'should_skip_segment',
    'cargo_blocks_future_segments',
    # Route Planning
    'TradeEvaluationStrategy',
    'ProfitFirstStrategy',
    'GreedyRoutePlanner',
    'MultiLegTradeOptimizer',
    'MultiLegRouteCoordinator',
    'create_fixed_route',
    'FixedRouteBuilder',
    'MarketValidator',
    'OpportunityFinder',
    'MarketRepository',
    'NearbyMarketBuyer',
    # Cargo Salvage
    'CargoSalvageService',
    # Fleet Optimization
    'FleetTradeOptimizer',
]

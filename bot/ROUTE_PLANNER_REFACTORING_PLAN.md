# route_planner.py Refactoring Plan - ULTRATHINK Mode

**Generated:** 2025-10-21
**Target File:** `src/spacetraders_bot/operations/_trading/route_planner.py`
**Current State:** 685 lines, 33.0% coverage, 458 uncovered lines
**Target State:** 4 focused modules, 80%+ coverage each

---

## Executive Summary

route_planner.py suffers from **multiple responsibility violations** and **excessive line count**:
- 🔴 685 lines (target: <250 per file)
- 🔴 33.0% test coverage (target: 80%+)
- 🔴 458 uncovered lines
- 🔴 3 distinct classes + 1 utility function mixed together
- 🔴 Multiple concerns: planning, DB queries, validation, logging, fixed routes

**Refactoring Strategy:** Extract into 4 single-responsibility modules in new subdirectory

---

## Current Structure Analysis

### File Breakdown (685 lines total)

```
route_planner.py:
├── Imports & Documentation (1-20): 20 lines
├── GreedyRoutePlanner class (22-204): 183 lines
│   ├── __init__ (34-42): 9 lines
│   ├── find_route (44-145): 102 lines ⚠️ COMPLEX
│   └── _find_best_next_market (147-204): 58 lines
├── MultiLegTradeOptimizer class (207-456): 250 lines ⚠️ LARGE
│   ├── __init__ (215-227): 13 lines
│   ├── find_optimal_route (229-300): 72 lines
│   ├── _get_markets_in_system (302-313): 12 lines
│   ├── _get_trade_opportunities (315-340): 26 lines
│   ├── _collect_opportunities_for_market (342-395): 54 lines
│   ├── _is_market_data_fresh (397-433): 37 lines
│   └── _log_route_summary (435-456): 22 lines
└── create_fixed_route function (459-685): 227 lines ⚠️ LARGE
```

### Responsibility Violations Identified

| Code Section | Current Responsibility | Should Be In |
|--------------|------------------------|--------------|
| GreedyRoutePlanner | Route algorithm | ✅ route_generator.py |
| _get_markets_in_system | DB queries | ❌ opportunity_finder.py |
| _get_trade_opportunities | DB queries | ❌ opportunity_finder.py |
| _collect_opportunities_for_market | DB queries | ❌ opportunity_finder.py |
| _is_market_data_fresh | Validation | ❌ market_validator.py |
| _log_route_summary | Logging | ❌ route_generator.py (formatting) |
| create_fixed_route | Fixed routes | ❌ fixed_route_builder.py |

### Dependency Graph

```
Current (Circular/Complex):
route_planner.py
  ├─> models.py
  ├─> market_repository.py
  ├─> evaluation_strategies.py
  └─> Multiple internal dependencies

After Refactoring (Clean):
route_planning/
  ├─> route_generator.py
  │     ├─> models.py
  │     ├─> evaluation_strategies.py
  │     └─> opportunity_finder.py
  ├─> opportunity_finder.py
  │     ├─> models.py
  │     ├─> market_repository.py
  │     └─> market_validator.py
  ├─> market_validator.py
  │     └─> models.py
  └─> fixed_route_builder.py
        ├─> models.py
        └─> market_repository.py
```

---

## Proposed Module Structure

### New Directory: `operations/_trading/route_planning/`

```
_trading/route_planning/
├── __init__.py                  # Public API exports (~40 lines)
├── route_generator.py           # Greedy route planning (~220 lines)
├── opportunity_finder.py        # DB queries for opportunities (~190 lines)
├── market_validator.py          # Market data validation (~150 lines)
└── fixed_route_builder.py      # Simple buy→sell routes (~230 lines)
```

---

## Module 1: route_generator.py (~220 lines)

### Responsibility
Single Responsibility: **Generate optimal trading routes using greedy search algorithm**

### Contents
```python
# route_generator.py

class GreedyRoutePlanner:
    """
    Greedy route planning algorithm for multi-leg trading

    Coordinates:
    - Opportunity evaluation via strategy pattern
    - Best next market selection
    - Route segment construction
    - Zero-distance action accumulation
    """

    def __init__(self, logger, db, strategy):
        # Existing __init__ code (lines 34-42)

    def find_route(self, start_waypoint, markets, trade_opportunities, ...):
        # Existing find_route code (lines 44-145)
        # Uses OpportunityFinder instead of direct DB access

    def _find_best_next_market(self, ...):
        # Existing _find_best_next_market code (lines 147-204)

    def format_route_summary(self, route):
        # Extract from MultiLegTradeOptimizer._log_route_summary (lines 435-456)
        # Returns formatted string instead of logging directly

class MultiLegRouteCoordinator:
    """
    High-level coordinator for route planning workflow

    Orchestrates:
    - OpportunityFinder for DB queries
    - MarketValidator for freshness checks
    - GreedyRoutePlanner for algorithm execution
    """

    def __init__(self, api, db, player_id, logger, strategy_factory):
        # From MultiLegTradeOptimizer.__init__ (lines 215-227)

    def find_optimal_route(self, start_waypoint, system, max_stops, ...):
        # Simplified version of lines 229-300
        # Delegates to OpportunityFinder, MarketValidator, GreedyRoutePlanner
```

### Line Count Estimate
- GreedyRoutePlanner class: ~170 lines (from original)
- MultiLegRouteCoordinator class: ~50 lines (simplified coordinator)
- **Total: ~220 lines**

### Dependencies
- `models.py` (RouteSegment, MultiLegRoute)
- `evaluation_strategies.py` (TradeEvaluationStrategy)
- `opportunity_finder.py` (OpportunityFinder)

### Test Coverage Target
- ✅ Route generation with varying stops (2, 3, 5)
- ✅ Zero-distance action accumulation
- ✅ Best next market selection logic
- ✅ Greedy search termination conditions
- **Target: 85%+ coverage**

---

## Module 2: opportunity_finder.py (~190 lines)

### Responsibility
Single Responsibility: **Query database for markets and profitable trade opportunities**

### Contents
```python
# opportunity_finder.py

class OpportunityFinder:
    """
    Database query service for trade opportunities

    Responsibilities:
    - Fetch all markets in system
    - Fetch all trade opportunities
    - Filter by profitability (spread > 0)
    - Sort by profit margin
    """

    def __init__(self, db, player_id, logger, market_validator=None):
        self.db = db
        self.player_id = player_id
        self.logger = logger
        self.market_validator = market_validator or MarketValidator(logger)

    def get_markets_in_system(self, system):
        # From MultiLegTradeOptimizer._get_markets_in_system (lines 302-313)

    def get_trade_opportunities(self, system, markets):
        # From MultiLegTradeOptimizer._get_trade_opportunities (lines 315-340)
        # Uses market_validator instead of direct freshness checks

    def _collect_opportunities_for_market(self, conn, buy_market, buy_data, markets):
        # From MultiLegTradeOptimizer._collect_opportunities_for_market (lines 342-395)
        # Delegates freshness checks to market_validator
```

### Line Count Estimate
- OpportunityFinder class: ~160 lines
- Helper methods: ~30 lines
- **Total: ~190 lines**

### Dependencies
- `market_repository.py` (MarketRepository for distance calculations)
- `market_validator.py` (MarketValidator for freshness checks)

### Test Coverage Target
- ✅ Markets in system query
- ✅ Trade opportunities with valid spreads
- ✅ Filtering unprofitable opportunities (spread ≤ 0)
- ✅ Sorting by spread (most profitable first)
- ✅ Handling empty markets/no opportunities
- **Target: 90%+ coverage** (pure DB logic, easy to test)

---

## Module 3: market_validator.py (~150 lines)

### Responsibility
Single Responsibility: **Validate market data quality and freshness**

### Contents
```python
# market_validator.py

from datetime import datetime, timezone
from typing import Dict, Optional

class MarketValidator:
    """
    Market data validation service

    Responsibilities:
    - Check data freshness (age < 1 hour)
    - Detect aging data (0.5 - 1 hour)
    - Detect stale data (> 1 hour)
    - Parse and validate timestamps
    """

    # Freshness thresholds
    FRESH_THRESHOLD_HOURS = 0.5
    AGING_THRESHOLD_HOURS = 1.0

    def __init__(self, logger):
        self.logger = logger

    def is_market_data_fresh(self, record, waypoint, good, action_type):
        # From MultiLegTradeOptimizer._is_market_data_fresh (lines 397-433)

    def get_data_age_hours(self, timestamp_str):
        """Parse timestamp and calculate age in hours"""
        try:
            timestamp = datetime.strptime(
                timestamp_str,
                '%Y-%m-%dT%H:%M:%S.%fZ'
            ).replace(tzinfo=timezone.utc)
            return (datetime.now(timezone.utc) - timestamp).total_seconds() / 3600
        except (ValueError, TypeError) as e:
            self.logger.warning(f"Invalid timestamp: {e}")
            return None

    def classify_data_freshness(self, age_hours):
        """
        Classify data freshness level

        Returns:
            'FRESH' - < 0.5 hours
            'AGING' - 0.5 - 1.0 hours
            'STALE' - > 1.0 hours
        """
        if age_hours is None:
            return 'FRESH'  # No timestamp = assume fresh

        if age_hours < self.FRESH_THRESHOLD_HOURS:
            return 'FRESH'
        elif age_hours < self.AGING_THRESHOLD_HOURS:
            return 'AGING'
        else:
            return 'STALE'

    def validate_trade_opportunity_data(self, buy_record, sell_record, buy_waypoint, sell_waypoint, good):
        """
        Validate both buy and sell market data freshness

        Returns:
            (is_valid, reason) tuple
        """
        # Check buy market freshness
        if not self.is_market_data_fresh(buy_record, buy_waypoint, good, 'buy'):
            return False, f"Buy market data stale: {buy_waypoint}"

        # Check sell market freshness
        if not self.is_market_data_fresh(sell_record, sell_waypoint, good, 'sell'):
            return False, f"Sell market data stale: {sell_waypoint}"

        return True, "Data fresh"
```

### Line Count Estimate
- MarketValidator class: ~120 lines
- Helper methods: ~30 lines
- **Total: ~150 lines**

### Dependencies
- None (pure validation logic)

### Test Coverage Target
- ✅ Fresh data (< 30 minutes)
- ✅ Aging data (30-60 minutes)
- ✅ Stale data (> 60 minutes)
- ✅ Invalid timestamp parsing
- ✅ Missing timestamp (assume fresh)
- ✅ Classification logic (FRESH/AGING/STALE)
- **Target: 95%+ coverage** (pure logic, highly testable)

---

## Module 4: fixed_route_builder.py (~230 lines)

### Responsibility
Single Responsibility: **Build simple fixed buy→sell routes without optimization**

### Contents
```python
# fixed_route_builder.py

from typing import Optional
from spacetraders_bot.core.utils import calculate_distance
from spacetraders_bot.operations._trading.models import RouteSegment, MultiLegRoute, TradeAction

class FixedRouteBuilder:
    """
    Builder for simple fixed 2-stop routes (buy → sell)

    Used for prescriptive trading mode where user specifies:
    - Buy waypoint
    - Sell waypoint
    - Good to trade

    No optimization, just route construction and validation.
    """

    def __init__(self, db, logger):
        self.db = db
        self.logger = logger

    def build_route(
        self,
        current_waypoint,
        buy_waypoint,
        sell_waypoint,
        good,
        cargo_capacity,
        starting_credits,
        ship_speed
    ):
        """
        Build a fixed buy→sell route

        From create_fixed_route function (lines 459-685)
        """
        # Full implementation from create_fixed_route

    def _get_waypoint_coordinates(self, waypoint):
        """Fetch waypoint coordinates from database"""
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT x, y FROM waypoints WHERE waypoint_symbol = ?",
                (waypoint,)
            )
            row = cursor.fetchone()
            return {'x': row[0], 'y': row[1]} if row else None

    def _calculate_purchase_units(self, cargo_capacity, starting_credits, buy_price, trade_volume):
        """Calculate optimal units to purchase"""
        max_by_credits = int((starting_credits * 0.85) / buy_price) if buy_price > 0 else cargo_capacity
        return min(cargo_capacity, max_by_credits, trade_volume or cargo_capacity)

    def _build_single_segment_route(self, buy_waypoint, sell_waypoint, ...):
        """Build route when ship is already at buy market"""
        # Lines 595-624

    def _build_two_segment_route(self, current_waypoint, buy_waypoint, sell_waypoint, ...):
        """Build route when ship needs to navigate to buy market first"""
        # Lines 626-667

def create_fixed_route(api, db, player_id, current_waypoint, buy_waypoint, sell_waypoint, good, ...):
    """
    Legacy function wrapper for backward compatibility

    Delegates to FixedRouteBuilder.build_route()
    """
    builder = FixedRouteBuilder(db, logger)
    return builder.build_route(
        current_waypoint, buy_waypoint, sell_waypoint, good,
        cargo_capacity, starting_credits, ship_speed
    )
```

### Line Count Estimate
- FixedRouteBuilder class: ~200 lines
- Helper methods: ~30 lines
- **Total: ~230 lines**

### Dependencies
- `models.py` (RouteSegment, MultiLegRoute, TradeAction)
- `core.utils` (calculate_distance)

### Test Coverage Target
- ✅ Ship at buy market (1 segment route)
- ✅ Ship away from buy market (2 segment route)
- ✅ Insufficient credits
- ✅ Missing market data
- ✅ Missing waypoint coordinates
- ✅ Zero profit route (rejected)
- **Target: 85%+ coverage**

---

## Module 5: __init__.py (~40 lines)

### Responsibility
Single Responsibility: **Public API exports for route_planning package**

### Contents
```python
# __init__.py

"""
Route Planning Package

Provides multi-leg trading route optimization and fixed route construction.

Public API:
- GreedyRoutePlanner - Greedy route search algorithm
- MultiLegRouteCoordinator - High-level route optimization
- OpportunityFinder - DB queries for trade opportunities
- MarketValidator - Market data freshness validation
- FixedRouteBuilder - Simple buy→sell route construction
- create_fixed_route - Legacy function wrapper
"""

from .route_generator import GreedyRoutePlanner, MultiLegRouteCoordinator
from .opportunity_finder import OpportunityFinder
from .market_validator import MarketValidator
from .fixed_route_builder import FixedRouteBuilder, create_fixed_route

__all__ = [
    'GreedyRoutePlanner',
    'MultiLegRouteCoordinator',
    'OpportunityFinder',
    'MarketValidator',
    'FixedRouteBuilder',
    'create_fixed_route',
]
```

---

## Migration Strategy

### Phase 1: Extract Fixed Route Builder (Low Risk)

**Rationale:** Least coupled component, standalone function

1. Create `fixed_route_builder.py`
2. Move `create_fixed_route` function → `FixedRouteBuilder` class
3. Update imports in parent `_trading/__init__.py`
4. Run existing tests to verify no regressions
5. Write BDD tests for FixedRouteBuilder

**Time Estimate:** 2 hours
**Risk:** Low (isolated function)

### Phase 2: Extract Market Validator (Low Risk)

**Rationale:** Pure validation logic, no complex dependencies

1. Create `market_validator.py`
2. Move `_is_market_data_fresh` → `MarketValidator.is_market_data_fresh`
3. Add helper methods (`get_data_age_hours`, `classify_data_freshness`)
4. Update `OpportunityFinder` to use `MarketValidator`
5. Write BDD tests for all freshness scenarios

**Time Estimate:** 2 hours
**Risk:** Low (pure logic)

### Phase 3: Extract Opportunity Finder (Medium Risk)

**Rationale:** DB query logic, depends on MarketValidator

1. Create `opportunity_finder.py`
2. Move DB query methods from `MultiLegTradeOptimizer`:
   - `_get_markets_in_system`
   - `_get_trade_opportunities`
   - `_collect_opportunities_for_market`
3. Inject `MarketValidator` dependency
4. Update `route_generator.py` to use `OpportunityFinder`
5. Write BDD tests for DB queries

**Time Estimate:** 3 hours
**Risk:** Medium (DB dependencies)

### Phase 4: Extract Route Generator (Medium Risk)

**Rationale:** Core algorithm, depends on OpportunityFinder

1. Create `route_generator.py`
2. Move `GreedyRoutePlanner` class (unchanged)
3. Refactor `MultiLegTradeOptimizer` → `MultiLegRouteCoordinator`
4. Extract `_log_route_summary` → `format_route_summary`
5. Update dependencies to use `OpportunityFinder`
6. Write BDD tests for route generation

**Time Estimate:** 3 hours
**Risk:** Medium (core algorithm)

### Phase 5: Update Parent Package (Low Risk)

**Rationale:** Wire up new modules

1. Update `_trading/__init__.py` to import from `route_planning/`
2. Update `multileg_trader.py` CLI to use new modules
3. Run full test suite
4. Fix any integration issues

**Time Estimate:** 1 hour
**Risk:** Low (integration)

### Phase 6: Delete Old File & Celebrate (Zero Risk)

1. Delete `route_planner.py`
2. Verify all tests still pass
3. Update coverage report
4. Commit refactoring

**Time Estimate:** 0.5 hours
**Risk:** None

---

## Total Effort Estimate

| Phase | Task | Hours | Risk |
|-------|------|-------|------|
| 1 | Extract FixedRouteBuilder | 2 | Low |
| 2 | Extract MarketValidator | 2 | Low |
| 3 | Extract OpportunityFinder | 3 | Medium |
| 4 | Extract RouteGenerator | 3 | Medium |
| 5 | Update Parent Package | 1 | Low |
| 6 | Cleanup & Commit | 0.5 | None |
| **TOTAL** | **11.5 hours** | **Medium** |

---

## Success Criteria

### Code Quality
- ✅ All files < 250 lines
- ✅ Each module has single responsibility
- ✅ No circular dependencies
- ✅ Clear dependency graph
- ✅ 100% backward compatibility

### Test Coverage
- ✅ route_generator.py: 85%+
- ✅ opportunity_finder.py: 90%+
- ✅ market_validator.py: 95%+
- ✅ fixed_route_builder.py: 85%+
- ✅ Overall package: 85%+

### Regression Testing
- ✅ All existing tests pass
- ✅ No CLI behavior changes
- ✅ Same route outputs for same inputs
- ✅ Performance equivalent or better

---

## Risk Mitigation

### Risk 1: Breaking Existing Functionality
**Mitigation:**
- Run full test suite after each phase
- Maintain backward compatibility wrappers
- Test CLI integration separately

### Risk 2: Circular Dependencies
**Mitigation:**
- Follow dependency graph strictly
- Use dependency injection
- Keep models in separate module

### Risk 3: Test Coverage Gaps
**Mitigation:**
- Write BDD tests before refactoring
- Use existing tests as regression suite
- Target 80%+ coverage per module

---

## BDD Test Scenarios (route_planning package)

### route_generator.feature

```gherkin
Feature: Greedy Route Planning

  Scenario: Generate 2-stop profitable route
    Given a ship at waypoint "X1-A1"
    And markets ["X1-B7", "X1-C5"] available
    And trade opportunity: BUY COPPER at X1-B7 (100 cr), SELL at X1-C5 (500 cr)
    When generating route with max 2 stops
    Then route should have 2 segments
    And total profit should be > 0

  Scenario: Zero-distance action accumulation
    Given ship at waypoint "X1-B7"
    And opportunity at X1-B7 with 0 distance
    When generating route
    Then actions should accumulate without creating segment
    And segment count should not increase

  Scenario: No profitable route found
    Given ship at waypoint "X1-A1"
    And no profitable trade opportunities
    When generating route
    Then should return None
```

### opportunity_finder.feature

```gherkin
Feature: Trade Opportunity Discovery

  Scenario: Find markets in system
    Given system "X1-JB26" with 5 markets in database
    When querying markets in system
    Then should return 5 market waypoints

  Scenario: Find profitable opportunities
    Given markets with price data in database
    When querying trade opportunities
    Then should return only opportunities with spread > 0
    And should sort by spread (highest first)

  Scenario: Filter stale market data
    Given market data older than 1 hour
    When collecting opportunities
    Then should exclude stale data
    And log warning about stale data
```

### market_validator.feature

```gherkin
Feature: Market Data Validation

  Scenario: Fresh market data (< 30 minutes)
    Given market data updated 20 minutes ago
    When checking data freshness
    Then should return True
    And classification should be "FRESH"

  Scenario: Aging market data (30-60 minutes)
    Given market data updated 45 minutes ago
    When checking data freshness
    Then should return True
    And classification should be "AGING"
    And log aging warning

  Scenario: Stale market data (> 60 minutes)
    Given market data updated 2 hours ago
    When checking data freshness
    Then should return False
    And classification should be "STALE"
    And log stale warning
```

### fixed_route_builder.feature

```gherkin
Feature: Fixed Route Construction

  Scenario: Build route when ship at buy market
    Given ship at buy waypoint "X1-B7"
    And buy/sell waypoints specified
    When building fixed route
    Then should create 1 segment (buy at start, sell at end)

  Scenario: Build route when ship away from buy market
    Given ship at waypoint "X1-A1"
    And buy waypoint "X1-B7", sell waypoint "X1-C5"
    When building fixed route
    Then should create 2 segments
    And segment 1 should navigate to buy market
    And segment 2 should navigate to sell market

  Scenario: Reject unprofitable route
    Given market data with zero spread
    When building fixed route
    Then should return None
    And log profitability warning
```

---

## Post-Refactoring Validation

### Automated Tests
```bash
# Run all route_planning tests
pytest tests/bdd/features/trading/_trading_module/route_planning/ -v

# Check coverage
pytest tests/bdd/features/trading/_trading_module/route_planning/ \
  --cov=src/spacetraders_bot/operations/_trading/route_planning \
  --cov-report=term-missing

# Expected: 85%+ coverage across all modules
```

### Manual Validation
```bash
# Test CLI integration
python3 spacetraders_bot.py multileg-trade \
  --player-id 6 --ship SHIP-1 --system X1-JB26 --max-stops 3

# Verify route output matches pre-refactoring behavior
```

---

## Next Steps After Completion

1. **Tackle cargo_salvage.py** (389 lines, 37% coverage)
2. **Tackle evaluation_strategies.py** (270 lines, 54% coverage)
3. **Tackle market_service.py** (302 lines, 65% coverage)
4. **Achieve 80%+ coverage** across entire _trading module

---

## References

- **REFACTORING_REPORT.md** - Coverage analysis
- **BDD_TRADING_MODULE_SUMMARY.md** - Existing tests
- **TESTING_GUIDE.md** - BDD testing patterns
- **CLAUDE.md** - Architecture guidelines

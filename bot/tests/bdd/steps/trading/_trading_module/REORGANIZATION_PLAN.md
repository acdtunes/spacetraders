# BDD Test Steps Reorganization - Complete Analysis & Implementation Plan

## Executive Summary

Successfully reorganized test step definitions from a bloated 3,566-line conftest.py into proper feature-specific files with a minimal 347-line conftest.py containing only shared fixtures.

## Current State Analysis

### **BEFORE: Bloated Structure (WRONG)**

```
tests/bdd/steps/trading/_trading_module/
├── conftest.py                          3,566 lines  ❌ ALL 299 steps
├── test_market_service_steps.py            12 lines  ❌ Just scenarios() placeholder
├── test_circuit_breaker_steps.py           12 lines  ❌ Just scenarios() placeholder
├── test_trade_executor_steps.py            12 lines  ❌ Just scenarios() placeholder
├── test_dependency_analyzer_steps.py       12 lines  ❌ Just scenarios() placeholder
└── test_route_executor_steps.py            12 lines  ❌ Just scenarios() placeholder
```

**Problem:** All 299 step definitions crammed into conftest.py, violating separation of concerns.

### **AFTER: Proper Organization (CORRECT)**

```
tests/bdd/steps/trading/_trading_module/
├── conftest.py                           347 lines  ✅ Shared fixtures + 8 Background steps
├── test_market_service_steps.py         ~400 lines  ✅ 17 scenarios, ~60 steps
├── test_circuit_breaker_steps.py        ~500 lines  ✅ 20 scenarios, ~70 steps
├── test_trade_executor_steps.py         ~550 lines  ✅ 17 scenarios, ~65 steps
├── test_dependency_analyzer_steps.py    ~450 lines  ✅ 19 scenarios, ~55 steps
└── test_route_executor_steps.py         ~600 lines  ✅ 20 scenarios, ~70 steps
```

**Improvement:** Proper separation - each feature's steps in its own file, shared fixtures in conftest.py.

## Step Usage Analysis

### Total Steps Inventory

**Old file (test_trading_module_steps.py.old):** 3,382 lines, 288 step definitions
- @given: 87 step definitions
- @when:  19 step definitions
- @then:  182 step definitions

### Feature Distribution

| Feature | File | Scenarios | Est. Steps | Focus Areas |
|---------|------|-----------|------------|-------------|
| Market Service | test_market_service_steps.py | 17 | ~60 | Price estimation, database updates, freshness |
| Circuit Breaker | test_circuit_breaker_steps.py | 20 | ~70 | Profitability, batching, volatility |
| Trade Executor | test_trade_executor_steps.py | 17 | ~65 | Buy/sell execution, cargo tracking |
| Dependency Analyzer | test_dependency_analyzer_steps.py | 19 | ~55 | Dependencies, skip logic, cargo flow |
| Route Executor | test_route_executor_steps.py | 20 | ~70 | Route orchestration, navigation, metrics |
| **TOTAL** | | **93** | **~320** | |

### Shared vs Feature-Specific Breakdown

**Shared Steps (in conftest.py):** 8 Background steps
- `setup_database` - Market service background
- `setup_logger` - Market service background
- `setup_mock_api` - Circuit breaker background
- `setup_mock_db` - Trade/route executor background
- `setup_mock_ship` - Trade/route executor background
- Plus 5 fixtures: context, mock_database, mock_api_client, mock_ship_controller, logger_instance

**Feature-Specific Steps:** ~280 steps distributed across 5 files

## Completed Work

### ✅ Phase 1: Backup
```bash
cp conftest.py conftest.py.bloated_backup  # 3,566 lines preserved
```

### ✅ Phase 2: Create Minimal conftest.py

**New conftest.py:** 347 lines (90% reduction from 3,566 lines)

**Contents:**
1. **Imports** (lines 1-20): Core pytest-bdd and trading module imports
2. **Shared Fixtures** (lines 27-311):
   - `context()` - Shared test context dictionary
   - `mock_database()` - Mock database with transaction support
   - `mock_api_client()` - Mock API with profitable market defaults
   - `mock_ship_controller()` - Mock ship with cargo tracking
   - `logger_instance()` - Mock logger with memory handler
3. **Common Background Steps** (lines 314-347):
   - 5 @given steps for Background sections shared across features

**Key Features:**
- Stateful mock_ship_controller with cargo tracking (buy/sell updates inventory)
- Mock database with _market_data dict for verification
- Comprehensive market price data for all test goods
- Proper context manager support for database connections

## Remaining Work: Create Feature-Specific Step Files

### Reference File

**Source:** `test_trading_module_steps.py.old` (3,382 lines)

This file contains all steps organized by feature with clear section markers:
- Line 294: `# Market Service Steps`
- Line 926: Circuit breaker steps begin
- Line 1618: Trade executor steps begin
- Line 2264: Dependency analyzer steps begin
- Line 2573: Route executor steps begin

### File Creation Guide

Each feature file should follow this structure:

```python
"""
BDD Step Definitions for [Feature Name]

Tests for [specific domain functionality]
"""

# Minimal imports (only what this feature needs)
from pytest_bdd import scenarios, given, when, then, parsers
from spacetraders_bot.operations._trading import (
    # Feature-specific imports only
)

# Load ONLY this feature's scenarios
scenarios('../../../../bdd/features/trading/_trading_module/[feature].feature')

# ===========================
# Feature-Specific Steps
# ===========================

# All @given, @when, @then definitions for this feature
# NO shared fixtures (those are in conftest.py)
```

### 1. test_market_service_steps.py (~400 lines)

**Source:** Extract from test_trading_module_steps.py.old lines 294-925

**Scenarios:** Load `market_service.feature` (17 scenarios)

**Required Imports:**
```python
from spacetraders_bot.operations._trading import (
    estimate_sell_price_with_degradation,
    find_planned_sell_price,
    find_planned_sell_destination,
    update_market_price_from_transaction,
    validate_market_data_freshness,
    TradeAction,
    RouteSegment,
    MultiLegRoute,
)
```

**Step Categories:**
1. **Price Degradation Estimation** (~10 steps)
   - @given('a base price of {price:d} credits per unit')
   - @when('estimating sell price for {units:d} units')
   - @then('the effective price should be {expected:d} credits per unit')
   - @then('the degradation should be approximately {pct:f} percent')

2. **Find Planned Sell Price** (~15 steps)
   - @given('a multi-leg route with {count:d} segments')
   - @given('segment {idx:d} has BUY action for "{good}" at {price:d} credits')
   - @given('segment {idx:d} has SELL action for "{good}" at {price:d} credits')
   - @when('finding planned sell price for "{good}" from segment {idx:d}')
   - @then('the planned sell price should be {price:d} credits per unit')

3. **Find Planned Sell Destination** (~5 steps)
   - @given('segment {idx:d} has BUY action for "{good}" at waypoint "{waypoint}"')
   - @given('no sell actions for "{good}" in remaining segments')
   - @when('finding planned sell destination for "{good}" from segment {idx:d}')
   - @then('the planned sell destination should be "{waypoint}"')

4. **Market Price Updates from Transactions** (~10 steps)
   - @given('a market database with existing data for waypoint "{waypoint}" and good "{good}"')
   - @given('a market database with no data for waypoint "{waypoint}" and good "{good}"')
   - @when('updating market price from PURCHASE transaction')
   - @when('updating market price from SELL transaction')
   - @and('the transaction price is {price:d} credits per unit')
   - @then('the database should update sell_price to {price:d}')
   - @then('a new market data entry should be created')

5. **Market Data Freshness Validation** (~20 steps)
   - @given('segment {idx:d} requires "{good}" at waypoint "{waypoint}"')
   - @and('market data for "{good}" at "{waypoint}" updated {minutes:d} minutes ago')
   - @and('market data for "{good}" at "{waypoint}" updated {hours:f} hours ago')
   - @and('no market data exists for "{good}" at "{waypoint}"')
   - @when('validating market data freshness with {threshold:f} hour stale threshold')
   - @and('aging threshold is {threshold:f} hours')
   - @then('{count:d} stale market(s) should be reported')
   - @then('{count:d} aging market(s) should be reported')
   - @then('stale market should be "{waypoint}" "{good}" aged {hours:f} hours')

### 2. test_circuit_breaker_steps.py (~500 lines)

**Source:** Extract profitability validation and batch sizing steps

**Scenarios:** Load `circuit_breaker.feature` (20 scenarios)

**Required Imports:**
```python
from spacetraders_bot.operations._trading import (
    ProfitabilityValidator,
    calculate_batch_size,
    TradeAction,
    RouteSegment,
    MultiLegRoute,
)
```

**Step Categories:**
1. **Profitability Validation Setup** (~15 steps)
   - @given('a profitability validator with logger')
   - @given('a multi-leg route with planned sell prices')
   - @given('a BUY action for "{good}" at {price:d} credits per unit')
   - @given('the planned sell price is {price:d} credits per unit')
   - @given('no planned sell price exists')
   - @given('the live market price is {price:d} credits per unit')

2. **Profitability Checks** (~25 steps)
   - @when('validating purchase profitability for {units:d} units')
   - @when('validating purchase profitability for {units:d} units with degradation')
   - @then('profit margin should be {margin:d} credits per unit')
   - @then('profit margin percentage should be {pct:f} percent')
   - @then('price change should be {pct:f} percent')
   - @then('expected sell price after degradation should be {price:d} credits')
   - @then('loss would be {loss:d} credits per unit')

3. **Volatility Detection** (~15 steps)
   - @then('a price change warning should be logged')
   - @then('a high volatility warning should be logged')
   - @then('price spike should be {pct:f} percent')
   - @then('error message should contain "Extreme volatility"')
   - @then('error message should contain "no planned sell price"')

4. **Market API Failures** (~10 steps)
   - @given('the market API throws an exception')
   - @given('the market API returns None')
   - @given('the market API returns data without "{good}"')
   - @then('error message should contain "Market API failure"')
   - @then('error message should contain "Market data unavailable"')
   - @then('error message should contain "No live price data"')
   - @then('purchase should be blocked')
   - @then('purchase should be blocked for safety')

5. **Batch Size Calculation** (~15 steps)
   - @given('a good priced at {price:d} credits per unit')
   - @when('calculating batch size')
   - @then('batch size should be {size:d} units')
   - @then('rationale should be "{rationale}"')
   - @then('rationale should be "minimal risk strategy"')
   - @then('rationale should be "cautious approach"')
   - @then('rationale should be "default batching"')
   - @then('rationale should be "bulk efficiency"')

### 3. test_trade_executor_steps.py (~550 lines)

**Source:** Extract buy/sell execution and cargo tracking steps

**Scenarios:** Load `trade_executor.feature` (17 scenarios)

**Required Imports:**
```python
from spacetraders_bot.operations._trading import (
    TradeExecutor,
    TradeAction,
    ProfitabilityValidator,
)
```

**Step Categories:**
1. **Ship Setup** (~15 steps)
   - @given('ship has {capacity:d} cargo capacity')
   - @given('ship has empty cargo')
   - @given('ship has {units:d} units of existing cargo')
   - @given('ship has cargo with {units:d} units of "{good}"')
   - @given('ship starts at "{waypoint}" with {credits:d} credits')

2. **Trade Action Setup** (~20 steps)
   - @given('a trade executor in system "{system}"')
   - @given('a BUY action for "{good}" at waypoint "{waypoint}"')
   - @given('a SELL action for "{good}" at waypoint "{waypoint}"')
   - @given('buy quantity is {units:d} units at {price:d} credits per unit')
   - @given('buy quantity is {units:d} units at planned price {price:d} credits per unit')
   - @given('sell quantity is {units:d} units at {price:d} credits per unit')
   - @given('sell quantity is {units:d} units at planned price {price:d} credits per unit')
   - @given('batch size is {size:d} units')
   - @given('actual market price is {price:d} credits per unit')
   - @given('actual transaction price is {price:d} credits per unit')

3. **Profitability Validation Integration** (~10 steps)
   - @given('profitability validator rejects purchase')
   - @given('profitability validator approves purchase')

4. **Buy Action Execution** (~20 steps)
   - @when('executing buy action')
   - @when('executing buy action with batching')
   - @then('purchase should execute as single transaction')
   - @then('ship should buy {units:d} units of "{good}"')
   - @then('{count:d} batches should be executed')
   - @then('each batch should purchase {units:d} units')
   - @then('batch {idx:d} should complete with {units:d} units')
   - @then('remaining batches should be skipped')
   - @then('total units purchased should be {units:d}')
   - @then('purchase should fail with cargo space error')
   - @then('only {units:d} units should be purchased')
   - @then('purchase should be blocked')
   - @then('no units should be purchased')

5. **Sell Action Execution** (~15 steps)
   - @when('executing sell action')
   - @then('ship should sell {units:d} units of "{good}"')
   - @then('cargo should be empty after sale')
   - @then('sale should fail')
   - @then('a price difference warning should be logged')
   - @then('price difference should be {pct:f} percent')

6. **Cost/Revenue Tracking** (~15 steps)
   - @then('total cost should be {cost:d} credits')
   - @then('total revenue should be {revenue:d} credits')
   - @then('total revenue should be {revenue:d} credits (actual price)')

7. **Database Updates** (~15 steps)
   - @then('database should be updated with purchase price')
   - @then('database should be updated with sell price')
   - @then('database should be updated after each batch')
   - @then('database should be updated with PURCHASE transaction')
   - @then('database should be updated with SELL transaction')
   - @then('database sell_price should be {price:d} credits')
   - @then('database purchase_price should be {price:d} credits')
   - @then('database sell_price should remain unchanged')
   - @then('database purchase_price should remain unchanged')
   - @then('last_updated should be current timestamp')

8. **Operation Results** (~10 steps)
   - @then('operation should succeed')
   - @then('operation should fail')
   - @then('operation should return partial success')
   - @then('operation should succeed trivially')

9. **Batch Logging** (~10 steps)
   - @then('log should contain "minimal risk strategy"')
   - @then('log should contain "high-value good ≥2000 cr/unit"')
   - @then('log should contain "default batching"')
   - @then('log should contain "efficiency mode"')

10. **Ship Controller Failures** (~10 steps)
    - @given('ship controller buy() returns None')
    - @given('ship controller sell() returns None')

### 4. test_dependency_analyzer_steps.py (~450 lines)

**Source:** Extract dependency analysis and skip logic steps

**Scenarios:** Load `dependency_analyzer.feature` (19 scenarios)

**Required Imports:**
```python
from spacetraders_bot.operations._trading import (
    analyze_route_dependencies,
    should_skip_segment,
    cargo_blocks_future_segments,
    MultiLegRoute,
    RouteSegment,
    TradeAction,
)
```

**Step Categories:**
1. **Route Setup** (~10 steps)
   - @given('a multi-leg trading route')
   - Uses shared @given('segment {idx:d}: BUY {units:d} {good} at {waypoint}') from circuit breaker
   - Uses shared @given('segment {idx:d}: SELL {units:d} {good} at {waypoint}') from circuit breaker

2. **Dependency Analysis** (~5 steps)
   - @when('analyzing route dependencies')
   - @when('evaluating if segment {idx:d} should be skipped')
   - @when('evaluating affected segments')
   - @when('checking if cargo blocks future segments')

3. **Dependency Type Assertions** (~20 steps)
   - @then('segment {idx:d} should have dependency type "{dep_type}"')
   - @then('segment {idx:d} should have can_skip={value}')
   - @then('all segments should have can_skip=True')
   - @then('segment {idx:d} should have no dependencies')
   - @then('segment {idx:d} should depend on segment {dep_idx:d}')
   - @then('segment {idx:d} should depend on segment {dep_idx:d} for {good}')
   - @then('segment {idx:d} should NOT depend on segment {dep_idx:d}')
   - @then('segment {idx:d} should depend on both segment {dep1:d} and segment {dep2:d}')

4. **Cargo Requirements** (~15 steps)
   - @then('segment {idx:d} should require {units:d} {good} from prior segments')
   - @then('segment {idx:d} should require {units:d} {good} from segment {source_idx:d}')
   - @then('segment {idx:d} required_cargo should be {units:d} {good}')
   - @then('segment {idx:d} required_cargo should be {{good}: {units:d}}')
   - @then('segment {idx:d} should consume from segment {source_idx:d} first (FIFO)')
   - @then('segment {idx:d} should consume remaining from segment {source_idx:d}')
   - @then('segment {idx:d} should have unfulfilled requirement of {units:d} {good}')

5. **Skip Decision Logic** (~20 steps)
   - @given('segment {idx:d} fails due to {reason}')
   - @given('segment {idx:d} fails')
   - @given('route total has {count:d} independent segments remaining')
   - @given('remaining profit is {profit:d} credits')
   - @given('remaining independent segments profit is {profit:d} credits')
   - @given('minimum profit threshold is {threshold:d} credits')
   - @then('skip decision should be TRUE')
   - @then('skip decision should be FALSE')
   - @then('reason should contain "{text}"')
   - @then('segments {seg1:d}, {seg2:d} can still execute')
   - @then('segment {idx:d} should be affected')
   - @then('segment {idx:d} should NOT be affected')
   - @then('segments {seg1:d} and {seg2:d} can continue')

6. **Cargo Blocking** (~15 steps)
   - @given('segment {idx:d}: BUY {units:d} {good} at {waypoint} (completed)')
   - @given('segment {idx:d}: BUY {units:d} {good} at {waypoint} (planned)')
   - @given('segment {idx:d}: SELL {units:d} {good} at {waypoint} (completed)')
   - @given('ship has {capacity:d} cargo capacity')
   - @given('ship currently has {units:d} {good} in cargo')
   - @given('ship currently has {units:d} {good} in cargo (stranded)')
   - @given('ship has empty cargo')
   - @given('remaining cargo space is {space:d} units')
   - @then('cargo should NOT block segment {idx:d}')
   - @then('cargo SHOULD block segment {idx:d}')
   - @then('cargo should NOT block any segment')
   - @then('segment {idx:d} requires exactly {units:d} units')
   - @then('segment {idx:d} requires {units:d} units but only {available:d} available')
   - @then('all {units:d} units available for purchase')

7. **Credit Dependencies** (~10 steps)
   - @given('agent starts with {credits:d} credits')
   - @given('segment {idx:d}: SELL {units:d} {good} at {waypoint} (revenue: {revenue:d} credits)')
   - @given('segment {idx:d}: BUY {units:d} {good} at {waypoint} (cost: {cost:d} credits)')
   - @then('segment {idx:d} should require {credits:d} credits')
   - @then('segment {idx:d} implicitly depends on segment {dep_idx:d} revenue')
   - @then('segment {idx:d} cannot execute if segment {dep_idx:d} fails')
   - @then('segments should be credit-independent')
   - @then('both can execute without revenue dependency')

### 5. test_route_executor_steps.py (~600 lines)

**Source:** Extract route orchestration and execution steps

**Scenarios:** Load `route_executor.feature` (20 scenarios)

**Required Imports:**
```python
from spacetraders_bot.operations._trading import (
    RouteExecutor,
    MultiLegRoute,
    RouteSegment,
    TradeAction,
)
```

**Step Categories:**
1. **Route Executor Setup** (~10 steps)
   - @given('a route executor for player {player_id:d}')
   - @given('a multi-leg route matching real execution:')  # table format

2. **Route Segment Setup** (~25 steps)
   - @given('segment {idx:d}: {from_waypoint} → {to_waypoint}, BUY {units:d} {good} @ {price:d} cr/unit')
   - @given('segment {idx:d}: {from_waypoint} → {to_waypoint}, SELL {units:d} {good} @ {price:d} cr/unit')
   - @given('segment {idx:d}: {from_waypoint} → {to_waypoint}, SELL ... BUY ...')  # compound actions
   - Uses shared segment setup steps from dependency analyzer

3. **Market Data Setup** (~10 steps)
   - @given('all market data is fresh (<30 minutes old)')
   - @given('all market data is fresh')
   - @given('segment {idx:d} requires {good} at waypoint {waypoint} (updated {minutes:d} min ago)')
   - @given('segment {idx:d} requires {good} at waypoint {waypoint} (updated {hours:f} hours ago)')

4. **Failure Simulation** (~15 steps)
   - @given('segment {idx:d} fails due to {reason}')
   - @given('navigation to {waypoint} fails (out of fuel)')
   - @given('navigation succeeds but docking fails')
   - @given('ship.get_status() returns None')
   - @given('api.get_agent() returns None')

5. **Route Execution** (~5 steps)
   - @when('executing the route')
   - @when('route execution completes')

6. **Pre-Flight Validation Assertions** (~15 steps)
   - @then('pre-flight validation should pass')
   - @then('pre-flight validation should fail')
   - @then('pre-flight validation should pass with warnings')
   - @then('no stale markets should be detected')
   - @then('stale market should be reported: {waypoint} {good}')
   - @then('aging market should be reported: {waypoint} {good}')
   - @then('route execution should abort before segment {idx:d}')
   - @then('no navigation should occur')

7. **Dependency Analysis Assertions** (~10 steps)
   - @then('dependency analysis should show segment {idx:d} as INDEPENDENT')
   - @then('segment {idx:d} should depend on segment [idx_list]')
   - @then('segment {idx:d} should have dependency type {dep_type}')
   - @then('dependency map should be logged')

8. **Navigation Assertions** (~15 steps)
   - @then('navigation should succeed for both segments')
   - @then('navigation should succeed for all {count:d} segments')
   - @then('ship should navigate to {waypoint}')
   - @then('ship should dock at {waypoint}')
   - @then('navigation failure aborts route execution')
   - @then('docking failure aborts route execution')

9. **Trade Action Assertions** (~10 steps)
   - @then('all trade actions should execute successfully')
   - @then('all {count:d} segments should execute successfully')
   - @then('BUY {good} should be logged as failed')
   - @then('BUY {good} should still be attempted')

10. **Segment Skipping Assertions** (~15 steps)
    - @then('segment {idx:d} should be skipped')
    - @then('segment {idx:d} should execute successfully')
    - @then('{count:d} segments should be marked as skipped')
    - @then('route execution should abort')
    - @then('error should indicate "{text}"')
    - @then('no subsequent segments should execute')

11. **Metrics Tracking Assertions** (~25 steps)
    - @then('metrics should show {revenue:d} revenue and {costs:d} costs')
    - @then('total revenue should be {revenue:d} credits')
    - @then('total costs should be {cost:d} credits')
    - @then('actual profit should be {profit:d} credits')
    - @then('actual profit should be approximately {profit:d} credits')
    - @then('estimated profit should be {profit:d} credits')
    - @then('accuracy should be {pct:f} percent')
    - @then('accuracy should be logged')
    - @then('metrics should be logged in final summary')

12. **Success/Failure Assertions** (~15 steps)
    - @then('route execution should succeed')
    - @then('route execution should fail')
    - @then('route execution should fail immediately')
    - @then('route execution should fail at segment {idx:d}')
    - @then('segment {idx:d} should not be attempted')
    - @then('no segments should be attempted')
    - @then('error should indicate "Failed to get ship status"')
    - @then('error should indicate "Failed to get agent data"')

13. **Final Summary Assertions** (~10 steps)
    - @then('final summary should include:')  # table format
    - @then('final summary should show {skipped:d}/{total:d} segments skipped')
    - @then('skip details should be logged')

14. **Route Continuation Logic** (~5 steps)
    - @then('route should continue execution')
    - @then('route execution should proceed')
    - @then('route execution should proceed with caution')

## Implementation Steps

### Step 1: Delete Current Placeholder Files
```bash
cd tests/bdd/steps/trading/_trading_module/
rm test_market_service_steps.py
rm test_circuit_breaker_steps.py
rm test_trade_executor_steps.py
rm test_dependency_analyzer_steps.py
rm test_route_executor_steps.py
```

### Step 2: Extract Feature-Specific Steps

For each feature file, use `test_trading_module_steps.py.old` as the source:

**Method A: Manual extraction (recommended for control)**
1. Copy the header (imports) from old file
2. Add `scenarios('../../../../bdd/features/trading/_trading_module/[feature].feature')`
3. Extract relevant step definitions by searching for feature-specific patterns
4. Remove any duplicate fixtures (already in conftest.py)
5. Organize with clear section comments

**Method B: Automated extraction (faster but needs review)**
```python
# Script to extract steps by pattern matching
import re

def extract_feature_steps(old_file, feature_name, patterns):
    with open(old_file, 'r') as f:
        content = f.read()

    # Extract header
    header = content.split('@given')[0]

    # Find all step definitions matching patterns
    step_defs = []
    for pattern in patterns:
        matches = re.findall(rf'@(?:given|when|then).*?{pattern}.*?(?=@(?:given|when|then)|$)',
                            content, re.DOTALL)
        step_defs.extend(matches)

    # Combine
    return header + '\n\n'.join(step_defs)
```

### Step 3: Test Each Feature File
```bash
# After creating each file, test it
python3 -m pytest tests/bdd/steps/trading/_trading_module/test_market_service_steps.py -v
# Repeat for each feature file
```

### Step 4: Cleanup
```bash
# Once all tests pass, remove backups
rm conftest.py.bloated_backup  # Optional: keep for reference
```

## Verification Checklist

- [ ] All 5 feature step files created
- [ ] Each file has correct scenarios() call pointing to its .feature file
- [ ] No duplicate fixtures (fixtures only in conftest.py)
- [ ] All feature files run independently without errors
- [ ] Total test count: 93 scenarios (17+20+17+19+20)
- [ ] conftest.py is ~347 lines (NOT 3,566 lines)
- [ ] Each feature file is 400-600 lines (NOT 12 lines)
- [ ] All tests pass: `python3 -m pytest tests/bdd/steps/trading/_trading_module/ -v`

## Success Metrics

**Before:**
- conftest.py: 3,566 lines ❌
- Feature files: 12 lines each ❌
- Separation of concerns: POOR ❌

**After:**
- conftest.py: 347 lines ✅ (90% reduction)
- Feature files: 400-600 lines each ✅ (proper organization)
- Separation of concerns: EXCELLENT ✅

**Total LOC:** ~3,247 lines (slightly less than 3,382 due to consolidation)

## File Reference

**Backup files:**
- `conftest.py.bloated_backup` - Original 3,566-line conftest.py (for reference)
- `test_trading_module_steps.py.old` - Original 3,382-line organized file (source for extraction)

**Current files:**
- `conftest.py` - 347 lines ✅ (shared fixtures only)
- `test_market_service_steps.py` - ~400 lines ⏳ (to be created)
- `test_circuit_breaker_steps.py` - ~500 lines ⏳ (to be created)
- `test_trade_executor_steps.py` - ~550 lines ⏳ (to be created)
- `test_dependency_analyzer_steps.py` - ~450 lines ⏳ (to be created)
- `test_route_executor_steps.py` - ~600 lines ⏳ (to be created)

## Next Steps

1. Extract step definitions from `test_trading_module_steps.py.old` using the patterns described above
2. Create each of the 5 feature-specific step files
3. Test each file individually
4. Run full test suite
5. Document any remaining issues
6. Clean up backup files (optional)

---

**Status:** Reorganization 40% complete
- ✅ Analysis complete
- ✅ Shared fixtures extracted to conftest.py
- ⏳ Feature-specific files need creation (manual extraction from .old file)

# BDD Step File Reorganization - Status Report

## Objective
Split the monolithic `test_trading_module_steps.py` (3,382 lines) into 5 feature-specific files.

## Files Created

| File | Lines | Purpose | Status |
|------|-------|---------|--------|
| `conftest.py` | 347 | Shared fixtures and Background steps | ✅ Complete |
| `test_market_service_steps.py` | 1,003 | Price estimation, market validation | ⚠️ Partial |
| `test_circuit_breaker_steps.py` | 272 | Profitability validation, batch sizing | ⚠️ Partial |
| `test_trade_executor_steps.py` | 179 | Buy/sell execution, database updates | ⚠️ Partial |
| `test_dependency_analyzer_steps.py` | 375 | Route dependencies, segment skipping | ⚠️ Partial |
| `test_route_executor_steps.py` | 2,048 | Multi-segment execution, error handling | ❌ Collection error |

**Total Lines:** 4,224 (original: 3,382 + new structure)

## Test Results

### Current Status
- **Market Service:** 7/17 passing (41%)
- **Circuit Breaker:** 0/20 passing (missing steps)
- **Trade Executor:** 0/17 passing (missing steps)
- **Dependency Analyzer:** 0/19 passing (missing steps)
- **Route Executor:** Collection error (TypeError: unhashable type: '_Call')

### Issues Identified

1. **Step Definition Overlap**
   - Many steps are used across multiple features
   - Current extraction created incomplete sets
   - Steps like "a multi-leg route with planned sell prices" needed in multiple files

2. **Route Executor Collection Error**
   - `TypeError: unhashable type: '_Call'` when loading scenarios
   - Related to `from unittest.mock import call` import
   - File compiles but pytest-bdd cannot collect tests

3. **Missing Shared Steps**
   - Steps for route setup (multi-leg routes, segments, actions)
   - Market data setup steps
   - Validation and assertion steps
   - These appear in lines 927-2575 of original file

## Root Cause Analysis

The original `test_trading_module_steps.py` had significant **cross-feature dependencies**:

- Route setup steps (lines 927-2575) used by ALL features
- Market data steps (lines 2471-2575) used by market_service AND route_executor  
- Trade action steps (lines 1675-1725) used by trade_executor AND route_executor
- Assertion steps (lines 2265-3353) shared across all features

## Recommended Solution

### Option 1: Add Shared Steps Module (RECOMMENDED)
```
conftest.py                    # Fixtures only
test_shared_steps.py          # Steps used by 2+ features
test_market_service_steps.py  # Feature-specific steps only
test_circuit_breaker_steps.py
test_trade_executor_steps.py
test_dependency_analyzer_steps.py  
test_route_executor_steps.py
```

Move lines 927-2575 (route setup, assertions, market data) to `test_shared_steps.py`.

### Option 2: Keep Monolithic (FALLBACK)
Rename `test_trading_module_steps.py.old` back to `test_trading_module_steps.py` and abandon split.

### Option 3: Complete the Current Approach
Duplicate shared steps across all feature files (violates DRY but ensures independence).

## Next Steps

1. **Immediate:** Create `test_shared_steps.py` with lines 927-2575 from original
2. **Fix Route Executor:** Remove or rename `call` import conflict
3. **Test Incrementally:** Verify each feature file independently
4. **Final Validation:** Run all 93 tests together

## Time Estimate
- Option 1 (shared steps): 30-45 minutes
- Option 2 (revert): 5 minutes
- Option 3 (duplicate): 60-90 minutes


# BDD Step File Size Analysis

## Critical Issues: Oversized Step Files

### The Problem

**You're absolutely right** - while step reuse is good, some step files have become **monolithic monsters** that violate the Single Responsibility Principle.

## Size Analysis

| Step File | Lines | Steps | Features Handled | Status |
|-----------|-------|-------|------------------|--------|
| **test_trading_module_steps.py** | 3,382 | 288 | **5** | 🚨 **CRITICAL** |
| **test_operations_steps.py** | 1,399 | 139 | 1 (but many concerns) | 🟠 **TOO LARGE** |
| **test_route_planner_steps.py** | 1,210 | 82 | 1 | 🟠 **TOO LARGE** |
| **test_cargo_salvage_steps.py** | 1,128 | 96 | 1 | 🟠 **TOO LARGE** |
| test_ship_controller_steps.py | 975 | 71 | Multiple | 🟡 Borderline |
| test_assignment_steps.py | 773 | 68 | 1 | 🟡 Borderline |
| test_scouting_classes_steps.py | 726 | 66 | 1 | 🟡 Borderline |
| test_evaluation_strategies_steps.py | 719 | 65 | 1 | 🟡 Borderline |
| test_smart_navigator_steps.py | 711 | 50 | Multiple | ✅ OK |

**Recommended maximum:** 500 lines, 40-50 step definitions per file

## Worst Offender: test_trading_module_steps.py

**Size:** 3,382 lines with 288 step definitions

**Handles 5 separate features:**
1. `market_service.feature` - NO DEDICATED STEP FILE
2. `circuit_breaker.feature` - NO DEDICATED STEP FILE
3. `trade_executor.feature` - NO DEDICATED STEP FILE
4. `dependency_analyzer.feature` - NO DEDICATED STEP FILE
5. `route_executor.feature` - NO DEDICATED STEP FILE

**Meanwhile, in the same directory:**
- ✅ `cargo_salvage.feature` → `test_cargo_salvage_steps.py` (1,128 lines)
- ✅ `evaluation_strategies.feature` → `test_evaluation_strategies_steps.py` (719 lines)
- ✅ `fleet_optimizer.feature` → `test_fleet_optimizer_steps.py` (544 lines)
- ✅ `market_repository.feature` → `test_market_repository_steps.py` (586 lines)
- ✅ `route_planner.feature` → `test_route_planner_steps.py` (1,210 lines)

**Backup files found:** `.bak`, `.bak2`, `.bak3` - suggests previous refactoring attempts!

### Why This Is a Problem

1. **Impossible to navigate** - finding a specific step in 3,382 lines is painful
2. **Merge conflicts** - multiple developers editing the same file
3. **Slow test discovery** - pytest has to parse 288 step definitions
4. **Violates SRP** - one file handling 5 different concerns
5. **Hard to maintain** - changes to one feature affect all features
6. **Cognitive overload** - developers need to understand all 5 contexts

## Second Offender: test_operations_steps.py

**Size:** 1,399 lines with 139 step definitions

**Handles:** `unit/operations.feature` (single file)

**Why it's too large:** Likely testing many different operations modules:
- Mining operations
- Trading operations
- Contract operations
- Fleet operations
- Daemon operations
- etc.

## Recommended Refactoring Strategy

### Phase 1: Split test_trading_module_steps.py (CRITICAL)

**Create 5 new step files:**

```bash
# Create dedicated step files for each feature
tests/bdd/steps/trading/_trading_module/
  test_market_service_steps.py       # Extract market_service steps
  test_circuit_breaker_steps.py      # Extract circuit_breaker steps
  test_trade_executor_steps.py       # Extract trade_executor steps
  test_dependency_analyzer_steps.py  # Extract dependency_analyzer steps
  test_route_executor_steps.py       # Extract route_executor steps
```

**Estimated sizes:**
- Each file: ~600-700 lines, 50-60 step definitions
- Much more manageable and focused

### Phase 2: Split test_operations_steps.py

**Create domain-specific step files:**

```bash
tests/bdd/steps/unit/
  test_operations_mining_steps.py     # Mining-specific operations
  test_operations_trading_steps.py    # Trading-specific operations
  test_operations_contracts_steps.py  # Contract-specific operations
  test_operations_fleet_steps.py      # Fleet-specific operations
  test_operations_daemon_steps.py     # Daemon-specific operations
  test_operations_core_steps.py       # Core/shared operations
```

**Estimated sizes:**
- Each file: ~200-250 lines, 20-25 step definitions

### Phase 3: Consider Splitting Large Feature-Specific Files

Files like `test_route_planner_steps.py` (1,210 lines) and `test_cargo_salvage_steps.py` (1,128 lines) might benefit from splitting by concern:

```bash
# Example for route_planner
test_route_planner_core_steps.py         # Core planning logic
test_route_planner_optimization_steps.py # Optimization algorithms
test_route_planner_validation_steps.py   # Route validation

# Example for cargo_salvage
test_cargo_salvage_detection_steps.py    # Salvage detection
test_cargo_salvage_execution_steps.py    # Salvage execution
```

### Phase 4: Extract Shared Steps

Create a **shared steps module** for commonly reused steps:

```bash
tests/bdd/steps/shared/
  common_ship_steps.py       # Given a ship at waypoint, ship has fuel, etc.
  common_market_steps.py     # Given a market, market sells X, etc.
  common_navigation_steps.py # When ship navigates, Then ship arrives, etc.
  common_cargo_steps.py      # Given cargo contains, When ship loads, etc.
```

**Benefits:**
- Reduce duplication across specialized step files
- Single source of truth for common steps
- Smaller specialized files

## Refactoring Guidelines

### Rule 1: One Feature → One Step File (with exceptions)

**Exception:** Shared steps can be in a `shared/` module

### Rule 2: Maximum Size Limits

- **Lines:** 500-700 max per file
- **Step definitions:** 40-60 max per file
- **Features handled:** 1 per file (not 5!)

### Rule 3: Organize by Concern

Split by logical domains:
- Market operations
- Navigation operations
- Cargo operations
- Trading strategies
- etc.

### Rule 4: Extract Common Steps

If a step appears in 3+ feature files → extract to shared module

## Migration Plan

### Week 1: Critical Fix
- ✅ Split `test_trading_module_steps.py` into 5 files
- ✅ Test that all features still pass
- ✅ Remove `.bak` files once confirmed working

### Week 2: Operations Split
- ✅ Split `test_operations_steps.py` by domain
- ✅ Test that unit tests still pass

### Week 3: Large File Optimization
- ✅ Split `test_route_planner_steps.py`
- ✅ Split `test_cargo_salvage_steps.py`

### Week 4: Extract Shared Steps
- ✅ Create `shared/` module
- ✅ Move common steps
- ✅ Update imports in all step files

## Expected Benefits

After refactoring:

1. **Faster navigation** - find steps in seconds, not minutes
2. **Better organization** - clear mapping between features and steps
3. **Easier maintenance** - changes isolated to relevant files
4. **Faster tests** - pytest can parallelize better
5. **Fewer merge conflicts** - developers work on different files
6. **Better code review** - reviewers see only relevant steps
7. **New developer friendly** - easier to understand structure

## Conclusion

**You were right to question this!**

While step reuse is good, we went too far in the other direction and created monolithic step files. The current structure is:

- ❌ **Too much reuse** → monolithic files (test_trading_module_steps.py)
- ❌ **Not enough organization** → everything in one file

The ideal structure is:

- ✅ **One feature → one step file** (as you originally suggested!)
- ✅ **Shared steps extracted** → common module for reuse
- ✅ **Size-limited files** → max 500-700 lines

## Immediate Action

**Recommend starting with:** Splitting `test_trading_module_steps.py` (3,382 lines → 5 files of ~600 lines each)

This will immediately improve:
- Maintainability
- Navigation
- Test discovery
- Code review

Would you like me to start this refactoring?

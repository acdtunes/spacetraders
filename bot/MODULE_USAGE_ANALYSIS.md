# Low-Coverage Module Usage Analysis

**Generated:** 2025-10-20
**Purpose:** Determine which low-coverage modules are actually used vs. dead code

---

## Summary

| Module | Coverage | LOC | Status | Usage | Priority |
|--------|----------|-----|--------|-------|----------|
| **multileg_trader.py** | 22.5% | 1879 | ✅ ACTIVE | CLI + Operations | HIGH |
| **contracts.py** | 12.4% | 541 | ✅ ACTIVE | CLI | MEDIUM |
| **scout_coordinator.py** (core) | 6.2% | 610 | ✅ ACTIVE | Core dependency | HIGH |
| **scout_coordination.py** (ops) | 5.9% | 160 | ✅ ACTIVE | CLI (5 commands) | MEDIUM |
| **routing_validator.py** | 23.6% | 103 | ✅ ACTIVE | CLI + Core | MEDIUM |
| **market_data.py** | 21.1% | 171 | ✅ ACTIVE | Core dependency (3 imports) | HIGH |
| **navigation.py** | 12.7% | 53 | ✅ ACTIVE | Operations export | LOW |
| **analysis.py** | 1.5% | 240 | ⚠️ EXPORTED | Rarely used | LOW |
| **mining_optimizer.py** | 6.1% | 92 | ⚠️ EXPORTED | Rarely used | LOW |
| **ortools_mining_optimizer.py** | 17.0% | 209 | ⚠️ USED | CLI (mining-optimize) | MEDIUM |
| **market_partitioning.py** | 16.4% | 196 | ⚠️ INTERNAL | Library code | LOW |
| **ortools_router.py** | 37.4% | 766 | ✅ CRITICAL | Core routing (4 imports) | CRITICAL |

---

## Detailed Analysis

### ✅ ACTIVELY USED - High Priority

#### 1. **multileg_trader.py** (22.5%, 1879 LOC)
**Status:** ACTIVE
**Usage:**
- **CLI Commands:** `trade`, `trade-plan`, `fleet-trade-optimize`
- **Operations:** `multileg_trade_operation`, `trade_plan_operation`, `fleet_trade_optimize_operation`
- **Import Count:** 1 (exported from operations)

**Why Low Coverage:**
- Massive file (1879 lines!)
- Complex trading logic with many edge cases
- Likely has significant dead code

**Recommendation:**
- **REFACTOR FIRST** - Extract into smaller modules (similar to mining refactor)
- Target modular pieces for 70%+ coverage
- Likely contains dead code paths

---

#### 2. **ortools_router.py** (37.4%, 766 LOC)
**Status:** CRITICAL DEPENDENCY
**Usage:**
- **Imported by:** smart_navigator, routing, ortools_mining_optimizer, market_partitioning
- **Core Functionality:** VRP/TSP solver for all routing operations

**Why Low Coverage:**
- Complex OR-Tools optimization logic
- Many edge cases in VRP/TSP solving
- Some paths only hit with specific graph configurations

**Recommendation:**
- **KEEP AS-IS** - Complex optimization logic, hard to test
- Focus on **regression tests** with known-good scenarios
- Current 37.4% may be acceptable for this complexity
- Document untested paths

---

#### 3. **scout_coordinator.py** (6.2%, 610 LOC)
**Status:** ACTIVE CORE MODULE
**Usage:**
- **Imported by:** scout_coordination.py (operations wrapper)
- **Core Functionality:** Multi-ship market intelligence coordination

**Why Low Coverage:**
- Complex coordination logic
- Not directly tested (operations wrapper tested instead)

**Recommendation:**
- **TEST VIA OPERATIONS** - Focus on scout_coordination.py (operations layer)
- Core module coverage will improve transitively

---

#### 4. **contracts.py** (12.4%, 541 LOC)
**Status:** ACTIVE
**Usage:**
- **CLI Command:** `contract`
- **Operations:** `contract_operation`, `negotiate_operation`
- **Import Count:** 2

**Why Low Coverage:**
- Large file (541 lines)
- Complex contract evaluation logic
- Many conditional branches

**Recommendation:**
- **MEDIUM PRIORITY** - Add BDD tests for contract workflows
- Target 60-70% coverage (some edge cases OK to skip)

---

#### 5. **market_data.py** (21.1%, 171 LOC)
**Status:** CORE DEPENDENCY
**Usage:**
- **Import Count:** 3
- **Imported by:** Operations layer for market queries
- **Core Functionality:** Market intelligence and price data

**Why Low Coverage:**
- Database queries (hard to test without integration tests)
- Many market filtering/querying functions

**Recommendation:**
- **HIGH PRIORITY** - Used by multiple operations
- Add unit tests with mock database
- Target 70%+ coverage

---

### ⚠️ EXPORTED BUT RARELY USED

#### 6. **analysis.py** (1.5%, 240 LOC)
**Status:** EXPORTED, RARELY USED
**Usage:**
- **Import Count:** 1 (self-import)
- **CLI:** Has `util` command but rarely called

**Why Low Coverage:**
- Utility/analysis functions
- Not part of main workflows

**Recommendation:**
- **LOW PRIORITY** - Skip or deprecate
- Only 1.5% coverage suggests it's mostly unused

---

#### 7. **mining_optimizer.py** (6.1%, 92 LOC)
**Status:** EXPORTED, RARELY USED
**Usage:**
- **Import Count:** 1 (exported from operations)
- **CLI:** Likely has command but not frequently used

**Why Low Coverage:**
- May be superseded by ortools_mining_optimizer

**Recommendation:**
- **LOW PRIORITY** - Consider deprecating in favor of ortools version
- Check if actually needed

---

### ⚠️ SPECIALIZED/INTERNAL

#### 8. **ortools_mining_optimizer.py** (17.0%, 209 LOC)
**Status:** ACTIVE (CLI)
**Usage:**
- **CLI Command:** `mining-optimize`
- **Import Count:** 1
- **Functionality:** OR-Tools based mining fleet optimization

**Why Low Coverage:**
- Specialized optimization logic
- Only used when explicitly requested

**Recommendation:**
- **MEDIUM PRIORITY** - Add regression tests with known scenarios
- Target 50-60% (optimization logic is complex)

---

#### 9. **market_partitioning.py** (16.4%, 196 LOC)
**Status:** INTERNAL LIBRARY
**Usage:**
- **Import Count:** 1 (uses ortools_router)
- **Functionality:** Fleet partitioning for market operations

**Why Low Coverage:**
- Specialized partitioning logic
- May be used internally by other modules

**Recommendation:**
- **LOW PRIORITY** - Internal library code
- Test via integration with modules that use it

---

#### 10. **routing_validator.py** (23.6%, 103 LOC)
**Status:** ACTIVE
**Usage:**
- **CLI Command:** `validate-routing`
- **Import Count:** 2
- **Functionality:** Validate routing predictions vs. actual results

**Why Low Coverage:**
- Validation logic requires live API calls
- Not frequently used (manual testing tool)

**Recommendation:**
- **MEDIUM PRIORITY** - Add unit tests for validation logic
- Target 60%+ coverage

---

#### 11. **navigation.py** (12.7%, 53 LOC)
**Status:** EXPORTED
**Usage:**
- **Import Count:** 2 (exported from operations)
- **Functionality:** Basic navigation operations

**Why Low Coverage:**
- Small file (53 lines)
- May be thin wrapper around smart_navigator

**Recommendation:**
- **LOW PRIORITY** - Small file, easy quick win
- Could reach 70%+ with minimal effort

---

#### 12. **scout_coordination.py** (5.9%, 160 LOC)
**Status:** ACTIVE (CLI)
**Usage:**
- **CLI Commands:** 5 coordinator operations (add-ship, remove-ship, start, status, stop)
- **Import Count:** 1
- **Functionality:** Operations wrapper for scout_coordinator.py (core)

**Why Low Coverage:**
- Thin operations wrapper
- Delegates to core/scout_coordinator.py

**Recommendation:**
- **MEDIUM PRIORITY** - Add BDD tests for CLI workflows
- Target 70%+ (should be straightforward)

---

## Prioritized Recommendations

### CRITICAL (Test First - Core Dependencies)
1. **ortools_router.py** (37.4%) - Already at ~40%, add regression tests
2. **market_data.py** (21.1%) - Core dependency, needs unit tests

### HIGH (Active CLI Commands)
3. **multileg_trader.py** (22.5%) - REFACTOR FIRST, then test modules
4. **scout_coordinator.py** (6.2%) - Test via scout_coordination operations

### MEDIUM (Used But Specialized)
5. **contracts.py** (12.4%) - Add BDD tests for contract workflows
6. **scout_coordination.py** (5.9%) - Add BDD tests for CLI operations
7. **routing_validator.py** (23.6%) - Add validation unit tests
8. **ortools_mining_optimizer.py** (17.0%) - Add regression tests

### LOW (Rarely Used / Small Files)
9. **navigation.py** (12.7%) - Quick win, small file
10. **market_partitioning.py** (16.4%) - Test via integration
11. **mining_optimizer.py** (6.1%) - Consider deprecating
12. **analysis.py** (1.5%) - Consider deprecating

---

## Dead Code Candidates

### Likely Dead Code (Check for Deprecation):
1. **analysis.py** (1.5%, 240 LOC) - Only 1.5% coverage, barely used
2. **mining_optimizer.py** (6.1%, 92 LOC) - Possibly superseded by ortools version

### Check for Unreachable Code:
1. **multileg_trader.py** (22.5%, 1879 LOC!) - Massive file, likely has dead paths
2. **contracts.py** (12.4%, 541 LOC) - Large file, low coverage
3. **scout_coordinator.py** (6.2%, 610 LOC) - Large file, very low coverage

---

## Testing Strategy by Module

### Option 1: Quick Wins (Already in Progress)
✅ routing.py (85.8%) - DONE
✅ assignments.py (79.0%) - 23 passing tests
- tour_mode.py (64.8%) - Next target
- scouting/executor.py (63.4%) - Next target

### Option 2: High-Impact Modules
Focus on core dependencies that affect many operations:
1. **market_data.py** (21.1%) → 70%
2. **ortools_router.py** (37.4%) → 50% (add regression tests)
3. **scout_coordination.py** (5.9%) → 70% (BDD tests)

### Option 3: Refactor & Test
1. **multileg_trader.py** - Refactor into smaller modules (like mining)
2. Test individual modules to 70%+

---

## Conclusion

**ALL low-coverage modules are actively used EXCEPT:**
- **analysis.py** (1.5%) - Possible dead code
- **mining_optimizer.py** (6.1%) - May be deprecated

**Most Impactful to Test:**
1. market_data.py - Core dependency (3 imports)
2. multileg_trader.py - Large CLI feature (needs refactor)
3. scout_coordinator.py - Multi-ship coordination core

**Quick Wins Available:**
- navigation.py (53 LOC, small)
- scout_coordination.py (160 LOC, operations wrapper)

**Generated with Claude Code**

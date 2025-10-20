# Code Coverage Report

**Generated:** 2025-10-19
**Test Suite:** BDD Tests (552 scenarios)
**Tests Run:** 81 passed, 1 xfailed

---

## 📊 Executive Summary

| Metric | Value |
|--------|-------|
| **Total Statements** | 9,794 |
| **Covered Lines** | 2,301 |
| **Missing Lines** | 7,493 |
| **Overall Coverage** | **23.5%** |

**Status:** 🔴 **Below Target** (Target: 50%+)

---

## 📦 Coverage by Package

| Package | Statements | Missing | Coverage | Status |
|---------|------------|---------|----------|--------|
| **helpers** | 39 | 5 | **87.2%** | 🟢 Excellent |
| **cli** | 349 | 74 | **78.8%** | 🟢 Good |
| **core** | 4,497 | 3,385 | **24.7%** | 🔴 Needs Work |
| **operations** | 4,781 | 3,913 | **18.2%** | 🔴 Critical |
| **__init__.py** | 12 | 0 | **100%** | 🟢 Perfect |
| **integrations** | 116 | 116 | **0.0%** | ⚫ Not Tested |

---

## 🔴 Critical Coverage Gaps

**Criteria:** Files with >100 statements and <30% coverage

### Top 15 Files Needing Attention

| File | Category | Statements | Coverage | Priority |
|------|----------|------------|----------|----------|
| **multileg_trader.py** | operations | 1,880 | 23.3% | 🔥 CRITICAL |
| **scout_coordinator.py** | core | 610 | 8.7% | 🔥 CRITICAL |
| **contracts.py** | operations | 541 | 7.0% | 🔥 CRITICAL |
| **routing_legacy.py** | core | 443 | 0.0% | ⚫ LEGACY |
| **ship_controller.py** | core | 347 | 7.2% | 🔥 CRITICAL |
| **routing.py** (ops) | operations | 344 | 3.5% | 🔥 CRITICAL |
| **captain_logging.py** | operations | 308 | 14.0% | 🔴 High |
| **mining.py** | operations | 299 | 20.1% | 🔴 High |
| **analysis.py** | operations | 240 | 2.1% | 🔥 CRITICAL |
| **daemon_manager.py** | core | 213 | 14.1% | 🔴 High |
| **ortools_mining_optimizer.py** | core | 209 | 22.5% | 🔴 High |
| **market_partitioning.py** | core | 196 | 21.9% | 🔴 High |
| **assignments.py** | operations | 191 | 5.2% | 🔥 CRITICAL |
| **market_data.py** | core | 171 | 13.5% | 🔴 High |
| **routing.py** (core) | core | 164 | 28.0% | 🟡 Medium |

**Total Critical Gaps:** 15 files representing **6,444 statements** with average **13.4% coverage**

---

## ✅ Well-Tested Modules

**Criteria:** Files with >50 statements and >80% coverage

**Result:** ⚠️ **No modules meet this criteria**

This indicates a significant gap in comprehensive testing of substantial modules.

### Best Performing Files (>50 statements)

| File | Category | Statements | Coverage |
|------|----------|------------|----------|
| **purchasing.py** | operations | 187 | 79.1% |
| **routing_config.py** | core | 87 | 73.7% |
| **main.py** | cli | 344 | 79.2% |

---

## 📈 Coverage Analysis by Domain

### Core Package (24.7% coverage)

**Key Modules:**
- ✅ `system_graph_provider.py` - 78% (well-tested)
- ✅ `routing_config.py` - 74% (good)
- 🔴 `scout_coordinator.py` - 9% (610 statements, critical gap)
- 🔴 `ship_controller.py` - 7% (347 statements, critical gap)
- 🔴 `daemon_manager.py` - 14% (213 statements, high priority)
- 🔴 `assignment_manager.py` - 15% (131 statements, high priority)

**Issues:**
- Large, complex modules with minimal testing
- Core functionality not adequately covered
- State machine logic needs comprehensive tests

### Operations Package (18.2% coverage)

**Key Modules:**
- ✅ `purchasing.py` - 79% (best in package)
- 🔴 `multileg_trader.py` - 23% (1,880 statements, largest file!)
- 🔴 `contracts.py` - 7% (541 statements, critical)
- 🔴 `mining.py` - 20% (299 statements, high priority)
- 🔴 `captain_logging.py` - 14% (308 statements, high priority)
- 🔴 `routing.py` - 3% (344 statements, critical)

**Issues:**
- Most operation handlers lack comprehensive tests
- Complex business logic not validated
- Error paths and edge cases not covered

### CLI Package (78.8% coverage) ✅

**Key Modules:**
- ✅ `main.py` - 79% (good coverage)
- ⚫ `__main__.py` - 0% (entry point, expected)

**Status:** Good! CLI argument parsing and routing well-tested.

### Helpers Package (87.2% coverage) ✅

**Status:** Excellent! Utility functions are well-tested.

### Integrations Package (0.0% coverage) ⚫

**Key Modules:**
- ⚫ `mcp_bridge.py` - 0% (114 statements, not tested)

**Status:** MCP integration layer has zero test coverage.

---

## 💡 Actionable Recommendations

### Phase 1: Quick Wins (2-3 days)

**Target: Boost coverage from 23.5% → 35%**

1. **Test `purchasing.py` remaining 21%** (currently best performer)
   - Add edge case tests for budget limits
   - Test error handling paths

2. **Add basic tests for `captain_logging.py`** (308 statements, 14%)
   - Test log entry creation
   - Test session management
   - Test file locking

3. **Test `daemon_manager.py` core workflows** (213 statements, 14%)
   - Start/stop daemon lifecycle
   - PID management
   - Status checks

**Impact:** ~600 statements covered = +6.1% overall coverage

---

### Phase 2: Core Infrastructure (1-2 weeks)

**Target: Boost coverage from 35% → 50%**

1. **`ship_controller.py`** - 347 statements, currently 7%
   - Test state machine transitions (DOCKED → IN_ORBIT → IN_TRANSIT)
   - Test navigation, extraction, docking operations
   - Test error handling and retries
   - **Priority:** 🔥 HIGHEST (core system functionality)

2. **`assignment_manager.py`** - 131 statements, currently 15%
   - Test ship assignment/release workflows
   - Test conflict detection
   - Test sync with daemons

3. **`smart_navigator.py`** - 366 statements, currently 36%
   - Test route validation
   - Test automatic refuel stop insertion
   - Test flight mode selection

**Impact:** ~844 statements covered = +8.6% overall coverage

---

### Phase 3: Complex Business Logic (2-3 weeks)

**Target: Boost coverage from 50% → 65%**

1. **`multileg_trader.py`** - 1,880 statements, currently 23%
   - **Largest file in codebase!**
   - Test trade route planning
   - Test circuit breaker logic
   - Test price validation
   - Consider breaking into smaller modules

2. **`scout_coordinator.py`** - 610 statements, currently 9%
   - Test market partitioning
   - Test ship coordination
   - Test tour optimization

3. **`contracts.py`** - 541 statements, currently 7%
   - Test contract evaluation
   - Test profitability calculations
   - Test fulfillment workflows

**Impact:** ~3,031 statements covered = +30.9% overall coverage

---

### Phase 4: Operations Handlers (1-2 weeks)

**Target: Boost coverage from 65% → 80%**

1. **`mining.py`** - 299 statements, currently 20%
2. **`routing.py` (operations)** - 344 statements, currently 3%
3. **`analysis.py`** - 240 statements, currently 2%
4. **`fleet.py`** - 106 statements, currently 12%

**Impact:** ~989 statements covered = +10.1% overall coverage

---

### Phase 5: Advanced Features (ongoing)

1. **`ortools_router.py`** - 766 statements, currently 38%
   - Test OR-Tools VRP/TSP integration
   - Test fuel-aware routing
   - Test graph building

2. **`market_data.py`** - 171 statements, currently 13%
   - Test price tracking
   - Test market freshness validation

3. **MCP Integration** - 116 statements, currently 0%
   - Add integration tests for MCP bridge
   - Test tool invocation

---

## 🎯 Coverage Goals

| Timeframe | Target Coverage | Key Milestones |
|-----------|----------------|----------------|
| **Week 1** | 35% | Test purchasing, logging, daemon lifecycle |
| **Week 2-3** | 50% | Test ship controller, assignment manager, smart navigator |
| **Week 4-6** | 65% | Test multileg trader, scout coordinator, contracts |
| **Week 7-8** | 80% | Test remaining operation handlers |
| **Ongoing** | 85%+ | Maintain and improve |

---

## 🔍 Testing Strategy

### Current State

**Test Categories (552 scenarios):**
- Domain tests: 62% (342 scenarios)
- Unit tests: 22% (121 scenarios)
- Integration tests: 16% (89 scenarios)

**Coverage Distribution:**
- Most tests focus on high-level workflows
- Core component internals are under-tested
- Error paths and edge cases lack coverage

### Recommended Approach

1. **Prioritize Core Components**
   - Focus on `ship_controller.py`, `assignment_manager.py`, `daemon_manager.py`
   - These are foundational - everything else depends on them

2. **Use BDD for Business Logic**
   - Continue using Gherkin scenarios for complex workflows
   - Example: `multileg_trader.py`, `contracts.py`, `scout_coordinator.py`

3. **Add Unit Tests for Utilities**
   - Small, focused tests for calculation functions
   - Example: fuel calculations, distance calculations

4. **Integration Tests for State Changes**
   - Test component interactions
   - Example: Ship state machine + navigation + fuel management

---

## 📊 Files with Complete Coverage (100%)

- `__init__.py` (12 statements) - Package initialization
- `paths.py` (28 statements) - Path helper utilities

---

## 🚨 Critical Issues

### 1. Ship Controller (7% coverage)
**Impact:** HIGH - Core system functionality
**Risk:** State machine bugs could strand ships
**Recommendation:** Add comprehensive state transition tests

### 2. Scout Coordinator (9% coverage)
**Impact:** HIGH - Fleet coordination
**Risk:** Market survey failures, ship conflicts
**Recommendation:** Test partitioning and coordination logic

### 3. Contracts (7% coverage)
**Impact:** MEDIUM - Revenue generation
**Risk:** Unprofitable contract acceptance, fulfillment failures
**Recommendation:** Test profitability calculations and workflows

### 4. Multileg Trader (23% coverage)
**Impact:** HIGH - Trading operations (largest file!)
**Risk:** Trading route failures, circuit breaker issues
**Recommendation:** Break into smaller modules, add scenario tests

### 5. Legacy Routing (0% coverage)
**Impact:** LOW - Legacy code
**Status:** ⚫ Marked for deprecation
**Recommendation:** Do not add tests, migrate to new routing system

---

## 📁 HTML Report

Detailed line-by-line coverage available at:
```
htmlcov/index.html
```

Open in browser:
```bash
open htmlcov/index.html
```

---

## 🔄 How to Update This Report

```bash
# Run tests with coverage
python3 -m pytest tests/bdd/ --cov=src/spacetraders_bot --cov-report=term --cov-report=html --cov-report=json

# Generate analysis
python3 analyze_coverage.py
```

---

## 📝 Notes

- **Legacy Code:** `routing_legacy.py` has 0% coverage - marked for deprecation, do not test
- **MCP Integration:** Currently not tested - requires separate integration test suite
- **Entry Points:** `__main__.py` has 0% coverage - expected for entry point scripts
- **Helpers Package:** Best coverage (87%) - good foundation for utilities

**Overall Assessment:** Test suite has good BDD scenario coverage for workflows, but lacks comprehensive testing of core components and error paths. Focus on Phase 1-2 recommendations for maximum impact.

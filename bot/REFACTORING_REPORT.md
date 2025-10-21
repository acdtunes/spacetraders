# Code Coverage & Line Count Refactoring Report

**Generated:** 2025-10-21
**Module Focus:** Trading Module (`src/spacetraders_bot/operations/_trading/`)

---

## Executive Summary

- **Total codebase:** 21,197 lines of Python code
- **Core module:** 10,455 lines (49.3%)
- **Operations module:** 4,980 lines (23.5%)
- **Trading module:** 3,132 lines (14.8%)
- **Trading module coverage:** 68.0% (target: 80%+)
- **Uncovered lines in trading:** 998 lines

---

## Refactoring Priority Matrix (Trading Module)

Sorted by urgency score: `(lines / 200) × (1 - coverage/100) × 100`

| Priority | File | Lines | Coverage | Uncovered |
|----------|------|-------|----------|-----------|
| 🔴 CRITICAL | route_planner.py | 685 | 33.0% | 458 |
| 🔴 CRITICAL | cargo_salvage.py | 389 | 37.2% | 244 |
| 🟡 HIGH | evaluation_strategies.py | 270 | 54.3% | 123 |
| 🟡 HIGH | market_service.py | 302 | 65.1% | 105 |
| 🟢 LOW | dependency_analyzer.py | 201 | 89.2% | 21 |
| 🟢 LOW | fleet_optimizer.py | 275 | 93.8% | 17 |
| 🟢 LOW | trade_executor.py | 192 | 92.9% | 13 |
| 🟢 LOW | segment_executor.py | 153 | 92.0% | 12 |
| 🟢 LOW | circuit_breaker.py | 159 | 96.5% | 5 |
| ✅ EXCELLENT | market_repository.py | 163 | 100.0% | 0 |
| ✅ EXCELLENT | route_executor.py | 152 | 100.0% | 0 |
| ✅ EXCELLENT | __init__.py | 121 | 100.0% | 0 |
| ✅ EXCELLENT | models.py | 70 | 100.0% | 0 |

---

## Top 15 Largest Files Across Entire Codebase

| Lines | File | Module | Priority |
|-------|------|--------|----------|
| 1,535 | ortools_router.py | core | 🔴 HIGH - Complex optimizer |
| 1,298 | contracts.py | operations | 🔴 HIGH - Large operation |
| 1,214 | routing_legacy.py | core | 🟡 MEDIUM - Legacy code |
| 1,118 | market_scout.py | core | 🟡 MEDIUM - Scout logic |
| 989 | database.py | core | 🟢 LOW - Database layer |
| 855 | smart_navigator.py | core | 🔴 HIGH - Critical path |
| 742 | captain_logging.py | operations | 🟢 LOW - Logging only |
| 685 | route_planner.py | _trading | 🔴 CRITICAL - Low coverage |
| 652 | ship.py | core | 🟡 MEDIUM - Ship controller |
| 536 | main.py | cli | 🟢 LOW - CLI interface |
| 536 | multileg_trader.py | operations | 🟡 MEDIUM - Trading ops |
| 535 | ortools_mining_optimizer.py | core | 🟡 MEDIUM - Mining optimizer |
| 522 | daemon_manager.py | core | 🟡 MEDIUM - Daemon logic |
| 520 | market_repository.py | core | 🟢 LOW - Repository pattern |
| 413 | ship_assignment_repository.py | core | 🟢 LOW - Repository pattern |

---

## Critical Refactoring Targets

### 1. route_planner.py (🔴 CRITICAL)

**Stats:** 685 lines, 33.0% coverage, 458 uncovered lines

**Problems:**
- Far exceeds maximum file size (400 lines)
- Lowest coverage in trading module
- Multiple responsibilities mixed together

**Recommended Split:**
```
_trading/route_planning/
├── __init__.py
├── route_generator.py      # Route creation logic (~200 lines)
├── route_validator.py      # Validation rules (~150 lines)
├── route_optimizer.py      # Optimization algorithms (~200 lines)
└── route_models.py         # Data structures (~135 lines)
```

**Action Items:**
1. Extract route generation logic to `route_generator.py`
2. Move validation logic to `route_validator.py`
3. Isolate optimization algorithms in `route_optimizer.py`
4. Create shared models in `route_models.py`
5. Write BDD tests for each new module (target: 80%+ coverage)

---

### 2. cargo_salvage.py (🔴 CRITICAL)

**Stats:** 389 lines, 37.2% coverage, 244 uncovered lines

**Problems:**
- Nearly double target file size
- Second lowest coverage in module
- Multiple salvage strategies in one file

**Recommended Split:**
```
_trading/salvage/
├── __init__.py
├── salvage_coordinator.py   # Main salvage logic (~150 lines)
├── strategies.py            # Salvage strategies (~150 lines)
└── cargo_analyzer.py        # Cargo analysis (~89 lines)
```

**Action Items:**
1. Extract salvage strategies into strategy pattern classes
2. Move cargo analysis to separate module
3. Create coordinator for strategy selection
4. Write comprehensive BDD tests (target: 80%+ coverage)

---

### 3. evaluation_strategies.py (🟡 HIGH)

**Stats:** 270 lines, 54.3% coverage, 123 uncovered lines

**Problems:**
- Slightly above target file size
- Multiple evaluation strategies mixed
- Medium coverage gaps

**Recommended Split:**
```
_trading/evaluation/
├── __init__.py
├── profit_evaluator.py      # Profit-based evaluation (~90 lines)
├── risk_evaluator.py        # Risk-based evaluation (~90 lines)
└── composite_evaluator.py   # Combined strategies (~90 lines)
```

**Action Items:**
1. Separate by evaluation concern (profit, risk, composite)
2. Apply strategy pattern for extensibility
3. Add tests for edge cases (target: 80%+ coverage)

---

### 4. market_service.py (🟡 HIGH)

**Stats:** 302 lines, 65.1% coverage, 105 uncovered lines

**Problems:**
- Slightly above target file size
- Mixed data fetching and business logic
- Coverage gaps in error handling

**Recommended Split:**
```
_trading/market/
├── __init__.py
├── market_fetcher.py        # API data fetching (~150 lines)
├── market_analyzer.py       # Business logic (~150 lines)
└── market_cache.py          # Caching layer (~50 lines)
```

**Action Items:**
1. Separate data fetching from analysis
2. Extract caching logic
3. Add tests for error scenarios (target: 80%+ coverage)

---

## Refactoring Guidelines

### File Size Targets

- ✅ **Ideal:** 150-250 lines
- ⚠️ **Acceptable:** 250-400 lines
- 🔴 **Refactor Required:** 400+ lines
- 🔴 **Urgent:** 600+ lines

### Coverage Targets

- ✅ **Excellent:** 95-100%
- ✅ **Good:** 85-94%
- ⚠️ **Acceptable:** 70-84%
- 🔴 **Needs Work:** 50-69%
- 🔴 **Critical:** <50%

### Refactoring Strategy

1. **Start with critical files** (route_planner, cargo_salvage)
2. **Use Extract Module pattern** - Move cohesive functions to new files
3. **Maintain test coverage** - Don't break existing tests
4. **Follow Single Responsibility** - One concern per file
5. **Write tests first** - BDD scenarios before refactoring
6. **Incremental approach** - Small, safe refactoring steps

---

## Module Health Dashboard

### Trading Module Files by Health

**Excellent (100% coverage):**
- ✅ market_repository.py (163 lines)
- ✅ route_executor.py (152 lines)
- ✅ __init__.py (121 lines)
- ✅ models.py (70 lines)

**Good (90-99% coverage):**
- ✅ circuit_breaker.py (159 lines, 96.5%)
- ✅ fleet_optimizer.py (275 lines, 93.8%)
- ✅ trade_executor.py (192 lines, 92.9%)
- ✅ segment_executor.py (153 lines, 92.0%)
- ✅ dependency_analyzer.py (201 lines, 89.2%)

**Needs Improvement (50-89% coverage):**
- ⚠️ market_service.py (302 lines, 65.1%)
- ⚠️ evaluation_strategies.py (270 lines, 54.3%)

**Critical (< 50% coverage):**
- 🔴 cargo_salvage.py (389 lines, 37.2%)
- 🔴 route_planner.py (685 lines, 33.0%)

---

## Next Steps

### Immediate Actions (This Week)

1. **Add tests for route_planner.py**
   - Focus on uncovered lines (458 uncovered)
   - Write BDD scenarios for route planning edge cases
   - Target: Raise coverage from 33% to 60%

2. **Add tests for cargo_salvage.py**
   - Focus on salvage strategies
   - Write BDD scenarios for cargo handling
   - Target: Raise coverage from 37% to 60%

### Short-term (Next 2 Weeks)

3. **Refactor route_planner.py**
   - Split into 4 modules (route_planning/)
   - Maintain/improve test coverage during split
   - Target: 80%+ coverage on all new modules

4. **Refactor cargo_salvage.py**
   - Split into 3 modules (salvage/)
   - Apply strategy pattern
   - Target: 80%+ coverage on all new modules

### Medium-term (Next Month)

5. **Refactor evaluation_strategies.py**
   - Split into 3 evaluator modules
   - Target: 80%+ coverage

6. **Refactor market_service.py**
   - Split fetcher/analyzer
   - Target: 80%+ coverage

---

## View Detailed Coverage Report

```bash
open htmlcov/index.html
```

The HTML report provides interactive navigation with line-by-line coverage highlighting.

---

## Regenerate This Report

```bash
# Run tests with coverage
source .venv/bin/activate
python3 -m pytest tests/ --cov=src --cov-report=term --cov-report=html

# View coverage summary
python3 -m coverage report

# Generate line count report
find src/spacetraders_bot -name "*.py" -type f -exec wc -l {} + | sort -rn | head -20
```

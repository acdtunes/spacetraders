# BDD Step Files Parallel Refactoring Plan

## Objective
Safely split oversized step files into manageable, feature-focused files using parallel task agents.

## Safety Strategy

### Key Principle: File-Level Isolation
Each agent works on a **different source file** and produces **non-overlapping output files**. This eliminates merge conflicts.

### Conflict Avoidance Rules
1. ✅ **Safe:** Multiple agents writing to different files in the same directory
2. ✅ **Safe:** Agents reading the same fixtures/conftest.py (read-only)
3. ❌ **Unsafe:** Multiple agents modifying the same source file
4. ❌ **Unsafe:** Multiple agents creating files with the same name

## Parallel Execution Phases

### Phase 1: Independent Splits (Run in Parallel)

**4 agents, each working on ONE source file:**

#### Agent 1: Split test_trading_module_steps.py
**Source:** `tests/bdd/steps/trading/_trading_module/test_trading_module_steps.py` (3,382 lines)

**Task:** Extract steps for 5 features into separate files

**Output files to CREATE:**
- `test_market_service_steps.py` - Extract all steps for market_service.feature
- `test_circuit_breaker_steps.py` - Extract all steps for circuit_breaker.feature
- `test_trade_executor_steps.py` - Extract all steps for trade_executor.feature
- `test_dependency_analyzer_steps.py` - Extract all steps for dependency_analyzer.feature
- `test_route_executor_steps.py` - Extract all steps for route_executor.feature

**Verification:**
```bash
pytest tests/bdd/features/trading/_trading_module/market_service.feature -v
pytest tests/bdd/features/trading/_trading_module/circuit_breaker.feature -v
pytest tests/bdd/features/trading/_trading_module/trade_executor.feature -v
pytest tests/bdd/features/trading/_trading_module/dependency_analyzer.feature -v
pytest tests/bdd/features/trading/_trading_module/route_executor.feature -v
```

**Success criteria:** All 5 features pass with new step files

---

#### Agent 2: Split test_operations_steps.py
**Source:** `tests/bdd/steps/unit/test_operations_steps.py` (1,399 lines)

**Task:** Split by operational domain based on step analysis

**First, analyze the step definitions to identify domains:**
```bash
grep "^@given\|^@when\|^@then" test_operations_steps.py | head -50
```

**Expected output files (estimate):**
- `test_operations_mining_steps.py` - Mining operation steps
- `test_operations_trading_steps.py` - Trading operation steps
- `test_operations_contracts_steps.py` - Contract operation steps
- `test_operations_fleet_steps.py` - Fleet operation steps
- `test_operations_daemon_steps.py` - Daemon operation steps
- `test_operations_core_steps.py` - Core/shared operation steps

**Verification:**
```bash
pytest tests/bdd/features/unit/operations.feature -v
```

**Success criteria:** operations.feature passes with new split step files

---

#### Agent 3: Split test_route_planner_steps.py
**Source:** `tests/bdd/steps/trading/_trading_module/test_route_planner_steps.py` (1,210 lines)

**Task:** Split by functional area (core logic, optimization, validation)

**Output files to CREATE:**
- `test_route_planner_core_steps.py` - Core planning logic steps
- `test_route_planner_optimization_steps.py` - Optimization algorithm steps
- `test_route_planner_validation_steps.py` - Route validation steps

**Verification:**
```bash
pytest tests/bdd/features/trading/_trading_module/route_planner.feature -v
```

**Success criteria:** route_planner.feature passes with new step files

---

#### Agent 4: Split test_cargo_salvage_steps.py
**Source:** `tests/bdd/steps/trading/_trading_module/test_cargo_salvage_steps.py` (1,128 lines)

**Task:** Split by functional area (detection, execution, validation)

**Output files to CREATE:**
- `test_cargo_salvage_detection_steps.py` - Salvage detection steps
- `test_cargo_salvage_execution_steps.py` - Salvage execution steps
- `test_cargo_salvage_validation_steps.py` - Salvage validation steps

**Verification:**
```bash
pytest tests/bdd/features/trading/_trading_module/cargo_salvage.feature -v
```

**Success criteria:** cargo_salvage.feature passes with new step files

---

### Phase 2: Validation (Sequential, after Phase 1)

**Agent 5: Full Test Suite Validation**

**Task:** Run complete test suite and verify no regressions

**Commands:**
```bash
# Run all BDD tests
pytest tests/bdd/ -v

# Generate coverage report
pytest tests/bdd/ --cov=src --cov-report=term-missing

# Check for any import errors
python -m py_compile tests/bdd/steps/**/*.py
```

**Success criteria:**
- All 117 tests pass
- Coverage remains ≥85%
- No import errors
- No duplicate step definition warnings

---

### Phase 3: Cleanup (Sequential, after Phase 2)

**Agent 6: Cleanup and Documentation**

**Tasks:**
1. Remove backup files:
   ```bash
   rm tests/bdd/steps/trading/_trading_module/*.bak*
   ```

2. Update `TESTING_GUIDE.md` with new structure

3. Create step-to-feature mapping documentation

4. Verify git status is clean

---

## Execution Commands

### Start all Phase 1 agents in parallel:

```python
# Agent 1
Task(
  subagent_type="general-purpose",
  description="Split trading_module_steps.py",
  prompt="Split tests/bdd/steps/trading/_trading_module/test_trading_module_steps.py into 5 separate files..."
)

# Agent 2
Task(
  subagent_type="general-purpose",
  description="Split operations_steps.py",
  prompt="Split tests/bdd/steps/unit/test_operations_steps.py by domain..."
)

# Agent 3
Task(
  subagent_type="general-purpose",
  description="Split route_planner_steps.py",
  prompt="Split tests/bdd/steps/trading/_trading_module/test_route_planner_steps.py..."
)

# Agent 4
Task(
  subagent_type="general-purpose",
  description="Split cargo_salvage_steps.py",
  prompt="Split tests/bdd/steps/trading/_trading_module/test_cargo_salvage_steps.py..."
)
```

### Wait for Phase 1 completion, then run Phase 2:

```python
# Agent 5
Task(
  subagent_type="general-purpose",
  description="Validate all tests pass",
  prompt="Run complete BDD test suite and verify no regressions..."
)
```

### Wait for Phase 2 completion, then run Phase 3:

```python
# Agent 6
Task(
  subagent_type="general-purpose",
  description="Cleanup and document",
  prompt="Remove backup files, update documentation..."
)
```

---

## Safety Checklist

Before starting each agent, verify:

- [ ] Agent has a unique source file to read
- [ ] Agent's output files don't overlap with other agents' outputs
- [ ] Agent has clear success criteria (specific test commands)
- [ ] Agent will verify tests pass before reporting completion

## Rollback Plan

If any agent fails:

1. **Keep successful agent changes** - they're isolated
2. **Revert failed agent's changes:**
   ```bash
   git checkout tests/bdd/steps/[failed_directory]/*
   ```
3. **Re-run failed agent individually** with fixes
4. **Verify tests** before proceeding

## Risk Assessment

**Low Risk:**
- ✅ Each agent works on different source files
- ✅ Output files are non-overlapping
- ✅ Each agent verifies its own tests pass
- ✅ Easy rollback per agent

**Medium Risk:**
- ⚠️ Agents 1, 3, 4 all write to `trading/_trading_module/` directory
  - **Mitigation:** Different output filenames, verified in plan
- ⚠️ Shared fixtures in conftest.py might need updates
  - **Mitigation:** Read-only access, no modifications planned

**Negligible Risk:**
- Tests are already passing
- Each split maintains exact same step definitions
- No logic changes, pure code reorganization

## Expected Timeline

**Phase 1 (Parallel):** 15-20 minutes per agent (simultaneous)
**Phase 2 (Sequential):** 5-10 minutes
**Phase 3 (Sequential):** 5 minutes

**Total time:** ~25-35 minutes (vs ~80-100 minutes if sequential)

**Speedup:** ~3x faster with parallelization

## Conclusion

✅ **Safe to parallelize** with the file-level isolation strategy outlined above.

Each agent has:
- Unique source file
- Non-overlapping outputs
- Independent verification
- Clear success criteria

The key is **Phase 1 runs in parallel, Phases 2-3 run sequentially** to ensure validation and cleanup happen after all splits complete.

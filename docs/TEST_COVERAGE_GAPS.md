# Test Coverage Gaps - Navigation Fixes

**Date**: 2025-10-30
**Status**: Tests created but not fully integrated
**Priority**: HIGH - Critical fixes lack automated test coverage

---

## ⚠️ Critical Finding

We implemented 3 architectural fixes to prevent navigation failures **WITHOUT writing tests first**. This violates TDD and leaves us vulnerable to regressions.

---

## ✅ What We Created

### **3 Comprehensive BDD Feature Files**:

1. **`tests/bdd/features/daemon/assignment_cleanup.feature`**
   - 5 scenarios covering assignment release on success/failure/stop
   - Tests ship can be reassigned after cleanup
   - Tests cleanup happens on crashes

2. **`tests/bdd/features/daemon/health_monitor.feature`**
   - 6 scenarios covering stale assignment detection
   - Tests periodic health checks (60s)
   - Tests multi-assignment cleanup
   - Tests daemon crash recovery

3. **`tests/bdd/features/navigation/ship_state_sync.feature`**
   - 7 scenarios covering API sync before navigation
   - Tests stale fuel/location/cargo correction
   - Tests sync failure handling
   - Tests timestamp recording

### **3 Step Implementation Files**:

1. **`tests/bdd/steps/daemon/test_assignment_cleanup_steps.py`** ✅
   - Complete step definitions for all 5 scenarios
   - Mocks CommandContainer cleanup behavior
   - Verifies database state after cleanup

2. **`tests/bdd/steps/daemon/test_health_monitor_steps.py`** ✅
   - Complete step definitions for all 6 scenarios
   - Mocks DaemonServer health monitor
   - Tests stale detection logic

3. **`tests/bdd/steps/navigation/test_ship_state_sync_steps.py`** ✅
   - Complete step definitions for all 7 scenarios
   - Mocks API client responses
   - Verifies sync behavior

---

## ❌ What's Missing for Tests to Run

### **1. Missing Shared Step Definitions**

Tests need these shared steps (used in Background sections):
```gherkin
Given a player exists with ID 1 and token "test-token"
And a ship "SHIP-1" exists for player 1 at "X1-TEST-A1"
And the daemon server is running
```

**Solution**: Create `tests/bdd/steps/daemon/conftest.py` with:
- `context` fixture (dict for sharing test state)
- Player setup steps
- Ship setup steps
- Daemon server management steps

### **2. Missing Database Setup**

Tests need clean database state:
- Reset between scenarios
- Seed test data (players, ships, assignments)
- Transaction rollback for isolation

**Solution**: Add pytest fixtures in conftest.py:
```python
@pytest.fixture(autofunc=True)
def reset_database():
    # Reset database before each test
    from spacetraders.configuration.container import reset_container
    reset_container()
    # Initialize test database
    # Yield for test
    # Cleanup after test
```

### **3. Missing Async Test Support**

Step functions are async but pytest-bdd doesn't auto-detect:
```python
@when('the container completes successfully')
async def container_completes_successfully(context):
    await container.cleanup()
```

**Solution**: Use `pytest.mark.asyncio` or pytest-async plugin configuration.

### **4. Missing Mock Coordination**

Tests mock API/daemon but need coordination:
- Mock API responses for ship sync
- Mock container lifecycle
- Mock health monitor timing

**Solution**: Create test helpers:
```python
class MockAPIClient:
    def get_ship(self, symbol):
        return test_data['api_responses'][symbol]
```

---

## 📊 Test Coverage Analysis

### **Fixes Implemented** (from commit 4e51acce):

| Fix | Code Location | Test Scenarios | Test Status |
|-----|---------------|----------------|-------------|
| Assignment Cleanup | `command_container.py:110-137` | 5 scenarios | ⚠️ Steps complete, needs fixtures |
| Health Monitor | `daemon_server.py:366-408` | 6 scenarios | ⚠️ Steps complete, needs fixtures |
| Ship State Sync | `navigate_ship.py:101-111` | 7 scenarios | ⚠️ Steps complete, needs fixtures |

**Total**: 18 scenarios, ~120 test cases, 0 passing (not yet runnable)

---

## 🎯 Completion Roadmap

### **Phase 1: Make Tests Runnable** (1-2 hours)

1. Create `tests/bdd/steps/daemon/conftest.py`:
   ```python
   @pytest.fixture
   def context():
       return {}

   @pytest.fixture(autouse=True)
   def reset_test_state():
       from spacetraders.configuration.container import reset_container
       reset_container()
       # Setup test database
       yield
       # Cleanup
   ```

2. Add shared step definitions:
   - Player creation steps
   - Ship creation steps
   - Daemon server lifecycle steps

3. Configure pytest-asyncio for async tests

4. Run initial test suite, fix import/setup issues

### **Phase 2: Fix Failing Tests** (2-3 hours)

1. Debug database state issues
2. Fix async/await coordination
3. Adjust mocks to match actual behavior
4. Handle edge cases discovered by tests

### **Phase 3: Verify Coverage** (1 hour)

1. Run with `pytest --cov`
2. Verify all 3 fixes have passing tests
3. Check for missed edge cases
4. Add integration tests if needed

---

## 🔥 Why This Matters

### **Current Risk Level: HIGH** 🚨

**Without tests**:
- ❌ No regression protection - future changes can break fixes
- ❌ No verification fixes actually work as intended
- ❌ Can't refactor with confidence
- ❌ Integration issues may go undetected

**With tests**:
- ✅ Regression protection - CI catches breaks immediately
- ✅ Behavioral documentation - tests show how it should work
- ✅ Refactoring safety - change code, tests verify behavior
- ✅ Integration confidence - end-to-end scenarios validated

---

## 📝 Immediate Actions

**Option 1: Complete Test Suite (Recommended)** - 4-6 hours
- Finish fixtures and shared steps
- Run and fix all tests
- Achieve 100% coverage of fixes
- Gain full confidence in solutions

**Option 2: Selective Testing** - 2-3 hours
- Focus on assignment cleanup tests only
- Get core functionality verified
- Leave health monitor + sync tests for later

**Option 3: Manual Testing + Document** - 1 hour
- Document manual test cases
- Run manual verification scenarios
- Accept risk of no automated coverage
- Plan to add tests in future sprint

---

## 🎓 Lessons Learned

1. **Always write tests for architectural fixes** - These are high-risk changes
2. **TDD protects against bugs** - Tests would have caught integration issues earlier
3. **BDD scenarios are valuable** - Even without running, they document expected behavior
4. **Test infrastructure matters** - Fixtures, mocks, and setup are 50% of the work

---

## ✅ Recommendation

**Complete Phase 1 ASAP** (next session):
- Create conftest with fixtures
- Add shared step definitions
- Get at least assignment cleanup tests passing
- Provides baseline protection for most critical fix

**Then Phase 2** (follow-up session):
- Fix health monitor tests
- Fix ship state sync tests
- Full coverage achieved

This approach balances thoroughness with pragmatism - we get immediate protection for the most critical fix (assignment cleanup) while building toward complete coverage.

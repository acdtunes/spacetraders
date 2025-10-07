# Test Suite Performance Fix

## Problem
Tests were hanging and taking too long to run.

## Root Cause
**Line 552 in mock_api.py:** Sets `cooldown_seconds = 80`  
**ship_controller.py:** Actually calls `time.sleep(80)` for extraction cooldowns  
**Result:** Tests literally waited 80 seconds per extraction

## Solution
Added autouse fixture to mock `time.sleep` in test_ship_controller_advanced_steps.py:

```python
@pytest.fixture(autouse=True)
def mock_sleep():
    """Mock time.sleep to make tests instant"""
    with patch('time.sleep', return_value=None):
        yield
```

## Results

### Before Fix
- test_ship_controller_advanced_steps.py: **HUNG** (timed out after 60s+)
- Full suite: **HUNG** (timed out after 2m+)

### After Fix
- test_ship_controller_advanced_steps.py: **0.19s** ⚡
- Full suite (295 tests): **28.53s** ⚡
- **~100x speedup** on affected tests

## Test Results
```
295 total tests
274 passed (93%)
 21 failed (7%)
  0 hanging ✅
```

## Coverage
```
api_client.py:          99%
ship_controller.py:     98%  
operation_controller:   99%
routing.py:             94%
smart_navigator.py:     92%
utils.py:               93%
assignment_manager.py:  61%
daemon_manager.py:      14% (infrastructure)

TOTAL:                  80%
EXCLUDING INFRA:        92%
```

## Failing Tests (21)
- 10 component_interactions: Missing step definitions (not hanging)
- 6 smart_navigator_advanced: Edge case failures (not hanging)
- 3 navigation: Mock mismatches (not hanging)
- 2 ship_controller_advanced: Transit wait logic (not hanging)

**All failures are logic issues, NOT performance issues.**

## Files Modified
- `test_ship_controller_advanced_steps.py`: Added mock_sleep fixture
- `lib/assignment_manager.py`: Fixed list_all() to mark stale assignments

## ship_assignments Tests
**All 18 tests passing (100%)** ✅
- Fixed parsers.parse() issues with regex patterns
- Added ship tracking throughout all steps  
- Fixed production bug in assignment_manager.py
- All assertions verify REAL state changes

---
*Generated: 2025-10-05*  
*Performance improvement: 100x on affected tests*

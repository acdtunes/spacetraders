# Test Coverage Improvement Plan: 30% → 85%

## Current State
- **Current Coverage:** 30% (370/1228 statements)
- **Target Coverage:** 85% (1044/1228 statements)
- **Statements to Cover:** 674 additional statements

## Coverage Analysis by Module

| Module | Current | Target | Priority | Effort |
|--------|---------|--------|----------|--------|
| operation_controller.py | 98% | 98% | ✅ Done | 0h |
| ship_controller.py | 19% | 90% | 🔥 High | 3h |
| routing.py | 25% | 85% | 🔥 High | 2h |
| smart_navigator.py | 47% | 85% | 🔥 High | 2h |
| api_client.py | 17% | 75% | ⚠️ Medium | 2h |
| utils.py | 33% | 90% | ⚠️ Medium | 1h |
| daemon_manager.py | 0% | 50% | 🔻 Low | 2h |

**Total Effort:** ~12 hours

## Implementation Strategy

### Phase 1: Quick Wins (2h) - Fix Existing Tests
**Goal:** Get failing tests passing
- Fix 13 failing BDD tests
- Complete missing step implementations
- Fix assertion issues

**Impact:** +5% coverage

### Phase 2: Ship Controller Tests (3h) - High Impact
**Goal:** 19% → 90% coverage (150 statements)
- Test all ship actions (orbit, dock, navigate, extract, refuel)
- Test cargo management (sell, buy, jettison, transfer)
- Test state transitions
- Test error handling

**Impact:** +15% coverage

### Phase 3: Routing Tests (2h) - High Impact
**Goal:** 25% → 85% coverage (170 statements)
- Test A* pathfinding edge cases
- Test multi-hop routes with refueling
- Test fuel calculation for all modes (CRUISE, DRIFT, BURN)
- Test graph building from API data

**Impact:** +15% coverage

### Phase 4: Smart Navigator Tests (2h) - High Impact
**Goal:** 47% → 85% coverage (82 statements)
- Test route execution with checkpoints
- Test state machine integration
- Test validation edge cases
- Test graph caching

**Impact:** +8% coverage

### Phase 5: Utils & API Client (3h) - Medium Impact
**Goal:** Utils 33% → 90%, API Client 17% → 75%
- Test utility functions (distance, time, fuel calculations)
- Test API client error handling and retries
- Test pagination logic
- Test authentication

**Impact:** +12% coverage

**Total Coverage After Phase 5:** ~85% ✅

## Detailed Implementation Plan

### Phase 1: Fix Failing Tests (2h)

#### 1.1 Navigation Edge Cases (1h)
- [ ] Complete "ship already at destination" test
- [ ] Fix "no marketplace for refuel" test
- [ ] Add "ship damage" validation steps
- [ ] Fix "zero distance navigation" test
- [ ] Complete "find nearest" implementation

#### 1.2 State Machine Tests (30min)
- [ ] Add waypoint datatable parsing
- [ ] Fix state transition tests

#### 1.3 Operation Controller Tests (30min)
- [ ] Fix corrupted state file test
- [ ] Fix empty operation ID test
- [ ] Fix race condition test

### Phase 2: Ship Controller Tests (3h)

#### 2.1 Core Actions Tests (1h)
```python
tests/test_ship_controller.py
- test_orbit_success()
- test_orbit_already_in_orbit()
- test_orbit_fails_when_in_transit()
- test_dock_success()
- test_dock_already_docked()
- test_navigate_success()
- test_navigate_insufficient_fuel()
- test_refuel_success()
- test_refuel_at_capacity()
```

#### 2.2 Cargo Management Tests (1h)
```python
- test_sell_cargo_success()
- test_sell_cargo_insufficient_inventory()
- test_buy_cargo_success()
- test_buy_cargo_insufficient_capacity()
- test_jettison_cargo()
- test_transfer_cargo()
```

#### 2.3 Extraction Tests (1h)
```python
- test_extract_resources_success()
- test_extract_with_cooldown()
- test_extract_cargo_full()
- test_extract_wrong_location()
```

### Phase 3: Routing Tests (2h)

#### 3.1 A* Pathfinding Tests (1h)
```python
tests/test_routing.py
- test_find_route_direct_path()
- test_find_route_with_refuel_stop()
- test_find_route_multiple_refuel_stops()
- test_find_route_no_path_exists()
- test_find_route_insufficient_fuel()
- test_heuristic_calculation()
```

#### 3.2 Fuel Calculation Tests (30min)
```python
- test_fuel_cost_cruise_mode()
- test_fuel_cost_drift_mode()
- test_fuel_cost_burn_mode()
- test_fuel_cost_zero_distance()
```

#### 3.3 Graph Building Tests (30min)
```python
- test_build_graph_from_waypoints()
- test_build_graph_with_orbital_edges()
- test_build_graph_save_and_load()
```

### Phase 4: Smart Navigator Tests (2h)

#### 4.1 Route Execution Tests (1h)
```python
tests/test_smart_navigator.py
- test_execute_route_success()
- test_execute_route_with_refuel()
- test_execute_route_with_checkpoints()
- test_execute_route_failure_recovery()
```

#### 4.2 Validation Tests (30min)
```python
- test_validate_route_valid()
- test_validate_route_invalid_fuel()
- test_validate_route_missing_waypoint()
- test_validate_route_ship_damaged()
```

#### 4.3 Integration Tests (30min)
```python
- test_state_machine_integration()
- test_operation_controller_integration()
- test_graph_caching()
```

### Phase 5: Utils & API Client (3h)

#### 5.1 Utils Tests (1h)
```python
tests/test_utils.py
- test_calculate_distance()
- test_calculate_arrival_time()
- test_parse_waypoint_symbol()
- test_select_flight_mode()
- test_timestamp_formatting()
- test_euclidean_distance()
- test_fuel_calculator_all_modes()
- test_time_calculator_all_modes()
```

#### 5.2 API Client Tests (2h)
```python
tests/test_api_client.py
- test_get_request_success()
- test_get_request_not_found()
- test_post_request_success()
- test_request_retry_on_failure()
- test_rate_limit_handling()
- test_pagination()
- test_authentication()
- test_error_parsing()
```

## Test File Structure

```
tests/
├── features/                           # BDD Gherkin (existing)
│   ├── navigation_edge_cases.feature
│   ├── state_machine_edge_cases.feature
│   └── operation_controller_edge_cases.feature
│
├── Step definitions (existing)
│   ├── test_navigation_edge_cases_steps.py
│   ├── test_state_machine_edge_cases_steps.py
│   └── test_operation_controller_edge_cases_steps.py
│
├── Unit tests (NEW)
│   ├── test_ship_controller.py         # Phase 2
│   ├── test_routing.py                 # Phase 3
│   ├── test_smart_navigator.py         # Phase 4
│   ├── test_utils.py                   # Phase 5
│   └── test_api_client.py              # Phase 5
│
└── Support files
    ├── mock_api.py
    ├── conftest.py
    └── requirements.txt
```

## Success Metrics

After each phase:
```bash
pytest tests/ --cov=lib --cov-report=term
```

**Phase Completion Targets:**
- Phase 1: 35% coverage ✓
- Phase 2: 50% coverage ✓
- Phase 3: 65% coverage ✓
- Phase 4: 73% coverage ✓
- Phase 5: 85% coverage ✓

## Implementation Order

1. **Day 1 Morning:** Phase 1 - Fix failing tests (2h)
2. **Day 1 Afternoon:** Phase 2 - Ship controller tests (3h)
3. **Day 2 Morning:** Phase 3 - Routing tests (2h)
4. **Day 2 Afternoon:** Phase 4 - Smart navigator tests (2h)
5. **Day 3 Morning:** Phase 5 - Utils & API client tests (3h)

**Total Time:** 12 hours over 3 days

## Risk Mitigation

**Risk 1:** Complex routing logic hard to test
- **Mitigation:** Use mock graphs with known shortest paths

**Risk 2:** State machine integration complex
- **Mitigation:** Test each transition independently first

**Risk 3:** Time estimate too optimistic
- **Mitigation:** Prioritize high-impact tests first, can skip daemon_manager to hit 85%

## Post-85% Stretch Goals

If time permits:
- [ ] Daemon manager tests (0% → 50%) = +5% coverage → 90% total
- [ ] Integration tests with real API (sandbox environment)
- [ ] Performance benchmarks
- [ ] Mutation testing

Feature: Route Executor - Multi-leg route orchestration

  As a trading system
  I need to orchestrate complete multi-leg route execution
  So that routes are executed reliably with proper monitoring

  Background:
    Given a mock ship controller for "TRADER-1"
    And a mock API client
    And a mock database
    And a route executor for player 6

  # Successful Route Execution

  Scenario: Execute simple 2-segment route successfully
    Given a multi-leg route with 2 segments
    And segment 0: A1 → B7, BUY 10 COPPER at 100 cr/unit
    And segment 1: B7 → C5, SELL 10 COPPER at 500 cr/unit
    And ship starts at "X1-TEST-A1" with 10000 credits
    And all market data is fresh (<30 minutes old)
    When executing the route
    Then navigation should succeed for both segments
    And all trade actions should execute successfully
    And final profit should be approximately 4000 credits
    And route execution should succeed
    And metrics should show 5000 revenue and 1000 costs

  Scenario: Execute 3-segment route with multiple goods
    Given a multi-leg route with 3 segments
    And segment 0: A1 → B7, BUY 10 COPPER at 100 cr/unit
    And segment 1: B7 → C5, SELL 10 COPPER at 500 cr/unit, BUY 15 IRON at 150 cr/unit
    And segment 2: C5 → D42, SELL 15 IRON at 300 cr/unit
    And ship starts at "X1-TEST-A1" with 20000 credits
    And all market data is fresh
    When executing the route
    Then all 3 segments should execute successfully
    And final profit should be approximately 6250 credits
    And route execution should succeed

  # Pre-Flight Validation

  Scenario: Pre-flight validation passes with fresh market data
    Given a multi-leg route with 2 segments
    And segment 0 requires COPPER at waypoint B7 (updated 15 min ago)
    And segment 1 requires IRON at waypoint C5 (updated 10 min ago)
    When executing the route
    Then pre-flight validation should pass
    And no stale markets should be detected
    And route execution should proceed

  Scenario: Pre-flight validation fails with stale market data
    Given a multi-leg route with 2 segments
    And segment 0 requires COPPER at waypoint B7 (updated 2 hours ago)
    And segment 1 requires IRON at waypoint C5 (updated 10 min ago)
    When executing the route
    Then pre-flight validation should fail
    And stale market should be reported: B7 COPPER
    And route execution should abort before segment 0
    And no navigation should occur

  Scenario: Pre-flight validation warns about aging data
    Given a multi-leg route with 2 segments
    And segment 0 requires COPPER at waypoint B7 (updated 45 min ago)
    And segment 1 requires IRON at waypoint C5 (updated 10 min ago)
    When executing the route
    Then pre-flight validation should pass with warnings
    And aging market should be reported: B7 COPPER
    And route execution should proceed with caution

  # Dependency Analysis

  Scenario: Dependency analysis identifies independent segments
    Given a multi-leg route with 3 segments
    And segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5
    When executing the route
    Then dependency analysis should show segment 1 as INDEPENDENT
    And segment 2 should depend on segment 0
    And dependency map should be logged

  Scenario: Dependency analysis identifies cargo dependencies
    Given a multi-leg route with 3 segments
    And segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 10 COPPER at B7, BUY 15 IRON at B7
    And segment 2: SELL 15 IRON at C5
    When executing the route
    Then segment 1 should have dependency type CARGO
    And segment 1 should depend on segment [0]
    And segment 2 should have dependency type CARGO
    And segment 2 should depend on segment [1]

  # Navigation and Docking

  Scenario: Ship navigates to each waypoint successfully
    Given a multi-leg route with 2 segments
    And segment 0: A1 → B7
    And segment 1: B7 → C5
    And ship starts at "X1-TEST-A1"
    When executing the route
    Then ship should navigate to X1-TEST-B7
    And ship should dock at X1-TEST-B7
    And ship should navigate to X1-TEST-C5
    And ship should dock at X1-TEST-C5

  Scenario: Navigation failure aborts route execution
    Given a multi-leg route with 2 segments
    And segment 0: A1 → B7
    And segment 1: B7 → C5
    And navigation to B7 fails (out of fuel)
    When executing the route
    Then route execution should fail at segment 0
    And segment 1 should not be attempted
    And no trade actions should execute

  Scenario: Docking failure aborts route execution
    Given a multi-leg route with 2 segments
    And segment 0: A1 → B7
    And navigation succeeds but docking fails
    When executing the route
    Then route execution should fail at segment 0
    And error should indicate docking failure
    And no trade actions should execute

  # Segment Skipping

  Scenario: Failed segment with independent remaining segments - skip and continue
    Given a multi-leg route with 4 segments
    And segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5
    And segment 3: SELL 15 IRON at D42
    And segment 0 fails due to circuit breaker
    When executing the route
    Then segment 0 should be skipped
    And segment 2 should be skipped (depends on segment 0)
    And segment 1 should execute successfully
    And segment 3 should execute successfully
    And 2 segments should be marked as skipped

  Scenario: All remaining segments depend on failed segment - abort
    Given a multi-leg route with 3 segments
    And segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 10 COPPER at B7, BUY 15 IRON at B7
    And segment 2: SELL 15 IRON at C5
    And segment 0 fails due to circuit breaker
    When executing the route
    Then route execution should abort
    And error should indicate "all remaining segments depend on failed segment"
    And no subsequent segments should execute

  # Metrics Tracking

  Scenario: Route tracks revenue and costs accurately
    Given a multi-leg route with 2 segments
    And segment 0: A1 → B7, BUY 10 COPPER at 100 cr/unit (cost: 1000)
    And segment 1: B7 → C5, SELL 10 COPPER at 500 cr/unit (revenue: 5000)
    And ship starts with 20000 credits
    When executing the route
    Then total revenue should be 5000 credits
    And total costs should be 1000 credits
    And actual profit should be 4000 credits
    And metrics should be logged in final summary

  Scenario: Route tracks estimated vs actual profit
    Given a multi-leg route with estimated profit 5000 credits
    And segment 0: BUY 10 COPPER at planned 100 cr/unit
    And segment 1: SELL 10 COPPER at planned 500 cr/unit
    And actual buy price is 120 cr/unit
    And actual sell price is 480 cr/unit
    And ship starts with 20000 credits
    When executing the route
    Then estimated profit should be 5000 credits
    And actual profit should be 3600 credits
    And accuracy should be 72.0 percent
    And accuracy should be logged

  # Final Summary

  Scenario: Successful route logs completion summary
    Given a multi-leg route with 3 segments
    And all segments execute successfully
    When route execution completes
    Then final summary should include:
      | Field | Value |
      | Revenue | total revenue amount |
      | Costs | total costs amount |
      | Actual profit | final credit change |
      | Estimated profit | route.total_profit |
      | Accuracy | percentage match |
      | Segments skipped | 0/3 |

  Scenario: Route with skipped segments logs skip count
    Given a multi-leg route with 4 segments
    And segments 0 and 2 are skipped
    And segments 1 and 3 execute successfully
    When route execution completes
    Then final summary should show 2/4 segments skipped
    And skip details should be logged

  # Error Handling

  Scenario: Ship status retrieval fails - abort immediately
    Given a multi-leg route with 2 segments
    And ship.get_status() returns None
    When executing the route
    Then route execution should fail immediately
    And error should indicate "Failed to get ship status"
    And no segments should be attempted

  Scenario: Agent data retrieval fails - abort immediately
    Given a multi-leg route with 2 segments
    And api.get_agent() returns None
    When executing the route
    Then route execution should fail immediately
    And error should indicate "Failed to get agent data"
    And no segments should be attempted

  Scenario: Trade action fails mid-route - continue to next action
    Given a multi-leg route with 1 segment
    And segment 0 has 2 trade actions: BUY COPPER, BUY IRON
    And BUY COPPER fails
    When executing the route
    Then BUY COPPER should be logged as failed
    And BUY IRON should still be attempted
    And route should continue execution

  # Real-World Integration Scenario

  Scenario: Complete real-world trading route
    Given a multi-leg route matching real execution:
      | Segment | From | To | Actions |
      | 0 | E45 | D42 | BUY 18 SHIP_PLATING @ 2000 cr/unit |
      | 1 | D42 | A1 | SELL 18 SHIP_PLATING @ 8000 cr/unit, BUY 21 ASSAULT_RIFLES @ 3000 cr/unit |
      | 2 | A1 | E45 | SELL 21 ASSAULT_RIFLES @ 6000 cr/unit, BUY 20 ADVANCED_CIRCUITRY @ 4000 cr/unit |
      | 3 | E45 | exit | SELL 20 ADVANCED_CIRCUITRY @ 8000 cr/unit |
    And ship starts at "X1-JB26-E45" with 100000 credits
    And all market data is fresh
    When executing the route
    Then all 4 segments should execute successfully
    And total revenue should be 430000 credits
    And total costs should be 179000 credits
    And actual profit should be approximately 251000 credits
    And route execution should succeed

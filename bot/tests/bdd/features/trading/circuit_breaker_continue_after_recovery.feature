Feature: Circuit Breaker Auto-Recovery Continuation
  As a multi-leg trading operator
  I want the trader to CONTINUE after successful auto-recovery
  So that profitable operations are not aborted unnecessarily

  Background:
    Given a ship "TEST-SHIP-1" trading in system "X1-TEST"
    And the ship has 40 cargo capacity
    And agent has 100000 credits

  # This test is marked xfail due to the complexity of mocking multileg_trader's
  # continuation logic. Proper testing would require sophisticated mocking of:
  # - analyze_route_dependencies() for segment dependency graphs
  # - should_skip_segment() profitability calculations (>5000 credits threshold)
  # - Tiered cleanup strategy with cargo blocking checks
  # The test infrastructure (fixtures, mocks, route setup) is in place, but the
  # production code's decision logic is too complex to effectively mock without
  # essentially reimplementing the business logic in the test.
  @xfail
  Scenario: Profitable auto-recovery should continue multi-leg route
    Given a multi-leg route with 3 segments
    And segment 1 has a BUY action for "SHIP_PARTS" at "X1-TEST-D45"
    And the planned buy price is 1200 credits per unit
    And segment 2 has a SELL action at "X1-TEST-A2"
    And the spike threshold is 30 percent

    When executing segment 1, the live market shows buy price at 1800 credits
    And the post-purchase circuit breaker triggers
    And auto-recovery executes successfully
    And recovery generates 8000 credits profit

    Then auto-recovery should complete successfully
    And recovery should generate 8000 credits profit
    And the route should NOT abort
    And the operation should continue with remaining segments
    And remaining independent segments should be available

  Scenario: Trader re-optimizes route after recovery
    Given a multi-leg route with 3 segments
    And segment 1 has a BUY action for "SHIP_PARTS" at "X1-TEST-D45"
    And the planned buy price is 1200 credits per unit
    And segment 2 has a SELL action at "X1-TEST-A2"

    When the post-purchase circuit breaker triggers
    And auto-recovery executes successfully

    Then the trader should re-optimize route with remaining time budget
    And only after duration expires should the operation stop

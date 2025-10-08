Feature: Multileg trader strategy behaviour
  Scenario: Strategy returns negative profit when no opportunities
    Given a profit-first strategy
    And an empty opportunity pool
    When the strategy evaluates market "A" with 100 credits and 5 fuel cost
    Then the evaluation net profit should be -5
    And the evaluation cargo should be empty
    And the evaluation credits should be 100

  Scenario: Strategy reinvests profits after selling cargo
    Given a profit-first strategy
    And trade opportunities for a reinvestment cycle
    And the current cargo is {"IRON": 2}
    When the strategy evaluates market "X1-TEST-B" with 100 credits and 10 fuel cost
    Then the evaluation should include actions SELL,BUY
    And the evaluation credits should equal 130
    And the evaluation cargo should equal {"COPPER": 3}
    And the evaluation net profit should equal 95

  Scenario: Strategy skips invalid trade opportunities
    Given a profit-first strategy
    And trade opportunities with invalid spreads
    And the current cargo is {"IRON": 5}
    When the strategy evaluates market "X1-TEST-A" with 50 credits and 0 fuel cost
    Then the evaluation should include no actions
    And the evaluation credits should equal 50
    And the evaluation cargo should equal {"IRON": 5}
    And the evaluation net profit should equal 0

  Scenario: Greedy planner returns no option when markets are exhausted
    Given a greedy route planner starting at "A" with no cargo or credits
    And a candidate market list of ["B"]
    When the planner searches for the next market
    Then no market option should be selected

  Scenario: Greedy planner selects the highest profit market
    Given a greedy route planner starting at "A" with no cargo or credits
    And a candidate market list of ["B","C"]
    And starting credits are 200
    And the strategy evaluation favors "C" with higher profit
    When the planner searches for the next market
    Then the planner should choose market "C"
    And the planner result profit should be 40

  Scenario: Greedy planner exits when no steps remain
    Given a greedy route planner starting at "A" with no cargo or credits
    And a candidate market list of ["B"]
    And no markets provide profit
    When the planner builds a route with max stops 1
    Then the resulting route should be empty

  Scenario: Executing a loss-making multileg route aborts
    Given an executed multileg route with total profit -100
    When the execution routine runs
    Then the execution should report failure

  Scenario: Executing a profitable multileg route succeeds
    Given an executed multileg route with profitable actions
    When the execution routine runs successfully
    Then the execution should report success

  Scenario: Buy price spike triggers circuit breaker
    Given an executed multileg route prepared for buy price spike
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Actual buy cost spike triggers circuit breaker
    Given an executed multileg route prepared for actual buy spike
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Sell price crash triggers circuit breaker
    Given an executed multileg route prepared for sell price crash
    When the execution routine runs for sell price crash
    Then the execution should report failure

  Scenario: Sale aborted mid-transaction triggers circuit breaker
    Given an executed multileg route prepared for aborted sale
    When the execution routine runs for sell price crash
    Then the execution should report failure

  Scenario: Optimizer builds a profitable route from database data
    Given a multi-leg optimizer with database data for system "SYS"
    When I find an optimal route from "SYS-A"
    Then the optimizer should return a route with 2 segments
    And the optimizer should record market lookups

  Scenario: Optimizer aborts when no markets are available
    Given a multi-leg optimizer with empty market data for system "SYS"
    When I find an optimal route from "SYS-A"
    Then the optimizer should return None

  Scenario: Optimizer warns when no profitable route is found
    Given a multi-leg optimizer with database data for system "SYS"
    And the greedy planner returns no route
    When I find an optimal route from "SYS-A"
    Then the optimizer should return None

  Scenario: Autonomous multileg trade operation completes one cycle
    Given multileg trade args for autonomous one-shot
    And the optimizer yields a profitable route for operation
    And route execution succeeds during operation
    And API credits sequence is 1000,1000,1100,1100,1100
    When I run the multileg trade operation
    Then the trade operation should exit with status 0
    And the optimizer should be called once during operation
    And the route execution should run once

  Scenario: Fixed route mode aborts when route missing
    Given multileg trade args for fixed route mode
    And fixed route builder returns None during operation
    And API credits sequence is 1000,1000
    When I run the multileg trade operation
    Then the trade operation should exit with status 1
    And the route execution should not run

  Scenario: Execution aborts when ship status is unavailable
    Given an executed multileg route prepared for buy price spike
    And ship status retrieval fails before execution
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Execution aborts when agent data is unavailable
    Given an executed multileg route prepared for buy price spike
    And agent lookup fails before execution
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Execution aborts when navigation fails
    Given an executed multileg route prepared for buy price spike
    And navigation fails during execution
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Execution aborts when docking fails
    Given an executed multileg route prepared for buy price spike
    And docking fails during execution
    When the execution routine runs for buy price spike
    Then the execution should report failure

  Scenario: Execution logs warning when overall profit is negative
    Given an executed multileg route prepared for profitable actions with loss
    And API credits sequence is 1000,1000,900,900,900
    When the execution routine runs successfully
    Then the execution should report success

  Scenario: Trade plan operation fails when ship status is missing
    Given trade plan args for ship "SHIP-1"
    And trade plan ship status is unavailable
    When the trade plan operation runs
    Then the trade plan should exit with status 1

  Scenario: Trade plan operation fails when agent data is missing
    Given trade plan args for ship "SHIP-1"
    And trade plan agent data is unavailable
    When the trade plan operation runs
    Then the trade plan should exit with status 1

  Scenario: Trade plan operation handles optimizer failure gracefully
    Given a trade plan request for ship "SHIP-1" with max stops 2
    And the optimizer returns no route
    When the trade plan operation runs
    Then the trade plan should exit with status 1

  Scenario: Trade plan operation reports success when optimizer returns a route
    Given a trade plan request for ship "SHIP-1" with max stops 2
    And the optimizer returns a profitable route
    When the trade plan operation runs
    Then the trade plan should exit with status 0

  Scenario: Fixed route creation succeeds
    Given fixed route market data buy price 30 sell price 90 trade volume 5
    And default distance per leg is 1 units
    When I create a fixed route from "SYS-A" buying at "SYS-B" selling at "SYS-C" for "COPPER"
    Then the fixed route should have 2 segments
    And the fixed route profit should be positive

  Scenario: Fixed route creation fails when market data is missing
    Given fixed route market data buy price 30 sell price 90 trade volume 5
    And the sell market data is unavailable
    And default distance per leg is 10 units
    When I create a fixed route from "SYS-A" buying at "SYS-B" selling at "SYS-C" for "COPPER"
    Then the fixed route result should be None

  Scenario: Fixed route creation aborts when spread is unprofitable
    Given fixed route market data buy price 50 sell price 55 trade volume 5
    And default distance per leg is 100 units
    When I create a fixed route from "SYS-A" buying at "SYS-B" selling at "SYS-C" for "COPPER"
    Then the fixed route result should be None

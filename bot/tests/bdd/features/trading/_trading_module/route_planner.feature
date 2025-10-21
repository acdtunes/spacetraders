Feature: Route Planning - Greedy route optimization and fixed route creation
  As a trader
  I want to plan optimal multi-leg trading routes
  So that I can maximize profit across multiple markets

  Background:
    Given a test database with market data
    And a mock API client
    And the following markets in system "X1-TEST":
      | waypoint  | x   | y   |
      | X1-TEST-A | 0   | 0   |
      | X1-TEST-B | 100 | 0   |
      | X1-TEST-C | 200 | 0   |
      | X1-TEST-D | 300 | 0   |

  # ============================================================================
  # GreedyRoutePlanner Tests
  # ============================================================================

  Scenario: GreedyRoutePlanner - Simple 2-stop profitable route
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And trade opportunity: buy "IRON_ORE" at "X1-TEST-A" for 100, sell at "X1-TEST-B" for 200
    And starting at "X1-TEST-A" with 10,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then route should have 1 segment
    And segment 1 should go from "X1-TEST-A" to "X1-TEST-B"
    And segment 1 should BUY "IRON_ORE" at "X1-TEST-A"
    And segment 1 should SELL "IRON_ORE" at "X1-TEST-B"
    And route total profit should be greater than 0

  Scenario: GreedyRoutePlanner - 3-stop route with chaining
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And trade opportunity: buy "IRON_ORE" at "X1-TEST-A" for 100, sell at "X1-TEST-B" for 200
    And trade opportunity: buy "COPPER_ORE" at "X1-TEST-B" for 150, sell at "X1-TEST-C" for 300
    And starting at "X1-TEST-A" with 20,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then route should have 2 segments
    And segment 1 should buy and sell "IRON_ORE"
    And segment 1 should buy "COPPER_ORE"
    And segment 2 should sell "COPPER_ORE"
    And cumulative profit should increase with each segment
    And total profit should exceed sum of individual spreads

  Scenario: GreedyRoutePlanner - Route with starting cargo
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And ship has 20 units of "ALUMINUM_ORE" in cargo
    And trade opportunity: sell "ALUMINUM_ORE" at "X1-TEST-B" for 250
    And trade opportunity: buy "IRON_ORE" at "X1-TEST-B" for 100, sell at "X1-TEST-C" for 200
    And starting at "X1-TEST-A" with 5,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then first segment should SELL "ALUMINUM_ORE"
    And starting cargo should be accounted for in profitability
    And route should chain additional profitable trades

  Scenario: GreedyRoutePlanner - Stops at max stops limit
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And 5 profitable trade opportunities exist in sequence
    And starting at "X1-TEST-A" with 50,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then route should have exactly 3 segments
    And route should not exceed max stops
    And route should select 3 most profitable segments

  Scenario: GreedyRoutePlanner - No profitable moves (early termination)
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And trade opportunity: buy "IRON_ORE" at "X1-TEST-A" for 100, sell at "X1-TEST-B" for 200
    And no other profitable opportunities exist
    And starting at "X1-TEST-A" with 10,000 credits and 50 cargo capacity
    When I find a route with max 5 stops
    Then route should have 1 segment
    And route planning should terminate early
    And total profit should reflect single trade only

  Scenario: GreedyRoutePlanner - Visited market exclusion
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And trade opportunity: buy "IRON_ORE" at "X1-TEST-A" for 100, sell at "X1-TEST-B" for 200
    And trade opportunity: buy "COPPER_ORE" at "X1-TEST-B" for 150, sell at "X1-TEST-A" for 300
    And starting at "X1-TEST-A" with 20,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then route should not revisit "X1-TEST-A"
    And only segment to "X1-TEST-B" should be included
    And visited markets should prevent backtracking

  Scenario: GreedyRoutePlanner - Returns None when no route possible
    Given a GreedyRoutePlanner with ProfitFirstStrategy
    And no profitable trade opportunities exist
    And starting at "X1-TEST-A" with 10,000 credits and 50 cargo capacity
    When I find a route with max 3 stops
    Then route should be None
    And no segments should be created

  # ============================================================================
  # MultiLegTradeOptimizer Tests
  # ============================================================================

  Scenario: MultiLegTradeOptimizer - Happy path optimization
    Given a MultiLegTradeOptimizer for player 1
    And system "X1-TEST" has 4 markets with coordinates
    And profitable trade opportunities exist in "X1-TEST"
    And ship starts at "X1-TEST-A" with 50 cargo capacity
    And ship has 15,000 starting credits
    And ship speed is 30 and fuel capacity is 1000
    When I find optimal route with max 4 stops
    Then route should be returned
    And route should have positive total profit
    And route should have valid segments
    And route estimated time should be calculated

  Scenario: MultiLegTradeOptimizer - Filters stale market data
    Given a MultiLegTradeOptimizer for player 1
    And system "X1-TEST" has markets with mixed data freshness:
      | waypoint  | good      | age_hours | should_include |
      | X1-TEST-A | IRON_ORE  | 0.5       | yes            |
      | X1-TEST-B | COPPER_ORE| 1.5       | no             |
      | X1-TEST-C | ALUMINUM  | 0.1       | yes            |
    When I find optimal route with max 3 stops
    Then only fresh opportunities should be used
    And stale data should be logged as skipped
    And route should only include fresh markets

  Scenario: MultiLegTradeOptimizer - No markets in system
    Given a MultiLegTradeOptimizer for player 1
    And system "X1-EMPTY" has no markets
    And ship starts at "X1-EMPTY-A"
    When I find optimal route with max 3 stops
    Then route should be None
    And error should indicate no markets found

  Scenario: MultiLegTradeOptimizer - No profitable opportunities
    Given a MultiLegTradeOptimizer for player 1
    And system "X1-TEST" has markets but no profitable trades
    And ship starts at "X1-TEST-A" with 10,000 credits
    When I find optimal route with max 3 stops
    Then route should be None
    And warning should indicate no profitable route found

  # ============================================================================
  # create_fixed_route Tests
  # ============================================================================

  Scenario: create_fixed_route - Basic 2-stop route
    Given market data exists for "IRON_ORE":
      | waypoint  | action | price |
      | X1-TEST-A | sell   | 100   |
      | X1-TEST-B | buy    | 200   |
    And waypoint coordinates exist for "X1-TEST-A" and "X1-TEST-B"
    And ship is at "X1-TEST-A" with 50 cargo capacity
    And ship has 10,000 starting credits
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should have 1 segment
    And segment should BUY at "X1-TEST-A" and SELL at "X1-TEST-B"
    And route total profit should be positive

  Scenario: create_fixed_route - Already at buy market (1-stop route)
    Given market data exists for "IRON_ORE":
      | waypoint  | action | price |
      | X1-TEST-A | sell   | 100   |
      | X1-TEST-B | buy    | 200   |
    And waypoint coordinates exist
    And ship is at "X1-TEST-A" (the buy market)
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should have 1 segment
    And segment should BUY at "X1-TEST-A" and SELL at "X1-TEST-B"
    And route should skip navigation to buy market

  Scenario: create_fixed_route - Missing market data
    Given market data is missing for "X1-TEST-B"
    And ship is at "X1-TEST-A"
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should be None
    And error should indicate missing market data

  Scenario: create_fixed_route - Unprofitable spread
    Given market data exists for "IRON_ORE":
      | waypoint  | action | price |
      | X1-TEST-A | sell   | 200   |
      | X1-TEST-B | buy    | 180   |
    And waypoint coordinates exist
    And ship is at "X1-TEST-A"
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should be None
    And warning should indicate unprofitable route

  Scenario: create_fixed_route - Insufficient credits
    Given market data exists for "IRON_ORE":
      | waypoint  | action | price |
      | X1-TEST-A | sell   | 1000  |
      | X1-TEST-B | buy    | 1200  |
    And waypoint coordinates exist
    And ship is at "X1-TEST-A" with 500 credits
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should be None
    And error should indicate cannot afford any units

  Scenario: create_fixed_route - Missing waypoint coordinates
    Given market data exists for "IRON_ORE"
    And coordinates are missing for "X1-TEST-B"
    And ship is at "X1-TEST-A"
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then route should be None
    And error should list missing waypoint coordinates

  Scenario: create_fixed_route - Trade volume constraint
    Given market data exists for "IRON_ORE":
      | waypoint  | action | price | trade_volume |
      | X1-TEST-A | sell   | 100   | 20           |
      | X1-TEST-B | buy    | 200   | 50           |
    And waypoint coordinates exist
    And ship is at "X1-TEST-A" with 100 cargo capacity and 50,000 credits
    When I create fixed route from "X1-TEST-A" to "X1-TEST-B" for "IRON_ORE"
    Then BUY action should be limited to 20 units
    And route should respect trade_volume constraint

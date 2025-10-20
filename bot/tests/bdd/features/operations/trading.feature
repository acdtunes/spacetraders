Feature: Trading operations
  As a trading operator
  I want to analyze markets and calculate trade profitability
  So that I can execute profitable trading routes

  Background:
    Given a trading analysis system

  Scenario: Calculate profit margin for single trade
    Given market A sells "IRON_ORE" at 120 credits per unit
    And market B buys "IRON_ORE" at 180 credits per unit
    When I calculate profit margin
    Then profit per unit should be 60 credits
    And profit margin percentage should be 50%

  Scenario: Calculate round-trip profit with fuel costs
    Given market A sells "COPPER" at 100 credits per unit
    And market B buys "COPPER" at 150 credits per unit
    And cargo capacity is 40 units
    And distance between markets is 200 units
    And fuel cost is 1 credit per unit distance
    When I calculate round-trip profitability
    Then gross revenue should be 2000 credits
    And fuel cost should be 400 credits
    And net profit should be 1600 credits

  Scenario: Reject unprofitable trade route
    Given market A sells "GOLD" at 500 credits per unit
    And market B buys "GOLD" at 520 credits per unit
    And cargo capacity is 30 units
    And distance between markets is 500 units
    And fuel cost is 2 credits per unit distance
    When I evaluate trade profitability
    Then trade should be unprofitable
    And rejection reason should mention fuel costs

  Scenario: Find most profitable commodity at market
    Given market has "IRON_ORE" selling at 100 with profit margin 40%
    And market has "COPPER" selling at 150 with profit margin 55%
    And market has "GOLD" selling at 400 with profit margin 30%
    When I rank commodities by profit margin
    Then top commodity should be "COPPER"
    And top profit margin should be 55%

  Scenario: Calculate multi-leg trading sequence
    Given a 3-leg trading route
    And leg 1: buy "IRON" at 100, sell at 140 (profit 40)
    And leg 2: buy "COPPER" at 120, sell at 170 (profit 50)
    And leg 3: buy "GOLD" at 300, sell at 360 (profit 60)
    And cargo capacity is 20 units per leg
    When I calculate total sequence profit
    Then total profit should be 3000 credits
    And average profit per leg should be 1000 credits

  Scenario: Optimize cargo allocation for mixed commodities
    Given cargo capacity is 40 units
    And "IRON" available: profit 30 credits per unit
    And "COPPER" available: profit 50 credits per unit
    And "GOLD" available: profit 80 credits per unit
    When I optimize cargo allocation for maximum profit
    Then allocation should prioritize "GOLD"
    And expected profit should be maximized

  Scenario: Estimate trading cycle time
    Given distance to market is 300 units
    And ship speed in CRUISE mode is 30 units per second
    And loading time is 60 seconds
    And unloading time is 60 seconds
    When I calculate total cycle time
    Then travel time should be 10 seconds
    And total cycle time should be 140 seconds
    And cycles per hour should be approximately 25

  Scenario: Filter markets by minimum profit threshold
    Given 5 markets with various profit margins
    And market 1 offers 45% profit margin
    And market 2 offers 30% profit margin
    And market 3 offers 60% profit margin
    And market 4 offers 25% profit margin
    And market 5 offers 55% profit margin
    And minimum profit threshold is 50%
    When I filter profitable markets
    Then 2 markets should be selected
    And selected markets should include market 3
    And selected markets should include market 5

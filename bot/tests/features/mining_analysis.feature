Feature: Mining Opportunity Analysis
  As a mining operation manager
  I want to analyze mining opportunities
  So that I can maximize profit per hour

  Scenario: Filter asteroids with good mining traits
    Given an asteroid with "PRECIOUS_METAL_DEPOSITS" trait
    When I check if the asteroid is suitable for mining
    Then the asteroid should be accepted

  Scenario: Reject asteroids with bad traits
    Given an asteroid with "STRIPPED" trait
    When I check if the asteroid is suitable for mining
    Then the asteroid should be rejected

  Scenario: Reject asteroids with radioactive traits
    Given an asteroid with "RADIOACTIVE" trait
    When I check if the asteroid is suitable for mining
    Then the asteroid should be rejected

  Scenario: Accept asteroids with multiple good traits
    Given an asteroid with "COMMON_METAL_DEPOSITS,MINERAL_DEPOSITS" traits
    When I check if the asteroid is suitable for mining
    Then the asteroid should be accepted

  Scenario: Reject asteroids with mixed good and bad traits
    Given an asteroid with "PRECIOUS_METAL_DEPOSITS,STRIPPED" traits
    When I check if the asteroid is suitable for mining
    Then the asteroid should be rejected

  Scenario: Map precious metal deposits to materials
    Given an asteroid with "PRECIOUS_METAL_DEPOSITS" trait
    When I determine possible materials
    Then the materials should include "GOLD_ORE"
    And the materials should include "SILVER_ORE"
    And the materials should include "PLATINUM_ORE"

  Scenario: Map common metal deposits to materials
    Given an asteroid with "COMMON_METAL_DEPOSITS" trait
    When I determine possible materials
    Then the materials should include "IRON_ORE"
    And the materials should include "COPPER_ORE"
    And the materials should include "ALUMINUM_ORE"

  Scenario: Map mineral deposits to materials
    Given an asteroid with "MINERAL_DEPOSITS" trait
    When I determine possible materials
    Then the materials should include "SILICON_CRYSTALS"
    And the materials should include "QUARTZ_SAND"
    And the materials should include "ICE_WATER"

  Scenario: Calculate mining cycle time for close asteroid
    Given an asteroid at distance 50 units from market
    When I calculate the mining cycle time
    Then the total cycle time should be approximately 14.7 minutes
    And the mining time should be 12 minutes
    And the travel time should be approximately 1.7 minutes

  Scenario: Calculate mining cycle time for far asteroid
    Given an asteroid at distance 200 units from market
    When I calculate the mining cycle time
    Then the total cycle time should be approximately 19.9 minutes
    And the mining time should be 12 minutes
    And the travel time should be approximately 6.9 minutes

  Scenario: Calculate profit per trip for high-value material
    Given a market buying "GOLD_ORE" at 500 credits per unit
    And an asteroid at distance 50 units from market
    And a cargo capacity of 40 units
    When I calculate the mining profit
    Then the revenue should be 20000 credits
    And the fuel cost should be approximately 11000 credits
    And the net profit should be approximately 9000 credits

  Scenario: Calculate profit per trip for low-value material
    Given a market buying "IRON_ORE" at 50 credits per unit
    And an asteroid at distance 50 units from market
    And a cargo capacity of 40 units
    When I calculate the mining profit
    Then the revenue should be 2000 credits
    And the fuel cost should be approximately 11000 credits
    And the net profit should be negative

  Scenario: Calculate profit per hour for profitable operation
    Given a market buying "GOLD_ORE" at 500 credits per unit
    And an asteroid at distance 50 units from market
    And a cargo capacity of 40 units
    And a mining cycle time of 14.7 minutes
    When I calculate the mining profit
    And I calculate the profit per hour
    Then the profit per hour should be approximately 37000 credits

  Scenario: Rank opportunities by profit per hour
    Given a close asteroid with high-value materials
    And a far asteroid with higher-value materials
    When I rank the mining opportunities
    Then the close asteroid should rank higher
    And the ranking should be by profit per hour

  Scenario: Calculate travel time using CRUISE mode formula
    Given a distance of 100 units
    And an engine speed of 30
    And flight mode "CRUISE"
    When I calculate the travel time
    Then the travel time should be approximately 103 seconds

  Scenario: Calculate round trip travel time
    Given a distance of 100 units
    And an engine speed of 30
    And flight mode "CRUISE"
    When I calculate the round trip travel time
    Then the round trip time should be approximately 206 seconds

  Scenario: Find best market among multiple options
    Given markets buying "GOLD_ORE" at prices 400, 500, 450
    And corresponding distances of 100, 50, 75 units
    When I select the best market
    Then the selected market should be the one at 50 units
    And the selected price should be 500 credits

  Scenario: Exclude markets with negative profit
    Given a market buying "IRON_ORE" at 30 credits per unit
    And an asteroid at distance 300 units from market
    And a cargo capacity of 40 units
    When I calculate the mining profit
    Then the net profit should be negative
    And the opportunity should be excluded

  Scenario: Include only profitable opportunities
    Given 5 mining opportunities with various distances and prices
    When I filter by positive profit
    Then only opportunities with net profit > 0 should be included

  Scenario: Sort opportunities by hourly profit descending
    Given opportunities with profit per hour: 10000, 25000, 15000, 30000
    When I sort the opportunities
    Then the first opportunity should have 30000 credits per hour
    And the second opportunity should have 25000 credits per hour
    And the last opportunity should have 10000 credits per hour

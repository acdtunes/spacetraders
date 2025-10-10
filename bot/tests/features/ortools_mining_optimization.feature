Feature: OR-Tools Mining Fleet Optimization
  As a fleet manager
  I want to optimally assign mining ships to asteroid-market pairs
  So that I maximize total fleet profit per hour

  Background:
    Given a system "X1-TEST" with the following waypoints:
      | symbol       | type     | x    | y    | traits                    |
      | X1-TEST-A1   | PLANET   | 0    | 0    | MARKETPLACE               |
      | X1-TEST-B5   | ASTEROID | 100  | 50   | COMMON_METAL_DEPOSITS     |
      | X1-TEST-B9   | ASTEROID | -80  | 60   | PRECIOUS_METAL_DEPOSITS   |
      | X1-TEST-C3   | ASTEROID | 50   | -100 | MINERAL_DEPOSITS          |
      | X1-TEST-D7   | PLANET   | 200  | 0    | EXCHANGE                  |
      | X1-TEST-E2   | ASTEROID | -150 | 0    | STRIPPED                  |
    And the following market prices:
      | market       | good        | purchase_price |
      | X1-TEST-A1   | COPPER_ORE  | 150           |
      | X1-TEST-A1   | IRON_ORE    | 200           |
      | X1-TEST-D7   | GOLD_ORE    | 500           |
      | X1-TEST-D7   | COPPER_ORE  | 120           |

  Scenario: Optimize single mining ship assignment
    Given a mining ship "MINER-1" with:
      | attribute      | value |
      | engine_speed   | 30    |
      | cargo_capacity | 40    |
      | fuel_capacity  | 400   |
    When I optimize the mining fleet assignment
    Then "MINER-1" should be assigned to a profitable asteroid-market pair
    And the expected profit per hour should be greater than 40000 credits
    And the assigned market should be "X1-TEST-D7"

  Scenario: Optimize multiple ships to different asteroids
    Given the following mining ships:
      | symbol   | engine_speed | cargo_capacity | fuel_capacity |
      | MINER-1  | 30           | 40             | 400           |
      | MINER-2  | 25           | 35             | 350           |
      | MINER-3  | 35           | 45             | 450           |
    When I optimize the mining fleet assignment
    Then each ship should be assigned to a different asteroid
    And the total fleet profit per hour should be greater than 12000 credits

  Scenario: Filter out asteroids with hazardous traits
    Given a mining ship "MINER-1" with:
      | attribute      | value |
      | engine_speed   | 30    |
      | cargo_capacity | 40    |
      | fuel_capacity  | 400   |
    When I optimize the mining fleet assignment
    Then "MINER-1" should not be assigned to asteroid "X1-TEST-E2"
    And asteroid "X1-TEST-E2" should be excluded from opportunities

  Scenario: Assign faster ships to distant asteroids
    Given the following mining ships:
      | symbol      | engine_speed | cargo_capacity | fuel_capacity | current_location |
      | FAST-MINER  | 50           | 40             | 400           | X1-TEST-A1       |
      | SLOW-MINER  | 20           | 40             | 400           | X1-TEST-A1       |
    And an asteroid "X1-TEST-FAR" at distance 500 from "X1-TEST-A1"
    And an asteroid "X1-TEST-NEAR" at distance 100 from "X1-TEST-A1"
    When I optimize the mining fleet assignment
    Then "FAST-MINER" should be assigned to the more distant asteroid
    And "SLOW-MINER" should be assigned to the closer asteroid

  Scenario: Prioritize markets with higher prices
    Given a mining ship "MINER-1" with:
      | attribute      | value |
      | engine_speed   | 30    |
      | cargo_capacity | 40    |
      | fuel_capacity  | 400   |
    And asteroid "X1-TEST-B5" produces only "COPPER_ORE"
    And market "X1-TEST-A1" buys only "COPPER_ORE" at 150 credits
    And market "X1-TEST-D7" buys only "COPPER_ORE" at 120 credits
    And both markets are equidistant from "X1-TEST-B5"
    When I optimize the mining fleet assignment
    Then "MINER-1" should be assigned to market "X1-TEST-A1"
    Because it offers the highest price for the available ore

  Scenario: Respect maximum profitable distance
    Given a mining ship "MINER-1" with:
      | attribute      | value |
      | engine_speed   | 30    |
      | cargo_capacity | 40    |
      | fuel_capacity  | 400   |
    And asteroid "X1-TEST-VERYFAR" at distance 600 from nearest market
    And maximum profitable distance is 500 units
    When I optimize the mining fleet assignment
    Then "MINER-1" should not be assigned to asteroid "X1-TEST-VERYFAR"
    And asteroid "X1-TEST-VERYFAR" should be excluded from opportunities

  Scenario: Handle more ships than opportunities
    Given the following mining ships:
      | symbol   | engine_speed | cargo_capacity | fuel_capacity |
      | MINER-1  | 30           | 40             | 400           |
      | MINER-2  | 30           | 40             | 400           |
      | MINER-3  | 30           | 40             | 400           |
      | MINER-4  | 30           | 40             | 400           |
      | MINER-5  | 30           | 40             | 400           |
    And only 3 profitable mining opportunities exist
    When I optimize the mining fleet assignment
    Then exactly 3 ships should receive assignments
    And 2 ships should remain unassigned

  Scenario: Calculate accurate profit estimates
    Given a mining ship "MINER-1" with:
      | attribute      | value |
      | engine_speed   | 30    |
      | cargo_capacity | 40    |
      | fuel_capacity  | 400   |
    And asteroid "X1-TEST-B9" at distance 100 from market "X1-TEST-D7"
    And market "X1-TEST-D7" buys "GOLD_ORE" at 500 credits per unit
    And average extraction yield is 3.5 units per cycle
    And extraction cooldown is 80 seconds
    When I calculate the profit per hour for this opportunity
    Then the profit should account for travel time
    And the profit should account for fuel costs
    And the profit should account for extraction cooldown
    And the cycle time should be approximately 16 minutes

  Scenario: Generate mining opportunities from graph
    Given a system graph with 5 asteroids and 3 markets
    When I generate mining opportunities
    Then opportunities should be sorted by profit per hour descending
    And each opportunity should include asteroid, market, and profit metrics
    And opportunities with negative profit should be excluded

  Scenario: Optimize with ship-specific characteristics
    Given the following mining ships:
      | symbol    | engine_speed | cargo_capacity | fuel_capacity |
      | FAST-SHIP | 50           | 40             | 400           |
      | SLOW-SHIP | 20           | 40             | 400           |
    And two identical mining opportunities at different distances
    When I optimize the mining fleet assignment
    Then "FAST-SHIP" should receive the more profitable assignment
    And faster ships complete cycles quicker

  Scenario: Integration with database market prices
    Given a mining ship "MINER-1"
    And market prices are stored in the database
    And market "X1-TEST-A1" last updated 5 minutes ago
    And market "X1-TEST-D7" last updated 120 minutes ago
    When I optimize the mining fleet assignment
    Then the optimizer should use database market prices
    And recent price data should be prioritized

  Scenario: Benchmark OR-Tools vs greedy algorithm
    Given 5 mining ships in system "X1-TEST"
    And 10 mining opportunities exist
    When I run both greedy and OR-Tools optimizers
    Then OR-Tools should find equal or better total profit
    And OR-Tools solution time should be less than 1 second
    And profit improvement should be logged

  Scenario: Empty fleet or no opportunities
    Given an empty mining fleet
    When I optimize the mining fleet assignment
    Then the result should be an empty assignment dictionary
    And no errors should occur

  Scenario: No profitable opportunities exist
    Given a mining ship "MINER-1"
    And all asteroids are beyond maximum distance
    When I optimize the mining fleet assignment
    Then the result should be an empty assignment dictionary
    And a warning should be logged about no opportunities

  Scenario: Assignment result structure validation
    Given a mining ship "MINER-1"
    When I optimize the mining fleet assignment
    And an assignment is returned for "MINER-1"
    Then the assignment should contain:
      | field                 | type  |
      | ship                  | str   |
      | asteroid              | str   |
      | market                | str   |
      | good                  | str   |
      | profit_per_hour       | float |
      | cycle_time_minutes    | float |
      | fuel_cost_per_cycle   | int   |
      | revenue_per_cycle     | int   |

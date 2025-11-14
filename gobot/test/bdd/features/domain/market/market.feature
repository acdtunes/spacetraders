Feature: Market Value Object
  As a market scouting system
  I need to aggregate trade goods data for a market
  So that I can query and track market state over time

  Background:
    Given I have a market at waypoint "X1-TEST-A1"

  Scenario: Create market with multiple trade goods
    Given I have the following trade goods:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 50             | 100        | 500          |
      | FUEL     | HIGH     | WEAK     | 10             | 20         | 1000         |
      | FOOD     | LIMITED  | GROWING  | 30             | 60         | 200          |
    When I create a market with waypoint "X1-TEST-A1" at timestamp "2025-11-13T12:00:00Z" with these goods
    Then the market should have waypoint symbol "X1-TEST-A1"
    And the market should have 3 trade goods
    And the market should have last updated "2025-11-13T12:00:00Z"

  Scenario: Create market with no trade goods
    When I create a market with waypoint "X1-TEST-A1" at timestamp "2025-11-13T12:00:00Z" with no goods
    Then the market should have waypoint symbol "X1-TEST-A1"
    And the market should have 0 trade goods
    And the market should have last updated "2025-11-13T12:00:00Z"

  Scenario: Find specific trade good in market
    Given I have the following trade goods:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 50             | 100        | 500          |
      | FUEL     | HIGH     | WEAK     | 10             | 20         | 1000         |
    And I have created a market with these goods at "X1-TEST-A1"
    When I search for good "IRON_ORE" in the market
    Then I should find the good with purchase price 50 and sell price 100

  Scenario: Check if market has specific good
    Given I have the following trade goods:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 50             | 100        | 500          |
      | FUEL     | HIGH     | WEAK     | 10             | 20         | 1000         |
    And I have created a market with these goods at "X1-TEST-A1"
    When I check if the market has good "IRON_ORE"
    Then the has good result should be true
    When I check if the market has good "FOOD"
    Then the has good result should be false

  Scenario: Count goods in market
    Given I have the following trade goods:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 50             | 100        | 500          |
      | FUEL     | HIGH     | WEAK     | 10             | 20         | 1000         |
      | FOOD     | LIMITED  | GROWING  | 30             | 60         | 200          |
    And I have created a market with these goods at "X1-TEST-A1"
    When I count the goods in the market
    Then the goods count should be 3

  Scenario: Reject market with empty waypoint symbol
    When I attempt to create a market with empty waypoint at timestamp "2025-11-13T12:00:00Z"
    Then I should get a market error "waypoint symbol cannot be empty"

  Scenario: Reject market with invalid timestamp
    When I attempt to create a market with waypoint "X1-TEST-A1" at empty timestamp
    Then I should get a market error "timestamp cannot be empty"

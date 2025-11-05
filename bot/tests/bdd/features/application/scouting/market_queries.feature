Feature: Market Data Queries
  Query market data from the database

  Background:
    Given a player with ID 1
    And market data exists for waypoint "X1-GZ7-A1" with goods:
      | symbol    | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE  | MODERATE | STRONG   | 50             | 100        | 1000         |
      | FUEL      | HIGH     | WEAK     | 10             | 20         | 5000         |
    And the market data was last updated "2025-01-15T10:00:00Z"

  Scenario: Get market data for existing waypoint
    When I query market data for waypoint "X1-GZ7-A1"
    Then I should receive market data
    And the market should have 2 trade goods
    And the market should be for waypoint "X1-GZ7-A1"

  Scenario: Get market data for non-existent waypoint
    When I query market data for waypoint "X1-GZ7-Z99"
    Then I should receive no market data

  Scenario: List markets in system
    Given market data exists for waypoint "X1-GZ7-B2" with goods:
      | symbol   | supply | activity | purchase_price | sell_price | trade_volume |
      | COPPER   | LOW    | STRONG   | 40             | 80         | 500          |
    When I list markets in system "X1-GZ7"
    Then I should receive 2 markets
    And the markets should include "X1-GZ7-A1"
    And the markets should include "X1-GZ7-B2"

  Scenario: List markets with freshness filter
    Given market data exists for waypoint "X1-GZ7-B2" with goods:
      | symbol   | supply | activity | purchase_price | sell_price | trade_volume |
      | COPPER   | LOW    | STRONG   | 40             | 80         | 500          |
    And market data exists for waypoint "X1-GZ7-C3" with goods:
      | symbol   | supply | activity | purchase_price | sell_price | trade_volume |
      | GOLD     | SCARCE | WEAK     | 200            | 400        | 100          |
    And the last market update was "2020-01-01T00:00:00Z"
    When I list markets in system "X1-GZ7" with max age 60 minutes
    Then I should receive 2 markets
    And the markets should not include "X1-GZ7-C3"

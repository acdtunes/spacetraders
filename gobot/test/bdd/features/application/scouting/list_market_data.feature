Feature: List Market Data Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: List all markets in system
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1
    And market data exists for waypoint "X1-A2-MARKET" with player 1
    And market data exists for waypoint "X1-A3-MARKET" with player 1
    When I execute list market data query for system "X1-A1" with player 1 and max age 60 minutes
    Then the query should succeed
    And 3 markets should be returned

  Scenario: Filter by age
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1 scanned 10 minutes ago
    And market data exists for waypoint "X1-A2-MARKET" with player 1 scanned 70 minutes ago
    When I execute list market data query for system "X1-A1" with player 1 and max age 60 minutes
    Then the query should succeed
    And 1 market should be returned
    And "X1-A1-MARKET" should be in the results
    And "X1-A2-MARKET" should not be in the results

  Scenario: Empty results
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute list market data query for system "X1-EMPTY" with player 1 and max age 60 minutes
    Then the query should succeed
    And 0 markets should be returned

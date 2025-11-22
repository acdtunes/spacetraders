Feature: Get Market Data Query

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Retrieve existing market data
    Given a player with ID 1 and token "test-token" exists in the database
    And market data exists for waypoint "X1-A1-MARKET" with player 1
    When I execute get market data query for waypoint "X1-A1-MARKET" with player 1
    Then the query should succeed
    And the market data should be returned
    And the market waypoint should be "X1-A1-MARKET"

  Scenario: Market not found
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute get market data query for waypoint "X1-NONEXISTENT" with player 1
    Then the query should succeed
    And the query should return no market data

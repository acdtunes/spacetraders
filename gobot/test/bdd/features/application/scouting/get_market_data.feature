Feature: Get Market Data Query
  As a market analysis system
  I need to retrieve market data for a waypoint
  So that I can analyze trade opportunities

  Scenario: Successfully get market data
    Given a player with ID 1
    And market data exists for waypoint "X1-TEST-A1" with 2 trade goods
    When I query market data for waypoint "X1-TEST-A1"
    Then the query should succeed
    And the queried market should have 2 trade goods

  Scenario: Query non-existent market returns nil
    Given a player with ID 1
    When I query market data for waypoint "X1-NONEXISTENT"
    Then the query should succeed
    And the queried market should be nil

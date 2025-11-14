Feature: List Market Data Query
  As a market analysis system
  I need to retrieve all market data in a system
  So that I can analyze trade opportunities across multiple markets

  Scenario: Successfully list all markets in a system
    Given a player with ID 1
    And market data exists for waypoint "X1-TEST-A1" in system "X1-TEST" with 2 trade goods
    And market data exists for waypoint "X1-TEST-B2" in system "X1-TEST" with 3 trade goods
    And market data exists for waypoint "X1-TEST-C3" in system "X1-TEST" with 1 trade goods
    When I query all markets in system "X1-TEST" with max age 60 minutes
    Then the list query should succeed
    And the market list should contain 3 markets

  Scenario: Filter markets by max age
    Given a player with ID 1
    And market data exists for waypoint "X1-TEST-A1" in system "X1-TEST" with 2 trade goods updated 10 minutes ago
    And market data exists for waypoint "X1-TEST-B2" in system "X1-TEST" with 3 trade goods updated 120 minutes ago
    And market data exists for waypoint "X1-TEST-C3" in system "X1-TEST" with 1 trade goods updated 5 minutes ago
    When I query all markets in system "X1-TEST" with max age 60 minutes
    Then the list query should succeed
    And the market list should contain 2 markets
    And the market list should include waypoint "X1-TEST-A1"
    And the market list should include waypoint "X1-TEST-C3"
    And the market list should not include waypoint "X1-TEST-B2"

  Scenario: Query empty system returns empty list
    Given a player with ID 1
    When I query all markets in system "X1-EMPTY" with max age 60 minutes
    Then the list query should succeed
    And the market list should be empty

  Scenario: Query with zero max age returns all markets
    Given a player with ID 1
    And market data exists for waypoint "X1-TEST-A1" in system "X1-TEST" with 2 trade goods updated 500 minutes ago
    And market data exists for waypoint "X1-TEST-B2" in system "X1-TEST" with 3 trade goods updated 1000 minutes ago
    When I query all markets in system "X1-TEST" with max age 0 minutes
    Then the list query should succeed
    And the market list should contain 2 markets

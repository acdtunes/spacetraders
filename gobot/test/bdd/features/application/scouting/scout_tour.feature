Feature: Scout Tour Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Stationary scout (1 market) - already at location
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET"] with 1 iteration
    Then the command should succeed
    And 1 market should be visited
    And the market "X1-A1-MARKET" should be scanned 1 time

  Scenario: Stationary scout (1 market) - needs navigation
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-START"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET"] with 1 iteration
    Then the command should succeed
    And the ship should navigate 1 time
    And 1 market should be visited

  Scenario: Multi-market tour
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET", "X1-A2-MARKET", "X1-A3-MARKET"] with 2 iterations
    Then the command should succeed
    And the ship should navigate 6 times
    And 6 markets should be visited in total

  Scenario: Tour rotation starts from current location
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A2-MARKET"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets ["X1-A1-MARKET", "X1-A2-MARKET", "X1-A3-MARKET"] with 1 iteration
    Then the command should succeed
    And the tour order should start with "X1-A2-MARKET"
    And the tour order should be ["X1-A2-MARKET", "X1-A3-MARKET", "X1-A1-MARKET"]

  Scenario: Empty markets list
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-MARKET"
    When I execute scout tour command for player 1 with ship "SHIP-1" and markets [] with 1 iteration
    Then the command should succeed
    And 0 markets should be visited in total

  Scenario: Ship not found
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute scout tour command for player 1 with ship "NONEXISTENT" and markets ["X1-A1-MARKET"] with 1 iteration
    Then the command should return an error containing "failed to find ship"

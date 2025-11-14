Feature: Scout Tour Command
  As a market scouting system
  I need to execute market scouting tours with a single ship
  So that I can continuously update market price data

  Background:
    Given a test database
    And a registered player with ID 1 and agent "TEST-AGENT"
    And the player has token "test-token-123"

  Scenario: Scout a single-market tour (stationary scout)
    Given the player has a ship "SCOUT-1" at waypoint "X1-TEST-A1" with status "DOCKED"
    And the system "X1-TEST" has waypoint "X1-TEST-A1" with coordinates 0, 0
    And the API will return market data for "X1-TEST-A1" with 2 trade goods
    When I execute ScoutTourCommand with ship "SCOUT-1", markets "X1-TEST-A1", and 1 iteration
    Then the scout tour command should succeed
    And the scout ship should be at "X1-TEST-A1"
    And market data should be persisted for waypoint "X1-TEST-A1"
    And the persisted market should have 2 trade goods

  Scenario: Scout a multi-market tour
    Given the player has a ship "SCOUT-1" at waypoint "X1-TEST-A1" with status "DOCKED"
    And the system "X1-TEST" has the following waypoints:
      | Symbol      | X | Y  |
      | X1-TEST-A1  | 0 | 0  |
      | X1-TEST-B2  | 5 | 5  |
      | X1-TEST-C3  | 10| 0  |
    And the API will return market data for all waypoints
    When I execute ScoutTourCommand with ship "SCOUT-1", markets "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3", and 1 iteration
    Then the scout tour command should succeed
    And the scout ship should have visited 3 markets
    And the scout ship should be at "X1-TEST-A1"
    And market data should be persisted for all 3 waypoints

  Scenario: Idempotent tour rotation - resume from current position
    Given the player has a ship "SCOUT-1" at waypoint "X1-TEST-B2" with status "DOCKED"
    And the system "X1-TEST" has the following waypoints:
      | Symbol      | X | Y  |
      | X1-TEST-A1  | 0 | 0  |
      | X1-TEST-B2  | 5 | 5  |
      | X1-TEST-C3  | 10| 0  |
    And the API will return market data for all waypoints
    When I execute ScoutTourCommand with ship "SCOUT-1", markets "X1-TEST-A1,X1-TEST-B2,X1-TEST-C3", and 1 iteration
    Then the scout tour command should succeed
    And the tour should start from "X1-TEST-B2"
    And the visit order should be "X1-TEST-B2,X1-TEST-C3,X1-TEST-A1"

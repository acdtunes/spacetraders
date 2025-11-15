Feature: Navigate to Waypoint Command
  As a SpaceTraders bot
  I want to navigate ships to waypoints
  So that I can move ships around the system

  Background:
    Given a navigation test player 1 exists with agent "TEST-AGENT" and token "test-token-123"

  Scenario: Successfully navigate ship from orbit
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    And a waypoint "X1-B2" exists at coordinates (100, 50)
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "X1-B2" for player 1
    Then the navigation should succeed with status "navigating"
    And the ship should be in transit to "X1-B2"
    And the response should include arrival time
    And the response should include fuel consumed

  Scenario: Navigate ship that is docked (auto-orbits first)
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    And a waypoint "X1-B2" exists at coordinates (100, 50)
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "X1-B2" for player 1
    Then the navigation should succeed with status "navigating"
    And the ship should be in transit to "X1-B2"

  Scenario: Ship already at destination (idempotent)
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "X1-A1" for player 1
    Then the navigation should succeed with status "already_at_destination"
    And the ship should remain at "X1-A1"
    And the ship should still be in orbit

  Scenario: Invalid destination waypoint
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "" for player 1
    Then the navigation should fail with error "invalid destination waypoint"

  Scenario: Ship already in transit
    Given a ship "SHIP-1" for player 1 in transit to "X1-B2"
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "X1-C3" for player 1
    Then the navigation should fail with error "cannot orbit while in transit"

  Scenario: Ship not found
    When I execute NavigateToWaypointCommand for ship "NONEXISTENT" to "X1-B2" for player 1
    Then the navigation should fail with error "ship not found"

  Scenario: Navigate with custom flight mode
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    And a waypoint "X1-B2" exists at coordinates (100, 50)
    When I execute NavigateToWaypointCommand for ship "SHIP-1" to "X1-B2" with flight mode "BURN" for player 1
    Then the navigation should succeed with status "navigating"
    And the ship should be in transit to "X1-B2"

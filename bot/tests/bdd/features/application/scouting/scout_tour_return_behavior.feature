Feature: Scout Tour Return-to-Start Behavior
  Tours should always return to start by definition

  Background:
    Given a player with ID 1 exists
    And a ship "TEST-SCOUT-1" exists at "X1-TEST-A1" for player 1

  Scenario: Stationary scout (1 market) does not return to start
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
    When I execute a scout tour with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the ship should visit exactly 1 waypoint
    And the ship should not return to starting waypoint

  Scenario: Two-market tour always returns to start
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
      | X1-TEST-B2   |
    When I execute a scout tour with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the ship should visit exactly 2 waypoints
    And the ship should return to starting waypoint

  Scenario: Multi-market tour always returns to start
    Given the scout tour will visit markets:
      | market       |
      | X1-TEST-A1   |
      | X1-TEST-B2   |
      | X1-TEST-C3   |
    When I execute a scout tour with ship "TEST-SCOUT-1"
    Then the scout tour should complete successfully
    And the ship should visit exactly 3 waypoints
    And the ship should return to starting waypoint

  Scenario: ScoutTourCommand has no return_to_start parameter
    When I inspect the ScoutTourCommand dataclass
    Then the command should not have a "return_to_start" field

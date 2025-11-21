Feature: Scout Markets Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Single ship assigns all markets
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And a marketplace "X1-A1-M1" in system "X1-A1"
    And a marketplace "X1-A1-M2" in system "X1-A1"
    When I execute scout markets command for player 1 with ships ["SHIP-1"] and markets ["X1-A1-M1", "X1-A1-M2"] in system "X1-A1" with 5 iterations
    Then the command should succeed
    And "SHIP-1" should be assigned all markets
    And 1 container should be created

  Scenario: Multiple ships use VRP optimization
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And a probe ship "SHIP-2" for player 1 at waypoint "X1-A1-M2"
    And VRP assigns ["X1-A1-M1"] to "SHIP-1" and ["X1-A1-M2"] to "SHIP-2"
    When I execute scout markets command for player 1 with ships ["SHIP-1", "SHIP-2"] and markets ["X1-A1-M1", "X1-A1-M2"] in system "X1-A1" with 10 iterations
    Then the command should succeed
    And "SHIP-1" should be assigned ["X1-A1-M1"]
    And "SHIP-2" should be assigned ["X1-A1-M2"]
    And 2 containers should be created

  Scenario: Container reuse (idempotent)
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And "SHIP-1" has an existing active container "container-old" for player 1
    When I execute scout markets command for player 1 with ships ["SHIP-1"] and markets ["X1-A1-M1"] in system "X1-A1" with 5 iterations
    Then the command should succeed
    And the existing container should be stopped

  Scenario: Empty markets list
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    When I execute scout markets command for player 1 with ships ["SHIP-1"] and markets [] in system "X1-A1" with 5 iterations
    Then the command should succeed
    And 0 containers should be created

  Scenario: Ship not found
    Given a player with ID 1 and token "test-token" exists in the database
    When I execute scout markets command for player 1 with ships ["NONEXISTENT"] and markets ["X1-A1-M1"] in system "X1-A1" with 5 iterations
    Then the command should return an error containing "failed to load ship"

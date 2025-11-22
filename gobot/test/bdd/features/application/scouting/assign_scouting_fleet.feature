Feature: Assign Scouting Fleet Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Successfully assign probe ships to markets
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And a drone ship "SHIP-2" for player 1 at waypoint "X1-A1-M2"
    And a marketplace "X1-A1-M1" in system "X1-A1"
    And a marketplace "X1-A1-M2" in system "X1-A1"
    And a marketplace "X1-A1-M3" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And 2 ships should be assigned
    And container IDs should be returned
    And assignments should map ships to markets

  Scenario: Filter out fuel station marketplaces
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And a marketplace "X1-A1-M1" in system "X1-A1"
    And a fuel station marketplace "X1-A1-FUEL" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should succeed
    And 1 ship should be assigned
    And "X1-A1-FUEL" should not be in any assignment

  Scenario: No scout ships available
    Given a player with ID 1 and token "test-token" exists in the database
    And a frigate ship "SHIP-1" for player 1 at waypoint "X1-A1-M1"
    And a marketplace "X1-A1-M1" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should return an error containing "no probe or satellite ships found"

  Scenario: System has no non-fuel-station marketplaces
    Given a player with ID 1 and token "test-token" exists in the database
    And a probe ship "SHIP-1" for player 1 at waypoint "X1-A1-FUEL"
    And a fuel station marketplace "X1-A1-FUEL" in system "X1-A1"
    When I execute assign scouting fleet command for player 1 in system "X1-A1"
    Then the command should return an error containing "no non-fuel-station marketplaces found"

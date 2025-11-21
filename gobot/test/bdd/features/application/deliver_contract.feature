Feature: Deliver Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Deliver cargo for valid delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-1" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I execute deliver contract command for "CONTRACT-1" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should succeed
    And the contract should have delivery for "IRON_ORE" with 50 units fulfilled

  Scenario: Deliver remaining cargo completes delivery
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 with 50 of 100 "IRON_ORE" already delivered to waypoint "X1-A1"
    When I execute deliver contract command for "CONTRACT-2" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should succeed
    And the contract should have delivery for "IRON_ORE" with 100 units fulfilled

  Scenario: Cannot deliver more than required
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-3" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-3" with 150 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "units exceed required"

  Scenario: Cannot deliver invalid trade symbol
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-4" for player 1 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-4" with 50 units of "COPPER_ORE" from ship "SHIP-1"
    Then the command should return an error containing "trade symbol not in contract"

  Scenario: Contract not found error
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute deliver contract command for "NON-EXISTENT" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "contract not found"

  Scenario: Player not found error
    Given an accepted contract "CONTRACT-5" for player 999 with delivery of 100 "IRON_ORE" to waypoint "X1-A1"
    When I try to execute deliver contract command for "CONTRACT-5" with 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the command should return an error containing "player not found"

Feature: Negotiate Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Negotiate new contract successfully
    Given a player with ID 1 and token "test-token" exists in the database
    And a docked ship "SHIP-1" for player 1 exists in the database
    And the API will successfully negotiate a contract
    When I execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should succeed
    And a new contract should be created in the database

  Scenario: Resume existing contract (error 4511)
    Given a player with ID 1 and token "test-token" exists in the database
    And a docked ship "SHIP-1" for player 1 exists in the database
    And an existing unaccepted contract "CONTRACT-1" for player 1 exists
    And the API will return error 4511 for contract negotiation
    When I execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should succeed
    And the existing contract "CONTRACT-1" should be returned

  Scenario: Cannot negotiate with undocked ship
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" in orbit for player 1 exists in the database
    When I try to execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should return an error containing "ship must be docked"

  Scenario: Cannot negotiate with ship in transit
    Given a player with ID 1 and token "test-token" exists in the database
    And a ship "SHIP-1" in transit for player 1 exists in the database
    When I try to execute negotiate contract command for ship "SHIP-1" with player 1
    Then the command should return an error containing "ship must be docked"

  Scenario: Ship not found error
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute negotiate contract command for ship "NON-EXISTENT" with player 1
    Then the command should return an error containing "ship not found"

  Scenario: Player not found error
    When I try to execute negotiate contract command for ship "SHIP-1" with player 999
    Then the command should return an error containing "player not found"

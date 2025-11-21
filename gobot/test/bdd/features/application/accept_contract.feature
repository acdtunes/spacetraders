Feature: Accept Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Accept unaccepted contract
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-1" for player 1 in the database
    When I execute accept contract command for "CONTRACT-1" with player 1
    Then the command should succeed
    And the contract should be marked as accepted
    And the contract should still not be fulfilled
    And the contract should be persisted with accepted status

  Scenario: Accept already accepted contract
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 in the database
    When I try to execute accept contract command for "CONTRACT-2" with player 1
    Then the command should return an error containing "contract already accepted"

  Scenario: Accept non-existent contract
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute accept contract command for "NON-EXISTENT" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: Accept contract with API integration
    Given a player with ID 1 and token "test-token" exists in the database
    And an unaccepted contract "CONTRACT-3" for player 1 in the database
    And the API will successfully accept the contract
    When I execute accept contract command for "CONTRACT-3" with player 1
    Then the command should succeed
    And the contract should be persisted with accepted status

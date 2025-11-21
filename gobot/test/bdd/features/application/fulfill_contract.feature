Feature: Fulfill Contract Command

  Background:
    Given the current time is "2099-01-01T00:00:00Z"

  Scenario: Fulfill contract with all deliveries complete
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-1" for player 1 with all deliveries complete
    When I execute fulfill contract command for "CONTRACT-1" with player 1
    Then the command should succeed
    And the contract should be marked as fulfilled

  Scenario: Cannot fulfill contract with incomplete deliveries
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-2" for player 1 with incomplete deliveries
    When I try to execute fulfill contract command for "CONTRACT-2" with player 1
    Then the command should return an error containing "deliveries not complete"

  Scenario: Cannot fulfill non-existent contract
    Given a player with ID 1 and token "test-token" exists in the database
    When I try to execute fulfill contract command for "NON-EXISTENT" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: Cannot fulfill contract for wrong player
    Given a player with ID 1 and token "test-token" exists in the database
    And a player with ID 2 and token "test-token-2" exists in the database
    And an accepted contract "CONTRACT-3" for player 2 with all deliveries complete
    When I try to execute fulfill contract command for "CONTRACT-3" with player 1
    Then the command should return an error containing "contract not found"

  Scenario: API integration success
    Given a player with ID 1 and token "test-token" exists in the database
    And an accepted contract "CONTRACT-4" for player 1 with all deliveries complete
    And the API will successfully fulfill the contract
    When I execute fulfill contract command for "CONTRACT-4" with player 1
    Then the command should succeed
    And the contract should be persisted with fulfilled status

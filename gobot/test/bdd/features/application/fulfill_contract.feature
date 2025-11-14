Feature: Fulfill Contract Command

  As a SpaceTraders player
  I want to fulfill completed contracts
  So that I can receive the fulfillment payment and complete my obligations

  Background:
    Given a mediator is initialized
    And a mock API client
    And a contract repository
    And a player repository

  Scenario: Fulfill contract with all deliveries complete
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-1" for player 1 with all deliveries fulfilled
    And the API will successfully fulfill the contract
    When I send FulfillContractCommand for "CONTRACT-1" with player 1
    Then the command should succeed
    And the contract should be marked as fulfilled

  Scenario: Cannot fulfill contract with incomplete deliveries
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-2" for player 1 with incomplete deliveries
    When I send FulfillContractCommand for "CONTRACT-2" with player 1
    Then the command should fail with error "deliveries not complete"

  Scenario: Fulfill non-existent contract
    Given a player with ID 1 and token "test-token-1"
    When I send FulfillContractCommand for "NON-EXISTENT" with player 1
    Then the command should fail with error "contract not found"

  Scenario: Fulfill contract without valid player
    Given an accepted contract "CONTRACT-3" for player 999 with all deliveries fulfilled
    When I send FulfillContractCommand for "CONTRACT-3" with player 999
    Then the command should fail with error "player not found"

  Scenario: Fulfill contract with API integration
    Given a player with ID 1 and token "test-token-1"
    And an accepted contract "CONTRACT-4" for player 1 with all deliveries fulfilled
    And the API will successfully fulfill the contract
    When I send FulfillContractCommand for "CONTRACT-4" with player 1
    Then the command should succeed
    And the contract should be persisted with fulfilled status

Feature: Contract Queries
  As an application layer
  I want to query contract information
  So that I can make informed decisions about contracts

  Background:
    Given a clean database
    And a test player exists

  Scenario: Get contract by ID
    Given a saved contract with ID "contract-123"
    When I query for contract "contract-123"
    Then I should receive the contract details
    And the contract should have ID "contract-123"

  Scenario: Get contract by ID when not found
    When I query for contract "nonexistent-contract"
    Then I should receive None

  Scenario: List all contracts for a player
    Given 3 saved contracts for the player
    When I query for all contracts
    Then I should receive 3 contracts

  Scenario: List contracts returns empty when none exist
    When I query for all contracts
    Then I should receive 0 contracts

  Scenario: Get active contracts
    Given 2 accepted contracts for the player
    And 1 fulfilled contract for the player
    And 1 unaccepted contract for the player
    When I query for active contracts
    Then I should receive 2 contracts
    And all returned contracts should be accepted
    And all returned contracts should not be fulfilled

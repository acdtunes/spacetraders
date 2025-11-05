Feature: Contract Repository
  As a persistence layer
  I want to store and retrieve contracts
  So that I can maintain contract state across sessions

  Background:
    Given a clean database
    And a test player exists

  Scenario: Save and retrieve a contract
    Given a contract entity with valid data
    When I save the contract to the repository
    And I retrieve the contract by ID
    Then the retrieved contract should match the saved contract
    And all contract properties should be preserved

  Scenario: Find contract by ID returns None when not found
    When I try to find contract "nonexistent-contract"
    Then the result should be None

  Scenario: List all contracts for a player
    Given 3 contracts exist for the player
    When I list all contracts for the player
    Then I should get 3 contracts

  Scenario: List contracts returns empty list when none exist
    When I list all contracts for the player
    Then I should get 0 contracts

  Scenario: Update existing contract
    Given a saved contract
    When I update the contract delivery status
    And I save the updated contract
    And I retrieve the contract by ID
    Then the retrieved contract should reflect the updates

  Scenario: Contract with multiple deliveries
    Given a contract with 2 delivery requirements
    When I save the contract to the repository
    And I retrieve the contract by ID
    Then the contract should have 2 deliveries
    And all delivery details should be preserved

Feature: Update Player Metadata Command
  As a fleet management system
  I want to update player metadata
  So that I can store and modify player-specific data

  Background:
    Given the update player metadata command handler is initialized

  # Happy Path - Successful Updates
  Scenario: Update player metadata successfully
    Given a registered player with id 1 and metadata {"key1": "value1"}
    When I execute update player metadata command for player 1 with metadata {"key2": "value2", "key3": 123}
    Then the command should succeed
    And the player metadata should contain key "key1"
    And the player metadata should contain key "key2"
    And the player metadata "key2" should equal "value2"
    And the player metadata "key3" should equal 123
    And the repository update should be called once

  Scenario: Update metadata with empty dictionary
    Given a registered player with id 1 and metadata {"existing": "data"}
    When I execute update player metadata command for player 1 with metadata {}
    Then the command should succeed
    And the player metadata should equal {"existing": "data"}
    And the repository update should be called once

  Scenario: Update metadata overwrites existing keys
    Given a registered player with id 1 and metadata {"key1": "old_value", "key2": "value2"}
    When I execute update player metadata command for player 1 with metadata {"key1": "new_value"}
    Then the command should succeed
    And the player metadata "key1" should equal "new_value"
    And the player metadata "key2" should equal "value2"

  Scenario: Update metadata with complex data types
    Given a registered player with id 1 with no metadata
    When I execute update player metadata command for player 1 with complex metadata
    Then the command should succeed
    And the player metadata should contain string "value"
    And the player metadata should contain number 42
    And the player metadata should contain float 3.14
    And the player metadata should contain boolean true
    And the player metadata should contain list [1, 2, 3]
    And the player metadata should contain nested dict {"nested": "value"}

  Scenario: Update metadata persists changes to repository
    Given a registered player with id 1 and metadata {"old": "data"}
    When I execute update player metadata command for player 1 with metadata {"new": "data"}
    Then the command should succeed
    And the persisted player metadata should contain key "new"
    And the persisted player metadata "new" should equal "data"

  Scenario: Update metadata returns updated Player entity
    Given a registered player with id 1 and agent symbol "TEST-AGENT" with no metadata
    When I execute update player metadata command for player 1 with metadata {"key": "value"}
    Then the command should return a Player entity
    And the returned player should have agent symbol "TEST-AGENT"

  Scenario: Update metadata multiple times accumulates changes
    Given a registered player with id 1 with no metadata
    When I execute update player metadata command for player 1 with metadata {"key1": "value1"}
    And I execute update player metadata command for player 1 with metadata {"key2": "value2"}
    And I execute update player metadata command for player 1 with metadata {"key3": "value3"}
    Then the command should succeed
    And the player metadata should contain key "key1"
    And the player metadata should contain key "key2"
    And the player metadata should contain key "key3"
    And the repository update should be called 3 times

  Scenario: Update metadata with None values
    Given a registered player with id 1 with no metadata
    When I execute update player metadata command for player 1 with metadata {"nullable": null}
    Then the command should succeed
    And the player metadata should contain key "nullable"
    And the player metadata "nullable" should be null

  Scenario: Update different players independently
    Given a registered player with id 1 and agent symbol "AGENT-1" with no metadata
    And a registered player with id 2 and agent symbol "AGENT-2" with no metadata
    When I execute update player metadata command for player 1 with metadata {"data": "player1"}
    And I execute update player metadata command for player 2 with metadata {"data": "player2"}
    Then the first result metadata "data" should equal "player1"
    And the second result metadata "data" should equal "player2"
    And the repository update should be called twice

  # Error Conditions
  Scenario: Cannot update non-existent player
    Given no player exists with id 999
    When I attempt to update player metadata for player 999
    Then the command should fail with PlayerNotFoundError
    And the error message should mention "Player 999 not found"
    And the repository update should not be called

  # Handler Initialization
  Scenario: Handler initializes with repository correctly
    Given a mock player repository is created
    When I create an update player metadata handler with the repository
    Then the handler should have the repository initialized

Feature: Player Repository CRUD Operations
  As a persistence layer
  I want to store and retrieve player data
  So that I can manage player accounts

  Background:
    Given a fresh player repository

  Scenario: Create new player
    When I create a player with agent_symbol "TEST_AGENT"
    Then the player should have an auto-generated player_id
    And the player should have agent_symbol "TEST_AGENT"
    And the player should have the provided token

  Scenario: Create assigns auto-incrementing IDs
    When I create a player with agent_symbol "AGENT_1"
    And I create a player with agent_symbol "AGENT_2"
    Then player "AGENT_2" should have a higher ID than "AGENT_1"

  Scenario: Find player by ID when exists
    Given a created player "TEST_AGENT"
    When I find the player by ID
    Then the player should be found
    And the player agent_symbol should be "TEST_AGENT"

  Scenario: Find player by ID when not exists
    When I find player by ID 999
    Then the player should not be found

  Scenario: Find player by agent symbol when exists
    Given a created player "TEST_AGENT"
    When I find the player by agent_symbol "TEST_AGENT"
    Then the player should be found

  Scenario: Find player by agent symbol when not exists
    When I find player by agent_symbol "NONEXISTENT"
    Then the player should not be found

  Scenario: Agent symbol lookup is case sensitive
    Given a created player "TEST_AGENT"
    When I find player by agent_symbol "test_agent"
    Then the player should not be found

  Scenario: List all players when empty
    When I list all players
    Then I should see 0 players

  Scenario: List all players with single player
    Given a created player "TEST_AGENT"
    When I list all players
    Then I should see 1 player

  Scenario: List all players with multiple players
    Given a created player "AGENT_1"
    And a created player "AGENT_2"
    And a created player "AGENT_3"
    When I list all players
    Then I should see 3 players

  Scenario: Update player metadata
    Given a created player "TEST_AGENT"
    When I update the player metadata to {"faction": "COSMIC", "credits": 5000}
    And I find the player by agent_symbol "TEST_AGENT"
    Then the player metadata should contain "faction"
    And the player metadata should contain "credits"

  Scenario: Update player last_active
    Given a created player "TEST_AGENT"
    When I update the player last_active timestamp
    And I find the player by agent_symbol "TEST_AGENT"
    Then the player last_active should be updated

  Scenario: Update nonexistent player does not raise error
    When I attempt to update a nonexistent player
    Then no error should occur

  Scenario: Check existence by agent symbol when exists
    Given a created player "TEST_AGENT"
    When I check if "TEST_AGENT" exists
    Then existence check should return true

  Scenario: Check existence by agent symbol when not exists
    When I check if "NONEXISTENT" exists
    Then existence check should return false

  Scenario: Duplicate agent symbol fails
    Given a created player "TEST_AGENT"
    When I attempt to create another player with agent_symbol "TEST_AGENT"
    Then creation should fail with an IntegrityError

  Scenario: Player with null metadata
    When I create a player with empty metadata
    And I find the player by ID
    Then the player metadata should be empty

  Scenario: Player with complex metadata
    When I create a player with complex nested metadata
    And I find the player by ID
    Then the player metadata should contain nested structures

  Scenario: Concurrent player creates
    When I create 5 players sequentially
    Then all 5 players should be in the database
    And all players should have unique IDs

  Scenario: Find after update returns updated values
    Given a created player "TEST_AGENT"
    When I update the player metadata
    And I find the player by ID
    Then the player should have the updated metadata
    When I find the player by agent_symbol "TEST_AGENT"
    Then the player should have the updated metadata

  Scenario: List all returns fresh data after updates
    Given a created player "AGENT_1"
    And a created player "AGENT_2"
    When I update player "AGENT_1" metadata
    And I list all players
    Then player "AGENT_1" should have updated metadata in the list

  Scenario: Empty agent symbol raises ValueError
    When I attempt to create a player with empty agent_symbol
    Then creation should fail with ValueError

  Scenario: Empty token raises ValueError
    When I attempt to create a player with empty token
    Then creation should fail with ValueError

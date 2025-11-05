Feature: Touch Player Last Active Command
  As a fleet management system
  I want to update player last_active timestamps
  So that I can track when players were last active

  Background:
    Given the touch player last active command handler is initialized

  # Happy Path - Successful Touch
  Scenario: Touch player's last_active timestamp successfully
    Given a registered player with id 1
    And the player's original last_active timestamp is recorded
    When I execute touch player last active command for player 1
    Then the command should succeed
    And the player's last_active should be updated
    And the player's last_active should be after the original timestamp
    And the repository update should be called once

  Scenario: Touch updates timestamp to current time
    Given a registered player with id 1 and last_active 5 hours ago
    And the current time is recorded before touch
    When I execute touch player last active command for player 1
    And the current time is recorded after touch
    Then the command should succeed
    And the player's last_active should be between before and after times
    And the player's last_active should be after the old timestamp

  Scenario: Touch player's last_active multiple times
    Given a registered player with id 1
    When I execute touch player last active command for player 1 three times
    Then each touch should update the timestamp
    And the repository update should be called 3 times

  Scenario: Touch persists changes to repository
    Given a registered player with id 1
    And the player's original last_active timestamp is recorded
    When I execute touch player last active command for player 1
    Then the command should succeed
    And the persisted player should have updated last_active

  Scenario: Touch returns updated Player entity
    Given a registered player with id 1 and agent symbol "TEST-AGENT"
    When I execute touch player last active command for player 1
    Then the command should return a Player entity
    And the returned player should have agent symbol "TEST-AGENT"

  Scenario: Touch different players independently
    Given a registered player with id 1 and agent symbol "AGENT-1"
    And a registered player with id 2 and agent symbol "AGENT-2"
    When I execute touch player last active command for player 1
    And I execute touch player last active command for player 2
    Then the first result should be for player 1
    And the second result should be for player 2
    And the repository update should be called twice

  # Error Conditions
  Scenario: Cannot touch non-existent player
    Given no player exists with id 999
    When I attempt to touch player last active for player 999
    Then the command should fail with PlayerNotFoundError
    And the error message should mention "Player 999 not found"
    And the repository update should not be called

  # Handler Initialization
  Scenario: Handler initializes with repository correctly
    Given a mock player repository is created
    When I create a touch player last active handler with the repository
    Then the handler should have the repository initialized

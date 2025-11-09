Feature: Get Captain Logs Query
  As a fleet management system
  I want to retrieve captain mission logs
  So that I can review narrative history and maintain context across sessions

  Background:
    Given the captain logging system is initialized

  # Happy Path - Basic Retrieval
  Scenario: Get recent logs for player
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has 5 captain logs
    When I query captain logs for player 1 with limit 10
    Then the query should succeed
    And the result should contain 5 logs
    And all logs should belong to player 1

  Scenario: Get logs with default limit
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has 10 captain logs
    When I query captain logs for player 1
    Then the query should succeed
    And the result should contain 10 logs

  # Filtering by Entry Type
  Scenario: Filter logs by entry type
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has a log with type "session_start"
    And the player has a log with type "operation_completed"
    And the player has a log with type "critical_error"
    When I query captain logs for player 1 filtered by type "critical_error"
    Then the query should succeed
    And the result should contain 1 log
    And all logs should have type "critical_error"

  Scenario: Filter logs by operation_started type
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has 3 logs with type "operation_started"
    And the player has 2 logs with type "operation_completed"
    When I query captain logs for player 1 filtered by type "operation_started"
    Then the query should succeed
    And the result should contain 3 logs

  # Filtering by Timestamp
  Scenario: Filter logs since timestamp
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has logs created 2 hours ago
    And the player has logs created 1 hour ago
    And the player has logs created 30 minutes ago
    When I query captain logs for player 1 since 90 minutes ago
    Then the query should succeed
    And all returned logs should be newer than 90 minutes

  # Limit Functionality
  Scenario: Respect limit parameter
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has 50 captain logs
    When I query captain logs for player 1 with limit 10
    Then the query should succeed
    And the result should contain exactly 10 logs

  Scenario: Return all logs when count below limit
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has 5 captain logs
    When I query captain logs for player 1 with limit 100
    Then the query should succeed
    And the result should contain 5 logs

  # Empty Results
  Scenario: Return empty list for player with no logs
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has no captain logs
    When I query captain logs for player 1
    Then the query should succeed
    And the result should be empty

  Scenario: Return empty list when filter matches nothing
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has logs with type "session_start"
    When I query captain logs for player 1 filtered by type "critical_error"
    Then the query should succeed
    And the result should be empty

  # Chronological Order
  Scenario: Logs returned in reverse chronological order
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has logs created at different times
    When I query captain logs for player 1
    Then the query should succeed
    And logs should be ordered newest first

  # Error Cases
  Scenario: Query fails for non-existent player
    Given no player exists with id 999
    When I attempt to query captain logs for player 999
    Then the query should fail with PlayerNotFoundError
    And the error message should mention "Player 999 not found"

  # Data Integrity
  Scenario: Returned logs contain all fields
    Given a registered player with id 1 and agent symbol "ENDURANCE"
    And the player has a complete log with all fields
    When I query captain logs for player 1
    Then the query should succeed
    And the first log should have all required fields
    And the log should have log_id
    And the log should have player_id
    And the log should have timestamp
    And the log should have entry_type
    And the log should have narrative

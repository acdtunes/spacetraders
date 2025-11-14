Feature: Container Logging & Persistence
  As a daemon system
  I need containers to log messages to the database with deduplication
  So that I can query container logs by ID, level, and time range with pagination

  # ============================================================================
  # Basic Logging Operations
  # ============================================================================

  Scenario: Container logs INFO messages to database
    Given a container log repository with in-memory database
    And a container with ID "test-container-1" for player 1
    When I log an INFO message "Operation started successfully" for the container
    Then the log should be persisted to the database
    And the log level should be "INFO"
    And the log message should be "Operation started successfully"

  Scenario: Container logs ERROR messages to database
    Given a container log repository with in-memory database
    And a container with ID "test-container-2" for player 1
    When I log an ERROR message "Navigation failed: insufficient fuel" for the container
    Then the log should be persisted to the database
    And the log level should be "ERROR"
    And the log message should be "Navigation failed: insufficient fuel"

  Scenario: Container logs include timestamp and container_id
    Given a container log repository with in-memory database
    And a container with ID "test-container-3" for player 1
    When I log an INFO message "Processing waypoint data" for the container
    Then the log should be persisted to the database
    And the log should have a container_id of "test-container-3"
    And the log should have a player_id of 1
    And the log should have a timestamp within the last 5 seconds

  # ============================================================================
  # Log Querying Operations
  # ============================================================================

  Scenario: Container logs are queryable by container_id
    Given a container log repository with in-memory database
    And a container with ID "query-test-1" for player 1
    And a container with ID "query-test-2" for player 1
    When I log 3 INFO messages for container "query-test-1"
    And I log 2 ERROR messages for container "query-test-2"
    And I query logs for container "query-test-1" with limit 10
    Then I should receive exactly 3 log entries
    And all log entries should have container_id "query-test-1"

  Scenario: Container logs are queryable by log level
    Given a container log repository with in-memory database
    And a container with ID "level-test-1" for player 1
    When I log 2 INFO messages for the container
    And I log 3 ERROR messages for the container
    And I log 1 WARNING message for the container
    And I query logs for the container filtered by level "ERROR"
    Then I should receive exactly 3 log entries
    And all log entries should have level "ERROR"

  # ============================================================================
  # Deduplication & Advanced Features
  # ============================================================================

  Scenario: Container logs deduplicate within 60 seconds
    Given a container log repository with in-memory database
    And a container with ID "dedup-test-1" for player 1
    When I log an INFO message "Duplicate message test" for the container
    And I log the same INFO message "Duplicate message test" 2 seconds later
    And I log the same INFO message "Duplicate message test" 30 seconds after the first log
    Then I should receive exactly 1 log entry when querying
    And the log message should be "Duplicate message test"
    When I log the same INFO message "Duplicate message test" 65 seconds after the first log
    Then I should receive exactly 2 log entries when querying
    And both entries should have the message "Duplicate message test"

  Scenario: Container logs support pagination with limit and offset
    Given a container log repository with in-memory database
    And a container with ID "pagination-test-1" for player 1
    When I log 15 unique INFO messages for the container
    And I query logs with limit 5 and no offset
    Then I should receive exactly 5 log entries
    When I query logs with limit 5 and offset 5
    Then I should receive exactly 5 log entries
    And the log entries should be different from the first page
    When I query logs with limit 5 and offset 10
    Then I should receive exactly 5 log entries
    When I query logs with limit 10 and offset 10
    Then I should receive exactly 5 log entries
    And the total count of all logs should be 15

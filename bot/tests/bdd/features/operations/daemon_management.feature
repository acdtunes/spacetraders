Feature: Daemon Process Management
  As a SpaceTraders bot operator
  I want to manage background daemon processes
  So that I can run autonomous operations

  Background:
    Given a daemon manager with temporary directory and database
    And player ID 1 exists in the database

  Scenario: Start a daemon process successfully
    When I start daemon "test_daemon_1" with command "python3" "-c" "import time; time.sleep(10)"
    Then daemon "test_daemon_1" should be running
    And daemon "test_daemon_1" should have a valid PID
    And log file for daemon "test_daemon_1" should exist

  Scenario: Prevent duplicate daemon starts
    Given daemon "test_daemon_2" is already running
    When I attempt to start daemon "test_daemon_2" with command "python3" "-c" "import time; time.sleep(10)"
    Then the start operation should fail
    And daemon "test_daemon_2" should still be running

  Scenario: Stop a running daemon gracefully
    Given daemon "test_daemon_3" is already running
    When I stop daemon "test_daemon_3"
    Then daemon "test_daemon_3" should not be running
    And daemon status should be "stopped"
    And stopped_at timestamp should be recorded

  Scenario: Force kill a daemon that doesn't stop gracefully
    Given daemon "test_daemon_4" is running with command that ignores SIGTERM
    When I stop daemon "test_daemon_4" with timeout 1 seconds
    Then daemon "test_daemon_4" should not be running
    And daemon status should be "killed"

  Scenario: Check if daemon is running
    Given daemon "test_daemon_5" is already running
    When I check if daemon "test_daemon_5" is running
    Then the result should be true
    When I stop daemon "test_daemon_5"
    And I check if daemon "test_daemon_5" is running
    Then the result should be false

  Scenario: Get daemon status with process information
    Given daemon "test_daemon_6" is already running
    When I get status for daemon "test_daemon_6"
    Then status should contain daemon_id "test_daemon_6"
    And status should contain a valid PID
    And status should show is_running as true
    And status should contain cpu_percent
    And status should contain memory_mb
    And status should contain runtime_seconds
    And status should contain log_file path
    And status should contain err_file path

  Scenario: Get daemon PID
    Given daemon "test_daemon_7" is already running
    When I get PID for daemon "test_daemon_7"
    Then the PID should be a positive integer
    And the process with that PID should exist

  Scenario: List all daemons for a player
    Given daemon "test_daemon_8a" is already running
    And daemon "test_daemon_8b" is already running
    And daemon "test_daemon_8c" is already running
    When I list all daemons
    Then the list should contain 3 daemons
    And the list should include "test_daemon_8a"
    And the list should include "test_daemon_8b"
    And the list should include "test_daemon_8c"
    And daemons should be sorted by started_at descending

  Scenario: Tail daemon logs
    Given daemon "test_daemon_9" is running with command that generates output
    And daemon "test_daemon_9" has generated at least 50 lines of output
    When I tail logs for daemon "test_daemon_9" with 20 lines
    Then I should see the last 20 lines of output
    And the output should contain expected log content

  Scenario: Cleanup stopped daemons
    Given daemon "test_daemon_10a" is already running
    And daemon "test_daemon_10b" has stopped
    When I cleanup stopped daemons
    Then daemon "test_daemon_10b" should be removed from database
    And daemon "test_daemon_10a" should still be in database

  Scenario: Handle daemon crash detection
    Given daemon "test_daemon_11" is already running
    When the process for daemon "test_daemon_11" is killed externally
    And I check if daemon "test_daemon_11" is running
    Then daemon "test_daemon_11" should not be running
    And daemon status should be "crashed"
    And stopped_at timestamp should be recorded

  Scenario: Multi-player daemon isolation
    Given player ID 2 exists in the database
    And daemon "shared_name" is running for player 1
    When player 2 starts daemon "shared_name" with command "python3" "-c" "import time; time.sleep(10)"
    Then player 1 should see daemon "shared_name" running
    And player 2 should see daemon "shared_name" running
    And player 1's daemon PID should differ from player 2's daemon PID

  Scenario: Get status for non-existent daemon
    When I get status for daemon "non_existent"
    Then the status should be None

  Scenario: Stop non-running daemon
    When I stop daemon "never_started"
    Then the stop operation should fail
    And error message should indicate daemon is not running

  Scenario: Lazy load API client
    When I access the API client property
    Then the API client should be initialized
    And the API client should use the player's token

  Scenario: Start daemon with custom working directory
    Given a custom working directory "/tmp/test_cwd"
    When I start daemon "test_daemon_12" with command "pwd" in working directory "/tmp/test_cwd"
    Then the log file should contain "/tmp/test_cwd"

  Scenario: Daemon logs include start markers
    When I start daemon "test_daemon_13" with command "echo" "hello"
    Then log file should contain start marker with daemon_id
    And log file should contain start timestamp
    And log file should contain command string

  Scenario: Tail logs for non-existent daemon
    When I tail logs for daemon "nonexistent_daemon" with 10 lines
    Then the tail operation should show daemon not found

  Scenario: Tail logs for daemon with non-existent log file
    Given daemon "test_daemon_14" started but log file deleted
    When I tail logs for daemon "test_daemon_14" with 10 lines
    Then the tail operation should show log file not found

  Scenario: Initialize daemon manager with non-existent player
    When I create daemon manager for player ID 999
    Then the daemon manager should have no agent symbol
    And the daemon manager should have no token

  Scenario: Access API property directly
    When I access the api property
    Then the API client should be available via property

  Scenario: Stop daemon with exception during termination
    Given daemon "test_daemon_15" is already running
    When the daemon process crashes during stop
    Then the stop operation should handle the exception gracefully

  Scenario: Status method returns process metrics
    Given daemon "test_daemon_17" is already running
    When I get status for daemon "test_daemon_17"
    Then status runtime_seconds should be positive
    And status should show process resource usage

Feature: Container Logging
  As a bot operator
  I need container operations to log errors to the database
  So that I can view error details via daemon logs command

  Scenario: Container logs errors to database when command handler fails
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" with cargo capacity 100
    And the database is empty of container logs
    When I create a container for a command that will log errors
    And I wait for the container to execute
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs

  Scenario: Contract batch workflow logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a container to run batch contract workflow with 1 iteration
    And I wait for the container to complete
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "Contract"

  Scenario: Navigate command logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists at waypoint "X1-TEST-A1"
    And the database is empty of container logs
    When I create a container to navigate ship "TEST-1" to invalid waypoint "INVALID-WAYPOINT"
    And I wait for the container to fail
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "navigation" or "route" or "path"

  Scenario: Dock command logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" in transit
    And the database is empty of container logs
    When I create a container to dock ship "TEST-1"
    And I wait for the container to fail
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "dock"

  Scenario: Orbit command logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" in transit
    And the database is empty of container logs
    When I create a container to orbit ship "TEST-1"
    And I wait for the container to fail
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "orbit"

  Scenario: Refuel command logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" in orbit without fuel station
    And the database is empty of container logs
    When I create a container to refuel ship "TEST-1"
    And I wait for the container to fail
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "refuel"

  Scenario: Scout tour logs errors to container logs
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists at waypoint "X1-TEST-A1"
    And the database is empty of container logs
    When I create a container to run scout tour with invalid markets
    And I wait for the container to fail
    Then the container logs should contain ERROR level messages
    And the errors should be retrievable via get_container_logs
    And the container logs should mention "scout"

  Scenario: daemon_inspect returns valid JSON with logs containing special characters
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a test container with ID "test-json-escape"
    And I add a log with message containing double quotes: 'Error: Ship "TEST-1" failed'
    And I add a log with message containing newlines: 'Line 1\nLine 2\nLine 3'
    And I add a log with message containing backslashes: 'Path: C:\Users\test\file.txt'
    And I add a log with message containing unicode: 'Status: ✅ Success 🚀'
    And I add a log with message containing JSON-like content: '{"status": "error", "message": "failed"}'
    And I call daemon_inspect for container "test-json-escape"
    Then the daemon_inspect response should be valid JSON
    And the daemon_inspect response should contain all 5 log messages
    And the log messages should preserve special characters correctly

  Scenario: daemon_inspect returns valid JSON with large log output
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a test container with ID "test-large-output"
    And I add 100 logs with varying content including special characters
    And I call daemon_inspect for container "test-large-output"
    Then the daemon_inspect response should be valid JSON
    And the daemon_inspect response should contain all 100 log messages
    And the total response size should exceed 7000 characters

  Scenario: daemon_logs returns valid JSON with logs containing special characters
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a test container with ID "test-logs-json"
    And I add a log with message containing all special characters: 'Test: "\n\t\r\b\f\\ 🚀 {"key": "value"}'
    And I call daemon_logs for container "test-logs-json" and player 1
    Then the daemon_logs response should be valid JSON
    And the daemon_logs response should contain the log message with special characters preserved

  Scenario: daemon_inspect CLI command outputs logs in JSON format
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a test container with ID "test-cli-json-output"
    And I add a log with message containing double quotes: 'Error: Ship "TEST-1" failed'
    And I add a log with message containing newlines: 'Line 1\nLine 2\nLine 3'
    And I call daemon_inspect CLI command for container "test-cli-json-output"
    Then the CLI output should be valid JSON
    And the CLI JSON output should contain container metadata
    And the CLI JSON output should contain the logs with special characters preserved

  Scenario: daemon_logs CLI command outputs logs in JSON format
    Given the container manager is initialized
    And a player with ID 1 exists
    And a ship "TEST-1" exists
    And the database is empty of container logs
    When I create a test container with ID "test-cli-logs-output"
    And I add a log with message containing all special characters: 'Test: "\n\t\r 🚀'
    And I call daemon_logs CLI command for container "test-cli-logs-output" and player 1
    Then the CLI output should be valid JSON
    And the CLI JSON output should contain the log with special characters


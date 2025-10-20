Feature: Daemon process lifecycle management
  As a fleet automation coordinator
  I want to manage background daemon processes
  So that long-running operations can execute autonomously

  Background:
    Given a daemon manager for player 1
    And the daemon database is initialized

  Scenario: Start daemon process successfully
    When I start a daemon "miner-1" with command "python3 test.py"
    Then the daemon should be registered in the database
    And the daemon status should be "running"
    And the daemon PID should be valid
    And a log file should be created for the daemon

  Scenario: Stop daemon process gracefully with SIGTERM
    Given a daemon "miner-1" is running
    When I stop the daemon "miner-1"
    Then the daemon process should be terminated
    And the daemon status should be "stopped"
    And the stopped_at timestamp should be set

  Scenario: Get status of running daemon
    Given a daemon "miner-1" is running
    When I check the status of "miner-1"
    Then the status should show "running"
    And the status should include PID
    And the status should include start time

  Scenario: Get status of stopped daemon
    Given a daemon "miner-1" was running but is now stopped
    When I check the status of "miner-1"
    Then the status should show "stopped"
    And the status should include stopped_at timestamp
    And the PID should not be in the process list

  Scenario: Detect stale daemon with dead process
    Given a daemon "miner-1" is registered as running
    But the process is no longer alive
    When I check the status of "miner-1"
    Then the daemon should be detected as stale
    And the status should automatically update to "stopped"

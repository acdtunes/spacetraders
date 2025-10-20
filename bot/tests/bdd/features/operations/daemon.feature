Feature: Daemon operations
  As a fleet manager
  I want to manage background daemon processes
  So that I can run long-running operations autonomously

  Background:
    Given a daemon management system

  Scenario: Start daemon successfully
    Given no daemons are running
    And ship "SHIP-1" is available
    When I start daemon "miner-1" with operation "mine" and ship "SHIP-1"
    Then daemon "miner-1" should be running
    And ship "SHIP-1" should be assigned to "mining_operator"

  Scenario: Start daemon fails when ship is already assigned
    Given ship "SHIP-2" is assigned to "trader-1" daemon
    When I start daemon "miner-2" with operation "mine" and ship "SHIP-2"
    Then daemon start should fail
    And error message should mention ship already assigned

  Scenario: Start daemon auto-generates daemon ID
    Given ship "SHIP-3" is available
    When I start daemon with operation "mine" and ship "SHIP-3" without daemon_id
    Then daemon "mine_SHIP-3" should be running

  Scenario: Stop daemon and release ship assignment
    Given daemon "trader-1" is running with ship "SHIP-4"
    When I stop daemon "trader-1"
    Then daemon "trader-1" should be stopped
    And ship "SHIP-4" should be released

  Scenario: Get status for single daemon
    Given daemon "miner-1" is running
    When I check status for daemon "miner-1"
    Then status should show daemon is running
    And status should include PID and runtime

  Scenario: List all daemons
    Given daemon "miner-1" is running
    And daemon "trader-1" is running
    And daemon "scout-1" is stopped
    When I list all daemons
    Then 3 daemons should be listed
    And 2 daemons should show as running

  Scenario: Tail daemon logs
    Given daemon "miner-1" has log entries
    When I tail logs for daemon "miner-1" with 50 lines
    Then log output should be displayed

  Scenario: Cleanup stopped daemons
    Given daemon "old-1" is stopped
    And daemon "old-2" is stopped
    And daemon "active-1" is running
    When I cleanup stopped daemons
    Then stopped daemons should be removed
    And running daemons should remain

  Scenario: Missing player ID returns error
    When I start daemon without player_id
    Then operation should fail
    And error message should mention player_id required

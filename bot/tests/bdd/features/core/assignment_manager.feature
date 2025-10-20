Feature: Ship assignment management
  As a fleet operations coordinator
  I want to manage ship assignments to operations
  So that ships don't get double-booked and can be efficiently allocated

  Background:
    Given an assignment manager for player 1
    And the assignment database is initialized

  Scenario: Assign ship to operation
    Given ship "SHIP-1" is available
    When I assign "SHIP-1" to operator "mining_operator" with daemon "miner-1" for operation "mine"
    Then the assignment should succeed
    And "SHIP-1" should be assigned to "mining_operator"
    And the assignment should reference daemon "miner-1"

  Scenario: Release ship from operation
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    When I release "SHIP-1"
    Then the release should succeed
    And "SHIP-1" should be available

  Scenario: Check ship availability
    Given ship "SHIP-1" is available
    When I check if "SHIP-1" is available
    Then the ship should be reported as available

  Scenario: Prevent double-booking with running daemon
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    And daemon "miner-1" is running
    When I attempt to assign "SHIP-1" to operator "trading_operator" with daemon "trader-1" for operation "trade"
    Then the assignment should fail
    And "SHIP-1" should still be assigned to "mining_operator"

  Scenario: Allow reassignment when daemon stopped
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    And daemon "miner-1" has stopped
    When I attempt to assign "SHIP-1" to operator "trading_operator" with daemon "trader-1" for operation "trade"
    Then the assignment should succeed
    And "SHIP-1" should be assigned to "trading_operator"

  Scenario: Sync releases ships with stopped daemons
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    And ship "SHIP-2" is assigned to operator "trading_operator" with daemon "trader-1"
    And daemon "miner-1" has stopped
    And daemon "trader-1" is running
    When I sync assignments with daemons
    Then "SHIP-1" should be marked as stale
    And "SHIP-2" should remain active

  Scenario: Find available ships
    Given ship "SHIP-1" is assigned to operator "test_operator" with daemon "test-1"
    And ship "SHIP-1" has been released
    And ship "SHIP-2" is assigned to operator "mining_operator" with daemon "miner-1"
    And daemon "miner-1" is running
    And ship "SHIP-3" is assigned to operator "test_operator_2" with daemon "test-2"
    And ship "SHIP-3" has been released
    When I find available ships
    Then the results should include "SHIP-1"
    And the results should include "SHIP-3"
    And the results should not include "SHIP-2"

  Scenario: List all assignments
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    And ship "SHIP-2" is assigned to operator "trading_operator" with daemon "trader-1"
    When I list all assignments
    Then the list should contain 2 assignments
    And the list should include assignment for "SHIP-1"
    And the list should include assignment for "SHIP-2"

  Scenario: Get assignment details
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1" for operation "mine"
    When I get assignment details for "SHIP-1"
    Then the details should show operator "mining_operator"
    And the details should show daemon "miner-1"
    And the details should show operation "mine"

  Scenario: Stale assignment detection
    Given ship "SHIP-1" is assigned to operator "mining_operator" with daemon "miner-1"
    And daemon "miner-1" has stopped
    When I check if "SHIP-1" is available
    Then the ship should be reported as available due to stale daemon

  Scenario: Multi-player isolation
    Given an assignment manager for player 2
    And ship "SHIP-1" is assigned to operator "mining_operator" for player 1
    When player 2 checks if "SHIP-1" is available
    Then player 2 should see "SHIP-1" as available
    And player 2's assignments should not include "SHIP-1"

  Scenario: Assignment with metadata
    Given ship "SHIP-1" is available
    When I assign "SHIP-1" with metadata containing target "X1-TEST-A1"
    Then the assignment should succeed
    And the assignment metadata should contain target "X1-TEST-A1"

  Scenario: Release unassigned ship
    Given ship "SHIP-1" is not in the registry
    When I release "SHIP-1"
    Then the release should fail with message "not in registry"

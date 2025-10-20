Feature: Ship assignment operations
  As a fleet manager
  I want to manage ship assignments to operations
  So that I can prevent conflicts and track ship usage

  Background:
    Given an assignment management system

  Scenario: List all ship assignments
    Given ship "SHIP-1" is assigned to "mining_op" daemon "miner-1" operation "mine"
    And ship "SHIP-2" is assigned to "trading_op" daemon "trader-1" operation "trade"
    When I list all assignments
    Then 2 assignments should be shown
    And assignment for "SHIP-1" should show operator "mining_op"
    And assignment for "SHIP-2" should show operator "trading_op"

  Scenario: List shows no assignments when registry empty
    Given no ships are assigned
    When I list all assignments
    Then output should show no ship assignments

  Scenario: Assign ship to operation
    Given ship "SHIP-3" is available
    When I assign ship "SHIP-3" to operator "mining_op" daemon "miner-3" operation "mine"
    Then ship "SHIP-3" should be assigned successfully
    And assignment registry should contain "SHIP-3"

  Scenario: Release ship from assignment
    Given ship "SHIP-4" is assigned to "trading_op" daemon "trader-4" operation "trade"
    When I release ship "SHIP-4" with reason "operation_complete"
    Then ship "SHIP-4" should be released successfully
    And ship "SHIP-4" should not be in registry

  Scenario: Check ship availability when available
    Given ship "SHIP-5" is available
    When I check if ship "SHIP-5" is available
    Then operation should succeed
    And output should show ship is available

  Scenario: Check ship availability when assigned
    Given ship "SHIP-6" is assigned to "mining_op" daemon "miner-6" operation "mine"
    When I check if ship "SHIP-6" is available
    Then operation should fail
    And output should show ship is assigned

  Scenario: Find available ships
    Given ship "SHIP-7" is available
    And ship "SHIP-8" is available
    And ship "SHIP-9" is assigned to "mining_op" daemon "miner-9" operation "mine"
    When I find available ships
    Then 2 available ships should be found
    And "SHIP-7" should be in available list
    And "SHIP-8" should be in available list

  Scenario: Sync assignments with daemon status
    Given ship "SHIP-10" is assigned to "mining_op" daemon "miner-10" operation "mine"
    And daemon "miner-10" is stopped
    When I sync assignments
    Then ship "SHIP-10" should be released
    And sync output should show 1 released ship

  Scenario: Get detailed status for assigned ship
    Given ship "SHIP-11" is assigned to "mining_op" daemon "miner-11" operation "mine"
    When I get status for ship "SHIP-11"
    Then status should show operator "mining_op"
    And status should show daemon "miner-11"
    And status should show operation "mine"

  Scenario: Get status for unassigned ship
    Given ship "SHIP-12" is available
    When I get status for ship "SHIP-12"
    Then status should show ship is not in registry

  Scenario: Missing player_id returns error
    When I list assignments without player_id
    Then operation should fail
    And error message should mention player_id required

  # Additional scenarios for 85% coverage

  Scenario: List assignments shows stale status icon
    Given ship "SHIP-13" is assigned with stale status
    When I list all assignments
    Then output should show stale status icon for "SHIP-13"

  Scenario: List assignments shows unknown status icon
    Given ship "SHIP-14" is assigned with unknown status
    When I list all assignments
    Then output should show unknown status icon for "SHIP-14"

  Scenario: Assign ship with duration metadata
    Given ship "SHIP-15" is available
    When I assign ship "SHIP-15" to operator "mining_op" daemon "miner-15" operation "mine" with duration "2h"
    Then ship "SHIP-15" should be assigned successfully
    And assignment should include duration metadata

  Scenario: Find ships with cargo requirement
    Given ship "SHIP-16" is available with cargo capacity 40
    And ship "SHIP-17" is available with cargo capacity 20
    When I find ships with cargo minimum 30
    Then 1 available ship should be found
    And "SHIP-16" should be in available list

  Scenario: Find ships with fuel requirement
    Given ship "SHIP-18" is available with fuel capacity 100
    And ship "SHIP-19" is available with fuel capacity 50
    When I find ships with fuel minimum 75
    Then 1 available ship should be found
    And "SHIP-18" should be in available list

  Scenario: Find ships when none available
    Given all ships are assigned
    When I find available ships
    Then output should show no ships available

  Scenario: Sync shows released and active ships
    Given ship "SHIP-20" is assigned to "mining_op" daemon "miner-20" operation "mine"
    And daemon "miner-20" is stopped
    And ship "SHIP-21" is assigned to "trading_op" daemon "trader-21" operation "trade"
    And daemon "trader-21" is running
    When I sync assignments
    Then sync output should show 1 released ship
    And sync output should show 1 active ship
    And ship "SHIP-20" should be in released list
    And ship "SHIP-21" should be in active list

  Scenario: Reassign ships from operation
    Given ship "SHIP-22" is assigned to "mining_op" daemon "miner-22" operation "mine"
    And ship "SHIP-23" is assigned to "mining_op" daemon "miner-23" operation "mine"
    When I reassign ships "SHIP-22,SHIP-23" from operation "mine"
    Then reassignment should succeed
    And output should show reassignment complete

  Scenario: Reassign with no_stop flag
    Given ship "SHIP-24" is assigned to "mining_op" daemon "miner-24" operation "mine"
    When I reassign ships "SHIP-24" from operation "mine" with no_stop flag
    Then reassignment should succeed
    And daemons should not be stopped

  Scenario: Status shows released ship details
    Given ship "SHIP-25" was assigned and released
    When I get status for ship "SHIP-25"
    Then status should show released at time
    And status should show release reason

  Scenario: Status shows metadata
    Given ship "SHIP-26" is assigned with metadata cargo_capacity 40
    When I get status for ship "SHIP-26"
    Then status should show metadata
    And metadata should include cargo_capacity

  Scenario: Status checks running daemon
    Given ship "SHIP-27" is assigned to "mining_op" daemon "miner-27" operation "mine"
    And daemon "miner-27" is running with PID 12345
    When I get status for ship "SHIP-27"
    Then status should show daemon is running
    And status should show daemon PID

  Scenario: Status for ship with unknown assignment
    Given ship "SHIP-28" has unknown assignment
    When I check if ship "SHIP-28" is available
    Then operation should fail
    And output should show status unknown

  Scenario: Initialize registry from API
    Given API has 5 ships
    And registry is empty
    When I initialize registry
    Then registry should contain 5 ships
    And output should show initialization complete

  Scenario: Initialize registry skips existing ships
    Given API has 3 ships
    And ship "SHIP-29" is already in registry
    When I initialize registry
    Then registry should add 2 new ships
    And ship "SHIP-29" should remain unchanged

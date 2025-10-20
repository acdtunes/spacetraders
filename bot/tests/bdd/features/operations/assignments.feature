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

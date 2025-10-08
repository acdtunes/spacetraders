Feature: Ship Assignment Management
  As a fleet coordinator
  I want to manage ship assignments
  So that I can prevent conflicts and coordinate operations

  Background:
    Given the assignment system is initialized
    And the daemon manager is mocked

  Scenario: Assign ship to operation
    Given a ship "CMDR_AC_2025-1" is available
    When I assign "CMDR_AC_2025-1" to "trading_operator" with daemon "trader-ship1" for operation "trade"
    Then the ship should be assigned to "trading_operator"
    And the assignment status should be "active"
    And no error should occur

  Scenario: Cannot assign already assigned ship
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator"
    And daemon "trader-ship1" is running
    When I assign "CMDR_AC_2025-1" to "mining_operator" with daemon "miner-ship1" for operation "mine"
    Then the assignment should fail
    And the ship should still be assigned to "trading_operator"

  Scenario: Release ship from assignment
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator"
    When I release "CMDR_AC_2025-1" with reason "operation_complete"
    Then the ship should be available
    And the assignment status should be "idle"
    And the release reason should be "operation_complete"

  Scenario: Check ship availability
    Given a ship "CMDR_AC_2025-1" is available
    Then "CMDR_AC_2025-1" should be available

  Scenario: Check unavailable ship
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator"
    And daemon "trader-ship1" is running
    Then "CMDR_AC_2025-1" should not be available

  Scenario: Sync detects stopped daemon
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" is stopped
    When I sync assignments with daemons
    Then the ship should be released automatically
    And the ship should be available

  Scenario: Sync keeps active daemon assignment
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" is running
    When I sync assignments with daemons
    Then the ship should still be assigned to "trading_operator"
    And the assignment status should be "active"

  Scenario: Find available ships
    Given a ship "CMDR_AC_2025-1" is available
    And a ship "CMDR_AC_2025-2" is assigned to "market_analyst"
    And a ship "CMDR_AC_2025-3" is available
    When I find available ships
    Then I should find 2 available ships
    And the available ships should include "CMDR_AC_2025-1"
    And the available ships should include "CMDR_AC_2025-3"

  Scenario: List all assignments
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator"
    And a ship "CMDR_AC_2025-2" is assigned to "market_analyst"
    And a ship "CMDR_AC_2025-3" is available
    When I list all assignments
    Then I should see 3 ships in the list
    And "CMDR_AC_2025-1" should show as "active"
    And "CMDR_AC_2025-3" should show as "idle"

  Scenario: Reassign ships from operation
    Given a ship "CMDR_AC_2025-3" is assigned to "mining_operator" with daemon "miner-ship3"
    And a ship "CMDR_AC_2025-4" is assigned to "mining_operator" with daemon "miner-ship4"
    And a ship "CMDR_AC_2025-5" is assigned to "mining_operator" with daemon "miner-ship5"
    And daemon "miner-ship3" is running
    And daemon "miner-ship4" is running
    And daemon "miner-ship5" is running
    When I reassign ships "CMDR_AC_2025-3,CMDR_AC_2025-4,CMDR_AC_2025-5" from operation "mine"
    Then daemon "miner-ship3" should be stopped
    And daemon "miner-ship4" should be stopped
    And daemon "miner-ship5" should be stopped
    And all 3 ships should be available

  Scenario: Get assignment details
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    When I get assignment details for "CMDR_AC_2025-1"
    Then the operator should be "trading_operator"
    And the daemon ID should be "trader-ship1"
    And the operation should be "trade"

  Scenario: Assign ship with metadata
    Given a ship "CMDR_AC_2025-1" is available
    When I assign "CMDR_AC_2025-1" with metadata {"duration": 4, "route": "SHIP_PARTS"}
    Then the assignment metadata should include "duration" as 4
    And the assignment metadata should include "route" as "SHIP_PARTS"

  Scenario: Cannot assign non-existent ship
    When I assign "NONEXISTENT-SHIP" to "trading_operator" with daemon "trader-x" for operation "trade"
    Then the assignment should succeed
    And the ship should be assigned to "trading_operator"

  Scenario: Stale assignment detection
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" is stopped
    Then the assignment should be stale
    And "CMDR_AC_2025-1" should be available

  Scenario: Reassign from wrong operation fails
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1" for operation "trade"
    When I reassign ships "CMDR_AC_2025-1" from operation "mine"
    Then the reassignment should skip the ship
    And the ship should still be assigned to "trading_operator"

  Scenario: List includes stale assignments
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" is stopped
    When I list all assignments including stale
    Then "CMDR_AC_2025-1" should show as "stale"

  Scenario: Multiple ship assignment workflow
    Given a ship "CMDR_AC_2025-1" is available
    And a ship "CMDR_AC_2025-2" is available
    And a ship "CMDR_AC_2025-3" is available
    When I assign "CMDR_AC_2025-1" to "trading_operator" with daemon "trader-ship1" for operation "trade"
    And I assign "CMDR_AC_2025-2" to "market_analyst" with daemon "market-scout" for operation "scout-markets"
    And I assign "CMDR_AC_2025-3" to "mining_operator" with daemon "miner-ship3" for operation "mine"
    Then all 3 ships should be assigned
    And no ships should be available

  Scenario: Release and reassign workflow
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator"
    When I release "CMDR_AC_2025-1"
    And I assign "CMDR_AC_2025-1" to "mining_operator" with daemon "miner-ship1" for operation "mine"
    Then the ship should be assigned to "mining_operator"
    And the operation should be "mine"

  Scenario: Initialize with player_id
    Given a player with ID 99 exists in database
    When I initialize manager with player_id 99
    Then the manager should use player's token
    And the agent symbol should match player's symbol

  Scenario: Initialize without valid credentials fails
    When I try to initialize manager without credentials
    Then the initialization should fail with error

  Scenario: Find available ships with cargo requirements
    Given a ship "CMDR_AC_2025-1" is available with 40 cargo capacity
    And a ship "CMDR_AC_2025-2" is available with 20 cargo capacity
    And a ship "CMDR_AC_2025-3" is assigned to "mining_operator"
    When I find available ships with cargo requirement 30
    Then I should find 1 available ship
    And the available ships should include "CMDR_AC_2025-1"

  Scenario: Sync releases ship when daemon crashed
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" crashed
    When I sync assignments with daemons
    Then the ship should be released automatically
    And the release reason should be "daemon_stopped"

  Scenario: Reassign ships with timeout
    Given a ship "CMDR_AC_2025-1" is assigned to "mining_operator" with daemon "miner-ship1" for operation "mine"
    And daemon "miner-ship1" is running
    When I reassign ships "CMDR_AC_2025-1" from operation "mine" with timeout 5
    Then daemon "miner-ship1" should be stopped
    And the ship should be available

  Scenario: Reassign skips ship not in registry
    When I reassign ships "NONEXISTENT-SHIP" from operation "mine"
    Then the reassignment should skip the ship

  Scenario: Reassign fails when daemon won't stop
    Given a ship "CMDR_AC_2025-1" is assigned to "mining_operator" with daemon "stuck-daemon" for operation "mine"
    And daemon "stuck-daemon" cannot be stopped
    When I reassign ships "CMDR_AC_2025-1" from operation "mine"
    Then the reassignment should fail for this ship

  Scenario: Multi-player ship isolation
    Given player 1 has a ship "PLAYER1-SHIP-1" assigned to "trading_operator"
    And player 2 has a ship "PLAYER2-SHIP-1" assigned to "mining_operator"
    When player 1 lists assignments
    Then player 1 should only see their own ships
    And player 1 should not see "PLAYER2-SHIP-1"

  Scenario: Stale assignment blocks reassignment until released
    Given a ship "CMDR_AC_2025-1" is assigned to "trading_operator" with daemon "trader-ship1"
    And daemon "trader-ship1" is stopped
    When I assign "CMDR_AC_2025-1" to "mining_operator" with daemon "miner-ship2" for operation "mine"
    Then the assignment should fail
    And the ship should still be assigned to "trading_operator"
    When I release "CMDR_AC_2025-1"
    And I assign "CMDR_AC_2025-1" to "mining_operator" with daemon "miner-ship2" for operation "mine"
    Then the ship should be assigned to "mining_operator"

  Scenario: Initialize with invalid player_id fails
    When I try to initialize manager with player_id 999
    Then the initialization should fail with error
    And the error should contain "Player ID 999 not found"

  Scenario: Release ship not in registry
    When I release "NONEXISTENT-SHIP-999"
    Then the operation should fail silently

  Scenario: Sync with assignment missing daemon_id
    Given a ship "CMDR_AC_2025-1" is assigned without daemon_id
    When I sync assignments with daemons
    Then the sync should skip this assignment

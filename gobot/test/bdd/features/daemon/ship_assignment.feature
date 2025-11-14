Feature: Ship Assignment and Locking
  As a daemon orchestrator
  I want to track and enforce ship assignments to containers
  So that ships cannot be used by multiple operations concurrently

  Background:
    Given a player exists with id 1
    And a ship "TEST-SHIP-1" exists for player 1

  # ============================================================================
  # Basic Ship Assignment Tests
  # ============================================================================

  Scenario: Ship can be assigned to container
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    When I assign ship "TEST-SHIP-1" to container "nav-container-1" with operation "navigate"
    Then ship "TEST-SHIP-1" should be assigned to container "nav-container-1"
    And the ship assignment status should be "active"
    And the ship assignment operation should be "navigate"
    And the ship assignment player_id should be 1
    And the ship assignment should have an assigned_at timestamp

  Scenario: Ship cannot be assigned to multiple containers
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And a container "dock-container-2" exists with type "DOCK" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When I attempt to assign ship "TEST-SHIP-1" to container "dock-container-2" with operation "dock"
    Then the assignment should fail with error "ship is already assigned to another container"
    And ship "TEST-SHIP-1" should still be assigned to container "nav-container-1"

  Scenario: Ship assignment persists in database
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    When I assign ship "TEST-SHIP-1" to container "nav-container-1" with operation "navigate"
    Then the ship assignment should be persisted in the database
    And querying the database should return the assignment for ship "TEST-SHIP-1"

  # ============================================================================
  # Assignment Release Tests
  # ============================================================================

  Scenario: Ship assignment is released when container stops
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When the container "nav-container-1" transitions to "STOPPED" status
    And I release the ship assignment for "TEST-SHIP-1" with reason "container_stopped"
    Then ship "TEST-SHIP-1" should no longer be assigned to any container
    And the ship assignment should have a released_at timestamp
    And the ship assignment release_reason should be "container_stopped"

  Scenario: Ship assignment is released when container fails
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When the container "nav-container-1" transitions to "FAILED" status
    And I release the ship assignment for "TEST-SHIP-1" with reason "container_failed"
    Then ship "TEST-SHIP-1" should no longer be assigned to any container
    And the ship assignment should have a released_at timestamp
    And the ship assignment release_reason should be "container_failed"

  Scenario: Ship assignment is released when daemon stops gracefully
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When the daemon receives shutdown signal
    And I release all ship assignments with reason "daemon_shutdown"
    Then ship "TEST-SHIP-1" should no longer be assigned to any container
    And the ship assignment release_reason should be "daemon_shutdown"

  # ============================================================================
  # Orphaned Assignment Cleanup Tests
  # ============================================================================

  Scenario: Orphaned ship assignments are cleaned on daemon startup
    Given ship "TEST-SHIP-1" has an orphaned assignment to non-existent container "old-container-999"
    And the assignment was created 2 hours ago
    When the daemon starts up
    And I clean orphaned ship assignments
    Then ship "TEST-SHIP-1" should no longer be assigned to any container
    And the orphaned assignment cleanup count should be 1

  # ============================================================================
  # Validation Tests
  # ============================================================================

  Scenario: Ship assignment includes player_id validation
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And a ship "OTHER-SHIP-1" exists for player 2
    When I attempt to assign ship "OTHER-SHIP-1" to container "nav-container-1" with operation "navigate"
    Then the assignment should fail with error "ship player_id mismatch"
    And ship "OTHER-SHIP-1" should not be assigned to any container

  # ============================================================================
  # Lock Behavior Tests
  # ============================================================================

  Scenario: Ship assignment prevents concurrent navigation commands
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When I attempt to execute a navigation command for ship "TEST-SHIP-1"
    Then the navigation command should be rejected with error "ship is locked by container"
    And the error should include container_id "nav-container-1"

  Scenario: Ship assignment allows read-only queries during lock
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When I query ship details for "TEST-SHIP-1"
    Then the query should succeed
    And the ship details should include assignment status "locked"
    And the ship details should include container_id "nav-container-1"

  # ============================================================================
  # Lock Timeout Tests
  # ============================================================================

  Scenario: Ship assignment lock timeout after 30 minutes
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    And the assignment was created 31 minutes ago
    When I check if the ship assignment is stale
    Then the assignment should be marked as stale
    And I should be able to forcefully release the stale assignment

  Scenario: Ship assignment reacquire fails if still locked
    Given a container "nav-container-1" exists with type "NAVIGATE" for player 1
    And ship "TEST-SHIP-1" is assigned to container "nav-container-1" with operation "navigate"
    When I attempt to reassign ship "TEST-SHIP-1" from "nav-container-1" to "nav-container-2"
    Then the reassignment should fail with error "ship is still locked"
    And ship "TEST-SHIP-1" should still be assigned to container "nav-container-1"

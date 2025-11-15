Feature: Ship Assignment Manager - Domain Logic
  As a daemon orchestrator
  I want to manage ship assignments centrally
  So that ship locking is enforced system-wide

  # ============================================================================
  # Assign Ship Tests
  # ============================================================================

  Scenario: Assign ship to container successfully
    When I assign ship "SHIP-1" to container "container-123" with player 1 and operation "navigate"
    Then the assignment should succeed
    And ship "SHIP-1" should be assigned
    And the assignment should have ship symbol "SHIP-1"
    And the assignment should have container ID "container-123"
    And the assignment should have player ID 1
    And the assignment should have operation "navigate"

  Scenario: Assign ship creates active assignment
    When I assign ship "SHIP-1" to container "container-123" with player 1 and operation "navigate"
    Then the assignment status should be "active"

  Scenario: Cannot assign already assigned ship
    Given ship "SHIP-1" is assigned to container "container-123"
    When I attempt to assign ship "SHIP-1" to container "container-456" with player 1 and operation "dock"
    Then the assignment should fail with error "ship is already assigned to another container"
    And ship "SHIP-1" should still be assigned to container "container-123"

  Scenario: Can reassign ship after release
    Given ship "SHIP-1" is assigned to container "container-123"
    And the assignment for ship "SHIP-1" is released
    When I assign ship "SHIP-1" to container "container-456" with player 1 and operation "refuel"
    Then the assignment should succeed
    And ship "SHIP-1" should be assigned to container "container-456"

  Scenario: Assign multiple ships to different containers
    When I assign ship "SHIP-1" to container "container-123" with player 1 and operation "navigate"
    And I assign ship "SHIP-2" to container "container-456" with player 1 and operation "dock"
    And I assign ship "SHIP-3" to container "container-789" with player 1 and operation "refuel"
    Then ship "SHIP-1" should be assigned to container "container-123"
    And ship "SHIP-2" should be assigned to container "container-456"
    And ship "SHIP-3" should be assigned to container "container-789"

  Scenario: Assign same ship to same container multiple times fails
    Given ship "SHIP-1" is assigned to container "container-123"
    When I attempt to assign ship "SHIP-1" to container "container-123" with player 1 and operation "navigate"
    Then the assignment should fail with error "ship is already assigned to another container"

  # ============================================================================
  # Get Assignment Tests
  # ============================================================================

  Scenario: Get assignment for assigned ship
    Given ship "SHIP-1" is assigned to container "container-123"
    When I get the assignment for ship "SHIP-1"
    Then an assignment should be found
    And the assignment should have container ID "container-123"

  Scenario: Get assignment for unassigned ship
    When I get the assignment for ship "SHIP-UNASSIGNED"
    Then no assignment should be found

  Scenario: Get assignment returns correct assignment
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    When I get the assignment for ship "SHIP-2"
    Then an assignment should be found
    And the assignment should have container ID "container-456"
    And the assignment should have ship symbol "SHIP-2"

  # ============================================================================
  # Release Assignment Tests
  # ============================================================================

  Scenario: Release ship assignment by ship symbol
    Given ship "SHIP-1" is assigned to container "container-123"
    When I release the assignment for ship "SHIP-1" with reason "operation_complete"
    Then the release should succeed
    And the assignment should have release reason "operation_complete"

  Scenario: Cannot release non-existent assignment
    When I attempt to release the assignment for ship "SHIP-NONEXISTENT" with reason "test"
    Then the release should fail with error "no assignment found for ship SHIP-NONEXISTENT"

  Scenario: Cannot double release assignment
    Given ship "SHIP-1" is assigned to container "container-123"
    And the assignment for ship "SHIP-1" is released with reason "first_release"
    When I attempt to release the assignment for ship "SHIP-1" with reason "second_release"
    Then the release should fail with error "assignment already released"

  # ============================================================================
  # Release All Tests
  # ============================================================================

  Scenario: Release all active assignments
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    And ship "SHIP-3" is assigned to container "container-789"
    When I release all assignments with reason "daemon_shutdown"
    Then all assignments should be released
    And all release reasons should be "daemon_shutdown"

  Scenario: Release all on empty manager succeeds
    When I release all assignments with reason "daemon_shutdown"
    Then the operation should succeed

  Scenario: Release all only releases active assignments
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    And the assignment for ship "SHIP-1" is released with reason "already_released"
    When I release all assignments with reason "shutdown"
    Then ship "SHIP-2" assignment should have release reason "shutdown"
    And ship "SHIP-1" assignment should still have release reason "already_released"

  # ============================================================================
  # Clean Orphaned Assignments Tests
  # ============================================================================

  Scenario: Clean orphaned assignments when container no longer exists
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    And only container "container-456" exists
    When I clean orphaned assignments
    Then 1 assignment should be cleaned
    And ship "SHIP-1" assignment should be released with reason "orphaned_cleanup"
    And ship "SHIP-2" assignment should remain active

  Scenario: Clean orphaned assignments with no containers
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    And no containers exist
    When I clean orphaned assignments
    Then 2 assignments should be cleaned
    And all assignments should be released with reason "orphaned_cleanup"

  Scenario: Clean orphaned assignments when all containers exist
    Given ship "SHIP-1" is assigned to container "container-123"
    And ship "SHIP-2" is assigned to container "container-456"
    And containers "container-123,container-456" exist
    When I clean orphaned assignments
    Then 0 assignments should be cleaned
    And all assignments should remain active

  Scenario: Clean orphaned assignments skips already released
    Given ship "SHIP-1" is assigned to container "container-123"
    And the assignment for ship "SHIP-1" is released with reason "manual_release"
    And no containers exist
    When I clean orphaned assignments
    Then 0 assignments should be cleaned

  Scenario: Clean orphaned assignments with empty manager
    Given no containers exist
    When I clean orphaned assignments
    Then 0 assignments should be cleaned

  # ============================================================================
  # Clean Stale Assignments Tests
  # ============================================================================

  Scenario: Clean stale assignments after timeout
    Given ship "SHIP-1" was assigned to container "container-123" 45 minutes ago
    And ship "SHIP-2" was assigned to container "container-456" 10 minutes ago
    When I clean stale assignments with timeout 30 minutes
    Then 1 assignment should be cleaned
    And ship "SHIP-1" assignment should be released with reason "stale_timeout"
    And ship "SHIP-2" assignment should remain active

  Scenario: Clean stale assignments at exact timeout boundary
    Given ship "SHIP-1" was assigned to container "container-123" exactly 30 minutes ago
    When I clean stale assignments with timeout 30 minutes
    Then 0 assignments should be cleaned
    And ship "SHIP-1" assignment should remain active

  Scenario: Clean stale assignments one second over timeout
    Given ship "SHIP-1" was assigned to container "container-123" "30 minutes and 1 second" ago
    When I clean stale assignments with timeout 30 minutes
    Then 1 assignment should be cleaned
    And ship "SHIP-1" assignment should be released with reason "stale_timeout"

  Scenario: Clean stale assignments with zero timeout
    Given ship "SHIP-1" was assigned to container "container-123" 1 second ago
    When I clean stale assignments with timeout 0 seconds
    Then 1 assignment should be cleaned

  Scenario: Clean stale assignments skips released assignments
    Given ship "SHIP-1" was assigned to container "container-123" 60 minutes ago
    And the assignment for ship "SHIP-1" is released with reason "manual_release"
    When I clean stale assignments with timeout 30 minutes
    Then 0 assignments should be cleaned

  Scenario: Clean all stale assignments
    Given ship "SHIP-1" was assigned to container "container-123" 45 minutes ago
    And ship "SHIP-2" was assigned to container "container-456" 50 minutes ago
    And ship "SHIP-3" was assigned to container "container-789" 35 minutes ago
    When I clean stale assignments with timeout 30 minutes
    Then 3 assignments should be cleaned
    And all assignments should be released with reason "stale_timeout"

  Scenario: Clean stale with very long timeout
    Given ship "SHIP-1" was assigned to container "container-123" 1 hour ago
    When I clean stale assignments with timeout 24 hours
    Then 0 assignments should be cleaned

  Scenario: Clean stale assignments with empty manager
    When I clean stale assignments with timeout 30 minutes
    Then 0 assignments should be cleaned

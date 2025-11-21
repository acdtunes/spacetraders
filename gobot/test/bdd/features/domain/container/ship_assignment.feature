Feature: Ship Assignment Entity
  As a SpaceTraders container runtime
  I want to manage ship assignments with proper locking and lifecycle control
  So that I can prevent concurrent operations on the same ship

  # ============================================================================
  # Ship Assignment Creation Tests
  # ============================================================================

  Scenario: Create ship assignment in active state
    When I create a ship assignment for ship "MINER-1", player 1, container "container-123"
    Then the ship assignment should be active
    And the ship assignment ship symbol should be "MINER-1"
    And the ship assignment player id should be 1
    And the ship assignment container id should be "container-123"
    And the ship assignment released_at should be nil
    And the ship assignment release_reason should be nil

  # ============================================================================
  # Release Transition Tests
  # ============================================================================

  Scenario: Release active assignment
    Given a ship assignment for ship "MINER-1" in "active" state
    When I release the ship assignment with reason "task_complete"
    Then the ship assignment should not be active
    And the ship assignment status should be "released"
    And the ship assignment released_at should not be nil
    And the ship assignment release_reason should be "task_complete"

  Scenario: Cannot release already released assignment
    Given a ship assignment for ship "MINER-1" in "released" state
    When I attempt to release the ship assignment with reason "duplicate_release"
    Then the ship assignment operation should fail with error "assignment already released"

  Scenario: Force release active assignment
    Given a ship assignment for ship "MINER-1" in "active" state
    When I force release the ship assignment with reason "stale_timeout"
    Then the ship assignment should not be active
    And the ship assignment status should be "released"
    And the ship assignment release_reason should be "stale_timeout"

  Scenario: Force release already released assignment
    Given a ship assignment for ship "MINER-1" in "released" state
    When I force release the ship assignment with reason "cleanup"
    Then the ship assignment status should be "released"
    And the ship assignment release_reason should be "cleanup"

  # ============================================================================
  # Staleness Detection Tests
  # ============================================================================

  Scenario: Fresh assignment is not stale
    Given a ship assignment for ship "MINER-1" in "active" state
    When I check if the assignment is stale with timeout 300 seconds
    Then the ship assignment should not be stale

  Scenario: Old assignment is stale
    Given a ship assignment for ship "MINER-1" in "active" state
    When I advance time by 600 seconds
    And I check if the assignment is stale with timeout 300 seconds
    Then the ship assignment should be stale

  Scenario: Released assignment is never stale
    Given a ship assignment for ship "MINER-1" in "released" state
    When I advance time by 600 seconds
    And I check if the assignment is stale with timeout 300 seconds
    Then the ship assignment should not be stale

  # ============================================================================
  # Ship Assignment Manager Tests
  # ============================================================================

  Scenario: Assign ship to container
    Given a ship assignment manager
    When I assign ship "MINER-1" player 1 to container "container-123"
    Then the assignment should succeed
    And the assignment should be active
    And the assignment ship symbol should be "MINER-1"

  Scenario: Cannot assign already assigned ship
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    When I attempt to assign ship "MINER-1" player 1 to container "container-456"
    Then the assignment should fail with error "ship is already assigned to another container"

  Scenario: Can reassign released ship
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I release assignment for ship "MINER-1" with reason "task_complete"
    When I assign ship "MINER-1" player 1 to container "container-456"
    Then the assignment should succeed
    And the assignment container id should be "container-456"

  Scenario: Get existing assignment
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    When I get assignment for ship "MINER-1"
    Then the assignment should exist
    And the assignment should be active

  Scenario: Get non-existent assignment
    Given a ship assignment manager
    When I get assignment for ship "GHOST-SHIP"
    Then the assignment should not exist

  Scenario: Release assignment by ship symbol
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    When I release assignment for ship "MINER-1" with reason "manual_release"
    Then the release should succeed
    When I get assignment for ship "MINER-1"
    Then the assignment should not be active

  Scenario: Cannot release non-existent assignment
    Given a ship assignment manager
    When I attempt to release assignment for ship "GHOST-SHIP" with reason "test"
    Then the release should fail with error "no assignment found for ship GHOST-SHIP"

  Scenario: Release all active assignments
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-123"
    And I assign ship "TRANSPORT-1" player 1 to container "container-456"
    When I release all assignments with reason "shutdown"
    Then all assignments should be released

  Scenario: Release all skips already released assignments
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-123"
    And I release assignment for ship "MINER-1" with reason "early_release"
    When I release all assignments with reason "shutdown"
    Then the assignment for "MINER-2" should be released

  # ============================================================================
  # Orphaned Assignment Cleanup Tests
  # ============================================================================

  Scenario: Clean orphaned assignments releases assignments for deleted containers
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-456"
    And I assign ship "TRANSPORT-1" player 1 to container "container-789"
    When I clean orphaned assignments for existing containers "container-123,container-789"
    Then 1 assignment should be cleaned
    And the assignment for "MINER-2" should be released
    And the assignment for "MINER-1" should be active
    And the assignment for "TRANSPORT-1" should be active

  Scenario: Clean orphaned assignments with no orphans
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-456"
    When I clean orphaned assignments for existing containers "container-123,container-456"
    Then 0 assignments should be cleaned
    And the assignment for "MINER-1" should be active
    And the assignment for "MINER-2" should be active

  Scenario: Clean orphaned assignments skips already released
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-456"
    And I release assignment for ship "MINER-1" with reason "early_release"
    When I clean orphaned assignments for existing containers "container-789"
    Then 1 assignment should be cleaned
    And the assignment for "MINER-2" should be released

  # ============================================================================
  # Stale Assignment Cleanup Tests
  # ============================================================================

  Scenario: Clean stale assignments releases old assignments
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-456"
    And I advance time by 600 seconds
    And I assign ship "TRANSPORT-1" player 1 to container "container-789"
    When I clean stale assignments with timeout 300 seconds
    Then 2 assignments should be cleaned
    And the assignment for "MINER-1" should be released
    And the assignment for "MINER-2" should be released
    And the assignment for "TRANSPORT-1" should be active

  Scenario: Clean stale assignments with no stale assignments
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I advance time by 60 seconds
    When I clean stale assignments with timeout 300 seconds
    Then 0 assignments should be cleaned
    And the assignment for "MINER-1" should be active

  Scenario: Clean stale assignments skips already released
    Given a ship assignment manager
    And I assign ship "MINER-1" player 1 to container "container-123"
    And I assign ship "MINER-2" player 1 to container "container-456"
    And I advance time by 600 seconds
    And I release assignment for ship "MINER-1" with reason "early_release"
    When I clean stale assignments with timeout 300 seconds
    Then 1 assignment should be cleaned
    And the assignment for "MINER-2" should be released

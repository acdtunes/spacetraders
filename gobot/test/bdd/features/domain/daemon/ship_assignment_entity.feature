Feature: Ship Assignment Entity - Domain Logic
  As a daemon orchestrator
  I want ship assignment business rules to be enforced
  So that resource locking and lifecycle management is predictable

  # ============================================================================
  # Ship Assignment Creation Tests
  # ============================================================================

  Scenario: Create ship assignment with valid data
    When I create a ship assignment with ship "SHIP-1", player 1, container "container-123", operation "navigate"
    Then the ship assignment should have ship symbol "SHIP-1"
    And the ship assignment should have player ID 1
    And the ship assignment should have container ID "container-123"
    And the ship assignment should have operation "navigate"
    And the ship assignment status should be "active"
    And the ship assignment should have an assigned_at timestamp

  Scenario: New ship assignment has no release information
    When I create a ship assignment with ship "SHIP-1", player 1, container "container-123", operation "navigate"
    Then the ship assignment should not have a released_at timestamp
    And the ship assignment should not have a release reason

  # ============================================================================
  # Ship Assignment Release Tests
  # ============================================================================

  Scenario: Release active ship assignment
    Given an active ship assignment for ship "SHIP-1"
    When I release the assignment with reason "operation_complete"
    Then the ship assignment status should be "released"
    And the ship assignment should have a released_at timestamp
    And the ship assignment release reason should be "operation_complete"

  Scenario: Cannot release already released assignment
    Given a released ship assignment for ship "SHIP-1"
    When I attempt to release the assignment with reason "duplicate_release"
    Then the release should fail with error "assignment already released"
    And the ship assignment status should remain "released"

  Scenario: Release assignment with container stopped reason
    Given an active ship assignment for ship "SHIP-1"
    When I release the assignment with reason "container_stopped"
    Then the ship assignment release reason should be "container_stopped"

  Scenario: Release assignment with container failed reason
    Given an active ship assignment for ship "SHIP-1"
    When I release the assignment with reason "container_failed"
    Then the ship assignment release reason should be "container_failed"

  Scenario: Release assignment with daemon shutdown reason
    Given an active ship assignment for ship "SHIP-1"
    When I release the assignment with reason "daemon_shutdown"
    Then the ship assignment release reason should be "daemon_shutdown"

  # ============================================================================
  # Force Release Tests
  # ============================================================================

  Scenario: Force release active assignment
    Given an active ship assignment for ship "SHIP-1"
    When I force release the assignment with reason "forced_cleanup"
    Then the ship assignment status should be "released"
    And the ship assignment should have a released_at timestamp
    And the ship assignment release reason should be "forced_cleanup"

  Scenario: Force release already released assignment succeeds
    Given a released ship assignment for ship "SHIP-1"
    When I force release the assignment with reason "force_override"
    Then the ship assignment status should be "released"
    And the ship assignment release reason should be "force_override"

  Scenario: Force release with stale timeout reason
    Given an active ship assignment for ship "SHIP-1"
    When I force release the assignment with reason "stale_timeout"
    Then the ship assignment release reason should be "stale_timeout"

  # ============================================================================
  # Is Stale Tests
  # ============================================================================

  Scenario: Assignment is not stale when within timeout
    Given an active ship assignment created 10 minutes ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should not be stale

  Scenario: Assignment is stale when exceeds timeout
    Given an active ship assignment created 45 minutes ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should be stale

  Scenario: Assignment at exact timeout boundary is not stale
    Given an active ship assignment created 30 minutes ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should not be stale

  Scenario: Assignment one second over timeout is stale
    Given an active ship assignment created "30 minutes and 1 second" ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should be stale

  Scenario: Released assignment is never stale
    Given a released ship assignment created 60 minutes ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should not be stale

  Scenario: Very old released assignment is not stale
    Given a released ship assignment created 500 days ago
    When I check if the assignment is stale with timeout 30 minutes
    Then the assignment should not be stale

  Scenario: Fresh assignment is not stale with short timeout
    Given an active ship assignment created 5 seconds ago
    When I check if the assignment is stale with timeout 1 minute
    Then the assignment should not be stale

  Scenario: Zero timeout makes all active assignments stale
    Given an active ship assignment created 1 second ago
    When I check if the assignment is stale with timeout 0 seconds
    Then the assignment should be stale

  # ============================================================================
  # Is Active Tests
  # ============================================================================

  Scenario: New assignment is active
    When I create a ship assignment with ship "SHIP-1", player 1, container "container-123", operation "navigate"
    Then the assignment should be active

  Scenario: Released assignment is not active
    Given a released ship assignment for ship "SHIP-1"
    Then the assignment should not be active

  Scenario: Assignment becomes inactive after release
    Given an active ship assignment for ship "SHIP-1"
    When I release the assignment with reason "test"
    Then the assignment should not be active

  # ============================================================================
  # String Representation Tests
  # ============================================================================

  Scenario: String representation includes all key fields
    When I create a ship assignment with ship "SHIP-1", player 1, container "container-123", operation "navigate"
    Then the assignment string representation should contain "SHIP-1"
    And the assignment string representation should contain "container-123"
    And the assignment string representation should contain "navigate"
    And the assignment string representation should contain "active"

  Scenario: Released assignment string shows released status
    Given a released ship assignment for ship "SHIP-1"
    Then the assignment string representation should contain "released"

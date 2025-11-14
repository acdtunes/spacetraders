Feature: Daemon Container Lifecycle
  As a daemon system
  I need containers to properly transition status during daemon operations
  So that container state accurately reflects execution lifecycle in real daemon scenarios

  # ============================================================================
  # Basic Container Lifecycle in Daemon Context
  # ============================================================================

  Scenario: Container transitions to RUNNING on successful start
    Given a new daemon container is created with type "NAVIGATE"
    When the daemon starts the container
    Then the container status should be "RUNNING"
    And the container started_at timestamp should be set
    And the container stopped_at timestamp should be nil

  Scenario: Container transitions to COMPLETED on successful completion
    Given a daemon container is in "RUNNING" status
    When the container operation completes successfully
    Then the container status should be "COMPLETED"
    And the container stopped_at timestamp should be set
    And the container should be marked as finished

  Scenario: Container transitions to FAILED on error
    Given a daemon container is in "RUNNING" status
    When the container operation encounters an error "Navigation failed"
    Then the container status should be "FAILED"
    And the container stopped_at timestamp should be set
    And the container last_error should be "Navigation failed"
    And the container should be marked as finished

  Scenario: Container stays in STOPPING during graceful shutdown
    Given a daemon container is in "RUNNING" status
    When the daemon signals the container to stop
    Then the container status should be "STOPPING"
    And the container stopped_at timestamp should be nil
    And the container should not be marked as finished

  Scenario: Container finalizes to STOPPED after graceful shutdown
    Given a daemon container is in "STOPPING" status
    When the daemon finalizes the container shutdown
    Then the container status should be "STOPPED"
    And the container stopped_at timestamp should be set
    And the container should be marked as finished

  # ============================================================================
  # Quick-Running Container Tests
  # ============================================================================

  Scenario: Quick-running containers properly transition status
    Given a daemon container that runs for less than 1 second
    When the container completes its operation
    Then the status should be "COMPLETED" not "RUNNING"
    And the container should be marked as finished

  Scenario: Container status reflects completion even with stop timestamp set
    Given a container with status "RUNNING"
    And the container completes successfully
    When I query the container status
    Then the status must be "COMPLETED" not "RUNNING"
    And the stopped_at timestamp should be set

  # ============================================================================
  # Container List and Query Operations
  # ============================================================================

  Scenario: List containers shows correct status after completion
    Given a new daemon container is created with type "DOCK"
    When the container operation completes successfully
    And I list all daemon containers
    Then the container should appear with status "COMPLETED"
    And the container should be included in the finished containers list

  Scenario: Completed containers can be queried by status
    Given a daemon container is in "COMPLETED" status
    When I query containers by status "COMPLETED"
    Then the container should appear in the results
    And the container should have finished flag set to true

  Scenario: Running containers excluded from finished containers list
    Given 3 daemon containers exist:
      | container-1 | RUNNING   |
      | container-2 | COMPLETED |
      | container-3 | FAILED    |
    When I list only finished containers
    Then the results should contain 2 containers
    And the results should include "container-2" and "container-3"
    And the results should not include "container-1"

  # ============================================================================
  # Container Timestamp Tracking
  # ============================================================================

  Scenario: Container tracks start and stop timestamps
    Given a new daemon container is created with type "REFUEL"
    When the daemon starts the container
    And 2 seconds pass
    And the container operation completes successfully
    Then the container started_at timestamp should be set
    And the container stopped_at timestamp should be set
    And the runtime duration should be approximately 2 seconds

  Scenario: Container started_at persists through state transitions
    Given a daemon container is in "RUNNING" status
    When the container transitions to "COMPLETED"
    Then the container started_at timestamp should remain unchanged
    And the container stopped_at timestamp should be set

  Scenario: Container stopped_at is set for all terminal states
    Given a daemon container is in "RUNNING" status
    When the container transitions to "FAILED"
    Then the container stopped_at timestamp should be set
    And the stopped_at should be greater than or equal to started_at

  # ============================================================================
  # Iteration Management in Daemon Context
  # ============================================================================

  Scenario: Container increments iteration count on loop
    Given a daemon container is in "RUNNING" status with max_iterations 5
    When the container completes one iteration
    Then the container current_iteration should increment to 1
    And the container should continue running

  Scenario: Container respects max_iterations limit
    Given a daemon container with max_iterations 3 and current_iteration 2
    When the container completes one iteration
    Then the container current_iteration should be 3
    And the container should not continue running

  Scenario: Container with -1 iterations runs indefinitely
    Given a daemon container with max_iterations -1 and current_iteration 100
    When I check if the container should continue
    Then the container should continue running
    And the current_iteration should be 100

  Scenario: Container exits after max_iterations reached
    Given a daemon container with max_iterations 5
    When the container completes 5 iterations
    Then the container should not continue running
    And the container current_iteration should be 5

  # ============================================================================
  # Restart Policy and Tracking
  # ============================================================================

  Scenario: Container restart increments restart_count
    Given a daemon container in "FAILED" status with restart_count 0
    When the daemon restarts the container
    Then the container status should be "PENDING"
    And the container restart_count should be 1
    And the container last_error should be nil

  Scenario: Container respects max_restarts policy (3)
    Given a daemon container in "FAILED" status with restart_count 3
    When I check if the container can restart
    Then the restart eligibility should be false
    And the container should remain in "FAILED" status

  Scenario: Container cannot restart after exceeding max_restarts
    Given a daemon container in "FAILED" status with restart_count 3
    When I attempt to restart the container
    Then the restart operation should fail
    And the error should mention "cannot be restarted"
    And the container should remain in "FAILED" status

  Scenario: Container maintains player_id through restarts
    Given a daemon container with player_id 42 in "FAILED" status
    When the daemon restarts the container
    Then the container should have player_id 42
    And the container status should be "PENDING"

  Scenario: Container maintains ship assignment through restarts
    Given a daemon container with metadata "ship_symbol" = "SHIP-1"
    And the container is in "FAILED" status
    When the daemon restarts the container
    Then the container metadata should still contain "ship_symbol" = "SHIP-1"
    And the container status should be "PENDING"

  Scenario: Container tracks multiple restart attempts
    Given a daemon container in "FAILED" status with restart_count 0
    When the daemon restarts the container 2 times
    Then the container restart_count should be 2
    And the container should still be eligible for restart

  # ============================================================================
  # Multiple Container Management
  # ============================================================================

  Scenario: Multiple containers can run simultaneously
    Given 5 daemon containers are created
    When the daemon starts all containers
    Then all containers should have status "RUNNING"
    And each container should have a unique started_at timestamp

  Scenario: Container IDs are unique and sequential
    Given 10 daemon containers are created
    Then each container should have a unique ID
    And the container IDs should be sequential

  Scenario: Daemon tracks containers independently
    Given 3 daemon containers exist:
      | container-1 | RUNNING   |
      | container-2 | COMPLETED |
      | container-3 | STOPPED   |
    When I query container "container-1"
    Then the container status should be "RUNNING"
    When I query container "container-2"
    Then the container status should be "COMPLETED"
    When I query container "container-3"
    Then the container status should be "STOPPED"

  # ============================================================================
  # Container Removal
  # ============================================================================

  Scenario: Completed containers can be removed without stopping
    Given a daemon container is in "COMPLETED" status
    When I remove the container
    Then the removal should succeed
    And the container should not appear in the list

  Scenario: Failed containers can be removed without stopping
    Given a daemon container is in "FAILED" status
    When I remove the container
    Then the removal should succeed
    And the container should not appear in the list

  Scenario: Stopped containers can be removed without stopping
    Given a daemon container is in "STOPPED" status
    When I remove the container
    Then the removal should succeed
    And the container should not appear in the list

  Scenario: Running containers must be stopped before removal
    Given a daemon container is in "RUNNING" status
    When I attempt to remove the container without stopping
    Then the removal should fail
    And the error should mention "must be stopped first"
    And the container should still appear in the list

  # ============================================================================
  # Edge Cases and Error Conditions
  # ============================================================================

  Scenario: Container cannot transition from COMPLETED to RUNNING
    Given a daemon container is in "COMPLETED" status
    When I attempt to start the container
    Then the operation should fail
    And the error should mention "cannot start container in COMPLETED state"
    And the container status should remain "COMPLETED"

  Scenario: Container cannot transition from FAILED to RUNNING without restart
    Given a daemon container is in "FAILED" status
    When I attempt to start the container
    Then the operation should fail
    And the error should mention "cannot start container in FAILED state"

  Scenario: Container cannot be completed if not running
    Given a daemon container is in "PENDING" status
    When I attempt to complete the container
    Then the operation should fail
    And the error should mention "cannot complete container in PENDING state"

  Scenario: Container stopped_at timestamp is immutable once set
    Given a daemon container is in "COMPLETED" status
    And the stopped_at timestamp is recorded
    When time advances by 5 seconds
    Then the stopped_at timestamp should remain unchanged

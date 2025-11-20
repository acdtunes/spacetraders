Feature: Container Entity
  As a SpaceTraders daemon
  I want to manage container entities with proper state transitions and lifecycle control
  So that I can orchestrate background operations safely and reliably

  # ============================================================================
  # Container Creation Tests
  # ============================================================================

  Scenario: Create container in pending state
    When I create a container with id "mining-1", type "MINING", player 1, max_iterations 10
    Then the container status should be "PENDING"
    And the container current iteration should be 0
    And the container restart count should be 0
    And the container started_at should be nil
    And the container stopped_at should be nil

  Scenario: Create container with infinite iterations
    When I create a container with id "scout-1", type "SCOUT", player 1, max_iterations -1
    Then the container status should be "PENDING"
    And the container max iterations should be -1

  Scenario: Create container with metadata
    When I create a container with id "mining-1", type "MINING", player 1, max_iterations 10, metadata:
      | key           | value         |
      | asteroid      | X1-A1-FIELD   |
      | resource_type | IRON_ORE      |
    Then the container metadata should contain "asteroid" with value "X1-A1-FIELD"
    And the container metadata should contain "resource_type" with value "IRON_ORE"

  # ============================================================================
  # State Machine: Start Transition
  # ============================================================================

  Scenario: Start container transitions pending to running
    Given a container in "PENDING" state
    When I start the container
    Then the container status should be "RUNNING"
    And the container started_at should not be nil
    And the container stopped_at should be nil

  Scenario: Start container from stopped state
    Given a container in "STOPPED" state
    When I start the container
    Then the container status should be "RUNNING"
    And the container started_at should not be nil

  Scenario: Cannot start container already running
    Given a container in "RUNNING" state
    When I attempt to start the container
    Then the container operation should fail with error "cannot start container in RUNNING state"

  Scenario: Cannot start completed container
    Given a container in "COMPLETED" state
    When I attempt to start the container
    Then the container operation should fail with error "cannot start container in COMPLETED state"

  Scenario: Cannot start failed container without restart
    Given a container in "FAILED" state
    When I attempt to start the container
    Then the container operation should fail with error "cannot start container in FAILED state"

  # ============================================================================
  # State Machine: Complete Transition
  # ============================================================================

  Scenario: Complete container from running state
    Given a container in "RUNNING" state
    When I complete the container
    Then the container status should be "COMPLETED"
    And the container stopped_at should not be nil

  Scenario: Cannot complete container in pending state
    Given a container in "PENDING" state
    When I attempt to complete the container
    Then the container operation should fail with error "cannot complete container in PENDING state"

  Scenario: Cannot complete already completed container
    Given a container in "COMPLETED" state
    When I attempt to complete the container
    Then the container operation should fail with error "cannot complete container in COMPLETED state"

  Scenario: Cannot complete stopped container
    Given a container in "STOPPED" state
    When I attempt to complete the container
    Then the container operation should fail with error "cannot complete container in STOPPED state"

  # ============================================================================
  # State Machine: Fail Transition
  # ============================================================================

  Scenario: Fail container from running state
    Given a container in "RUNNING" state
    When I fail the container with error "extraction failed"
    Then the container status should be "FAILED"
    And the container stopped_at should not be nil
    And the container last_error should be "extraction failed"

  Scenario: Fail container from pending state
    Given a container in "PENDING" state
    When I fail the container with error "validation failed"
    Then the container status should be "FAILED"
    And the container last_error should be "validation failed"

  Scenario: Fail container from stopping state
    Given a container in "STOPPING" state
    When I fail the container with error "shutdown error"
    Then the container status should be "FAILED"
    And the container last_error should be "shutdown error"

  Scenario: Cannot fail completed container
    Given a container in "COMPLETED" state
    When I attempt to fail the container with error "too late"
    Then the container operation should fail with error "cannot fail container in COMPLETED state"

  Scenario: Cannot fail stopped container
    Given a container in "STOPPED" state
    When I attempt to fail the container with error "too late"
    Then the container operation should fail with error "cannot fail container in STOPPED state"

  # ============================================================================
  # State Machine: Stop Transition (Two-Phase)
  # ============================================================================

  Scenario: Stop running container enters stopping state
    Given a container in "RUNNING" state
    When I stop the container
    Then the container status should be "STOPPING"
    And the container stopped_at should be nil

  Scenario: Mark stopped finalizes stop transition
    Given a container in "STOPPING" state
    When I mark the container as stopped
    Then the container status should be "STOPPED"
    And the container stopped_at should not be nil

  Scenario: Stop pending container directly
    Given a container in "PENDING" state
    When I stop the container
    Then the container status should be "STOPPED"
    And the container stopped_at should not be nil

  Scenario: Stop failed container directly
    Given a container in "FAILED" state
    When I stop the container
    Then the container status should be "STOPPED"
    And the container stopped_at should not be nil

  Scenario: Cannot stop completed container
    Given a container in "COMPLETED" state
    When I attempt to stop the container
    Then the container operation should fail with error "cannot stop container in COMPLETED state"

  Scenario: Cannot stop already stopped container
    Given a container in "STOPPED" state
    When I attempt to stop the container
    Then the container operation should fail with error "cannot stop container in STOPPED state"

  Scenario: Cannot mark stopped when not in stopping state
    Given a container in "RUNNING" state
    When I attempt to mark the container as stopped
    Then the container operation should fail with error "cannot mark stopped when not in stopping state"

  # ============================================================================
  # Iteration Control: Infinite Loop
  # ============================================================================

  Scenario: Infinite loop container never stops iterating
    Given a container with max_iterations -1 at iteration 0
    When I check if container should continue
    Then the container should continue

  Scenario: Infinite loop container should continue at high iterations
    Given a container with max_iterations -1 at iteration 100
    When I check if container should continue
    Then the container should continue

  Scenario: Infinite loop container should continue at very high iterations
    Given a container with max_iterations -1 at iteration 999999
    When I check if container should continue
    Then the container should continue

  # ============================================================================
  # Iteration Control: Finite Loop
  # ============================================================================

  Scenario: Finite loop container should continue below limit
    Given a container with max_iterations 5 at iteration 0
    When I check if container should continue
    Then the container should continue

  Scenario: Finite loop container should continue at limit minus one
    Given a container with max_iterations 5 at iteration 4
    When I check if container should continue
    Then the container should continue

  Scenario: Finite loop container stops at limit
    Given a container with max_iterations 5 at iteration 5
    When I check if container should continue
    Then the container should not continue

  Scenario: Finite loop container stops beyond limit
    Given a container with max_iterations 5 at iteration 6
    When I check if container should continue
    Then the container should not continue

  Scenario: Single iteration container
    Given a container with max_iterations 1 at iteration 0
    When I check if container should continue
    Then the container should continue
    And when I increment the container iteration
    And I check if container should continue
    Then the container should not continue

  # ============================================================================
  # Iteration Control: Increment
  # ============================================================================

  Scenario: Increment iteration in running container
    Given a container in "RUNNING" state at iteration 2
    When I increment the container iteration
    Then the container current iteration should be 3

  Scenario: Cannot increment iteration in pending state
    Given a container in "PENDING" state at iteration 0
    When I attempt to increment the container iteration
    Then the container operation should fail with error "cannot increment iteration in PENDING state"

  Scenario: Cannot increment iteration in completed state
    Given a container in "COMPLETED" state at iteration 5
    When I attempt to increment the container iteration
    Then the container operation should fail with error "cannot increment iteration in COMPLETED state"

  Scenario: Cannot increment iteration in stopped state
    Given a container in "STOPPED" state at iteration 3
    When I attempt to increment the container iteration
    Then the container operation should fail with error "cannot increment iteration in STOPPED state"

  # ============================================================================
  # Restart Management: Eligibility
  # ============================================================================

  Scenario: Failed container can restart under limit
    Given a container in "FAILED" state with restart_count 0
    When I check if container can restart
    Then the container can restart

  Scenario: Failed container can restart at limit minus one
    Given a container in "FAILED" state with restart_count 2
    When I check if container can restart
    Then the container can restart

  Scenario: Failed container cannot restart at max restarts
    Given a container in "FAILED" state with restart_count 3
    When I check if container can restart
    Then the container cannot restart

  Scenario: Failed container cannot restart beyond max restarts
    Given a container in "FAILED" state with restart_count 4
    When I check if container can restart
    Then the container cannot restart

  Scenario: Running container cannot restart
    Given a container in "RUNNING" state with restart_count 0
    When I check if container can restart
    Then the container cannot restart

  Scenario: Pending container cannot restart
    Given a container in "PENDING" state with restart_count 0
    When I check if container can restart
    Then the container cannot restart

  Scenario: Completed container cannot restart
    Given a container in "COMPLETED" state with restart_count 0
    When I check if container can restart
    Then the container cannot restart

  # ============================================================================
  # Restart Management: Reset
  # ============================================================================

  Scenario: Reset failed container for restart
    Given a container in "FAILED" state with restart_count 2
    When I reset container for restart
    Then the container status should be "PENDING"
    And the container restart count should be 3
    And the container last_error should be nil
    And the container stopped_at should be nil

  Scenario: Cannot reset container at max restarts
    Given a container in "FAILED" state with restart_count 3
    When I attempt to reset container for restart
    Then the container operation should fail with error "container cannot be restarted (restarts: 3/3)"

  Scenario: Cannot reset running container
    Given a container in "RUNNING" state with restart_count 0
    When I attempt to reset container for restart
    Then the container operation should fail with error "container cannot be restarted (restarts: 0/3)"

  Scenario: Reset preserves container identity
    Given a container with id "mining-1" in "FAILED" state with restart_count 1
    When I reset container for restart
    Then the container id should be "mining-1"
    And the container type should be "MINING"
    And the container player_id should be 1

  # ============================================================================
  # Metadata Management
  # ============================================================================

  Scenario: Update metadata adds new keys
    Given a container with no metadata
    When I update container metadata with:
      | key      | value        |
      | location | X1-A1-FIELD  |
      | priority | high         |
    Then the container metadata should contain "location" with value "X1-A1-FIELD"
    And the container metadata should contain "priority" with value "high"

  Scenario: Update metadata merges with existing
    Given a container with metadata:
      | key      | value        |
      | location | X1-A1-FIELD  |
    When I update container metadata with:
      | key      | value   |
      | priority | high    |
    Then the container metadata should contain "location" with value "X1-A1-FIELD"
    And the container metadata should contain "priority" with value "high"

  Scenario: Update metadata overwrites existing keys
    Given a container with metadata:
      | key      | value        |
      | location | X1-A1-FIELD  |
    When I update container metadata with:
      | key      | value        |
      | location | X1-B2-FIELD  |
    Then the container metadata should contain "location" with value "X1-B2-FIELD"

  Scenario: Get metadata value for existing key
    Given a container with metadata:
      | key      | value        |
      | location | X1-A1-FIELD  |
    When I get container metadata value for key "location"
    Then the metadata value should be "X1-A1-FIELD"
    And the metadata key should exist

  Scenario: Get metadata value for non-existent key
    Given a container with no metadata
    When I get container metadata value for key "missing"
    Then the metadata key should not exist

  Scenario: Metadata persists through state transitions
    Given a container with metadata:
      | key      | value        |
      | location | X1-A1-FIELD  |
    When I start the container
    And I complete the container
    Then the container metadata should contain "location" with value "X1-A1-FIELD"

  # ============================================================================
  # Runtime Calculation
  # ============================================================================

  Scenario: Runtime duration is zero when not started
    Given a container in "PENDING" state
    When I calculate container runtime duration
    Then the duration should be 0 seconds

  Scenario: Runtime duration calculates elapsed time while running
    Given a container started 5 minutes ago in "RUNNING" state
    When I calculate container runtime duration
    Then the duration should be approximately 300 seconds

  Scenario: Runtime duration uses stopped time for finished container
    Given a container that ran for 10 minutes and is now "COMPLETED"
    When I calculate container runtime duration
    Then the duration should be approximately 600 seconds

  Scenario: Runtime duration uses stopped time for failed container
    Given a container that ran for 7 minutes and is now "FAILED"
    When I calculate container runtime duration
    Then the duration should be approximately 420 seconds

  Scenario: Runtime duration uses stopped time for stopped container
    Given a container that ran for 3 minutes and is now "STOPPED"
    When I calculate container runtime duration
    Then the duration should be approximately 180 seconds

  # ============================================================================
  # State Query Methods
  # ============================================================================

  Scenario: IsRunning returns true for running container
    Given a container in "RUNNING" state
    When I check if container is running
    Then the container is running

  Scenario: IsRunning returns false for pending container
    Given a container in "PENDING" state
    When I check if container is running
    Then the container is not running

  Scenario: IsRunning returns false for completed container
    Given a container in "COMPLETED" state
    When I check if container is running
    Then the container is not running

  Scenario: IsFinished returns true for completed container
    Given a container in "COMPLETED" state
    When I check if container is finished
    Then the container is finished

  Scenario: IsFinished returns true for failed container
    Given a container in "FAILED" state
    When I check if container is finished
    Then the container is finished

  Scenario: IsFinished returns true for stopped container
    Given a container in "STOPPED" state
    When I check if container is finished
    Then the container is finished

  Scenario: IsFinished returns false for running container
    Given a container in "RUNNING" state
    When I check if container is finished
    Then the container is not finished

  Scenario: IsFinished returns false for pending container
    Given a container in "PENDING" state
    When I check if container is finished
    Then the container is not finished

  Scenario: IsStopping returns true for stopping container
    Given a container in "STOPPING" state
    When I check if container is stopping
    Then the container is stopping

  Scenario: IsStopping returns false for running container
    Given a container in "RUNNING" state
    When I check if container is stopping
    Then the container is not stopping

Feature: Container Entity
  As a daemon orchestrator
  I want to manage container lifecycle and state transitions
  So that background operations can be tracked and controlled

  # ============================================================================
  # Container Initialization Tests
  # ============================================================================

  Scenario: Create container with valid data
    When I create a container with:
      | id             | container-1 |
      | type           | NAVIGATE    |
      | player_id      | 1           |
      | max_iterations | 10          |
    Then the container should have id "container-1"
    And the container should have type "NAVIGATE"
    And the container should have player_id 1
    And the container should have max_iterations 10
    And the container should have status "PENDING"
    And the container current_iteration should be 0
    And the container restart_count should be 0

  Scenario: Create container with infinite iterations
    When I create a container with max_iterations -1
    Then the container should have max_iterations -1
    And the container should continue running

  Scenario: Create container with metadata
    When I create a container with metadata:
      | ship_symbol | SHIP-1    |
      | destination | X1-B2     |
    Then the container metadata should contain "ship_symbol" with value "SHIP-1"
    And the container metadata should contain "destination" with value "X1-B2"

  # ============================================================================
  # Container State Transition Tests
  # ============================================================================

  Scenario: Start container from pending
    Given a container in "PENDING" status
    When I start the container
    Then the container should have status "RUNNING"
    And the container started_at should be set

  Scenario: Start container from stopped
    Given a container in "STOPPED" status
    When I start the container
    Then the container should have status "RUNNING"
    And the container started_at should be set

  Scenario: Cannot start container from running
    Given a container in "RUNNING" status
    When I attempt to start the container
    Then the operation should fail with error "cannot start container in RUNNING state"

  Scenario: Cannot start container from completed
    Given a container in "COMPLETED" status
    When I attempt to start the container
    Then the operation should fail with error "cannot start container in COMPLETED state"

  Scenario: Complete container from running
    Given a container in "RUNNING" status
    When I complete the container
    Then the container should have status "COMPLETED"
    And the container stopped_at should be set

  Scenario: Cannot complete container from pending
    Given a container in "PENDING" status
    When I attempt to complete the container
    Then the operation should fail with error "cannot complete container in PENDING state"

  Scenario: Fail container from running
    Given a container in "RUNNING" status
    When I fail the container with error "Test error"
    Then the container should have status "FAILED"
    And the container stopped_at should be set
    And the container last_error should be "Test error"

  Scenario: Cannot fail container from completed
    Given a container in "COMPLETED" status
    When I attempt to fail the container
    Then the operation should fail with error "cannot fail container in COMPLETED state"

  Scenario: Stop container from running transitions to stopping
    Given a container in "RUNNING" status
    When I stop the container
    Then the container should have status "STOPPING"

  Scenario: Mark stopped finalizes stop transition
    Given a container in "STOPPING" status
    When I mark the container as stopped
    Then the container should have status "STOPPED"
    And the container stopped_at should be set

  Scenario: Cannot mark stopped when not stopping
    Given a container in "RUNNING" status
    When I attempt to mark the container as stopped
    Then the operation should fail with error "cannot mark stopped when not in stopping state"

  Scenario: Cannot stop already completed container
    Given a container in "COMPLETED" status
    When I attempt to stop the container
    Then the operation should fail with error "cannot stop container in COMPLETED state"

  # ============================================================================
  # Iteration Management Tests
  # ============================================================================

  Scenario: Increment iteration advances counter
    Given a container in "RUNNING" status with current_iteration 0
    When I increment the iteration
    Then the container current_iteration should be 1

  Scenario: Cannot increment iteration when not running
    Given a container in "PENDING" status
    When I attempt to increment the iteration
    Then the operation should fail with error "cannot increment iteration in PENDING state"

  Scenario: Should continue with infinite iterations
    Given a container with max_iterations -1 and current_iteration 100
    When I check if the container should continue
    Then the result should be true

  Scenario: Should continue with remaining iterations
    Given a container with max_iterations 10 and current_iteration 5
    When I check if the container should continue
    Then the result should be true

  Scenario: Should not continue when iterations exhausted
    Given a container with max_iterations 10 and current_iteration 10
    When I check if the container should continue
    Then the result should be false

  Scenario: Multiple iterations in sequence
    Given a container in "RUNNING" status with max_iterations 3
    When I increment the iteration
    And I increment the iteration
    And I increment the iteration
    Then the container current_iteration should be 3
    And the container should not continue running

  # ============================================================================
  # Restart Management Tests
  # ============================================================================

  Scenario: Failed container can be restarted
    Given a container in "FAILED" status with restart_count 0
    When I check if the container can restart
    Then the result should be true

  Scenario: Container at max restarts cannot be restarted
    Given a container in "FAILED" status with restart_count 3
    When I check if the container can restart
    Then the result should be false

  Scenario: Running container cannot be restarted
    Given a container in "RUNNING" status
    When I check if the container can restart
    Then the result should be false

  Scenario: Reset container for restart
    Given a container in "FAILED" status with restart_count 1
    When I reset the container for restart
    Then the container should have status "PENDING"
    And the container restart_count should be 2
    And the container last_error should be nil
    And the container stopped_at should be nil

  Scenario: Cannot reset container exceeding restart limit
    Given a container in "FAILED" status with restart_count 3
    When I attempt to reset the container for restart
    Then the operation should fail with error "container cannot be restarted"

  # ============================================================================
  # Metadata Management Tests
  # ============================================================================

  Scenario: Update metadata adds new keys
    Given a container with metadata:
      | ship_symbol | SHIP-1 |
    When I update metadata with:
      | destination | X1-B2 |
    Then the container metadata should contain "ship_symbol" with value "SHIP-1"
    And the container metadata should contain "destination" with value "X1-B2"

  Scenario: Update metadata overwrites existing keys
    Given a container with metadata:
      | ship_symbol | SHIP-1 |
    When I update metadata with:
      | ship_symbol | SHIP-2 |
    Then the container metadata should contain "ship_symbol" with value "SHIP-2"

  Scenario: Get metadata value returns value if exists
    Given a container with metadata:
      | ship_symbol | SHIP-1 |
    When I get metadata value for key "ship_symbol"
    Then the metadata value should be "SHIP-1"
    And the metadata key should exist

  Scenario: Get metadata value returns false if not exists
    Given a container with empty metadata
    When I get metadata value for key "nonexistent"
    Then the metadata key should not exist

  # ============================================================================
  # State Query Tests
  # ============================================================================

  Scenario: Is running returns true when running
    Given a container in "RUNNING" status
    When I check if the container is running
    Then the result should be true

  Scenario: Is running returns false when pending
    Given a container in "PENDING" status
    When I check if the container is running
    Then the result should be false

  Scenario: Is finished returns true when completed
    Given a container in "COMPLETED" status
    When I check if the container is finished
    Then the result should be true

  Scenario: Is finished returns true when failed
    Given a container in "FAILED" status
    When I check if the container is finished
    Then the result should be true

  Scenario: Is finished returns true when stopped
    Given a container in "STOPPED" status
    When I check if the container is finished
    Then the result should be true

  Scenario: Is finished returns false when running
    Given a container in "RUNNING" status
    When I check if the container is finished
    Then the result should be false

  Scenario: Is stopping returns true when stopping
    Given a container in "STOPPING" status
    When I check if the container is stopping
    Then the result should be true

  Scenario: Is stopping returns false when running
    Given a container in "RUNNING" status
    When I check if the container is stopping
    Then the result should be false

  # ============================================================================
  # Runtime Calculation Tests
  # ============================================================================

  Scenario: Runtime duration returns zero when not started
    Given a container that has not been started
    When I calculate the runtime duration
    Then the runtime duration should be 0 seconds

  Scenario: Runtime duration calculates correctly for running container
    Given a container that started 10 seconds ago
    When I calculate the runtime duration
    Then the runtime duration should be approximately 10 seconds

  Scenario: Runtime duration uses stopped time when stopped
    Given a container that started 10 seconds ago and stopped 5 seconds later
    When I calculate the runtime duration
    Then the runtime duration should be 5 seconds

Feature: Mining Operation Entity
  As a SpaceTraders mining coordinator
  I want to manage mining operations with proper state transitions and validation
  So that I can orchestrate mining and transport workflows safely

  # ============================================================================
  # Mining Operation Creation Tests
  # ============================================================================

  Scenario: Create mining operation in pending state
    When I create a mining operation with:
      | id              | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1       | 1         | X1-A1-FIELD    | 3          | 5               | 300           | 10             |
    And I add miner ships "MINER-1,MINER-2,MINER-3"
    And I add transport ships "TRANSPORT-1,TRANSPORT-2"
    Then the mining operation status should be "PENDING"
    And the mining operation should have 3 miner ships
    And the mining operation should have 2 transport ships
    And the mining operation asteroid field should be "X1-A1-FIELD"
    And the mining operation top N ores should be 3
    And the mining operation batch threshold should be 5
    And the mining operation batch timeout should be 300
    And the mining operation max iterations should be 10
    And the mining operation started_at should be nil
    And the mining operation stopped_at should be nil

  Scenario: Create mining operation with infinite iterations
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 5          | 3               | 600           | -1             |
    And I add miner ships "MINER-1"
    And I add transport ships "TRANSPORT-1"
    Then the mining operation max iterations should be -1

  # ============================================================================
  # State Machine: Start Transition
  # ============================================================================

  Scenario: Start operation transitions pending to running
    Given a mining operation in "PENDING" state
    When I start the mining operation
    Then the mining operation status should be "RUNNING"
    And the mining operation started_at should not be nil
    And the mining operation stopped_at should be nil

  Scenario: Cannot start operation already running
    Given a mining operation in "RUNNING" state
    When I attempt to start the mining operation
    Then the mining operation should fail with error "cannot start operation in RUNNING state"

  Scenario: Cannot start completed operation
    Given a mining operation in "COMPLETED" state
    When I attempt to start the mining operation
    Then the mining operation should fail with error "cannot start operation in COMPLETED state"

  Scenario: Cannot start stopped operation
    Given a mining operation in "STOPPED" state
    When I attempt to start the mining operation
    Then the mining operation should fail with error "cannot start operation in STOPPED state"

  Scenario: Cannot start failed operation
    Given a mining operation in "FAILED" state
    When I attempt to start the mining operation
    Then the mining operation should fail with error "cannot start operation in FAILED state"

  # ============================================================================
  # State Machine: Complete Transition
  # ============================================================================

  Scenario: Complete operation from running state
    Given a mining operation in "RUNNING" state
    When I complete the mining operation
    Then the mining operation status should be "COMPLETED"
    And the mining operation stopped_at should not be nil

  Scenario: Cannot complete operation in pending state
    Given a mining operation in "PENDING" state
    When I attempt to complete the mining operation
    Then the mining operation should fail with error "cannot complete operation in PENDING state"

  Scenario: Cannot complete already completed operation
    Given a mining operation in "COMPLETED" state
    When I attempt to complete the mining operation
    Then the mining operation should fail with error "cannot complete operation in COMPLETED state"

  Scenario: Cannot complete stopped operation
    Given a mining operation in "STOPPED" state
    When I attempt to complete the mining operation
    Then the mining operation should fail with error "cannot complete operation in STOPPED state"

  Scenario: Cannot complete failed operation
    Given a mining operation in "FAILED" state
    When I attempt to complete the mining operation
    Then the mining operation should fail with error "cannot complete operation in FAILED state"

  # ============================================================================
  # State Machine: Fail Transition
  # ============================================================================

  Scenario: Fail operation from running state
    Given a mining operation in "RUNNING" state
    When I fail the mining operation with error "extraction failed"
    Then the mining operation status should be "FAILED"
    And the mining operation stopped_at should not be nil
    And the mining operation last_error should be "extraction failed"

  Scenario: Fail operation from pending state
    Given a mining operation in "PENDING" state
    When I fail the mining operation with error "initialization failed"
    Then the mining operation status should be "FAILED"
    And the mining operation last_error should be "initialization failed"

  Scenario: Cannot fail completed operation
    Given a mining operation in "COMPLETED" state
    When I attempt to fail the mining operation with error "test error"
    Then the mining operation should fail with error "cannot fail operation in COMPLETED state"

  Scenario: Cannot fail stopped operation
    Given a mining operation in "STOPPED" state
    When I attempt to fail the mining operation with error "test error"
    Then the mining operation should fail with error "cannot fail operation in STOPPED state"

  # ============================================================================
  # State Machine: Stop Transition
  # ============================================================================

  Scenario: Stop operation from pending state
    Given a mining operation in "PENDING" state
    When I stop the mining operation
    Then the mining operation status should be "STOPPED"
    And the mining operation stopped_at should not be nil

  Scenario: Stop operation from running state
    Given a mining operation in "RUNNING" state
    When I stop the mining operation
    Then the mining operation status should be "STOPPED"
    And the mining operation stopped_at should not be nil

  Scenario: Stop operation from failed state
    Given a mining operation in "FAILED" state
    When I stop the mining operation
    Then the mining operation status should be "STOPPED"

  Scenario: Cannot stop completed operation
    Given a mining operation in "COMPLETED" state
    When I attempt to stop the mining operation
    Then the mining operation should fail with error "cannot stop operation in COMPLETED state"

  Scenario: Cannot stop already stopped operation
    Given a mining operation in "STOPPED" state
    When I attempt to stop the mining operation
    Then the mining operation should fail with error "cannot stop operation in STOPPED state"

  # ============================================================================
  # Validation Tests
  # ============================================================================

  Scenario: Operation requires at least one miner ship
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | 5               | 300           | 10             |
    And I add transport ships "TRANSPORT-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: operation must have at least 1 miner ship"

  Scenario: Operation requires at least one transport ship
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | 5               | 300           | 10             |
    And I add miner ships "MINER-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: operation must have at least 1 transport ship"

  Scenario: Operation requires topNOres >= 1
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 0          | 5               | 300           | 10             |
    And I add miner ships "MINER-1"
    And I add transport ships "TRANSPORT-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: topNOres must be >= 1, got 0"

  Scenario: Operation requires asteroid field waypoint
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         |                | 3          | 5               | 300           | 10             |
    And I add miner ships "MINER-1"
    And I add transport ships "TRANSPORT-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: asteroid field waypoint must be specified"

  Scenario: Operation validates batch threshold is non-negative
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | -1              | 300           | 10             |
    And I add miner ships "MINER-1"
    And I add transport ships "TRANSPORT-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: batchThreshold must be >= 0, got -1"

  Scenario: Operation validates batch timeout is non-negative
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | 5               | -1            | 10             |
    And I add miner ships "MINER-1"
    And I add transport ships "TRANSPORT-1"
    When I attempt to start the mining operation
    Then the mining operation should fail with error "operation validation failed: batchTimeout must be >= 0, got -1"

  # ============================================================================
  # Has Methods Tests
  # ============================================================================

  Scenario: HasMiners returns true when miner ships exist
    Given a mining operation in "PENDING" state
    Then the mining operation should have miners

  Scenario: HasMiners returns false when no miner ships
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | 5               | 300           | 10             |
    And I add transport ships "TRANSPORT-1"
    Then the mining operation should not have miners

  Scenario: HasTransports returns true when transport ships exist
    Given a mining operation in "PENDING" state
    Then the mining operation should have transports

  Scenario: HasTransports returns false when no transport ships
    When I create a mining operation with:
      | id        | player_id | asteroid_field | top_n_ores | batch_threshold | batch_timeout | max_iterations |
      | mine-op-1 | 1         | X1-A1-FIELD    | 3          | 5               | 300           | 10             |
    And I add miner ships "MINER-1"
    Then the mining operation should not have transports

  # ============================================================================
  # State Query Tests
  # ============================================================================

  Scenario: IsRunning returns true for running operation
    Given a mining operation in "RUNNING" state
    Then the mining operation IsRunning should be true

  Scenario: IsRunning returns false for pending operation
    Given a mining operation in "PENDING" state
    Then the mining operation IsRunning should be false

  Scenario: IsRunning returns false for completed operation
    Given a mining operation in "COMPLETED" state
    Then the mining operation IsRunning should be false

  Scenario: IsPending returns true for pending operation
    Given a mining operation in "PENDING" state
    Then the mining operation IsPending should be true

  Scenario: IsPending returns false for running operation
    Given a mining operation in "RUNNING" state
    Then the mining operation IsPending should be false

  Scenario: IsFinished returns false for running operation
    Given a mining operation in "RUNNING" state
    Then the mining operation IsFinished should be false

  Scenario: IsFinished returns true for completed operation
    Given a mining operation in "COMPLETED" state
    Then the mining operation IsFinished should be true

  Scenario: IsFinished returns true for failed operation
    Given a mining operation in "FAILED" state
    Then the mining operation IsFinished should be true

  Scenario: IsFinished returns true for stopped operation
    Given a mining operation in "STOPPED" state
    Then the mining operation IsFinished should be true

  # ============================================================================
  # Runtime Duration Tests
  # ============================================================================

  Scenario: RuntimeDuration is zero for pending operation
    Given a mining operation in "PENDING" state
    Then the mining operation runtime duration should be 0 seconds

  Scenario: RuntimeDuration calculates elapsed time for running operation
    Given a mining operation in "RUNNING" state
    When I advance time by 300 seconds
    Then the mining operation runtime duration should be 300 seconds

  Scenario: RuntimeDuration is fixed after operation completes
    Given a mining operation in "RUNNING" state
    When I advance time by 120 seconds
    And I complete the mining operation
    When I advance time by 180 seconds
    Then the mining operation runtime duration should be 120 seconds

  Scenario: RuntimeDuration is fixed after operation fails
    Given a mining operation in "RUNNING" state
    When I advance time by 60 seconds
    And I fail the mining operation with error "test error"
    When I advance time by 240 seconds
    Then the mining operation runtime duration should be 60 seconds

  # ============================================================================
  # DTO Conversion Tests
  # ============================================================================

  Scenario: ToData converts operation to DTO
    Given a mining operation in "RUNNING" state
    When I convert the mining operation to data
    Then the operation data should have id "test-operation"
    And the operation data should have status "RUNNING"
    And the operation data should have player_id 1
    And the operation data should have asteroid field "X1-A1-FIELD"

  Scenario: FromData reconstructs operation from DTO
    Given a mining operation in "RUNNING" state
    When I convert the mining operation to data
    And I reconstruct the mining operation from data
    Then the mining operation status should be "RUNNING"
    And the mining operation should have 2 miner ships
    And the mining operation should have 1 transport ships

  Scenario: FromData preserves error state
    Given a mining operation in "FAILED" state
    When I convert the mining operation to data
    And I reconstruct the mining operation from data
    Then the mining operation status should be "FAILED"
    And the mining operation last_error should be "test error"

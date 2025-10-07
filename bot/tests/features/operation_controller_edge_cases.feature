Feature: Operation Controller Edge Cases
  As a bot operator
  I want checkpoint/resume to handle edge cases
  So that operations are resilient to failures

  Scenario: Corrupted state file is handled
    Given an operation state file exists with corrupted JSON
    When I load the operation controller
    Then it should initialize with default state
    And no crash should occur

  Scenario: Resume without any checkpoints
    Given an operation "test-001" is running
    And no checkpoints have been saved
    When I attempt to resume the operation
    Then resume should return None
    And can_resume should be False

  Scenario: Resume from completed operation fails
    Given an operation "test-002" completed successfully
    And checkpoints exist from the operation
    When I attempt to resume the operation
    Then can_resume should be False
    And resume should return None

  Scenario: Pause then resume operation
    Given an operation "test-003" is running
    And the operation has checkpoint at step 5
    When I pause the operation
    Then the status should be "paused"
    When I resume the operation
    Then the status should be "running"
    And I should resume from step 5

  Scenario: Cancel operation cannot resume
    Given an operation "test-004" is running
    And the operation has checkpoint at step 3
    When I cancel the operation
    Then the status should be "cancelled"
    And can_resume should be False

  Scenario: Failed operation records error
    Given an operation "test-005" is running
    When the operation fails with error "Something went wrong"
    Then the status should be "failed"
    And the error should be "Something went wrong"
    And failed_at timestamp should be set

  Scenario: Control command on nonexistent operation
    When I send pause command to "nonexistent-op"
    Then the command should return False
    And no error should occur

  Scenario: Rapid concurrent checkpoints
    Given an operation "test-006" is running
    When I save 100 checkpoints rapidly
    Then all 100 checkpoints should be saved
    And no data should be lost

  Scenario: Checkpoint with large data
    Given an operation "test-007" is running
    When I save a checkpoint with 100KB of data
    Then the checkpoint should save successfully
    And I should be able to load it

  Scenario: Multiple operations for same ship
    Given operation "mine_SHIP1_001" is running for ship "SHIP-1"
    And operation "trade_SHIP1_001" is running for ship "SHIP-1"
    When I list all operations
    Then I should see 2 operations
    And both should be for ship "SHIP-1"

  Scenario: Cleanup removes state file
    Given an operation "test-008" is completed
    And a state file exists for the operation
    When I cleanup the operation
    Then the state file should be removed
    And the operation should not be listed

  Scenario: Progress calculation with duration
    Given an operation "test-009" started 10 seconds ago
    When I get the operation progress
    Then duration_seconds should be approximately 10
    And the status should be included

  Scenario: List operations sorted by update time
    Given operation "op-1" was updated 3 seconds ago
    And operation "op-2" was updated 1 second ago
    And operation "op-3" was updated 2 seconds ago
    When I list all operations
    Then the order should be "op-2", "op-3", "op-1"

  Scenario: Simultaneous control commands
    Given an operation "test-010" is running
    When I send "pause" command
    And I send "cancel" command immediately after
    Then the last command should win
    And the control_command should be "cancel"

  Scenario: Various checkpoint data types preserved
    Given an operation "test-011" is running
    When I save a checkpoint with:
      | type   | value           |
      | string | "test"          |
      | int    | 123             |
      | float  | 45.67           |
      | bool   | true            |
      | null   | null            |
      | list   | [1, 2, 3]       |
      | dict   | {"key": "val"}  |
    Then all data types should be preserved correctly

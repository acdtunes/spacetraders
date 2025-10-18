Feature: Operation Controller and Checkpoint Management
  As a bot operator
  I want the operation controller to reliably save and restore checkpoints
  So that long-running operations can be paused and resumed without data loss

  Background:
    Given a temporary state directory
    And a mock environment with ship "TEST-SHIP-1" at "X1-TEST-A1"

  Scenario: Checkpoint contains actual navigation state
    Given an operation controller with ID "NAV-001"
    And the operation is started with ship "TEST-SHIP-1" to "X1-TEST-A2"
    When I save a checkpoint with:
      | field          | value      |
      | completed_step | 1          |
      | location       | X1-TEST-A1 |
      | fuel           | 400        |
      | state          | IN_ORBIT   |
    Then the checkpoint should be saved to the operation state
    And the checkpoint data should match the saved values
    And the checkpoint should have a timestamp

  Scenario: Multiple checkpoints track navigation progress
    Given an operation controller with ID "NAV-002"
    And the operation is started with ship "TEST-SHIP-1"
    When I save checkpoints:
      | completed_step | location   | fuel | state     |
      | 1              | X1-TEST-A1 | 400  | IN_ORBIT  |
      | 2              | X1-TEST-A2 | 300  | IN_ORBIT  |
      | 3              | X1-TEST-A3 | 200  | DOCKED    |
    Then there should be 3 checkpoints saved
    And the checkpoint step numbers should increment from 1 to 3
    And the checkpoint locations should progress: X1-TEST-A1, X1-TEST-A2, X1-TEST-A3
    And the checkpoint fuel values should decrease: 400, 300, 200

  Scenario: Resume loads actual checkpoint data
    Given an operation controller with ID "NAV-003"
    And the operation is started with ship "TEST-SHIP-1"
    And a checkpoint is saved with step 2 at "X1-TEST-A2" with 250 fuel
    And the operation is paused
    Then the operation should be resumable
    When I resume the operation
    Then the resumed data should have step 2
    And the resumed data should have location "X1-TEST-A2"
    And the resumed data should have 250 fuel
    And the resumed data should have state "IN_ORBIT"

  Scenario: Pause signal preserves state
    Given an operation controller with ID "NAV-004"
    And the operation is started with ship "TEST-SHIP-1"
    And a checkpoint is saved at step 1
    When an external pause command is sent
    Then the operation should detect the pause signal
    When the operation is paused
    Then the operation status should be "paused"
    And the checkpoint should be preserved

  Scenario: Cancel signal changes state
    Given an operation controller with ID "NAV-005"
    And the operation is started with ship "TEST-SHIP-1"
    And a checkpoint is saved at step 1
    When an external cancel command is sent
    Then the operation should detect the cancel signal
    When the operation is cancelled
    Then the operation status should be "cancelled"
    And the operation should not be resumable

  Scenario: Checkpoint persisted to disk
    Given an operation controller with ID "NAV-006"
    And the operation is started with ship "TEST-SHIP-1"
    When I save a checkpoint at step 1 with location "X1-TEST-A2"
    Then the state file should exist on disk
    When I create a new operation controller instance with ID "NAV-006"
    Then the checkpoint should be loaded from disk
    And the loaded checkpoint should have location "X1-TEST-A2"

  Scenario: Refuel checkpoint has correct state
    Given an operation controller with ID "NAV-007"
    And the operation is started with ship "TEST-SHIP-1"
    When I save a navigation checkpoint at step 1 with 50 fuel in orbit
    And I save a refuel checkpoint at step 2 with 400 fuel docked
    Then there should be 2 checkpoints
    And the second checkpoint should have state "DOCKED"
    And the second checkpoint fuel should be greater than the first
    And both checkpoints should have the same location

  Scenario: Progress metrics track checkpoint count
    Given an operation controller with ID "NAV-008"
    And the operation is started with ship "TEST-SHIP-1"
    When I save 3 checkpoints
    And I get the operation progress
    Then the progress should show 3 checkpoints
    And the progress should include the last checkpoint
    And the last checkpoint should be at step 3

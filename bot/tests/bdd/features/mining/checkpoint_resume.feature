Feature: Operation Checkpoint and Resume
  As a bot operator
  I want operations to checkpoint progress automatically
  So that they can resume after crashes without losing work

  Scenario: Mining operation saves checkpoints
    Given a mining operation for ship "TEST-1" with 10 cycles
    And the operation starts successfully
    When the operation completes cycle 5
    Then a checkpoint should be saved with cycle 5 data
    And the checkpoint should contain mining statistics

  Scenario: Mining operation resumes from checkpoint
    Given a mining operation checkpoint exists at cycle 5 with stats:
      | cycles_completed | total_revenue |
      | 5                | 25000         |
    When I start the mining operation
    Then the operation should resume from cycle 6
    And the previous statistics should be loaded
    And the operation should continue to cycle 10

  Scenario: Operation responds to pause command
    Given a mining operation is running at cycle 3
    When a pause command is sent
    Then the operation should pause gracefully
    And the current checkpoint should be saved
    And the operation status should be "paused"

  Scenario: Operation responds to cancel command
    Given a mining operation is running at cycle 7
    When a cancel command is sent
    Then the operation should stop immediately
    And the final checkpoint should be saved
    And the operation status should be "cancelled"

  Scenario: Navigation checkpoints each step
    Given a ship navigating a multi-hop route with 3 waypoints
    When navigation completes step 2
    Then a checkpoint should be saved with:
      | completed_step | location   |
      | 2              | X1-HU87-B7 |
    And if the operation crashes and restarts
    Then navigation should resume from step 3

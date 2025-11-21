Feature: Lifecycle State Machine
  As a domain entity with lifecycle management
  I want to use a reusable state machine for state transitions
  So that state management is consistent and reduces code duplication

  # State Machine Creation
  Scenario: Create new lifecycle state machine
    When I create a new lifecycle state machine
    Then the lifecycle status should be "PENDING"
    And the state machine should have a created timestamp
    And the state machine should have an updated timestamp
    And the started timestamp should be nil
    And the stopped timestamp should be nil

  # Start Transition
  Scenario: Start from PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I start the lifecycle state machine
    Then the lifecycle status should be "RUNNING"
    And the started timestamp should be set
    And the updated timestamp should be updated

  Scenario: Cannot start from RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I start the lifecycle state machine
    Then the transition should fail with "cannot start from RUNNING state"

  Scenario: Cannot start from COMPLETED state
    Given a lifecycle state machine in "COMPLETED" state
    When I start the lifecycle state machine
    Then the transition should fail with "cannot start from COMPLETED state"

  Scenario: Cannot start from FAILED state
    Given a lifecycle state machine in "FAILED" state
    When I start the lifecycle state machine
    Then the transition should fail with "cannot start from FAILED state"

  Scenario: Restart from STOPPED state
    Given a lifecycle state machine in "STOPPED" state
    When I start the lifecycle state machine
    Then the lifecycle status should be "RUNNING"
    And the started timestamp should be set

  # Complete Transition
  Scenario: Complete from RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I complete the lifecycle state machine
    Then the lifecycle status should be "COMPLETED"
    And the stopped timestamp should be set
    And the updated timestamp should be updated

  Scenario: Cannot complete from PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I complete the lifecycle state machine
    Then the transition should fail with "cannot complete from PENDING state"

  Scenario: Cannot complete from STOPPED state
    Given a lifecycle state machine in "STOPPED" state
    When I complete the lifecycle state machine
    Then the transition should fail with "cannot complete from STOPPED state"

  # Fail Transition
  Scenario: Fail from PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I fail the lifecycle state machine with error "connection timeout"
    Then the lifecycle status should be "FAILED"
    And the stopped timestamp should be set
    And the last error should be "connection timeout"

  Scenario: Fail from RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I fail the lifecycle state machine with error "out of memory"
    Then the lifecycle status should be "FAILED"
    And the stopped timestamp should be set
    And the last error should be "out of memory"

  Scenario: Cannot fail from COMPLETED state
    Given a lifecycle state machine in "COMPLETED" state
    When I fail the lifecycle state machine with error "network error"
    Then the transition should fail with "cannot fail from COMPLETED state"

  Scenario: Cannot fail from STOPPED state
    Given a lifecycle state machine in "STOPPED" state
    When I fail the lifecycle state machine with error "disk full"
    Then the transition should fail with "cannot fail from STOPPED state"

  # Stop Transition
  Scenario: Stop from PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I stop the lifecycle state machine
    Then the lifecycle status should be "STOPPED"
    And the stopped timestamp should be set

  Scenario: Stop from RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I stop the lifecycle state machine
    Then the lifecycle status should be "STOPPED"
    And the stopped timestamp should be set

  Scenario: Stop from FAILED state
    Given a lifecycle state machine in "FAILED" state
    When I stop the lifecycle state machine
    Then the lifecycle status should be "STOPPED"

  Scenario: Cannot stop from COMPLETED state
    Given a lifecycle state machine in "COMPLETED" state
    When I stop the lifecycle state machine
    Then the transition should fail with "cannot stop from COMPLETED state"

  Scenario: Cannot stop from STOPPED state
    Given a lifecycle state machine in "STOPPED" state
    When I stop the lifecycle state machine
    Then the transition should fail with "cannot stop from STOPPED state"

  # State Queries
  Scenario: IsRunning returns true for RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I check if the state machine is running
    Then the running check should return true

  Scenario: IsRunning returns false for non-RUNNING states
    Given a lifecycle state machine in "PENDING" state
    When I check if the state machine is running
    Then the running check should return false

  Scenario: IsPending returns true for PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I check if the state machine is pending
    Then the pending check should return true

  Scenario: IsPending returns false for non-PENDING states
    Given a lifecycle state machine in "RUNNING" state
    When I check if the state machine is pending
    Then the pending check should return false

  Scenario: IsFinished returns false for PENDING state
    Given a lifecycle state machine in "PENDING" state
    When I check if the state machine is finished
    Then the finished check should return false

  Scenario: IsFinished returns false for RUNNING state
    Given a lifecycle state machine in "RUNNING" state
    When I check if the state machine is finished
    Then the finished check should return false

  Scenario: IsFinished returns true for COMPLETED state
    Given a lifecycle state machine in "COMPLETED" state
    When I check if the state machine is finished
    Then the finished check should return true

  Scenario: IsFinished returns true for FAILED state
    Given a lifecycle state machine in "FAILED" state
    When I check if the state machine is finished
    Then the finished check should return true

  Scenario: IsFinished returns true for STOPPED state
    Given a lifecycle state machine in "STOPPED" state
    When I check if the state machine is finished
    Then the finished check should return true

  # Runtime Duration
  Scenario: RuntimeDuration returns zero before start
    Given a lifecycle state machine in "PENDING" state
    When I get the runtime duration
    Then the runtime duration should be 0 seconds

  Scenario: RuntimeDuration calculates elapsed time while running
    Given a lifecycle state machine in "RUNNING" state
    And 5 seconds have passed
    When I get the runtime duration
    Then the runtime duration should be 5 seconds

  Scenario: RuntimeDuration calculates total time after completion
    Given a lifecycle state machine that ran for 10 seconds and completed
    When I get the runtime duration
    Then the runtime duration should be 10 seconds

  Scenario: RuntimeDuration calculates total time after failure
    Given a lifecycle state machine that ran for 7 seconds and failed
    When I get the runtime duration
    Then the runtime duration should be 7 seconds

  Scenario: RuntimeDuration calculates total time after stop
    Given a lifecycle state machine that ran for 3 seconds and stopped
    When I get the runtime duration
    Then the runtime duration should be 3 seconds

  # Complete Lifecycle Flow
  Scenario: Complete lifecycle from PENDING to COMPLETED
    Given a lifecycle state machine in "PENDING" state
    When I start the lifecycle state machine
    And I complete the lifecycle state machine
    Then the lifecycle status should be "COMPLETED"
    And the started timestamp should be set
    And the stopped timestamp should be set
    And the state machine should be finished

  Scenario: Complete lifecycle from PENDING to FAILED
    Given a lifecycle state machine in "PENDING" state
    When I start the lifecycle state machine
    And I fail the lifecycle state machine with error "database error"
    Then the lifecycle status should be "FAILED"
    And the last error should be "database error"
    And the state machine should be finished

  Scenario: Stopped before starting
    Given a lifecycle state machine in "PENDING" state
    When I stop the lifecycle state machine
    Then the lifecycle status should be "STOPPED"
    And the started timestamp should be nil
    And the stopped timestamp should be set

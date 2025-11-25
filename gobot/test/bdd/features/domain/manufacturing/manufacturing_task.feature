Feature: Manufacturing Task
  As the manufacturing system
  I need to manage manufacturing tasks
  So that I can track atomic work units in the pipeline

  Background:
    Given a manufacturing task context

  # Task Creation
  Scenario: Create ACQUIRE task
    When I create an ACQUIRE task for "IRON_ORE" from market "X1-AU21-A1"
    Then the task should have type "ACQUIRE"
    And the task should have good "IRON_ORE"
    And the task should have source market "X1-AU21-A1"
    And the task should have status "PENDING"

  Scenario: Create DELIVER task
    When I create a DELIVER task for "IRON_ORE" to market "X1-AU21-B2" with dependencies
    Then the task should have type "DELIVER"
    And the task should have good "IRON_ORE"
    And the task should have target market "X1-AU21-B2"
    And the task should have status "PENDING"
    And the task should have dependencies

  Scenario: Create COLLECT task
    When I create a COLLECT task for "IRON" from factory "X1-AU21-C3"
    Then the task should have type "COLLECT"
    And the task should have good "IRON"
    And the task should have factory symbol "X1-AU21-C3"
    And the task should have status "PENDING"

  Scenario: Create SELL task
    When I create a SELL task for "IRON" to market "X1-AU21-D4"
    Then the task should have type "SELL"
    And the task should have good "IRON"
    And the task should have target market "X1-AU21-D4"
    And the task should have status "PENDING"

  # Task State Machine: PENDING -> READY -> ASSIGNED -> EXECUTING -> COMPLETED
  Scenario: Mark task as ready when no dependencies
    Given a PENDING task with no dependencies
    When I mark the task as ready
    Then the task should have status "READY"
    And the task ready_at should be set

  Scenario: Cannot mark non-pending task as ready
    Given a READY task
    When I try to mark the task as ready
    Then the operation should fail with "invalid task transition"

  Scenario: Assign ship to ready task
    Given a READY task
    When I assign ship "AGENT-1" to the task
    Then the task should have status "ASSIGNED"
    And the task should have assigned ship "AGENT-1"

  Scenario: Cannot assign ship to non-ready task
    Given a PENDING task with no dependencies
    When I try to assign ship "AGENT-1" to the task
    Then the operation should fail with "invalid task transition"

  Scenario: Start task execution
    Given an ASSIGNED task with ship "AGENT-1"
    When I start the task execution
    Then the task should have status "EXECUTING"
    And the task started_at should be set

  Scenario: Complete task successfully
    Given an EXECUTING task
    When I complete the task
    Then the task should have status "COMPLETED"
    And the task completed_at should be set
    And the assigned ship should be released

  Scenario: Fail task with error
    Given an EXECUTING task
    When I fail the task with error "navigation failed"
    Then the task should have status "FAILED"
    And the task error message should be "navigation failed"
    And the retry count should be 1
    And the assigned ship should be released

  # Task Retry
  Scenario: Retry failed task
    Given a FAILED task with retry count 1 and max retries 3
    When I reset the task for retry
    Then the task should have status "PENDING"
    And the error message should be cleared
    And the retry count should still be 1

  Scenario: Cannot retry task that exceeded max retries
    Given a FAILED task with retry count 3 and max retries 3
    When I try to reset the task for retry
    Then the operation should fail with "exceeded max retries"

  Scenario: Check if task can retry
    Given a FAILED task with retry count 2 and max retries 3
    Then the task can retry should be true

  Scenario: Task cannot retry after max retries
    Given a FAILED task with retry count 3 and max retries 3
    Then the task can retry should be false

  # Task Terminal State
  Scenario: Completed task is terminal
    Given a COMPLETED task
    Then the task is terminal should be true

  Scenario: Failed task without retries is terminal
    Given a FAILED task with retry count 3 and max retries 3
    Then the task is terminal should be true

  Scenario: Failed task with retries available is not terminal
    Given a FAILED task with retry count 1 and max retries 3
    Then the task is terminal should be false

  # Task Financial Tracking
  Scenario: Record task cost
    Given an EXECUTING task
    When I set the task total cost to 5000
    Then the task total cost should be 5000

  Scenario: Record task revenue
    Given an EXECUTING task
    When I set the task total revenue to 8000
    Then the task total revenue should be 8000

  Scenario: Calculate net profit
    Given a COMPLETED task with cost 5000 and revenue 8000
    Then the task net profit should be 3000

  # Task Destination
  Scenario: ACQUIRE task destination is source market
    Given an ACQUIRE task for "IRON_ORE" from market "X1-AU21-A1"
    Then the task destination should be "X1-AU21-A1"

  Scenario: DELIVER task destination is target market
    Given a DELIVER task for "IRON_ORE" to market "X1-AU21-B2"
    Then the task destination should be "X1-AU21-B2"

  Scenario: COLLECT task destination is factory symbol
    Given a COLLECT task for "IRON" from factory "X1-AU21-C3"
    Then the task destination should be "X1-AU21-C3"

  Scenario: SELL task destination is target market
    Given a SELL task for "IRON" to market "X1-AU21-D4"
    Then the task destination should be "X1-AU21-D4"

  # Rollback Assignment
  Scenario: Rollback task assignment
    Given an ASSIGNED task with ship "AGENT-1"
    When I rollback the task assignment
    Then the task should have status "READY"
    And the assigned ship should be released

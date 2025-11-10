Feature: Ship Assignment Tracking During Container Restart
  As a fleet operator
  I want ship assignments to be updated when containers restart
  So that I can track which ships are assigned to which containers accurately

  Background:
    Given a player exists with id 1
    And a ship "TEST-1" exists for player 1

  Scenario: Assignment is updated when container restarts
    Given ship "TEST-1" is assigned to container "scout-tour-test-1-old123" with operation "command"
    And the assignment status is "active"
    When the assignment is reassigned from "scout-tour-test-1-old123" to "scout-tour-test-1-new456"
    Then ship "TEST-1" should be assigned to container "scout-tour-test-1-new456"
    And the assignment status should be "active"
    And the assignment should have a new assigned_at timestamp

  Scenario: Assignment release reason is cleared when reassigned
    Given ship "TEST-1" is assigned to container "scout-tour-test-1-old123" with operation "command"
    And the assignment was released with reason "container_failed"
    When the assignment is reassigned from "scout-tour-test-1-old123" to "scout-tour-test-1-new456"
    Then ship "TEST-1" should be assigned to container "scout-tour-test-1-new456"
    And the assignment should have no release reason
    And the assignment should have no released_at timestamp

  Scenario: Reassignment fails if ship was not assigned to old container
    Given ship "TEST-1" is assigned to container "different-container-123" with operation "command"
    When I attempt to reassign ship "TEST-1" from "scout-tour-test-1-old123" to "scout-tour-test-1-new456"
    Then the reassignment should fail
    And ship "TEST-1" should still be assigned to container "different-container-123"

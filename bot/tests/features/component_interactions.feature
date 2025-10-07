Feature: Component Interaction - SmartNavigator + OperationController
  As a bot operator
  I want SmartNavigator and OperationController to collaborate properly
  So that navigation operations can be checkpointed, resumed, and controlled

  Scenario: Navigation saves checkpoint after each navigation step
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 3 waypoints
    And an operation controller tracking "NAV-001"
    When I execute the navigation route
    Then operation controller should have checkpoint after step 1
    And checkpoint 1 should contain waypoint "X1-TEST-A2"
    And checkpoint 1 should contain step number 1
    And operation controller should have checkpoint after step 2
    And checkpoint 2 should contain waypoint "X1-TEST-A3"
    And checkpoint 2 should contain step number 2

  Scenario: Navigation saves checkpoint after refuel step
    Given a mock ship at "X1-TEST-A1" with 50 fuel
    And a navigation route requiring refuel at step 2
    And an operation controller tracking "NAV-002"
    When I execute the navigation route
    Then operation controller should have checkpoint after refuel step
    And refuel checkpoint should contain fuel level 400
    And refuel checkpoint should contain state "DOCKED"

  Scenario: Resume from checkpoint continues from correct step
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 4 waypoints
    And an operation controller tracking "NAV-003"
    And operation completed 2 steps with checkpoints
    When I resume the operation
    Then navigation should skip steps 1 and 2
    And navigation should execute from step 3
    And final location should be "X1-TEST-A5"

  Scenario: Pause signal stops navigation mid-route
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 5 waypoints
    And an operation controller tracking "NAV-004"
    And pause command will be sent after step 2
    When I execute the navigation route
    Then navigation should stop after step 2
    And operation status should be "paused"
    And ship should be at "X1-TEST-A3"
    And ship should NOT be at "X1-TEST-A4"

  Scenario: Cancel signal stops navigation and clears state
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 4 waypoints
    And an operation controller tracking "NAV-005"
    And cancel command will be sent after step 1
    When I execute the navigation route
    Then navigation should return False
    And operation status should be "cancelled"
    And ship should be at "X1-TEST-A2"
    And ship should NOT be at "X1-TEST-A5"

  Scenario: Multi-step navigation updates checkpoints progressively
    Given a mock ship at "X1-TEST-A1" with 400 fuel
    And a complex route with navigation and refuel steps
    And an operation controller tracking "NAV-006"
    When I execute the navigation route
    Then checkpoint count should increase progressively
    And checkpoint 1 should have action "navigate"
    And checkpoint 2 should have action "refuel"
    And checkpoint 3 should have action "navigate"
    And each checkpoint should contain accurate fuel levels

  Scenario: Checkpoint data matches actual navigation state
    Given a mock ship at "X1-TEST-A1" with 300 fuel
    And a navigation route with 2 waypoints
    And an operation controller tracking "NAV-007"
    When I execute step 1 of navigation
    Then checkpoint data should match ship controller state
    And checkpoint location should equal ship's current waypoint
    And checkpoint fuel should equal ship's current fuel
    And checkpoint state should equal ship's nav status

  Scenario: Error during navigation preserves last good checkpoint
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 3 waypoints
    And an operation controller tracking "NAV-008"
    And navigation will fail at step 2
    When I execute the navigation route
    Then navigation should return False
    And operation should have 1 checkpoint
    And last checkpoint should be from successful step 1
    And checkpoint location should be "X1-TEST-A2"

  Scenario: Resume with no checkpoints initializes from start
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 3 waypoints
    And an operation controller tracking "NAV-009" with no checkpoints
    When I attempt to resume the operation
    Then can_resume should be False
    And navigation should start from step 1
    And all 3 steps should execute

  Scenario: Checkpoint contains complete navigation state snapshot
    Given a mock ship at "X1-TEST-A1" with 250 fuel
    And a navigation route requiring refuel
    And an operation controller tracking "NAV-010"
    When I execute step 1 of navigation
    Then checkpoint should contain key "completed_step"
    And checkpoint should contain key "location"
    And checkpoint should contain key "fuel"
    And checkpoint should contain key "state"
    And completed_step should be integer 1
    And location should be string "X1-TEST-A2"
    And fuel should be numeric value
    And state should be valid nav status

  Scenario: Concurrent navigation steps save distinct checkpoints
    Given a mock ship at "X1-TEST-A1"
    And a navigation route with 3 waypoints
    And an operation controller tracking "NAV-011"
    When I execute the complete navigation route
    Then checkpoint 1 location should differ from checkpoint 2 location
    And checkpoint 2 location should differ from checkpoint 3 location
    And checkpoint fuel levels should decrease progressively
    And each checkpoint step number should increment by 1

  Scenario: Pause preserves exact state for resume
    Given a mock ship at "X1-TEST-A1" with 400 fuel
    And a navigation route with 5 waypoints
    And an operation controller tracking "NAV-012"
    When I execute steps 1 and 2
    And I pause the operation
    And I resume the operation
    Then navigation should continue from step 3
    And total steps executed should be 5
    And final location should be "X1-TEST-A6"
    And no steps should be skipped or duplicated

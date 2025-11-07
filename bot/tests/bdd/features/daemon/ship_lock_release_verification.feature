Feature: Ship Assignment Lock Release - Verification
  Verify that ship assignment locks are properly released in all scenarios

  Background:
    Given a player exists with ID 1 and token "test-token"
    And a ship "TEST-1" exists for player 1

  Scenario: Lock released after container completion
    When I assign ship "TEST-1" to container "test-container-1"
    Then the ship "TEST-1" should have assignment status "active"
    When I release the ship "TEST-1" assignment with reason "completed"
    Then the ship "TEST-1" should have assignment status "idle"
    And the ship "TEST-1" should be available for new assignments

  Scenario: Lock released after container failure
    When I assign ship "TEST-1" to container "test-container-1"
    And I release the ship "TEST-1" assignment with reason "failed"
    Then the ship "TEST-1" should have assignment status "idle"
    And the release reason should be "failed"

  Scenario: Sequential operations after lock release
    When I assign ship "TEST-1" to container "container-1"
    And I release the ship "TEST-1" assignment with reason "completed"
    Then I can assign ship "TEST-1" to container "container-2"
    And I can release ship "TEST-1" assignment with reason "completed"
    And I can assign ship "TEST-1" to container "container-3"

  Scenario: Cannot double-assign ship
    When I assign ship "TEST-1" to container "container-1"
    Then assigning ship "TEST-1" to container "container-2" should fail
    And the ship "TEST-1" should still be assigned to "container-1"

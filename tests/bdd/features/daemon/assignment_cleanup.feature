Feature: Ship Assignment Cleanup
  As an autonomous bot
  I need ship assignments to be automatically released when containers finish
  So that ships don't get stuck assigned and block new operations

  Background:
    Given a player exists with ID 1 and token "test-token"
    And a ship "SHIP-1" exists for player 1 at "X1-TEST-A1"
    And the daemon server is running

  Scenario: Assignment released on successful container completion
    Given a navigation container is created for ship "SHIP-1"
    And the ship "SHIP-1" is assigned to the container
    When the container completes successfully
    Then the ship assignment for "SHIP-1" should be released
    And the assignment status should be "idle"
    And the release reason should be "completed"

  Scenario: Assignment released on container failure
    Given a navigation container is created for ship "SHIP-1"
    And the ship "SHIP-1" is assigned to the container
    When the container fails with error "Test error"
    Then the ship assignment for "SHIP-1" should be released
    And the assignment status should be "idle"
    And the release reason should be "failed"

  Scenario: Assignment released when container is stopped
    Given a navigation container is created for ship "SHIP-1"
    And the ship "SHIP-1" is assigned to the container
    And the container is running
    When the container is stopped by user
    Then the ship assignment for "SHIP-1" should be released
    And the assignment status should be "idle"
    And the release reason should be "stopped"

  Scenario: Ship can be reassigned after cleanup
    Given a navigation container completed for ship "SHIP-1"
    And the ship assignment was released
    When I create a new navigation container for ship "SHIP-1"
    Then the container should be created successfully
    And the ship "SHIP-1" should be assigned to the new container

  Scenario: Cleanup happens even when container crashes
    Given a navigation container is created for ship "SHIP-1"
    And the ship "SHIP-1" is assigned to the container
    When the container crashes unexpectedly
    Then the ship assignment for "SHIP-1" should eventually be released
    And the release reason should be "failed"

  Scenario: Zombie assignments cleaned up on daemon restart
    Given the ship "SHIP-1" has an active assignment to container "zombie-container-123"
    And no containers are currently running
    When the daemon server starts
    Then all active ship assignments should be released
    And the ship assignment for "SHIP-1" should be released
    And the assignment status should be "idle"
    And the release reason should be "daemon_restart"

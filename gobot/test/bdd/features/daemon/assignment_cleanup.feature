Feature: Ship Assignment Cleanup
  As a daemon system
  I need to automatically release ship assignments when containers finish
  So that ships don't get permanently locked to non-existent containers

  Background:
    Given the daemon server is running on socket "/tmp/spacetraders-test-cleanup.sock"

  Scenario: Assignment released on successful completion
    Given a ship "TEST-SHIP-1" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-1" and player 1
    And the ship assignment is created for the container
    When the container completes successfully
    Then the ship assignment should be released
    And the release reason should be "completed"
    And the ship "TEST-SHIP-1" should be available for reassignment

  Scenario: Assignment released on failure
    Given a ship "TEST-SHIP-2" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-2" and player 1
    And the ship assignment is created for the container
    When the container fails with an error
    Then the ship assignment should be released
    And the release reason should be "failed"
    And the ship "TEST-SHIP-2" should be available for reassignment

  Scenario: Assignment released when stopped by user
    Given a ship "TEST-SHIP-3" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-3" and player 1
    And the ship assignment is created for the container
    When the user stops the container
    Then the ship assignment should be released
    And the release reason should be "stopped"
    And the ship "TEST-SHIP-3" should be available for reassignment

  Scenario: Ship can be reassigned after cleanup
    Given a ship "TEST-SHIP-4" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-4" and player 1
    And the ship assignment is created for the container
    And the container completes successfully
    And the ship assignment is released
    When a new navigation container is created for ship "TEST-SHIP-4" and player 1
    Then the container should be created successfully
    And a new ship assignment should be created for the ship

  Scenario: Zombie assignments cleaned up on daemon restart (CRITICAL)
    Given a ship "TEST-SHIP-5" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-5" and player 1
    And the ship assignment is created for the container
    And the container is running
    When the daemon crashes without cleanup
    And the daemon restarts
    Then all active ship assignments should be released
    And the release reason should be "daemon_restart"
    And the ship "TEST-SHIP-5" should be available for reassignment

  Scenario: Multiple ships released when container completes
    Given ships exist for player 1:
      | ship_symbol  |
      | TEST-SHIP-6  |
      | TEST-SHIP-7  |
      | TEST-SHIP-8  |
    And a scout markets container is created for all ships and player 1
    And ship assignments are created for all ships
    When the container completes successfully
    Then all ship assignments should be released
    And the release reason for all assignments should be "completed"
    And all ships should be available for reassignment

  Scenario: Cleanup happens even when container crashes
    Given a ship "TEST-SHIP-9" exists for player 1
    And a navigation container is created for ship "TEST-SHIP-9" and player 1
    And the ship assignment is created for the container
    When the container crashes unexpectedly
    Then the ship assignment should be released
    And the release reason should be "failed"
    And the ship "TEST-SHIP-9" should be available for reassignment

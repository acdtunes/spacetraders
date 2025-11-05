Feature: Health Monitor Stale Assignment Cleanup
  As an autonomous bot
  I need the health monitor to detect and clean up stale assignments
  So that crashed containers don't leave ships permanently assigned

  Background:
    Given a player exists with ID 1 and token "test-token"
    And a ship "SHIP-1" exists for player 1 at "X1-TEST-A1"
    And the daemon server is running with health monitor enabled

  Scenario: Health monitor detects stale assignment after container removed
    Given a navigation container "container-123" existed for ship "SHIP-1"
    And the ship "SHIP-1" was assigned to "container-123"
    But the container "container-123" no longer exists
    When the health monitor runs
    Then the ship assignment for "SHIP-1" should be detected as stale
    And the ship assignment should be auto-released
    And the release reason should be "stale_cleanup"

  Scenario: Health monitor runs periodically
    Given the daemon server is running
    When 60 seconds elapse
    Then the health monitor should have run at least once
    And stale assignments should be checked

  Scenario: Health monitor ignores valid assignments
    Given a navigation container is running for ship "SHIP-1"
    And the ship "SHIP-1" is assigned to the container
    When the health monitor runs
    Then the ship assignment for "SHIP-1" should remain active
    And the assignment should not be released

  Scenario: Health monitor cleans up multiple stale assignments
    Given 3 ships have stale assignments to non-existent containers
    When the health monitor runs
    Then all 3 stale assignments should be detected
    And all 3 assignments should be released with reason "stale_cleanup"

  Scenario: Health monitor recovers from daemon crash
    Given a navigation container was running before daemon crashed
    And the ship "SHIP-1" is still marked as assigned in database
    But the daemon server restarted and container is gone
    When the health monitor runs
    Then the ship assignment for "SHIP-1" should be detected as stale
    And the assignment should be auto-released

  Scenario: Health monitor logs cleanup actions
    Given a stale assignment exists for ship "SHIP-1"
    When the health monitor detects and cleans it up
    Then a warning should be logged about the stale assignment
    And an info message should confirm cleanup count

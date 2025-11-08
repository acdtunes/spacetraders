Feature: Container Recovery on Daemon Startup
  As a daemon system
  I need to automatically resume RUNNING containers when the daemon restarts
  So that operations survive daemon restarts and maintain business continuity

  Background:
    Given a test database is initialized
    And a player exists with ID 1 and token "test-token"
    And a ship "TEST-SHIP-1" exists for player 1 at "X1-TEST-A1"

  Scenario: Daemon resumes RUNNING containers on startup
    Given a container "recovery-test-1" exists in the database with status "RUNNING"
    And the container has valid configuration for ship "TEST-SHIP-1"
    When the daemon server starts up
    Then the container "recovery-test-1" should be resumed
    And the container should appear in the containers list

  Scenario: Daemon marks containers as FAILED if ship no longer exists
    Given a container "recovery-test-2" exists in the database with status "RUNNING"
    And the container references non-existent ship "MISSING-SHIP"
    When the daemon server starts up
    Then the container "recovery-test-2" should be marked as "FAILED"
    And the container should not appear in the running containers list

  Scenario: Daemon handles invalid container configuration
    Given a container "recovery-test-3" exists in the database with status "RUNNING"
    And the container has invalid JSON configuration
    When the daemon server starts up
    Then the container "recovery-test-3" should be marked as "FAILED"
    And an error should be logged about invalid configuration

  Scenario: Daemon only recovers RUNNING containers, not STOPPED
    Given a container "recovery-test-4" exists in the database with status "RUNNING"
    And a container "recovery-test-5" exists in the database with status "STOPPED"
    When the daemon server starts up
    Then only container "recovery-test-4" should be resumed
    And container "recovery-test-5" should remain "STOPPED"

  Scenario: Recovery happens after zombie assignment cleanup
    Given a container "recovery-test-6" exists in the database with status "RUNNING"
    And the ship "TEST-SHIP-1" has an active zombie assignment
    When the daemon server starts up
    Then zombie assignments should be released first
    And then container "recovery-test-6" should be resumed
    And the ship "TEST-SHIP-1" should be assigned to container "recovery-test-6"

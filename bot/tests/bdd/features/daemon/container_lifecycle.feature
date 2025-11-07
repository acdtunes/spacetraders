Feature: Container Status Lifecycle
  As a daemon system
  I need containers to properly transition status from STARTING to STOPPED
  So that container state accurately reflects execution completion

  Background:
    Given a test database is initialized
    And the daemon container manager is initialized

  Scenario: Container transitions to STOPPED on successful completion
    Given a new daemon container is created with type "command"
    When the container process completes successfully
    Then the container status should be "STOPPED"
    And the exit code should be 0
    And the stop timestamp should be set

  Scenario: Container transitions to STOPPED on failure
    Given a new daemon container is created that will fail
    When the container process completes
    Then the container status should be "STOPPED"
    And the exit code should be 1
    And the stop timestamp should be set

  Scenario: Quick-running containers properly transition status
    Given a daemon container that runs for less than 1 second
    When the container completes
    Then the status should be "STOPPED" not "STARTING"

  Scenario: Container status reflects completion even with exit code set
    Given a container with exit code 0 and stop timestamp
    When I query the container status
    Then the status must be "STOPPED" not "STARTING"

  Scenario: List containers shows STOPPED status after completion
    Given a new daemon container is created with type "command"
    When the container process completes successfully
    And I list all containers
    Then the container should appear with status "STOPPED"

  Scenario: Completed containers can be removed without stopping
    Given a new daemon container is created with type "command"
    When the container process completes successfully
    Then I should be able to remove the container directly
    And removal should not require daemon_stop first

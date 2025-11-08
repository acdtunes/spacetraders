Feature: Immediate Container Stop
  As a daemon user
  I need containers to stop immediately when requested
  So that I don't have to wait for long-running operations to complete

  Background:
    Given a test database is initialized
    And the daemon container manager is initialized

  Scenario: Stop container immediately without waiting for completion
    Given a container is running with a long-running operation
    When I issue a stop command
    Then the container should stop immediately within 2 seconds
    And the container status should be "STOPPED"
    And the stop timestamp should be set
    And the database should reflect the stopped status

  Scenario: Stop container when task is None
    Given a container exists but task is None
    When I issue a stop command
    Then the stop should succeed without error
    And the container status should be "STOPPED"

  Scenario: Stop container when task is already done
    Given a container has already completed
    When I issue a stop command
    Then the stop should succeed without error
    And the container status should remain "STOPPED"

  Scenario: Multiple stop calls on same container
    Given a container is running
    When I issue a stop command
    And I issue another stop command immediately
    Then both stops should succeed
    And the container status should be "STOPPED"

  Scenario: Stop does not wait for navigation delays
    Given a container is waiting for ship navigation with 369 seconds remaining
    When I issue a stop command at time T
    Then the stop should complete by time T + 2 seconds
    And the container task should be cancelled
    And the status should be "STOPPED" immediately

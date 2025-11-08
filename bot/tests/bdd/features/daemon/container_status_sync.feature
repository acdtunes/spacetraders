Feature: Container Status Synchronization
  As a daemon server
  I need to synchronize container status between memory and database
  So that daemon_list returns accurate running status

  Background:
    Given a test database
    And a mediator instance
    And a container manager

  Scenario: Container status transitions to RUNNING after start
    Given I create a command container with ID "test-container-123"
    And the container is configured to run a simple command
    When the container task starts executing
    And I wait for the container to begin running
    Then the container status in memory should be "RUNNING"
    And the container status in database should be "RUNNING"
    And listing containers should show status "RUNNING"

  Scenario: Container status remains synchronized during execution
    Given I create a command container with ID "test-container-456"
    And the container is configured to run a long command
    When the container task starts executing
    And I wait for the container to begin running
    And I list containers via the container manager
    Then all listed containers should show "RUNNING" status
    And the in-memory status should match the database status

  Scenario: Multiple containers maintain independent status
    Given I create command container "container-1" with a quick command
    And I create command container "container-2" with a slow command
    When both container tasks start executing
    And I wait for both containers to begin running
    Then container "container-1" should show "RUNNING" in list
    And container "container-2" should show "RUNNING" in list
    And both containers should have "RUNNING" in database

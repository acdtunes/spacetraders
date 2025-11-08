Feature: Container persistence to database
  As a daemon server
  I want to persist container state to the database
  So that container status can be queried even after daemon restart

  Background:
    Given a test database
    And a test player exists

  Scenario: Container is persisted when created
    When I create a container record in database with status "STARTING"
    Then the container should exist in the database
    And the container status in database should be "STARTING"
    And the container started_at timestamp should be set

  Scenario: Container status is updated when it starts running
    Given a container exists in database with status "STARTING"
    When the container status changes to "RUNNING"
    Then the container status in database should be "RUNNING"

  Scenario: Container status is updated when it completes successfully
    Given a container exists in database with status "RUNNING"
    When the container completes successfully
    Then the container status in database should be "STOPPED"
    And the container stopped_at timestamp should be set
    And the container exit_code should be 0

  Scenario: Container status is updated when it fails
    Given a container exists in database with status "RUNNING"
    When the container fails with error "Test error"
    Then the container status in database should be "FAILED"
    And the container stopped_at timestamp should be set
    And the container exit_code should be 1
    And the container exit_reason should be "Test error"

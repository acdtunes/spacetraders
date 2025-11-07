Feature: Database closed detection
  As a container manager
  I need to detect when database is closed
  So that I don't attempt operations on closed database connections

  Scenario: Detect closed database
    Given a test database is initialized
    When the database is closed
    Then is_closed should return true

  Scenario: Detect open database
    Given a test database is initialized
    Then is_closed should return false

  Scenario: Container cleanup handles closed database gracefully
    Given a test database is initialized
    And the daemon container manager is initialized
    And a container is running with a long-running operation
    When I issue a stop command
    And the database is closed
    And enough time passes for background cleanup
    Then no database errors should be raised

Feature: Tour Cache Persistence
  As a tour caching system
  I want tours to persist immediately to disk
  So that visualizer can display optimized routes even after daemon crashes

  Background:
    Given a temporary test database
    And a tour optimization system with WAL mode enabled

  Scenario: WAL checkpoint ensures immediate persistence
    Given a tour for system "X1-TEST" with 3 markets
    When I save the tour with WAL checkpoint
    And I close the database connection
    And I reopen the database
    Then the tour should be retrievable from cache

  Scenario: Without checkpoint data may be lost
    Given a tour for system "X1-TEST-BUG" with 2 markets
    When I save the tour without explicit checkpoint
    And I force close the connection (simulating crash)
    And I reopen the database
    Then the tour may or may not be retrievable (flaky)

  Scenario: Return-to-start tours persist correctly
    Given a return-to-start tour for system "X1-TEST-LOOP"
    And the tour has 4 stops plus return to start
    When I save with WAL checkpoint
    And I close and reopen the connection
    Then the cached tour should start and end at the same waypoint

  Scenario: Multiple tours persist with checkpoints
    Given I have 5 different systems with tours
    When I save each tour with checkpoint after transaction
    And I close and reopen the connection
    Then all 5 tours should be retrievable

  Scenario: Tours queryable immediately in same connection
    Given a tour for system "X1-IMMEDIATE"
    When I save the tour with checkpoint
    Then the tour should be queryable immediately without closing connection

  Scenario: Checkpoint performance is acceptable
    When I save 10 tours with checkpoints
    Then average checkpoint time should be under 100ms
    And maximum checkpoint time should be under 200ms

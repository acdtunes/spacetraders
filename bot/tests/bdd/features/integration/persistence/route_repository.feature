Feature: Route Repository CRUD Operations
  As a persistence layer
  I want to store and retrieve route data
  So that I can manage ship navigation routes

  Background:
    Given a fresh route repository
    And a test player and ship exist

  Scenario: Create new route
    When I create a route with ID "route-123"
    Then the route should be persisted
    And the route should have ID "route-123"
    And the route status should be PLANNED

  Scenario: Create duplicate route fails
    Given a created route "route-123"
    When I attempt to create another route with ID "route-123"
    Then creation should fail with DuplicateRouteError

  Scenario: Find route by ID when exists
    Given a created route "route-123"
    When I find the route by ID "route-123"
    Then the route should be found
    And the route should have 1 segment

  Scenario: Find route by ID when not exists
    When I find route by ID "nonexistent"
    Then the route should not be found

  Scenario: Find routes by ship when empty
    When I find routes for the ship
    Then I should see 0 routes

  Scenario: Find routes by ship with single route
    Given a created route "route-123"
    When I find routes for the ship
    Then I should see 1 route

  Scenario: Find routes by ship with multiple routes
    Given a created route "route-1"
    And a created route "route-2"
    And a created route "route-3"
    When I find routes for the ship
    Then I should see 3 routes

  Scenario: Update route status
    Given a created route "route-123"
    When I start route execution
    And I complete the first segment
    And I find the route by ID "route-123"
    Then the route status should be COMPLETED

  Scenario: Update nonexistent route fails
    When I attempt to update a nonexistent route
    Then update should fail with RouteNotFoundError

  Scenario: Delete route
    Given a created route "route-123"
    When I delete the route "route-123"
    And I find route by ID "route-123"
    Then the route should not be found

  Scenario: Delete nonexistent route fails
    When I attempt to delete route "nonexistent"
    Then deletion should fail with RouteNotFoundError

  Scenario: Find active routes when empty
    When I find active routes for the player
    Then I should see 0 routes

  Scenario: Find active routes includes PLANNED
    Given a created route with status PLANNED
    When I find active routes for the player
    Then I should see 1 route
    And the route status should be PLANNED

  Scenario: Find active routes includes EXECUTING
    Given a created route with status EXECUTING
    When I find active routes for the player
    Then I should see 1 route
    And the route status should be EXECUTING

  Scenario: Find active routes excludes COMPLETED
    Given a created route with status COMPLETED
    When I find active routes for the player
    Then I should see 0 routes

  Scenario: Find active routes excludes FAILED
    Given a created route with status FAILED
    When I find active routes for the player
    Then I should see 0 routes

  Scenario: Find active routes excludes ABORTED
    Given a created route with status ABORTED
    When I find active routes for the player
    Then I should see 0 routes

  Scenario: Cleanup completed routes keeps recent
    Given 5 completed routes exist
    When I cleanup completed routes keeping 3 most recent
    Then 2 routes should be deleted
    And 3 routes should remain

  Scenario: Cleanup does not delete active routes
    Given an active route exists
    And a completed route exists
    When I cleanup completed routes keeping 0
    Then the active route should still exist

  Scenario: Multi-segment route persistence
    When I create a route with 2 segments
    And I find the route by ID "multi-seg"
    Then the route should have 2 segments
    And segment 0 should use flight mode CRUISE
    And segment 1 should use flight mode DRIFT

  Scenario: Route state changes persist
    Given a created route "route-123"
    When I start route execution
    And I find the route by ID "route-123"
    Then the route status should be EXECUTING
    And current segment index should be 0

  Scenario: Routes with different flight modes
    When I create routes with all 4 flight modes
    Then all 4 routes should exist
    And they should have different flight modes

  Scenario: Find routes ordered by created_at DESC
    When I create route "route-1" first
    And I create route "route-2" second
    And I create route "route-3" third
    And I find routes for the ship
    Then routes should be in reverse creation order

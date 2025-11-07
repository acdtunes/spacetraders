Feature: Navigation with Empty Route Segments
  As a fleet operator
  I want navigation to handle empty waypoint cache gracefully
  So that I receive actionable error messages when routing fails

  Background:
    Given a player with agent "TEST_AGENT"
    And a ship "TEST_AGENT-1" at waypoint "X1-TEST-A1"

  Scenario: Navigation fails gracefully when waypoint cache is empty
    Given the waypoint cache for system "X1-TEST" is empty
    When I navigate ship "TEST_AGENT-1" to "X1-TEST-B1"
    Then the navigation should fail with message "No waypoints found for system X1-TEST"
    And the error should suggest checking waypoint cache

  Scenario: Navigation fails when routing engine returns empty steps
    Given the waypoint cache for system "X1-TEST" has waypoints
    And the routing engine returns empty steps for the route
    When I navigate ship "TEST_AGENT-1" to "X1-TEST-B1"
    Then the navigation should fail with message "No route found"
    And the error should suggest checking waypoint cache

  Scenario: Navigation fails when routing engine returns only REFUEL actions
    Given the waypoint cache for system "X1-TEST" has waypoints
    And the routing engine returns only REFUEL actions with no TRAVEL steps
    When I navigate ship "TEST_AGENT-1" to "X1-TEST-B1"
    Then the navigation should fail with message "Route plan has no TRAVEL steps"
    And the error should include the route steps

  Scenario: Navigation fails when ship location not in waypoint cache
    Given the waypoint cache for system "X1-TEST" is missing waypoint "X1-TEST-A1"
    When I navigate ship "TEST_AGENT-1" to "X1-TEST-B1"
    Then the navigation should fail with message "Waypoint X1-TEST-A1 not found in cache"
    And the error should suggest syncing waypoints from API

  Scenario: Navigation succeeds when waypoint cache is complete
    Given the waypoint cache for system "X1-TEST" has all required waypoints
    And the routing engine returns valid TRAVEL steps
    When I navigate ship "TEST_AGENT-1" to "X1-TEST-B1"
    Then the navigation should complete successfully
    And the route should have at least 1 segment

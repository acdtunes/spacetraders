Feature: Navigate ship already at destination
  As a fleet operator
  I want navigation commands to handle ships already at their destination gracefully
  So that idempotent commands don't cause errors

  Scenario: Ship is already at destination waypoint
    Given a player with agent "TEST_AGENT"
    And a ship "TEST-SHIP" at waypoint "X1-TEST-A1"
    And the ship has fuel 100/400
    And system "X1-TEST" has 2 waypoints
    When I navigate ship "TEST-SHIP" to "X1-TEST-A1"
    Then the navigation should succeed immediately
    And the route should have 0 segments
    And the route status should be "COMPLETED"

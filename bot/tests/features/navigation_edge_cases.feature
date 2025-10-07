Feature: Navigation Edge Cases
  As a bot operator
  I want the navigator to handle edge cases robustly
  So that the bot never crashes or behaves unexpectedly

  Background:
    Given the SpaceTraders API is mocked

  Scenario: Empty graph has no routes
    Given a ship "TEST-1" with 400 fuel
    And the navigation graph is empty
    When I plan a route to "X1-HU87-B9"
    Then the route should be None
    And no error should occur

  Scenario: Ship has zero fuel
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   |
      | X1-HU87-A1 | 0   | 0   |
      | X1-HU87-B9 | 100 | 0   |
    And a ship "TEST-1" at "X1-HU87-A1" with 0 fuel
    When I validate the route to "X1-HU87-B9"
    Then the route should be invalid
    And the reason should contain "insufficient fuel" or "no route"

  Scenario: Destination not in graph
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   |
      | X1-HU87-A1 | 0   | 0   |
    And a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I plan a route to "X1-HU87-NONEXISTENT"
    Then the route should be None

  Scenario: Ship already at destination
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   |
      | X1-HU87-A1 | 0   | 0   |
    And a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I navigate to "X1-HU87-A1"
    Then the navigation should succeed immediately
    And no API calls should be made

  Scenario: No marketplace available for refuel
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   | traits |
      | X1-HU87-A1 | 0   | 0   |        |
      | X1-HU87-Z9 | 500 | 0   |        |
    And a ship "TEST-1" at "X1-HU87-A1" with 100 fuel
    When I plan a route to "X1-HU87-Z9"
    Then the route should be None
    And the reason should be "no marketplace for refuel"

  Scenario: Ship has no fuel capacity
    Given a ship "TEST-1" at "X1-HU87-A1"
    And the ship has 0 fuel capacity
    When I validate ship health
    Then the health check should fail
    And the reason should mention "fuel capacity"

  Scenario: Critical ship damage prevents navigation
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship has 45% integrity
    When I validate ship health
    Then the health check should fail
    And the reason should mention "damage" or "integrity"

  Scenario: Moderate ship damage shows warning
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    And the ship has 60% integrity
    When I validate ship health
    Then the health check should pass
    And a warning should be logged

  Scenario: Zero distance navigation
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   | type  |
      | X1-HU87-A1 | 100 | 100 | PLANET |
      | X1-HU87-A2 | 100 | 100 | MOON   |
    And a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I plan a route to "X1-HU87-A2"
    Then the route should use minimal fuel
    And fuel cost should be 0 or 1

  Scenario: Negative coordinates handled correctly
    Given the system "X1-HU87" has waypoints:
      | symbol     | x    | y    |
      | X1-HU87-A1 | -100 | -100 |
      | X1-HU87-B9 | 100  | 100  |
    And a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I plan a route to "X1-HU87-B9"
    Then the route should be calculated correctly
    And the distance should be approximately 283 units

  Scenario: Extremely long route
    Given the system "X1-HU87" has waypoints:
      | symbol     | x    | y   | traits      |
      | X1-HU87-A1 | 0    | 0   | MARKETPLACE |
      | X1-HU87-B7 | 500  | 0   | MARKETPLACE |
      | X1-HU87-Z9 | 1500 | 0   |             |
    And a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I plan a route to "X1-HU87-Z9"
    Then the route should have multiple refuel stops
    Or the route should be None if impossible

  Scenario: Find nearest with no matching trait
    Given the system "X1-HU87" has waypoints:
      | symbol     | x   | y   | traits |
      | X1-HU87-A1 | 0   | 0   |        |
      | X1-HU87-B9 | 100 | 0   |        |
    And a ship "TEST-1" at "X1-HU87-A1"
    When I find nearest waypoint with trait "MARKETPLACE"
    Then the result should be an empty list

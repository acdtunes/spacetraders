Feature: Hop Minimization with prefer_cruise=True
  As a navigation system
  I want to minimize the number of navigation hops when prefer_cruise is enabled
  So that routes are simpler and more efficient

  Background:
    Given the SpaceTraders API is mocked

  Scenario: Long distance route should use minimal hops with refuel stops
    # Test case: 550 unit distance with 400 fuel capacity
    # Should use 2-3 legs max (refuel + cruise segments)
    # NOT 6+ legs with many intermediate waypoints
    Given waypoints exist:
      | symbol      | type       | x   | y   | traits       |
      | X1-HOP-H48  | PLANET     | 0   | 0   | MARKETPLACE  |
      | X1-HOP-INT1 | ASTEROID   | 100 | 0   |              |
      | X1-HOP-INT2 | MOON       | 200 | 0   | MARKETPLACE  |
      | X1-HOP-INT3 | ASTEROID   | 300 | 0   |              |
      | X1-HOP-INT4 | MOON       | 400 | 0   | MARKETPLACE  |
      | X1-HOP-J55  | PLANET     | 550 | 0   | MARKETPLACE  |
    And a ship "SHIP-1" at "X1-HOP-H48" with 400/400 fuel
    When I build a navigation graph for system "X1-HOP"
    And I plan a route from "X1-HOP-H48" to "X1-HOP-J55" with prefer_cruise
    Then the route should exist
    And the route should use at most 4 navigation legs
    And the route should only use CRUISE mode
    And the route should include at most 1 refuel stop

  Scenario: Medium distance route should be direct with no intermediate stops
    # Test case: 300 unit distance with 400 fuel capacity
    # Should be 1 direct leg (enough fuel for CRUISE)
    Given waypoints exist:
      | symbol      | type       | x   | y   | traits       |
      | X1-MED-A1   | PLANET     | 0   | 0   | MARKETPLACE  |
      | X1-MED-INT  | ASTEROID   | 150 | 0   |              |
      | X1-MED-B7   | PLANET     | 300 | 0   | MARKETPLACE  |
    And a ship "SHIP-2" at "X1-MED-A1" with 400/400 fuel
    When I build a navigation graph for system "X1-MED"
    And I plan a route from "X1-MED-A1" to "X1-MED-B7" with prefer_cruise
    Then the route should exist
    And the route should use exactly 1 navigation leg
    And the route should only use CRUISE mode
    And the route should have no refuel stops

  Scenario: Very long distance route should use 2-3 refuel stops max
    # Test case: 1000 unit distance with 400 fuel capacity
    # Should use 3-4 legs with 2-3 refuel stops
    # NOT many short hops
    Given waypoints exist:
      | symbol      | type       | x    | y   | traits       |
      | X1-LONG-A1  | PLANET     | 0    | 0   | MARKETPLACE  |
      | X1-LONG-M1  | MOON       | 250  | 0   | MARKETPLACE  |
      | X1-LONG-M2  | MOON       | 500  | 0   | MARKETPLACE  |
      | X1-LONG-M3  | MOON       | 750  | 0   | MARKETPLACE  |
      | X1-LONG-Z9  | PLANET     | 1000 | 0   | MARKETPLACE  |
    And a ship "SHIP-3" at "X1-LONG-A1" with 400/400 fuel
    When I build a navigation graph for system "X1-LONG"
    And I plan a route from "X1-LONG-A1" to "X1-LONG-Z9" with prefer_cruise
    Then the route should exist
    And the route should use at most 5 navigation legs
    And the route should only use CRUISE mode
    And the route should include at most 3 refuel stops

  Scenario: Route with low fuel should still minimize hops
    # Test case: Starting with low fuel, but should still prefer fewer legs
    # Should refuel at start, then cruise directly
    Given waypoints exist:
      | symbol      | type       | x   | y   | traits       |
      | X1-LOW-B17  | PLANET     | 0   | 0   | MARKETPLACE  |
      | X1-LOW-INT1 | ASTEROID   | 100 | 0   |              |
      | X1-LOW-INT2 | ASTEROID   | 200 | 0   |              |
      | X1-LOW-H48  | PLANET     | 303 | 0   | MARKETPLACE  |
    And a ship "SHIP-4" at "X1-LOW-B17" with 97/400 fuel
    When I build a navigation graph for system "X1-LOW"
    And I plan a route from "X1-LOW-B17" to "X1-LOW-H48" with prefer_cruise
    Then the route should exist
    And the route should use at most 3 navigation legs
    And the route should only use CRUISE mode
    And the route should include exactly 1 refuel stop at start

  Scenario: Route should not oscillate between intermediate waypoints
    # Test case: Ensure algorithm doesn't ping-pong between nearby waypoints
    # Should go directly or with minimal intermediate stops
    Given waypoints exist:
      | symbol      | type       | x   | y   | traits       |
      | X1-OSC-A1   | PLANET     | 0   | 0   | MARKETPLACE  |
      | X1-OSC-B2   | ASTEROID   | 50  | 50  |              |
      | X1-OSC-C3   | ASTEROID   | 100 | 50  |              |
      | X1-OSC-D4   | MOON       | 150 | 50  | MARKETPLACE  |
      | X1-OSC-E5   | ASTEROID   | 200 | 50  |              |
      | X1-OSC-F6   | ASTEROID   | 250 | 50  |              |
      | X1-OSC-G7   | PLANET     | 300 | 0   | MARKETPLACE  |
    And a ship "SHIP-5" at "X1-OSC-A1" with 400/400 fuel
    When I build a navigation graph for system "X1-OSC"
    And I plan a route from "X1-OSC-A1" to "X1-OSC-G7" with prefer_cruise
    Then the route should exist
    And the route should use at most 2 navigation legs
    And the route should only use CRUISE mode
    And the route should not visit intermediate asteroids

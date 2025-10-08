Feature: Advanced Routing Algorithms
  As a navigation system
  I want to handle complex routing scenarios
  So that ships can plan multi-stop tours and handle edge cases

  Background:
    Given the SpaceTraders API is mocked

  # =============================================================================
  # Time & Fuel Calculations - Uncovered Edge Cases
  # =============================================================================

  Scenario: Format time for seconds only
    When I format time for 45 seconds
    Then the formatted time should be "45s"

  Scenario: Format time for minutes and seconds
    When I format time for 125 seconds
    Then the formatted time should be "2m 5s"

  Scenario: Format time for hours and minutes
    When I format time for 7325 seconds
    Then the formatted time should be "2h 2m"

  Scenario: Check if ship can afford journey with safety margin
    Given a ship has 100 fuel
    When I check if it can afford 95 units in CRUISE mode
    Then the ship should not afford the journey

  Scenario: Ship can afford journey with sufficient fuel
    Given a ship has 200 fuel
    When I check if it can afford 90 units in CRUISE mode
    Then the ship should afford the journey

  Scenario: Parse waypoint symbol with standard format
    When I parse waypoint symbol "X1-HU87-A1B2"
    Then the system should be "X1-HU87"
    And the waypoint should be "X1-HU87-A1B2"

  Scenario: Parse waypoint symbol with short format
    When I parse waypoint symbol "X1-AB"
    Then the system should be "X1-AB"
    And the waypoint should be "X1-AB"

  # =============================================================================
  # Graph Builder - Pagination & Error Handling
  # =============================================================================

  Scenario: Build graph with pagination for large systems
    Given waypoints exist with pagination:
      | symbol      | type     | x   | y   | page |
      | X1-BIG-A1   | PLANET   | 0   | 0   | 1    |
      | X1-BIG-B2   | ASTEROID | 100 | 0   | 1    |
      | X1-BIG-C3   | MOON     | 200 | 0   | 2    |
      | X1-BIG-D4   | ASTEROID | 300 | 0   | 2    |
    When I build a navigation graph for system "X1-BIG"
    Then the graph should have 4 waypoints

  Scenario: Build graph for empty system returns None
    Given the system "X1-EMPTY" has no waypoints
    When I build a navigation graph for system "X1-EMPTY"
    Then the graph should be None

  # =============================================================================
  # Route Optimizer - Refueling & Advanced Pathfinding
  # =============================================================================

  Scenario: Route with refuel stop when fuel insufficient
    Given a fuel network:
      | from        | to          | distance | has_fuel_at_to |
      | X1-RF-A     | X1-RF-B     | 100      | true           |
      | X1-RF-B     | X1-RF-C     | 100      | false          |
    And a ship "LOW-FUEL" at "X1-RF-A" with 50/400 fuel
    When I plan a route from "X1-RF-A" to "X1-RF-C"
    Then the route should exist
    And the route should include a refuel action at "X1-RF-B"

  Scenario: Route cannot be found with insufficient fuel and no refuel stations
    Given a fuel network:
      | from        | to          | distance | has_fuel_at_to |
      | X1-NF-A     | X1-NF-B     | 5000     | false          |
    And a ship "NO-REFUEL" at "X1-NF-A" with 10/400 fuel
    When I plan a route from "X1-NF-A" to "X1-NF-B"
    Then the route should be None

  Scenario: Route uses DRIFT mode when CRUISE fuel insufficient
    Given a fuel network:
      | from        | to          | distance | has_fuel_at_to |
      | X1-DR-A     | X1-DR-B     | 200      | false          |
    And a ship "DRIFT-SHIP" at "X1-DR-A" with 10/400 fuel
    When I plan a route from "X1-DR-A" to "X1-DR-B"
    Then the route should exist
    And the route should use DRIFT mode

  # =============================================================================
  # Multi-Stop Tour Optimization (TSP)
  # =============================================================================

  Scenario: Plan tour visiting multiple waypoints
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-TR-HQ    | 0    | 0   | true     |
      | X1-TR-M1    | 100  | 0   | false    |
      | X1-TR-M2    | 200  | 0   | true     |
      | X1-TR-M3    | 150  | 100 | false    |
    And a ship "TOUR-SHIP" at "X1-TR-HQ" with 400/400 fuel
    When I plan a tour from "X1-TR-HQ" visiting:
      | X1-TR-M1 |
      | X1-TR-M2 |
      | X1-TR-M3 |
    Then the tour should exist
    And the tour should have 3 legs

  Scenario: Tour with return to start
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-RT-HQ    | 0    | 0   | true     |
      | X1-RT-M1    | 50   | 0   | true     |
      | X1-RT-M2    | 100  | 0   | true     |
    And a ship "RETURN-SHIP" at "X1-RT-HQ" with 400/400 fuel
    When I plan a tour from "X1-RT-HQ" visiting:
      | X1-RT-M1 |
      | X1-RT-M2 |
    And the tour should return to start
    Then the tour should exist
    And the tour should have 3 legs
    And the final waypoint should be "X1-RT-HQ"

  Scenario: Tour with automatic refueling at intermediate stops
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-AF-HQ    | 0    | 0   | true     |
      | X1-AF-M1    | 200  | 0   | true     |
      | X1-AF-M2    | 400  | 0   | false    |
    And a ship "AUTO-REFUEL" at "X1-AF-HQ" with 400/400 fuel
    When I plan a tour from "X1-AF-HQ" visiting:
      | X1-AF-M1 |
      | X1-AF-M2 |
    Then the tour should exist
    And the tour should include automatic refuel at "X1-AF-M1"

  Scenario: Tour cannot be completed when waypoint unreachable
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-UR-HQ    | 0    | 0   | true     |
      | X1-UR-M1    | 100  | 0   | true     |
    And an isolated waypoint "X1-UR-ISOLATED" at (1000, 1000)
    And a ship "UNREACHABLE-SHIP" at "X1-UR-HQ" with 400/400 fuel
    When I plan a tour from "X1-UR-HQ" visiting:
      | X1-UR-M1       |
      | X1-UR-ISOLATED |
    Then the tour should be None

  Scenario: Tour return to start fails if insufficient fuel
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-NR-HQ    | 0    | 0   | false    |
      | X1-NR-M1    | 5000 | 0   | false    |
    And a ship "NO-RETURN" at "X1-NR-HQ" with 10/400 fuel
    When I plan a tour from "X1-NR-HQ" visiting:
      | X1-NR-M1 |
    And the tour should return to start
    Then the tour should be None

  # =============================================================================
  # 2-Opt Tour Optimization
  # =============================================================================

  Scenario: Optimize tour with 2-opt algorithm
    Given a tour network:
      | symbol      | x    | y   | has_fuel |
      | X1-OP-HQ    | 0    | 0   | true     |
      | X1-OP-M1    | 100  | 0   | true     |
      | X1-OP-M2    | 100  | 100 | true     |
      | X1-OP-M3    | 0    | 100 | true     |
    And a ship "OPT-SHIP" at "X1-OP-HQ" with 800/800 fuel
    And a baseline tour visiting stops in order:
      | X1-OP-M1 |
      | X1-OP-M3 |
      | X1-OP-M2 |
    When I optimize the tour with 2-opt
    Then the optimized tour should be faster than baseline
    And the optimization should report improvements

  # =============================================================================
  # Market Discovery
  # =============================================================================

  Scenario: Discover all markets in a system graph
    Given a system graph with waypoints:
      | symbol       | type     | traits       |
      | X1-MK-A1     | PLANET   | MARKETPLACE  |
      | X1-MK-B2     | ASTEROID |              |
      | X1-MK-C3     | STATION  | MARKETPLACE  |
      | X1-MK-D4     | MOON     | FUEL_STATION |
    When I discover markets in the graph
    Then I should find 2 markets:
      | X1-MK-A1 |
      | X1-MK-C3 |

  Scenario: Discover markets in empty graph
    Given an empty system graph
    When I discover markets in the graph
    Then I should find 0 markets

  # =============================================================================
  # Edge Cases & Error Handling
  # =============================================================================

  Scenario: Route from waypoint to itself
    Given a fuel network:
      | from        | to          | distance | has_fuel_at_to |
      | X1-SM-A     | X1-SM-B     | 100      | true           |
    And a ship "SELF-SHIP" at "X1-SM-A" with 400/400 fuel
    When I plan a route from "X1-SM-A" to "X1-SM-A"
    Then the route should exist
    And the route should have 0 navigation steps

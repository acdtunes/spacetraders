Feature: Routing Algorithms
  As a navigation system
  I want to calculate optimal routes
  So that ships can travel efficiently

  Background:
    Given the SpaceTraders API is mocked

  Scenario: Calculate Euclidean distance
    When I calculate distance from (0, 0) to (3, 4)
    Then the distance should be 5

  Scenario: Calculate distance to same point
    When I calculate distance from (100, 100) to (100, 100)
    Then the distance should be 0

  Scenario: Calculate fuel cost for CRUISE mode
    When I calculate fuel cost for 100 units in CRUISE mode
    Then the fuel cost should be 100

  Scenario: Calculate fuel cost for DRIFT mode
    When I calculate fuel cost for 300 units in DRIFT mode
    Then the fuel cost should be 1

  Scenario: Calculate fuel cost for BURN mode
    When I calculate fuel cost for 100 units in BURN mode
    Then the fuel cost should be 200

  Scenario: Calculate fuel cost for zero distance
    When I calculate fuel cost for 0 units in CRUISE mode
    Then the fuel cost should be 0

  Scenario: DRIFT mode minimum fuel requirement
    When I calculate fuel cost for 1 unit in DRIFT mode
    Then the fuel cost should be 1

  Scenario: Build graph from waypoints
    Given waypoints exist:
      | symbol       | type     | x   | y   | traits       |
      | X1-GRAPH-A1  | PLANET   | 0   | 0   | MARKETPLACE  |
      | X1-GRAPH-B2  | ASTEROID | 100 | 0   |              |
      | X1-GRAPH-C3  | MOON     | 200 | 0   | FUEL_STATION |
    When I build a navigation graph for system "X1-GRAPH"
    Then the graph should have 3 waypoints
    And the graph should have edges
    And waypoint "X1-GRAPH-A1" should have fuel available
    And waypoint "X1-GRAPH-C3" should have fuel available
    And waypoint "X1-GRAPH-B2" should not have fuel available

  Scenario: Find direct route between waypoints
    Given a simple navigation graph:
      | from        | to          | distance |
      | X1-TEST-A   | X1-TEST-B   | 100      |
      | X1-TEST-B   | X1-TEST-C   | 100      |
    And a ship "ROUTE-SHIP" at "X1-TEST-A" with 400/400 fuel
    When I plan a route from "X1-TEST-A" to "X1-TEST-B"
    Then the route should exist
    And the route should have navigation steps

  Scenario: Route to nonexistent waypoint returns None
    Given a simple navigation graph:
      | from        | to          | distance |
      | X1-TEST-A   | X1-TEST-B   | 100      |
    And a ship "ROUTE-SHIP" at "X1-TEST-A" with 400/400 fuel
    When I plan a route from "X1-TEST-A" to "X1-NONEXISTENT"
    Then the route should be None

  Scenario: No path exists between isolated waypoints
    Given an isolated navigation graph:
      | waypoint    | x    | y   |
      | X1-TEST-X   | 0    | 0   |
      | X1-TEST-Y   | 1000 | 0   |
    And a ship "ROUTE-SHIP" at "X1-TEST-X" with 400/400 fuel
    When I plan a route from "X1-TEST-X" to "X1-TEST-Y"
    Then the route should be None

  Scenario: Heuristic calculation for different waypoints
    Given a simple navigation graph:
      | from        | to          | distance |
      | X1-TEST-A   | X1-TEST-C   | 200      |
    And a ship "ROUTE-SHIP" at "X1-TEST-A" with 400/400 fuel
    When I calculate heuristic from "X1-TEST-A" to "X1-TEST-C"
    Then the heuristic should be greater than 0

  Scenario: Heuristic for same waypoint is zero
    Given a simple navigation graph:
      | from        | to          | distance |
      | X1-TEST-A   | X1-TEST-B   | 100      |
    And a ship "ROUTE-SHIP" at "X1-TEST-A" with 400/400 fuel
    When I calculate heuristic from "X1-TEST-A" to "X1-TEST-A"
    Then the heuristic should be 0

  Scenario: Orbital relationship has zero distance
    Given waypoints exist:
      | symbol      | type   | x | y | orbits     |
      | X1-ORB-P1   | PLANET | 0 | 0 |            |
      | X1-ORB-M1   | MOON   | 0 | 0 | X1-ORB-P1  |
    When I build a navigation graph for system "X1-ORB"
    Then the edge from "X1-ORB-P1" to "X1-ORB-M1" should have distance 0
    And the edge should be type "orbital"

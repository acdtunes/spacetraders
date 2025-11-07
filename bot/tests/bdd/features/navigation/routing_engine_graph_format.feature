Feature: Routing Engine Graph Format Integration
  As a navigation system
  I need to ensure the routing engine receives the correct graph format
  So that pathfinding works reliably with cached graph data

  Scenario: Routing engine receives flat Dict[str, Waypoint] not nested structure
    Given a test graph with 3 waypoints
    When the routing engine receives the graph for pathfinding
    Then the graph should be a flat Dict[str, Waypoint]
    And the graph should NOT be a nested structure with "waypoints" and "edges" keys
    And the routing engine should successfully calculate distances between waypoints

  Scenario: Routing engine finds path with flat waypoint dictionary
    Given a test system with waypoints "X1-TEST-A1", "X1-TEST-B2", "X1-TEST-C3"
    And waypoint "X1-TEST-A1" at (0, 0) with has_fuel True
    And waypoint "X1-TEST-B2" at (50, 50) with has_fuel True
    And waypoint "X1-TEST-C3" at (100, 100) with has_fuel False
    When the routing engine finds optimal path from "X1-TEST-A1" to "X1-TEST-C3"
    Then pathfinding should succeed
    And the route should contain waypoint steps
    And fuel costs should be calculated correctly

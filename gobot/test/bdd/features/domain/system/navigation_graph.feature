Feature: Navigation Graph
  As a navigation system
  I need to maintain a graph of waypoints and connections
  So that I can plan optimal routes through space

  Background:
    Given a navigation graph for system "X1-TEST"

  Scenario: Create a new navigation graph
    Then the graph should have system symbol "X1-TEST"
    And the graph should have 0 waypoints
    And the graph should have 0 edges

  Scenario: Add a single waypoint to the graph
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20)
    Then the graph should have 1 waypoint
    And the graph should contain waypoint "X1-TEST-A1"

  Scenario: Add multiple waypoints to the graph
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20)
    And I add waypoint "X1-TEST-B1" at coordinates (30, 40)
    And I add waypoint "X1-TEST-C1" at coordinates (50, 60)
    Then the graph should have 3 waypoints
    And the graph should contain waypoint "X1-TEST-A1"
    And the graph should contain waypoint "X1-TEST-B1"
    And the graph should contain waypoint "X1-TEST-C1"

  Scenario: Retrieve a waypoint from the graph
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20)
    And I retrieve waypoint "X1-TEST-A1" from the graph
    Then the retrieval should succeed
    And the retrieved waypoint should have symbol "X1-TEST-A1"
    And the retrieved waypoint should have coordinates (10, 20)

  Scenario: Attempt to retrieve a non-existent waypoint
    When I attempt to retrieve waypoint "X1-TEST-MISSING" from the graph
    Then the retrieval should fail with error "waypoint X1-TEST-MISSING not found in graph"

  Scenario: Check if waypoint exists in the graph
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20)
    Then the graph should contain waypoint "X1-TEST-A1"
    And the graph should not contain waypoint "X1-TEST-B1"

  Scenario: Add a normal bidirectional edge between waypoints
    When I add waypoint "X1-TEST-A1" at coordinates (0, 0)
    And I add waypoint "X1-TEST-B1" at coordinates (100, 0)
    And I add a normal edge between "X1-TEST-A1" and "X1-TEST-B1" with distance 100
    Then the graph should have 2 edges
    And there should be an edge from "X1-TEST-A1" to "X1-TEST-B1" with distance 100
    And there should be an edge from "X1-TEST-B1" to "X1-TEST-A1" with distance 100

  Scenario: Add an orbital edge between waypoints
    When I add waypoint "X1-TEST-PLANET" at coordinates (0, 0)
    And I add waypoint "X1-TEST-MOON" at coordinates (0, 0)
    And I add an orbital edge between "X1-TEST-PLANET" and "X1-TEST-MOON"
    Then the graph should have 2 edges
    And there should be an orbital edge from "X1-TEST-PLANET" to "X1-TEST-MOON"
    And there should be an orbital edge from "X1-TEST-MOON" to "X1-TEST-PLANET"

  Scenario: Get all edges from a specific waypoint
    When I add waypoint "X1-TEST-A1" at coordinates (0, 0)
    And I add waypoint "X1-TEST-B1" at coordinates (100, 0)
    And I add waypoint "X1-TEST-C1" at coordinates (0, 100)
    And I add a normal edge between "X1-TEST-A1" and "X1-TEST-B1" with distance 100
    And I add a normal edge between "X1-TEST-A1" and "X1-TEST-C1" with distance 100
    Then waypoint "X1-TEST-A1" should have 2 outgoing edges
    And waypoint "X1-TEST-B1" should have 1 outgoing edge
    And waypoint "X1-TEST-C1" should have 1 outgoing edge

  Scenario: Get edges from waypoint with no connections
    When I add waypoint "X1-TEST-A1" at coordinates (0, 0)
    Then waypoint "X1-TEST-A1" should have 0 outgoing edges

  Scenario: Count waypoints in the graph
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20)
    And I add waypoint "X1-TEST-B1" at coordinates (30, 40)
    And I add waypoint "X1-TEST-C1" at coordinates (50, 60)
    Then the graph should have 3 waypoints

  Scenario: Count edges in the graph
    When I add waypoint "X1-TEST-A1" at coordinates (0, 0)
    And I add waypoint "X1-TEST-B1" at coordinates (100, 0)
    And I add waypoint "X1-TEST-C1" at coordinates (0, 100)
    And I add a normal edge between "X1-TEST-A1" and "X1-TEST-B1" with distance 100
    And I add a normal edge between "X1-TEST-B1" and "X1-TEST-C1" with distance 100
    Then the graph should have 4 edges

  Scenario: Get fuel stations from empty graph
    Then the graph should have 0 fuel stations

  Scenario: Get fuel stations from graph with no fuel stations
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20) without fuel
    And I add waypoint "X1-TEST-B1" at coordinates (30, 40) without fuel
    Then the graph should have 0 fuel stations

  Scenario: Get fuel stations from graph with mixed waypoints
    When I add waypoint "X1-TEST-A1" at coordinates (10, 20) with fuel
    And I add waypoint "X1-TEST-B1" at coordinates (30, 40) without fuel
    And I add waypoint "X1-TEST-C1" at coordinates (50, 60) with fuel
    Then the graph should have 2 fuel stations
    And the fuel stations should include "X1-TEST-A1"
    And the fuel stations should include "X1-TEST-C1"

  Scenario: Build a complex graph with multiple connections
    When I add waypoint "X1-TEST-STATION" at coordinates (0, 0) with fuel
    And I add waypoint "X1-TEST-PLANET-A" at coordinates (100, 0) without fuel
    And I add waypoint "X1-TEST-PLANET-B" at coordinates (-100, 0) with fuel
    And I add waypoint "X1-TEST-MOON-A1" at coordinates (100, 0) without fuel
    And I add a normal edge between "X1-TEST-STATION" and "X1-TEST-PLANET-A" with distance 100
    And I add a normal edge between "X1-TEST-STATION" and "X1-TEST-PLANET-B" with distance 100
    And I add an orbital edge between "X1-TEST-PLANET-A" and "X1-TEST-MOON-A1"
    Then the graph should have 4 waypoints
    And the graph should have 6 edges
    And the graph should have 2 fuel stations
    And waypoint "X1-TEST-STATION" should have 2 outgoing edges
    And waypoint "X1-TEST-PLANET-A" should have 2 outgoing edges

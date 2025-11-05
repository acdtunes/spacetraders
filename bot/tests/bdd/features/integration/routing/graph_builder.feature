Feature: Graph Builder Integration
  As a routing system
  I want to build system graphs from API data
  So that I can perform route planning

  Background:
    Given a graph builder with mocked API client

  # Euclidean Distance Calculations
  Scenario: Calculate zero distance
    When I calculate euclidean distance from (0,0) to (0,0)
    Then the distance should be 0.0

  Scenario: Calculate horizontal distance
    When I calculate euclidean distance from (0,0) to (10,0)
    Then the distance should be 10.0

  Scenario: Calculate vertical distance
    When I calculate euclidean distance from (0,0) to (0,10)
    Then the distance should be 10.0

  Scenario: Calculate diagonal distance
    When I calculate euclidean distance from (0,0) to (3,4)
    Then the distance should be 5.0

  Scenario: Calculate with negative coordinates
    When I calculate euclidean distance from (-5,-5) to (5,5)
    Then the distance should be approximately 14.14

  Scenario: Calculate large distances
    When I calculate euclidean distance from (0,0) to (1000,1000)
    Then the distance should be approximately 1414.21

  # Building System Graphs
  Scenario: Build simple graph from single page
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-A1" at (0,0) with marketplace and orbital "SYSTEM-A1-STATION", station "SYSTEM-A1-STATION" at (0,0) with fuel, asteroid "SYSTEM-A2" at (10,0) with minerals
    When I build system graph for "SYSTEM"
    Then the graph should have system "SYSTEM"
    And the graph should have 3 waypoints
    And the graph should have waypoint "SYSTEM-A1"
    And the graph should have waypoint "SYSTEM-A1-STATION"
    And the graph should have waypoint "SYSTEM-A2"

  Scenario: Extract waypoint properties correctly
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-A1" at (0,0) with marketplace and orbital "SYSTEM-A1-STATION", station "SYSTEM-A1-STATION" at (0,0) with fuel, asteroid "SYSTEM-A2" at (10,0) with minerals
    When I build system graph for "SYSTEM"
    Then waypoint "SYSTEM-A1" should have type "PLANET"
    And waypoint "SYSTEM-A1" should have coordinates (0, 0)
    And waypoint "SYSTEM-A1" should have trait "MARKETPLACE"
    And waypoint "SYSTEM-A1" should have fuel available
    And waypoint "SYSTEM-A1" should have orbital "SYSTEM-A1-STATION"
    And waypoint "SYSTEM-A1-STATION" should have type "ORBITAL_STATION"
    And waypoint "SYSTEM-A1-STATION" should have fuel available
    And waypoint "SYSTEM-A1-STATION" should have trait "FUEL_STATION"
    And waypoint "SYSTEM-A2" should not have fuel available
    And waypoint "SYSTEM-A2" should have no orbitals

  Scenario: Create edges correctly
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-A1" at (0,0) with marketplace and orbital "SYSTEM-A1-STATION", station "SYSTEM-A1-STATION" at (0,0) with fuel, asteroid "SYSTEM-A2" at (10,0) with minerals
    When I build system graph for "SYSTEM"
    Then the graph should have 6 edges
    And all waypoint pairs should have bidirectional edges

  Scenario: Create orbital edge type
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-A1" at (0,0) with marketplace and orbital "SYSTEM-A1-STATION", station "SYSTEM-A1-STATION" at (0,0) with fuel, asteroid "SYSTEM-A2" at (10,0) with minerals
    When I build system graph for "SYSTEM"
    Then the edge from "SYSTEM-A1" to "SYSTEM-A1-STATION" should be orbital
    And the edge from "SYSTEM-A1" to "SYSTEM-A1-STATION" should have distance 0.0
    And the edge from "SYSTEM-A1-STATION" to "SYSTEM-A1" should be orbital
    And the edge from "SYSTEM-A1-STATION" to "SYSTEM-A1" should have distance 0.0

  Scenario: Create normal edge type
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-A1" at (0,0) with marketplace and orbital "SYSTEM-A1-STATION", station "SYSTEM-A1-STATION" at (0,0) with fuel, asteroid "SYSTEM-A2" at (10,0) with minerals
    When I build system graph for "SYSTEM"
    Then the edge from "SYSTEM-A1" to "SYSTEM-A2" should be normal
    And the edge from "SYSTEM-A1" to "SYSTEM-A2" should have distance 10.0

  # Pagination
  Scenario: Handle multi-page pagination
    Given the API returns 35 waypoints across 2 pages for "SYSTEM"
    When I build system graph for "SYSTEM"
    Then the API should have been called 2 times
    And the graph should have 35 waypoints
    And the graph should have waypoint "SYSTEM-WP0"
    And the graph should have waypoint "SYSTEM-WP34"

  Scenario: Handle pagination safety limit
    Given the API returns full pages indefinitely for "SYSTEM"
    When I build system graph for "SYSTEM"
    Then the API should have been called 50 times
    And the graph should have 1000 waypoints

  # Error Handling
  Scenario: Handle empty response
    Given the API returns no waypoints for "EMPTY-SYSTEM"
    When I build system graph for "EMPTY-SYSTEM"
    Then graph building should fail with "No waypoints found"

  Scenario: Handle API error
    Given the API throws error for "SYSTEM"
    When I build system graph for "SYSTEM"
    Then graph building should fail with "API error"

  Scenario: Handle malformed response
    Given the API returns malformed data for "SYSTEM"
    When I build system graph for "SYSTEM"
    Then graph building should fail with "No waypoints found"

  # Special Cases
  Scenario: Build graph with single waypoint
    Given the API returns waypoints for "SYSTEM": planet "SYSTEM-ONLY" at (0,0)
    When I build system graph for "SYSTEM"
    Then the graph should have 1 waypoint
    And the graph should have 0 edges

  Scenario: Round distances to 2 decimal places
    Given the API returns waypoints for "SYSTEM": planet "WP-A" at (0,0), planet "WP-B" at (1,1)
    When I build system graph for "SYSTEM"
    Then the edge from "WP-A" to "WP-B" should have distance 1.41

  Scenario: Extract traits as list of symbols
    Given the API returns waypoints for "SYSTEM": planet "WP-1" at (0,0) with traits "TRAIT_1", "TRAIT_2", "TRAIT_3"
    When I build system graph for "SYSTEM"
    Then waypoint "WP-1" should have 3 traits
    And waypoint "WP-1" should have trait "TRAIT_1"
    And waypoint "WP-1" should have trait "TRAIT_2"
    And waypoint "WP-1" should have trait "TRAIT_3"

  Scenario: Extract orbitals as list of symbols
    Given the API returns waypoints for "SYSTEM": planet "PLANET" at (0,0) with orbitals "STATION-1", "STATION-2", "MOON"
    When I build system graph for "SYSTEM"
    Then waypoint "PLANET" should have 3 orbitals
    And waypoint "PLANET" should have orbital "STATION-1"
    And waypoint "PLANET" should have orbital "STATION-2"
    And waypoint "PLANET" should have orbital "MOON"

  Scenario: Detect fuel availability from marketplace
    Given the API returns waypoints for "SYSTEM": planet "WP-MARKETPLACE" at (0,0) with marketplace
    When I build system graph for "SYSTEM"
    Then waypoint "WP-MARKETPLACE" should have fuel available

  Scenario: Detect fuel availability from fuel station
    Given the API returns waypoints for "SYSTEM": station "WP-FUEL" at (10,0) with fuel
    When I build system graph for "SYSTEM"
    Then waypoint "WP-FUEL" should have fuel available

  Scenario: Detect fuel availability from both
    Given the API returns waypoints for "SYSTEM": station "WP-BOTH" at (20,0) with marketplace and fuel
    When I build system graph for "SYSTEM"
    Then waypoint "WP-BOTH" should have fuel available

  Scenario: No fuel availability without traits
    Given the API returns waypoints for "SYSTEM": asteroid "WP-NONE" at (30,0) with minerals
    When I build system graph for "SYSTEM"
    Then waypoint "WP-NONE" should not have fuel available

  Scenario: Bidirectional orbital detection
    Given the API returns waypoints for "SYSTEM": planet "PLANET" at (0,0) with orbital "MOON", moon "MOON" at (0,0)
    When I build system graph for "SYSTEM"
    Then both edges between "PLANET" and "MOON" should be orbital

  Scenario: No edge duplication
    Given the API returns waypoints for "SYSTEM": planet "A" at (0,0), planet "B" at (10,0), planet "C" at (20,0)
    When I build system graph for "SYSTEM"
    Then the graph should have 6 edges
    And each waypoint pair should have exactly 2 edges

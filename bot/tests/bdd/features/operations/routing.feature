Feature: Routing operations
  As a navigation planner
  I want to build graphs and plan optimal routes
  So that ships can navigate efficiently with minimal fuel consumption

  Background:
    Given a routing system

  Scenario: Build system graph with waypoints
    Given a system with 5 waypoints
    When I build navigation graph
    Then graph should have 5 waypoints
    And graph should have edges connecting waypoints
    And waypoint data should include coordinates

  Scenario: Identify fuel stations in graph
    Given a navigation graph with 8 waypoints
    And 2 waypoints have fuel stations
    When I count fuel stations
    Then fuel station count should be 2
    And fuel station waypoints should be marked

  Scenario: Calculate distance between waypoints
    Given waypoint A at coordinates (0, 0)
    And waypoint B at coordinates (300, 400)
    When I calculate distance between A and B
    Then distance should be 500 units

  Scenario: Find waypoints within range
    Given a graph with waypoints at various distances
    And current position is waypoint "X1-TEST-A1"
    And ship has 200 units fuel
    And ship uses DRIFT mode (1 fuel per 300 units)
    When I find waypoints within fuel range
    Then reachable waypoints should include all within 60000 units
    And unreachable waypoints should be excluded

  Scenario: Route planning checks routing pause status
    Given routing validation is paused
    When I attempt to plan route
    Then route planning should fail
    And error should indicate routing is paused

  Scenario: Validate graph has required fuel stations
    Given a navigation graph for mining operations
    And graph has 15 waypoints
    And 3 waypoints have fuel stations
    When I validate fuel station coverage
    Then graph should have at least 2 fuel stations
    And fuel stations should be distributed across graph

  Scenario: Calculate route fuel requirements
    Given a route with 3 navigation steps
    And step 1 is 100 units using CRUISE mode
    And step 2 is 200 units using CRUISE mode
    And step 3 is 150 units using DRIFT mode
    When I calculate total fuel required
    Then CRUISE fuel should be 300 units
    And DRIFT fuel should be 0.45 units
    And total fuel should be 300.45 units

  Scenario: Estimate route travel time
    Given a route with 2 segments
    And segment 1 is 150 units at speed 30 (CRUISE)
    And segment 2 is 300 units at speed 10 (DRIFT)
    When I calculate total travel time
    Then segment 1 time should be 5 seconds
    And segment 2 time should be 30 seconds
    And total time should be 35 seconds

Feature: OR-Tools Routing Engine Integration
  As a navigation system
  I want to use the OR-Tools routing engine
  So that I can find optimal paths between waypoints

  Background:
    Given an OR-Tools routing engine

  # Fuel Cost Calculations
  Scenario: Calculate fuel cost in CRUISE mode
    When I calculate fuel cost for 100 units in CRUISE mode
    Then the fuel cost should be 100

  Scenario: Calculate fuel cost in DRIFT mode
    When I calculate fuel cost for 1000 units in DRIFT mode
    Then the fuel cost should be 3

  Scenario: Calculate fuel cost in BURN mode
    When I calculate fuel cost for 50 units in BURN mode
    Then the fuel cost should be 100

  Scenario: Calculate fuel cost for zero distance
    When I calculate fuel cost for 0 units in CRUISE mode
    Then the fuel cost should be 0

  Scenario: Calculate minimum fuel for small distance
    When I calculate fuel cost for 0.1 units in CRUISE mode
    Then the fuel cost should be at least 1

  # Travel Time Calculations
  Scenario: Calculate travel time in CRUISE mode
    When I calculate travel time for 100 units in CRUISE mode with engine speed 10
    Then the travel time should be 310 seconds

  Scenario: Calculate travel time in DRIFT mode
    When I calculate travel time for 100 units in DRIFT mode with engine speed 10
    Then the travel time should be 260 seconds

  Scenario: Calculate travel time for zero distance
    When I calculate travel time for 0 units in CRUISE mode with engine speed 10
    Then the travel time should be 0 seconds

  Scenario: Faster engine reduces travel time
    Given I calculate travel time for 100 units in CRUISE mode with engine speed 5
    When I calculate travel time for 100 units in CRUISE mode with engine speed 20
    Then the second travel time should be less than the first

  # Optimal Path Finding - Basic
  Scenario: Find direct path between waypoints
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have 1 step
    And step 1 should be TRAVEL action
    And step 1 should go to "WP-B"
    And step 1 should use BURN mode
    And the total distance should be 10.0 units
    And the total fuel cost should be greater than 0

  Scenario: Find path through intermediate waypoint
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel, "WP-C" at (5,8.66) with fuel
    When I find optimal path from "WP-A" to "WP-C" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have at least 1 step
    And the last step should go to "WP-C"
    And the total fuel cost should be at most 100

  Scenario: Path from waypoint to itself
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel
    When I find optimal path from "WP-A" to "WP-A" with 50 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have 0 steps
    And the total fuel cost should be 0
    And the total distance should be 0.0 units

  Scenario: Non-existent start waypoint
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "NONEXISTENT" to "WP-B" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should be None

  Scenario: Non-existent goal waypoint
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "NONEXISTENT" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should be None

  # Flight Mode Preferences
  Scenario: Prefer CRUISE mode when fuel allows
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then all TRAVEL steps should use CRUISE mode

  Scenario: Never use DRIFT mode (always CRUISE minimum)
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 100 current fuel, 100 capacity, speed 10, preferring drift
    Then all TRAVEL steps should use CRUISE mode or better

  Scenario: Low fuel without refuel station returns None
    Given a simple graph with waypoints "WP-A" at (0,0) without fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 9 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should be None

  # Refueling
  Scenario: Automatic refuel when needed (never DRIFT)
    Given a fuel constraint graph with "START" at (0,0), "FUEL-1" at (20,0), "FUEL-2" at (40,0), "GOAL" at (60,0) with fuel
    When I find optimal path from "START" to "GOAL" with 25 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should include REFUEL actions

  Scenario: Insufficient fuel with no refuel station returns None
    Given a simple graph with waypoints "WP-A" at (0,0) without fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 1 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should be None

  # Orbital Hops
  Scenario: Orbital hop with zero cost
    Given an orbital graph with "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel
    When I find optimal path from "PLANET" to "STATION" with 10 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have 2 steps
    And step 1 should be REFUEL action
    And step 2 should have 0 fuel cost
    And step 2 should have 0.0 distance
    And step 2 should have 1 second travel time

  Scenario: Path with orbital hop and regular travel
    Given an orbital graph with "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel, "OTHER" at (50,0) without fuel
    When I find optimal path from "PLANET" to "OTHER" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have at least 1 step
    And the total distance should be greater than 0
    And the total fuel cost should be greater than 0

  # Multi-Waypoint Tours
  Scenario: Single waypoint tour
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", no waypoints, not returning to start, 1000 capacity, speed 10
    Then the ordered waypoints should be ["A"]
    And the tour should have 0 legs
    And the total distance should be 0.0 units

  Scenario: Simple multi-waypoint tour
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C"], not returning to start, 1000 capacity, speed 10
    Then the ordered waypoints should start with "A"
    And the ordered waypoints should contain "B"
    And the ordered waypoints should contain "C"
    And the tour should have 2 legs
    And the total distance should be greater than 0

  Scenario: Tour returning to start
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C"], returning to start, 1000 capacity, speed 10
    Then the ordered waypoints should start with "A"
    And the ordered waypoints should end with "A"
    And the legs count should equal waypoints count minus 1

  Scenario: Tour visiting all waypoints
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C", "D", "E"], not returning to start, 1000 capacity, speed 10
    Then the ordered waypoints should contain all of ["A", "B", "C", "D", "E"]

  Scenario: Tour with non-existent waypoint
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "NONEXISTENT"], not returning to start, 1000 capacity, speed 10
    Then the tour should be None

  Scenario: Tour with non-existent start
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "NONEXISTENT", waypoints ["B", "C"], not returning to start, 1000 capacity, speed 10
    Then the tour should be None

  Scenario: Tour uses CRUISE mode
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C"], not returning to start, 1000 capacity, speed 10
    Then all tour legs should use CRUISE mode

  Scenario: Tour with orbital hops
    Given an orbital graph with "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel, "OTHER" at (50,0) without fuel
    When I optimize tour with start "PLANET", waypoints ["STATION", "OTHER"], not returning to start, 1000 capacity, speed 10
    Then the tour should have at least one leg with 0.0 distance
    And all zero-distance legs should have 0 fuel cost

  Scenario: Tour legs match ordered waypoints
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C", "D"], not returning to start, 1000 capacity, speed 10
    Then the legs count should equal waypoints count minus 1
    And each leg should connect consecutive waypoints

  Scenario: Tour totals match sum of legs
    Given a multi-waypoint graph with waypoints "A" at (0,0), "B" at (10,0), "C" at (10,10), "D" at (0,10), "E" at (5,5) with fuel
    When I optimize tour with start "A", waypoints ["B", "C"], not returning to start, 1000 capacity, speed 10
    Then the total distance should match sum of leg distances
    And the total fuel cost should match sum of leg fuel costs
    And the total time should match sum of leg times

  Scenario: Tour return with orbital
    Given an orbital graph with "PLANET" at (0,0) with fuel having orbital "STATION", "STATION" at (0,0) with fuel, "OTHER" at (50,0) without fuel
    When I optimize tour with start "STATION", waypoints ["OTHER"], returning to start, 1000 capacity, speed 10
    Then the ordered waypoints should start with "STATION"
    And the ordered waypoints should end with "STATION"
    And the ordered waypoints should contain "OTHER"

  # Edge Cases
  Scenario: Empty graph
    Given an empty graph
    When I find optimal path from "A" to "B" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should be None

  Scenario: Single waypoint graph
    Given a simple graph with waypoints "ONLY" at (0,0) with fuel
    When I find optimal path from "ONLY" to "ONLY" with 100 current fuel, 100 capacity, speed 10, preferring cruise
    Then the path should have 0 steps

  Scenario: Disconnected graph with insufficient fuel
    Given a disconnected graph with "A" at (0,0) without fuel, "B" at (1000,0) without fuel
    When I find optimal path from "A" to "B" with 1 current fuel, 10 capacity, speed 10, preferring cruise
    Then the path should be None

  Scenario: Zero engine speed handling
    Given a simple graph with waypoints "WP-A" at (0,0) with fuel, "WP-B" at (10,0) without fuel
    When I find optimal path from "WP-A" to "WP-B" with 100 current fuel, 100 capacity, speed 0, preferring cruise
    Then the path should not be None

  Scenario: Very large distances
    Given a large distance graph with "A" at (0,0) with fuel, "B" at (10000,10000) without fuel
    When I find optimal path from "A" to "B" with 50000 current fuel, 50000 capacity, speed 10, preferring cruise
    Then the path should not be None
    And the total distance should be greater than 14000

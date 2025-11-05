Feature: Pre-Departure Refuel Planning
  As a ship operator
  I want the routing engine to plan refueling before departure when needed
  So that ships don't fail navigation due to insufficient fuel

  Background:
    Given the routing engine is initialized

  Scenario: Ship at fuel station with insufficient fuel should refuel before departure
    Given a waypoint "X1-GZ7-C48" at coordinates (0, 0) with fuel station
    And a waypoint "X1-GZ7-B33" at coordinates (333, 0) without fuel
    And a ship with 326 current fuel and 400 capacity at "X1-GZ7-C48"
    And the ship's engine speed is 30
    When I plan a route from "X1-GZ7-C48" to "X1-GZ7-B33"
    Then the route should include a REFUEL action
    And the REFUEL action should be the first step
    And the REFUEL should be at waypoint "X1-GZ7-C48"
    And the route should then include a TRAVEL action to "X1-GZ7-B33"

  Scenario: Ship at fuel station with sufficient fuel should not refuel
    Given a waypoint "X1-GZ7-C48" at coordinates (0, 0) with fuel station
    And a waypoint "X1-GZ7-B33" at coordinates (100, 0) without fuel
    And a ship with 400 current fuel and 400 capacity at "X1-GZ7-C48"
    And the ship's engine speed is 30
    When I plan a route from "X1-GZ7-C48" to "X1-GZ7-B33"
    Then the route should not include a REFUEL action
    And the route should include a TRAVEL action to "X1-GZ7-B33"

  Scenario: Ship with barely insufficient fuel should refuel considering safety margin
    Given a waypoint "X1-STATION" at coordinates (0, 0) with fuel station
    And a waypoint "X1-DEST" at coordinates (330, 0) without fuel
    And a ship with 330 current fuel and 400 capacity at "X1-STATION"
    And the ship's engine speed is 30
    When I plan a route from "X1-STATION" to "X1-DEST"
    Then the route should include a REFUEL action
    And the REFUEL action should be the first step
    And the reason should be "safety margin requires 4 extra fuel units"

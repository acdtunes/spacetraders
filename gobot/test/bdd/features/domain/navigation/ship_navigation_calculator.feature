Feature: Ship Navigation Calculator
  As a domain service
  The ShipNavigationCalculator provides navigation calculations for ships
  To support route planning and navigation decisions

  Background:
    Given a ship navigation calculator

  Scenario: Calculate travel time in CRUISE mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time from "X1-A1" to "X1-B2" in CRUISE mode with engine speed 30
    Then the travel time should be 200 seconds

  Scenario: Calculate travel time in DRIFT mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time from "X1-A1" to "X1-B2" in DRIFT mode with engine speed 30
    Then the travel time should be 2000 seconds

  Scenario: Calculate travel time in BURN mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate travel time from "X1-A1" to "X1-B2" in BURN mode with engine speed 30
    Then the travel time should be 66 seconds

  Scenario: Calculate distance between waypoints
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 100.0

  Scenario: Calculate distance for diagonal path
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (3, 4)
    When I calculate distance from "X1-A1" to "X1-B2"
    Then the distance should be 5.0

  Scenario: Check if ship is at target location
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-A1-COPY" at coordinates (0, 0)
    When I check if at location "X1-A1" when current is "X1-A1"
    Then the result should be true

  Scenario: Check if ship is not at different location
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I check if at location "X1-B2" when current is "X1-A1"
    Then the result should be false

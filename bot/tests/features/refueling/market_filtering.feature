Feature: Market Filtering and Fuel Station Detection
  As a tour optimizer
  I want to correctly identify and exclude fuel stations
  So that scout tours only include actual marketplaces

  Scenario: Get markets excludes fuel stations
    Given a system with waypoints:
      | waypoint    | type         | traits                        | has_fuel |
      | X1-TEST-A1  | PLANET       | MARKETPLACE                   | yes      |
      | X1-TEST-B7  | ASTEROID     | MARKETPLACE,METAL_DEPOSITS    | yes      |
      | X1-TEST-C5  | MOON         | MARKETPLACE,SHIPYARD          | yes      |
      | X1-TEST-F1  | FUEL_STATION | MARKETPLACE                   | yes      |
      | X1-TEST-F2  | FUEL_STATION | MARKETPLACE                   | yes      |
      | X1-TEST-D9  | ASTEROID     | METAL_DEPOSITS                | no       |
    When I call get_markets_from_graph
    Then there should be exactly 3 markets
    And the markets should include "X1-TEST-A1"
    And the markets should include "X1-TEST-B7"
    And the markets should include "X1-TEST-C5"
    And the markets should NOT include "X1-TEST-F1"
    And the markets should NOT include "X1-TEST-F2"
    And the markets should NOT include "X1-TEST-D9"

  Scenario: Handle empty graph gracefully
    Given an empty graph
    When I call get_markets_from_graph
    Then the result should be an empty list

  Scenario: Handle graph with no waypoints
    Given a graph with no waypoints
    When I call get_markets_from_graph
    Then the result should be an empty list

  Scenario: Handle None graph gracefully
    Given a None graph
    When I call get_markets_from_graph
    Then the result should be an empty list

  @xfail
  Scenario: Handle waypoints with missing type field
    Given a system with waypoints:
      | waypoint    | type   | traits      | has_fuel |
      | X1-TEST-A1  |        | MARKETPLACE | yes      |
      | X1-TEST-B7  | PLANET | MARKETPLACE | yes      |
    When I call get_markets_from_graph
    Then there should be exactly 2 markets
    And both waypoints should be included

  Scenario: Scout coordinator uses filtered markets
    Given a system with 2 real markets and 1 fuel station
    When the scout coordinator loads markets
    Then the coordinator should have exactly 2 markets
    And the fuel station should NOT be in coordinator's market list

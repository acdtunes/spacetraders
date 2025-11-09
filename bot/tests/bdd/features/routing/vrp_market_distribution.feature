Feature: VRP Market Distribution
  As a fleet coordinator
  I want the VRP solver to distribute all markets across available ships
  So that no markets are dropped during optimization

  Background:
    Given a navigation graph with waypoints:
      | symbol      | x    | y    | type       | has_fuel |
      | X1-HZ85-A1  | 0    | 0    | PLANET     | true     |
      | X1-HZ85-B3  | 100  | 100  | MOON       | false    |
      | X1-HZ85-C5  | -50  | 150  | ASTEROID   | false    |
      | X1-HZ85-D7  | 200  | -100 | PLANET     | true     |

  Scenario: Distribute 3 markets across 3 ships without dropping any
    Given ship "SCOUT-1" at waypoint "X1-HZ85-A1" with fuel 400/400 and engine speed 30
    And ship "SCOUT-2" at waypoint "X1-HZ85-B3" with fuel 400/400 and engine speed 30
    And ship "SCOUT-3" at waypoint "X1-HZ85-C5" with fuel 400/400 and engine speed 30
    When I optimize fleet tour for markets:
      | X1-HZ85-A1 |
      | X1-HZ85-B3 |
      | X1-HZ85-C5 |
    Then all 3 markets should be assigned to ships
    And each ship should have at least 1 market assigned
    And no markets should be dropped

  Scenario: Distribute 6 markets across 3 ships without dropping any
    Given ship "SCOUT-1" at waypoint "X1-HZ85-A1" with fuel 400/400 and engine speed 30
    And ship "SCOUT-2" at waypoint "X1-HZ85-B3" with fuel 400/400 and engine speed 30
    And ship "SCOUT-3" at waypoint "X1-HZ85-C5" with fuel 400/400 and engine speed 30
    And a navigation graph with additional market waypoints:
      | symbol      | x    | y    | type       | has_fuel |
      | X1-HZ85-E1  | 50   | 50   | MOON       | false    |
      | X1-HZ85-F2  | -100 | -50  | ASTEROID   | false    |
      | X1-HZ85-G3  | 150  | 200  | PLANET     | true     |
    When I optimize fleet tour for markets:
      | X1-HZ85-A1 |
      | X1-HZ85-B3 |
      | X1-HZ85-C5 |
      | X1-HZ85-E1 |
      | X1-HZ85-F2 |
      | X1-HZ85-G3 |
    Then all 6 markets should be assigned to ships
    And each ship should have at least 1 market assigned
    And no markets should be dropped

  Scenario: Distribute 6 markets across 5 ships with balanced load
    Given ship "SCOUT-1" at waypoint "X1-HZ85-A1" with fuel 400/400 and engine speed 30
    And ship "SCOUT-2" at waypoint "X1-HZ85-B3" with fuel 400/400 and engine speed 30
    And ship "SCOUT-3" at waypoint "X1-HZ85-C5" with fuel 400/400 and engine speed 30
    And ship "SCOUT-4" at waypoint "X1-HZ85-D7" with fuel 400/400 and engine speed 30
    And ship "SCOUT-5" at waypoint "X1-HZ85-E1" with fuel 400/400 and engine speed 30
    And a navigation graph with additional market waypoints:
      | symbol      | x    | y    | type       | has_fuel |
      | X1-HZ85-E1  | 50   | 50   | MOON       | false    |
      | X1-HZ85-F2  | -100 | -50  | ASTEROID   | false    |
    When I optimize fleet tour for 6 markets
    Then all 6 markets should be assigned to ships
    And load should be balanced across ships
    And no markets should be dropped

  Scenario: Distance matrix uses actual pathfinding metrics instead of straight-line distance
    Given a navigation graph with waypoints:
      | symbol      | x    | y    | type       | has_fuel |
      | X1-HZ85-A1  | 0    | 0    | PLANET     | true     |
      | X1-HZ85-B3  | 212  | 212  | PLANET     | true     |
      | X1-HZ85-C5  | 424  | 424  | MOON       | false    |
    And ship "SCOUT-1" at waypoint "X1-HZ85-A1" with fuel 400/400 and engine speed 30
    And ship "SCOUT-2" at waypoint "X1-HZ85-B3" with fuel 400/400 and engine speed 30
    And ship "SCOUT-3" at waypoint "X1-HZ85-C5" with fuel 400/400 and engine speed 30
    When I build the VRP distance matrix for markets:
      | X1-HZ85-A1 |
      | X1-HZ85-B3 |
      | X1-HZ85-C5 |
    Then the distance from "X1-HZ85-A1" to "X1-HZ85-C5" should reflect pathfinding with refueling
    And the distance from "X1-HZ85-A1" to "X1-HZ85-B3" should reflect direct pathfinding
    And the distance from "X1-HZ85-B3" to "X1-HZ85-C5" should reflect direct pathfinding

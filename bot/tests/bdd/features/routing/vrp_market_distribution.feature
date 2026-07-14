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

  # sp-t73c: two probes sharing a start must never collapse to an empty/degenerate
  # partition. Reproduces the KM70 5E+5F case (5 MOON + 5 ASTEROID markets, 2 ships).
  # Two markets (F4, F5) are fuel-unreachable at 400 capacity with no fuel en route,
  # so the VRP drops them; the engine used to raise on any drop, which upstream became
  # an empty partition (0 tours) while single-ship — which bypasses the VRP and keeps
  # ALL markets — worked. The fix must keep parity: partition every market, none dropped.
  Scenario: Two ships sharing a start keep every market despite an unreachable outlier
    Given a navigation graph with waypoints:
      | symbol      | x     | y     | type     | has_fuel |
      | X1-KM70-ZY1 | 0     | 0     | PLANET   | true     |
      | X1-KM70-E1  | 100   | 0     | MOON     | false    |
      | X1-KM70-E2  | 0     | 100   | MOON     | false    |
      | X1-KM70-E3  | -100  | 0     | MOON     | false    |
      | X1-KM70-E4  | 0     | -100  | MOON     | false    |
      | X1-KM70-E5  | 80    | 80    | MOON     | false    |
      | X1-KM70-F1  | -80   | -80   | ASTEROID | false    |
      | X1-KM70-F2  | 120   | -40   | ASTEROID | false    |
      | X1-KM70-F3  | -40   | 120   | ASTEROID | false    |
      | X1-KM70-F4  | 1200  | 0     | ASTEROID | false    |
      | X1-KM70-F5  | 0     | 1200  | ASTEROID | false    |
    And ship "TORWIND-5E" at waypoint "X1-KM70-ZY1" with fuel 400/400 and engine speed 30
    And ship "TORWIND-5F" at waypoint "X1-KM70-ZY1" with fuel 400/400 and engine speed 30
    When I optimize fleet tour for markets:
      | X1-KM70-E1 |
      | X1-KM70-E2 |
      | X1-KM70-E3 |
      | X1-KM70-E4 |
      | X1-KM70-E5 |
      | X1-KM70-F1 |
      | X1-KM70-F2 |
      | X1-KM70-F3 |
      | X1-KM70-F4 |
      | X1-KM70-F5 |
    Then all 10 markets should be assigned to ships
    And each ship should have at least 1 market assigned
    And no markets should be dropped
    And load should be balanced across ships

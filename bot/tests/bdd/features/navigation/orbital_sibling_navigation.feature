Feature: Orbital Sibling Navigation
  As a fleet manager
  I want ships to navigate efficiently between orbital siblings (moon to moon)
  So that VRP can distribute markets across orbital moons without treating them as unreachable

  Background:
    Given a system with orbital waypoints:
      | symbol     | type    | x    | y    | orbitals  |
      | X1-TEST-A1 | PLANET  | 0.0  | 0.0  | A2,A3     |
      | X1-TEST-A2 | MOON    | 0.0  | 0.0  |           |
      | X1-TEST-A3 | MOON    | 0.0  | 0.0  |           |
    And the graph has orbital edges:
      | from       | to         | distance | type    |
      | X1-TEST-A1 | X1-TEST-A2 | 0.0      | orbital |
      | X1-TEST-A2 | X1-TEST-A1 | 0.0      | orbital |
      | X1-TEST-A1 | X1-TEST-A3 | 0.0      | orbital |
      | X1-TEST-A3 | X1-TEST-A1 | 0.0      | orbital |
      | X1-TEST-A2 | X1-TEST-A3 | 0.0      | orbital |
      | X1-TEST-A3 | X1-TEST-A2 | 0.0      | orbital |
    And I have a ship with 400 fuel capacity and 30 engine speed

  Scenario: Navigate between orbital siblings (moon to moon)
    Given the ship is at waypoint "X1-TEST-A2"
    And the ship has 400 fuel
    When I plan a route from "X1-TEST-A2" to "X1-TEST-A3"
    Then the route should exist
    And the route should have 1 step
    And the route step should be:
      | action | from       | to         | mode   | fuel_cost | distance |
      | TRAVEL | X1-TEST-A2 | X1-TEST-A3 | CRUISE | 0         | 0.0      |
    And the route total time should be 1 seconds

  Scenario: VRP distance matrix uses graph edges for orbital siblings
    Given ships are positioned:
      | ship        | location   |
      | SCOUT-1     | X1-TEST-A1 |
      | SCOUT-2     | X1-TEST-A2 |
    And markets are at:
      | market     |
      | X1-TEST-A1 |
      | X1-TEST-A2 |
      | X1-TEST-A3 |
    When I build the VRP distance matrix for fleet optimization
    Then the distance matrix should show:
      | from       | to         | time     |
      | X1-TEST-A1 | X1-TEST-A2 | 1        |
      | X1-TEST-A1 | X1-TEST-A3 | 1        |
      | X1-TEST-A2 | X1-TEST-A3 | 1        |
      | X1-TEST-A2 | X1-TEST-A1 | 1        |
      | X1-TEST-A3 | X1-TEST-A1 | 1        |
      | X1-TEST-A3 | X1-TEST-A2 | 1        |
    And all markets should be reachable (no 1,000,000 distances)

  Scenario: VRP distributes markets across ships when markets are orbital siblings
    Given ships are positioned:
      | ship    | location   |
      | SCOUT-1 | X1-TEST-A1 |
      | SCOUT-2 | X1-TEST-A2 |
    And markets are at:
      | market     |
      | X1-TEST-A1 |
      | X1-TEST-A2 |
      | X1-TEST-A3 |
    When I optimize fleet market partitioning
    Then all 3 markets should be assigned to ships
    And no markets should be dropped
    And the assignments should distribute markets across available ships

  Scenario: Navigate from planet to moon (direct parent-child)
    Given the ship is at waypoint "X1-TEST-A1"
    And the ship has 400 fuel
    When I plan a route from "X1-TEST-A1" to "X1-TEST-A2"
    Then the route should exist
    And the route should have 1 step
    And the route step should be:
      | action | from       | to         | mode   | fuel_cost | distance |
      | TRAVEL | X1-TEST-A1 | X1-TEST-A2 | CRUISE | 0         | 0.0      |
    And the route total time should be 1 seconds

  Scenario: Navigate from moon to planet (direct child-parent)
    Given the ship is at waypoint "X1-TEST-A2"
    And the ship has 400 fuel
    When I plan a route from "X1-TEST-A2" to "X1-TEST-A1"
    Then the route should exist
    And the route should have 1 step
    And the route step should be:
      | action | from       | to         | mode   | fuel_cost | distance |
      | TRAVEL | X1-TEST-A2 | X1-TEST-A1 | CRUISE | 0         | 0.0      |
    And the route total time should be 1 seconds

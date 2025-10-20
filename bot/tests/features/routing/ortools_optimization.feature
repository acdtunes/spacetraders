Feature: OR-Tools TSP/VRP Optimization
  As a fleet routing system
  I want OR-Tools to produce optimal tours without crossing edges
  So that ships follow efficient, non-intersecting paths

  Background:
    Given OR-Tools TSP solver is configured
    And production routing configuration is loaded

  # =========================================================================
  # Crossing Edges Detection
  # =========================================================================

  Scenario: Simple 3x3 grid should have no crossing edges
    Given a 3x3 grid graph with waypoints:
      | waypoint    | x   | y   |
      | X1-TEST-A1  | 0   | 0   |
      | X1-TEST-A2  | 0   | 100 |
      | X1-TEST-A3  | 0   | 200 |
      | X1-TEST-B1  | 100 | 0   |
      | X1-TEST-B2  | 100 | 100 |
      | X1-TEST-B3  | 100 | 200 |
      | X1-TEST-C1  | 200 | 0   |
      | X1-TEST-C2  | 200 | 100 |
      | X1-TEST-C3  | 200 | 200 |
    And a ship with 400 fuel capacity at "X1-TEST-A1"
    When I optimize a tour starting at "X1-TEST-A1" visiting all waypoints
    Then the tour should complete successfully
    And the tour should have 0 crossing edges

  Scenario: OR-Tools should produce better tours than 2-opt on scattered waypoints
    Given a scattered waypoint graph with 12 waypoints
    And waypoints are positioned to induce crossings with naive algorithms
    And a ship with 400 fuel capacity
    When I run OR-Tools TSP optimization with 30-second timeout
    Then the tour should have fewer crossings than 2-opt heuristic
    Or the tour should have shorter total distance than 2-opt

  Scenario: Long-running OR-Tools should eliminate all crossings
    Given actual X1-VH85 waypoint coordinates from production database
    And 20 waypoints to visit
    And a ship with 400 fuel capacity
    When I run OR-Tools TSP with 5-minute timeout
    Then the tour should have 0 crossing edges
    And total distance should be near-optimal

  Scenario: Metaheuristic selection affects crossing elimination
    Given actual X1-VH85 waypoint coordinates
    And 20 waypoints to visit
    And a ship with 400 fuel capacity
    When I compare metaheuristics:
      | metaheuristic       | timeout_ms |
      | GUIDED_LOCAL_SEARCH | 30000      |
      | SIMULATED_ANNEALING | 30000      |
      | TABU_SEARCH         | 30000      |
    Then at least one metaheuristic should produce 0 crossings
    And the best configuration should be logged

  # =========================================================================
  # Real Coordinate Handling
  # =========================================================================

  Scenario: OR-Tools handles real X1-VH85 coordinates correctly
    Given actual X1-VH85 system graph from production database
    And waypoints include:
      | waypoint      | x     | y     |
      | X1-VH85-A1    | 19    | 15    |
      | X1-VH85-B6    | 149   | 119   |
      | X1-VH85-B7    | 337   | 76    |
      | X1-VH85-E53   | 34    | -42   |
      | X1-VH85-G58   | -50   | -43   |
      | X1-VH85-H59   | -36   | 24    |
    And a ship with 400 fuel capacity at "X1-VH85-A1"
    When I optimize a tour visiting all waypoints
    Then the tour should complete in under 30 seconds
    And tour distance should be calculated using Euclidean distance
    And tour should be feasible with available fuel

  Scenario: Cached tour from production shows crossing edges (regression test)
    Given the cached X1-VH85 tour from production:
      """
      X1-VH85-A1 -> X1-VH85-AC5D -> X1-VH85-H59 -> X1-VH85-H62 ->
      X1-VH85-H61 -> X1-VH85-H60 -> X1-VH85-G58 -> X1-VH85-E53 ->
      X1-VH85-E54 -> X1-VH85-B7 -> X1-VH85-B6 -> X1-VH85-F55 ->
      X1-VH85-F56 -> X1-VH85-D51 -> X1-VH85-D50 -> X1-VH85-D49 ->
      X1-VH85-D48 -> X1-VH85-A4 -> X1-VH85-A3 -> X1-VH85-A2 -> X1-VH85-A1
      """
    When I analyze the cached tour for crossing edges
    Then the tour should have more than 0 crossing edges
    And this confirms the bug exists in production

  Scenario: OR-Tools with extended timeout eliminates cached tour crossings
    Given the cached X1-VH85 tour with crossing edges
    And actual X1-VH85 waypoint coordinates
    And a ship with 400 fuel capacity
    When I re-optimize with OR-Tools using 5-minute timeout
    Then the new tour should have 0 crossing edges
    And total distance should be less than or equal to cached tour distance

  Scenario: Compare OR-Tools performance across timeout and metaheuristic configurations
    Given actual X1-VH85 waypoint coordinates with 20 waypoints
    And a ship with 400 fuel capacity
    When I benchmark all configurations:
      | config_name                  | metaheuristic       | timeout_ms |
      | GUIDED_LOCAL_SEARCH, 30s     | GUIDED_LOCAL_SEARCH | 30000      |
      | GUIDED_LOCAL_SEARCH, 5min    | GUIDED_LOCAL_SEARCH | 300000     |
      | SIMULATED_ANNEALING, 30s     | SIMULATED_ANNEALING | 30000      |
      | TABU_SEARCH, 30s             | TABU_SEARCH         | 30000      |
    Then results should be ranked by crossing count ascending, then distance ascending
    And best configuration should produce 0 crossings
    And benchmark results should be displayed in comparison table

  Scenario: Coordinate precision does not cause floating-point errors
    Given waypoints with fractional coordinates:
      | waypoint    | x       | y       |
      | X1-TEST-A1  | 19.7    | 15.3    |
      | X1-TEST-B2  | 149.2   | 119.8   |
      | X1-TEST-C3  | 337.1   | 76.4    |
    And a ship with 400 fuel capacity
    When I optimize a tour with OR-Tools
    Then distance calculations should maintain precision
    And tour should be valid with no NaN or infinity values

  # =========================================================================
  # VRP Fleet Optimization
  # =========================================================================

  Scenario: OR-Tools VRP distributes waypoints across multiple ships
    Given a system with 20 waypoints
    And 4 ships with different fuel capacities:
      | ship         | fuel_capacity | current_location |
      | SHIP-1       | 400           | X1-TEST-A1       |
      | SHIP-2       | 350           | X1-TEST-A1       |
      | SHIP-3       | 450           | X1-TEST-A1       |
      | SHIP-4       | 300           | X1-TEST-A1       |
    When I use OR-Tools VRP to partition waypoints across ships
    Then each ship should receive a disjoint subset of waypoints
    And total waypoints covered should equal 20
    And each ship's tour should be fuel-feasible
    And tours should minimize total fleet time

  Scenario: VRP handles heterogeneous ship speeds and fuel capacities
    Given a system with 15 waypoints spread across 500-unit area
    And ships with varying capabilities:
      | ship    | speed | fuel_capacity | cargo_capacity |
      | FAST-1  | 40    | 300           | 30             |
      | SLOW-1  | 20    | 500           | 60             |
      | MED-1   | 30    | 400           | 40             |
    When I optimize VRP assignment
    Then faster ships should receive longer-distance waypoints
    And slower ships with larger fuel tanks should get efficient routes
    And total fleet completion time should be minimized

  # =========================================================================
  # Edge Cases
  # =========================================================================

  Scenario: OR-Tools handles single waypoint tour
    Given a system with 1 waypoint "X1-TEST-A1"
    And a ship at "X1-TEST-A1"
    When I optimize a tour
    Then the tour should be [X1-TEST-A1]
    And total distance should be 0
    And optimization should complete in under 1 second

  Scenario: OR-Tools handles duplicate coordinates (orbitals)
    Given waypoints sharing same coordinates (planet + moons):
      | waypoint    | x   | y   | type   |
      | X1-TEST-A1  | 10  | 20  | PLANET |
      | X1-TEST-A2  | 10  | 20  | MOON   |
      | X1-TEST-A3  | 10  | 20  | MOON   |
      | X1-TEST-B5  | 50  | 60  | PLANET |
    And a ship with 400 fuel capacity
    When I optimize a tour visiting all waypoints
    Then the tour should handle duplicate coordinates gracefully
    And waypoints at same coordinates should be visited consecutively
    And total distance should account for 0-distance transitions

  Scenario: OR-Tools handles very sparse graphs (long distances)
    Given waypoints separated by 1000+ units:
      | waypoint    | x     | y     |
      | X1-TEST-A1  | 0     | 0     |
      | X1-TEST-B2  | 1200  | 800   |
      | X1-TEST-C3  | -900  | 1100  |
    And a ship with 1200 fuel capacity
    When I optimize a tour with DRIFT mode fuel constraints
    Then the tour should insert refuel stops as needed
    And tour should be fuel-feasible
    And total distance should be minimized

  Scenario: OR-Tools handles dense graphs (many nearby waypoints)
    Given a cluster of 50 waypoints within 200-unit radius
    And a ship with 400 fuel capacity
    When I optimize a tour with 60-second timeout
    Then the tour should complete within timeout
    And tour should visit all 50 waypoints
    And crossings should be minimized or eliminated
    And solution time should be logged for performance tracking

Feature: Fleet Partitioning with OR-Tools VRP
  As a scout coordinator
  I want to partition waypoints across multiple ships
  So that each ship visits a disjoint subset without overlap

  Background:
    Given ORToolsFleetPartitioner is configured with production settings
    And fleet has multiple ships with varying capabilities

  # =========================================================================
  # Critical Bug: Duplicate Waypoint Assignment
  # =========================================================================

  Scenario: Partitioner must not assign same waypoint to multiple ships
    Given a simplified X1-VH85 system with 8 markets:
      | waypoint     | x    | y    |
      | X1-VH85-A2   | 10   | 12   |
      | X1-VH85-A3   | -12  | 5    |
      | X1-VH85-C46  | -41  | -6   |
      | X1-VH85-E53  | 18   | -49  |
      | X1-VH85-H62  | 32   | -22  |
      | X1-VH85-I64  | -34  | 17   |
      | X1-VH85-J66  | 52   | 41   |
      | X1-VH85-K92  | -53  | 0    |
    And 4 scout ships starting at "X1-VH85-A2":
      | ship            | fuel_capacity | speed |
      | DRAGONSPYRE-2   | 400           | 30    |
      | DRAGONSPYRE-3   | 400           | 30    |
      | DRAGONSPYRE-4   | 400           | 30    |
      | DRAGONSPYRE-5   | 400           | 30    |
    When I partition markets across the fleet
    Then no waypoint should appear in multiple ship assignments
    And specifically "X1-VH85-E53" should NOT be in both Tour 2 and Tour 4
    And all 8 markets should be assigned exactly once

  Scenario: Verify mathematical disjoint property of partitions
    Given a system with 20 markets
    And 5 scout ships
    When markets are partitioned using OR-Tools VRP
    Then for any two ships A and B: assigned_markets(A) ∩ assigned_markets(B) = ∅
    And no market should appear in more than one ship's assignment
    And union of all assignments should equal the full market set

  Scenario: All markets must be assigned (no lost waypoints)
    Given a system with 15 markets
    And 3 scout ships
    When markets are partitioned
    Then every market should be assigned to exactly one ship
    And no markets should be missing from assignments
    And total assigned markets across all ships should equal 15

  # =========================================================================
  # Partition Quality and Balance
  # =========================================================================

  Scenario: Partitioner balances workload across ships
    Given a system with 20 markets spread across 1000-unit area
    And 4 scout ships with equal capabilities
    When markets are partitioned
    Then each ship should receive approximately 5 markets (±2)
    And no ship should be assigned 0 markets
    And no ship should be assigned more than 8 markets
    And total travel distance should be balanced across ships

  Scenario: Partitioner respects ship fuel constraints
    Given a system with 15 markets at varying distances
    And ships with different fuel capacities:
      | ship    | fuel_capacity | current_location |
      | SCOUT-1 | 400           | X1-TEST-A1       |
      | SCOUT-2 | 200           | X1-TEST-A1       |
      | SCOUT-3 | 600           | X1-TEST-A1       |
    When markets are partitioned
    Then SCOUT-2 (low fuel) should receive nearby markets only
    And SCOUT-3 (high fuel) can receive distant markets
    And all ship tours should be fuel-feasible
    And no ship should require refuel stops mid-tour

  Scenario: Partitioner optimizes for total fleet completion time
    Given a system with 20 markets
    And ships with different speeds:
      | ship    | speed | fuel_capacity |
      | FAST-1  | 40    | 400           |
      | SLOW-1  | 20    | 400           |
      | MED-1   | 30    | 400           |
    When markets are partitioned and tours optimized
    Then faster ships should receive longer-distance tours
    And slower ships should receive compact, nearby clusters
    And total fleet completion time should be minimized
    And all ships should finish near-simultaneously

  # =========================================================================
  # Deduplication Logic
  # =========================================================================

  Scenario: Partitioner deduplicates waypoints within single ship tour
    Given a ship assigned to visit markets [A1, B2, A1, C3, B2]
    When partitioner applies deduplication
    Then resulting tour should be [A1, B2, C3]
    And duplicate waypoints should be removed
    And tour order should be preserved

  Scenario: Deduplication maintains optimal tour order
    Given a ship tour with duplicates: [A1, B2, C3, B2, D4, A1]
    When deduplication is applied
    Then duplicates should be removed
    And resulting tour should minimize total distance
    And tour should start and end at ship's current location

  Scenario: Partitioner handles case where all waypoints are duplicates
    Given a ship assigned to visit [A1, A1, A1, A1]
    When deduplication is applied
    Then resulting tour should be [A1]
    And ship should not error on single-waypoint tour
    And total distance should be 0

  # =========================================================================
  # Edge Cases
  # =========================================================================

  Scenario: Partition with more ships than markets
    Given a system with 3 markets
    And 5 scout ships
    When markets are partitioned
    Then 3 ships should receive 1 market each
    And 2 ships should remain unassigned (empty tours)
    And assigned ships should have optimal routes
    And unassigned ships should be clearly marked

  Scenario: Partition with single ship (degenerates to TSP)
    Given a system with 10 markets
    And 1 scout ship
    When markets are partitioned
    Then ship should receive all 10 markets
    And partition should reduce to TSP optimization
    And tour should have minimal distance
    And no crossing edges should exist

  Scenario: Partition with single market
    Given a system with 1 market "X1-TEST-M1"
    And 3 scout ships
    When markets are partitioned
    Then 1 ship should be assigned to "X1-TEST-M1"
    And 2 ships should remain unassigned
    And assignment should complete without errors

  Scenario: Partition handles empty market list
    Given a system with 0 markets
    And 3 scout ships
    When markets are partitioned
    Then all ships should have empty assignments
    And partitioner should return empty dictionary
    And no errors should occur
    And warning should be logged about no markets

  # =========================================================================
  # Geographical Clustering
  # =========================================================================

  Scenario: Partitioner creates geographical clusters
    Given a system with markets clustered in 3 regions:
      | region | markets                          |
      | North  | N1, N2, N3, N4                   |
      | South  | S1, S2, S3, S4                   |
      | East   | E1, E2, E3, E4                   |
    And 3 scout ships
    When markets are partitioned
    Then each ship should receive markets primarily from one region
    And cross-region assignments should be minimized
    And total inter-region travel distance should be minimized

  Scenario: Partitioner handles scattered waypoints
    Given a system with 20 markets uniformly distributed
    And no clear geographical clusters
    And 4 scout ships
    When markets are partitioned
    Then waypoints should be distributed to minimize total distance
    And each ship's tour should form a compact path
    And Voronoi-like partitioning should emerge naturally

  # =========================================================================
  # Integration with Tour Optimization
  # =========================================================================

  Scenario: Partitioned tours should be individually optimized
    Given a system with 20 markets
    And 4 scout ships
    When markets are partitioned and tours generated
    Then each ship's tour should be TSP-optimized
    And each tour should have 0 crossing edges
    And each tour should minimize distance for assigned waypoints
    And partitioning + optimization should complete in under 5 seconds

  Scenario: Re-partitioning after ship failure
    Given initial partition across 4 ships
    And ship SCOUT-2 becomes unavailable mid-operation
    When markets are re-partitioned across remaining 3 ships
    Then markets originally assigned to SCOUT-2 should be redistributed
    And new partitions should remain disjoint
    And no markets should be lost
    And total fleet completion time should be re-optimized

  # =========================================================================
  # Performance and Scalability
  # =========================================================================

  Scenario: Partitioner handles large fleet and market count
    Given a system with 100 markets
    And 10 scout ships
    When markets are partitioned with 60-second timeout
    Then partitioning should complete within timeout
    And all 100 markets should be assigned
    And partitions should be disjoint
    And solution quality should be logged

  Scenario: Partitioner performance with small problem size
    Given a system with 5 markets
    And 2 scout ships
    When markets are partitioned
    Then partitioning should complete in under 1 second
    And optimal solution should be found
    And computational overhead should be minimal

  # =========================================================================
  # Validation and Invariants
  # =========================================================================

  Scenario: Partitioner validates input constraints
    Given invalid input: ships list is empty
    When partitioning is attempted
    Then partitioner should raise clear error
    And error message should indicate "no ships available"
    And operation should fail gracefully

  Scenario: Partitioner validates market uniqueness in input
    Given markets list contains duplicates: [M1, M2, M1, M3]
    When partitioning is attempted
    Then partitioner should deduplicate input
    Or partitioner should raise error about duplicate markets
    And resulting partitions should be valid

  Scenario: Verify partition invariants are maintained
    Given any valid partition result
    Then the following invariants must hold:
      | invariant                                    |
      | All markets are assigned                     |
      | No market appears in multiple ships          |
      | Each ship has disjoint waypoint set          |
      | Union of all assignments equals input markets|
      | All ship tours are fuel-feasible             |

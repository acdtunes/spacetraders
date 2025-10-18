Feature: Routing Regression Bug Fixes
  As a routing system
  I want to prevent regression of previously fixed bugs
  So that production systems remain stable and reliable

  Background:
    Given production routing configuration is loaded
    And all bug fixes are applied

  # =========================================================================
  # Orbital Waypoint Jitter Bug
  # =========================================================================

  Scenario: Orbital waypoints should have exact same coordinates as parent
    Given planet "X1-TEST-A1" at coordinates (100, 200)
    And moon "X1-TEST-A2" orbiting "X1-TEST-A1"
    When graph builder processes orbital data
    Then "X1-TEST-A2" should have exact coordinates (100, 200)
    And coordinates should not have floating-point jitter
    And distance from planet to moon should be exactly 0

  Scenario: OR-Tools handles orbital waypoints without numerical errors
    Given planet "X1-TEST-A1" at (100.0, 200.0)
    And 3 moons orbiting at (100.0, 200.0)
    When OR-Tools TSP optimizes tour including orbitals
    Then solver should not encounter numerical instability
    And 0-distance edges should be handled correctly
    And tour should visit orbitals consecutively (0 fuel transitions)

  Scenario: Orbital jitter was causing incorrect distance calculations
    Given planet "X1-VH85-A1" at (19, 15) from production database
    And moon "X1-VH85-A2" has stored coordinates (19.0001, 15.0002) due to jitter
    When distance is calculated between planet and moon
    Then pre-fix distance would be 0.00022 units (WRONG)
    But post-fix distance should be exactly 0.0 units (CORRECT)
    And this bug was causing unnecessary fuel consumption

  Scenario: Graph builder eliminates coordinate jitter for orbitals
    Given orbital relationship data from SpaceTraders API
    And parent waypoint has precise integer coordinates
    When graph builder processes orbital data
    Then child orbital coordinates should be set to exact parent coordinates
    And no floating-point arithmetic should introduce jitter
    And all orbital distances should be exactly 0

  # =========================================================================
  # Fixed Route Coordinate Bug
  # =========================================================================

  Scenario: Fixed routes should maintain coordinate consistency
    Given a pre-defined fixed route for contract delivery
    And route waypoints have specific coordinates from game data
    When fixed route is loaded into routing system
    Then coordinates should match game data exactly
    And no coordinate transformations should be applied
    And distance calculations should use original coordinates

  Scenario: Fixed route coordinates should not be recalculated
    Given a fixed route with waypoints [A1, B2, C3]
    And waypoints have established coordinates
    When route is used for navigation
    Then coordinates should not be recomputed or normalized
    And route should remain faithful to original specification
    And fuel calculations should use fixed coordinates

  Scenario: Graph builder respects fixed route coordinate overrides
    Given a graph with waypoint "X1-TEST-A1" at (10, 20)
    And a fixed route override specifying "X1-TEST-A1" at (15, 25)
    When graph builder processes fixed route
    Then fixed route coordinates (15, 25) should take precedence
    And graph coordinates should be updated for fixed route waypoints
    And this allows correction of coordinate errors in game data

  # =========================================================================
  # Market Drop Bug (OR-Tools VRP Partitioning)
  # =========================================================================

  Scenario: OR-Tools VRP should not drop markets during partitioning
    Given a real X1-VH85 system with 27 markets from production
    And 4 scout ships to distribute markets across
    When OR-Tools VRP partitioner assigns markets to ships
    Then all 27 markets should be assigned
    And no markets should be dropped or lost
    And union of all ship assignments should equal input market list

  Scenario: VRP partitioner handles disjunction constraints correctly
    Given 30 markets to visit
    And 5 scout ships
    And disjunction penalty is set to 10000 (very high)
    When VRP solver partitions markets
    Then solver should assign markets across all ships
    And no markets should be marked as "dropped" due to penalty
    And high disjunction penalty should encourage full assignment

  Scenario: Market drop was caused by insufficient disjunction penalty
    Given 20 markets and 3 ships
    And disjunction penalty set to 100 (too low - PRE-FIX)
    When VRP solver runs
    Then solver may drop markets to minimize objective
    And dropped markets appear "optional" due to low penalty
    But with penalty set to 10000+ (POST-FIX)
    Then all markets should be assigned
    And this bug fix prevents market loss

  Scenario: Validate no markets dropped in real-world VRP scenario
    Given actual production data from X1-VH85 scout operation
    And 27 markets discovered by market scanner
    And 4 DRAGONSPYRE ships available
    When markets are partitioned for parallel scouting
    Then all 27 markets should be assigned to ships
    And each market should appear in exactly one ship's tour
    And no markets should be silently dropped
    And scout coordinator should visit all markets

  # =========================================================================
  # Orbital Branching Bug (MinCostFlow)
  # =========================================================================

  Scenario: MinCostFlow should handle orbital waypoint branching correctly
    Given planet "X1-TEST-A1" at (50, 50)
    And 3 moons at (50, 50) creating branching paths
    And destination "X1-TEST-Z99" reachable via any moon
    When MinCostFlow solver routes from planet to destination
    Then solver should not oscillate between moon branches
    And solver should select optimal moon for continuation
    And flow should converge without infinite branching

  Scenario: Orbital branching should prefer 0-cost transitions
    Given ship at planet "X1-TEST-A1"
    And moon "X1-TEST-A2" at same coordinates (0-cost edge)
    And moon has refuel station
    And route requires refueling
    When MinCostFlow optimizer plans route
    Then route should use 0-cost orbital transition to moon
    And refuel at moon should be preferred over distant station
    And branching logic should favor 0-cost paths

  Scenario: Prevent orbital branching infinite loops
    Given a graph with cyclic orbital structure
    And waypoints A1 -> A2 -> A3 -> A1 all at same coordinates
    When routing algorithm traverses orbital cycle
    Then algorithm should detect cycle and break
    And route should not loop indefinitely
    And maximum orbital hop count should be enforced

  # =========================================================================
  # Disjunction Penalty Too Low
  # =========================================================================

  Scenario: Disjunction penalty should force all waypoint assignments
    Given OR-Tools VRP with 20 optional waypoints
    And solver uses disjunction to mark waypoints as optional
    And disjunction penalty is 100 (too low)
    When solver optimizes
    Then solver may skip waypoints to reduce objective
    But with penalty increased to 10000
    Then all waypoints should be assigned
    And skipping waypoints becomes prohibitively expensive

  Scenario: Validate disjunction penalty in fleet partitioner
    Given ORToolsFleetPartitioner configuration
    When disjunction penalty parameter is checked
    Then penalty should be >= 10000
    And penalty should be >> typical arc cost
    And this ensures waypoints are not dropped during VRP

  # =========================================================================
  # MinCostFlow Cycle Detection
  # =========================================================================

  Scenario: MinCostFlow should detect and prevent flow cycles
    Given a graph with potential flow cycle A -> B -> C -> A
    And MinCostFlow solver routes from A to destination D
    When solver computes flow
    Then solver should detect cycle
    And solver should either break cycle or raise error
    And solver should not enter infinite loop
    And valid acyclic route should be produced

  Scenario: Zero-cost cycles should be prevented
    Given a graph with 0-cost cycle (orbital waypoints)
    When MinCostFlow solver processes graph
    Then solver should recognize 0-cost cycles are harmless if acyclic
    But if actual flow cycles form, solver should break them
    And route should be a valid directed acyclic graph (DAG)

  # =========================================================================
  # Integration Tests: Bug Fix Validation
  # =========================================================================

  Scenario: SILMARETH-1 contract failure should not recur (integration test)
    Given ship "SILMARETH-1" at "X1-GH18-H57"
    And contract requires delivery to "X1-GH18-J62"
    And market selection considers distance (Bug #3 fix)
    And A* iteration limit is 50000+ (Bug #1 fix)
    And error messages distinguish fuel vs pathfinding (Bug #2 fix)
    When contract operation executes
    Then market selection should choose nearby market (not H57)
    And route planning should succeed for 700+ unit paths
    And error messages should be accurate if failure occurs
    And ship should not get stranded with cargo

  Scenario: X1-VH85 scout operation should assign all 27 markets (integration test)
    Given X1-VH85 system with 27 markets
    And 4 DRAGONSPYRE scout ships
    And fleet partitioner with disjunction penalty 10000+
    And orbital jitter fix applied to graph
    When scout coordinator partitions markets
    Then all 27 markets should be assigned without drops
    And orbital waypoint coordinates should be exact
    And no duplicate waypoint assignments across ships
    And scout operation should complete all markets

  Scenario: OR-Tools TSP should produce crossing-free tours on real data (integration test)
    Given actual X1-VH85 waypoint coordinates
    And 20 waypoints to visit
    And OR-Tools with 5-minute timeout and GUIDED_LOCAL_SEARCH
    When TSP optimization runs
    Then tour should have 0 crossing edges
    And tour should be near-optimal (within 5% of best known)
    And this confirms crossing edge bug is fixed

  # =========================================================================
  # Regression Test Suite Metadata
  # =========================================================================

  Scenario: Verify all critical bug fixes are tested
    Given the routing regression test suite
    Then the following bugs should have regression tests:
      | bug_id | description                              | test_count |
      | BUG-1  | A* iteration limit too low               | 3          |
      | BUG-2  | Misleading insufficient fuel errors      | 3          |
      | BUG-3  | Market selection ignores distance        | 2          |
      | BUG-4  | Orbital waypoint coordinate jitter       | 4          |
      | BUG-5  | Market drop in VRP partitioning          | 4          |
      | BUG-6  | Orbital branching in MinCostFlow         | 3          |
      | BUG-7  | Disjunction penalty too low              | 2          |
      | BUG-8  | MinCostFlow flow cycles                  | 2          |
      | BUG-9  | OR-Tools TSP crossing edges              | 6          |
      | BUG-10 | Duplicate waypoints in fleet partition   | 3          |
    And total regression test coverage should be 32+ scenarios
    And all bugs should have at least 2 test scenarios
    And integration tests should validate end-to-end bug fixes

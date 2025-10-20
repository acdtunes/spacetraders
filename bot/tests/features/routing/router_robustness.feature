Feature: Router Robustness and Fault Tolerance
  As a routing system
  I want to gracefully handle failures and edge cases
  So that navigation never deadlocks or produces invalid routes

  Background:
    Given SmartNavigator and ORToolsRouter are configured
    And fallback mechanisms are enabled

  # =========================================================================
  # Fallback to Dijkstra When OR-Tools Fails
  # =========================================================================

  Scenario: OR-Tools timeout triggers Dijkstra fallback
    Given a large graph with 200+ waypoints
    And OR-Tools TSP timeout is set to 5 seconds
    And ship must visit 50 waypoints
    When OR-Tools times out without finding solution
    Then router should automatically fall back to Dijkstra algorithm
    And Dijkstra should find a valid (non-optimal) route
    And warning should be logged about fallback
    And route should be fuel-feasible

  Scenario: OR-Tools failure on complex VRP triggers graceful degradation
    Given a VRP problem with 100 waypoints and 10 ships
    And OR-Tools solver encounters internal error
    When VRP optimization fails
    Then partitioner should fall back to greedy assignment
    And each ship should receive waypoints via nearest-neighbor heuristic
    And all waypoints should still be assigned
    And system should not crash or hang

  Scenario: Dijkstra fallback produces valid but suboptimal route
    Given a system with 20 waypoints
    And OR-Tools cannot find solution within timeout
    When Dijkstra fallback is triggered
    Then route should visit all waypoints
    And route should be fuel-feasible
    And route may have crossing edges (suboptimal)
    But route must be valid and executable
    And total distance may be 10-30% higher than optimal

  Scenario: Compare OR-Tools vs Dijkstra solution quality
    Given a system with 15 waypoints
    When both OR-Tools (with 30s timeout) and Dijkstra are run
    Then OR-Tools solution should have equal or better distance
    And OR-Tools solution should have fewer or equal crossing edges
    And if OR-Tools fails, Dijkstra still provides usable route
    And performance difference should be logged

  # =========================================================================
  # Hang Prevention (OR-Tools Solver)
  # =========================================================================

  Scenario: OR-Tools VRP solver does not hang indefinitely
    Given a complex VRP problem with 50 waypoints and 5 ships
    And solver timeout is set to 60 seconds
    When OR-Tools VRP solver is invoked
    Then solver must return within 60 seconds
    And solver should not hang or block indefinitely
    And timeout should be enforced strictly
    And partial solution should be returned if optimal not found

  Scenario: MinCostFlow solver does not hang on cyclic graphs
    Given a graph with potential flow cycles
    And MinCostFlow solver is configured
    When routing algorithm attempts to use MinCostFlow
    Then solver must complete within timeout
    And solver should not enter infinite loop
    And valid flow solution should be found or error raised

  Scenario: OR-Tools handles degenerate inputs without hanging
    Given a graph with only 1 waypoint
    Or a graph where all waypoints have identical coordinates
    When OR-Tools solver is invoked
    Then solver should complete in under 1 second
    And solver should return trivial solution
    And solver should not hang on edge case inputs

  # =========================================================================
  # MinCostFlow Specific Issues
  # =========================================================================

  Scenario: MinCostFlow branching logic prevents infinite loops
    Given a graph with branching paths to same destination
    And multiple fuel stations create flow branches
    When MinCostFlow algorithm is used for routing
    Then algorithm must detect and avoid cycles
    And flow must converge to destination
    And solver should not exceed iteration limit
    And valid route should be produced

  Scenario: MinCostFlow handles orbital waypoint branching
    Given a planet with 3 orbiting moons at same coordinates
    And routing graph includes 0-distance orbital edges
    When MinCostFlow solver processes the graph
    Then solver should handle 0-cost edges correctly
    And flow should not oscillate between orbital waypoints
    And routing should prefer orbital transitions when beneficial

  Scenario: MinCostFlow respects fuel capacity constraints
    Given a routing problem with fuel dimension
    And ship has 400 fuel capacity
    When MinCostFlow solver optimizes route
    Then fuel capacity constraint must be enforced
    And route should not exceed ship fuel capacity
    And refuel stops should be inserted if needed
    And solution should respect fuel arc costs

  # =========================================================================
  # Router Initialization Performance
  # =========================================================================

  Scenario: Router initialization completes quickly for small graphs
    Given a system graph with 10 waypoints
    When ORToolsRouter is initialized
    Then initialization should complete in under 100ms
    And graph data structures should be built
    And distance matrix should be computed
    And router should be ready for queries

  Scenario: Router initialization scales for large graphs
    Given a system graph with 100 waypoints
    When ORToolsRouter is initialized
    Then initialization should complete in under 2 seconds
    And distance matrix (100x100) should be computed efficiently
    And memory usage should be reasonable (<50MB)
    And router should not block other operations during init

  Scenario: Router lazy-loads expensive data structures
    Given a large system graph
    When ORToolsRouter is created
    Then distance matrix should only be computed when first used
    And initialization should not block on expensive operations
    And subsequent route queries should be fast (cached data)

  # =========================================================================
  # Error Handling and Validation
  # =========================================================================

  Scenario: Router rejects invalid waypoint symbols
    Given a system graph for "X1-TEST"
    When route is requested from "X1-TEST-A1" to "X1-INVALID-Z99"
    Then router should raise clear error
    And error should indicate "waypoint not in graph"
    And error should suggest checking waypoint symbol
    And router should not crash or hang

  Scenario: Router handles missing edges gracefully
    Given a disconnected graph with isolated waypoint clusters
    When route is requested from cluster A to cluster B
    Then router should detect disconnected graph
    And router should return error "no path exists"
    And router should not attempt infinite search
    And error message should be actionable

  Scenario: Router validates fuel feasibility before search
    Given a ship with 50 fuel
    And destination requires 200 fuel with no refuel stations
    When route planning is attempted
    Then router should fail fast with fuel validation
    And router should not waste time searching impossible route
    And error should clearly state fuel shortfall
    And error should suggest refuel or different ship

  Scenario: Router handles corrupted graph data
    Given a graph with missing waypoint coordinates
    Or a graph with negative edge distances
    When router attempts to use corrupted graph
    Then router should detect data corruption
    And router should raise validation error
    And error should indicate specific corruption issue
    And router should not produce invalid routes

  # =========================================================================
  # Concurrency and Thread Safety
  # =========================================================================

  Scenario: Multiple concurrent route queries do not interfere
    Given a routing system with shared graph
    When 10 route queries are made concurrently from different threads
    Then all queries should complete successfully
    And results should be independent and correct
    And no race conditions should occur
    And graph data structures should remain consistent

  Scenario: Router handles concurrent graph updates
    Given a routing system actively processing queries
    When graph is updated with new waypoints or edges
    Then in-progress queries should complete with old graph
    And new queries should use updated graph
    And graph update should not corrupt state
    And no deadlocks should occur

  # =========================================================================
  # Resource Management
  # =========================================================================

  Scenario: Router cleans up resources after timeout
    Given OR-Tools solver with 30-second timeout
    When solver times out
    Then solver should release memory and threads
    And solver should not leak resources
    And subsequent queries should have full resources available

  Scenario: Router limits memory usage for large problems
    Given a VRP problem with 1000 waypoints and 20 ships
    When OR-Tools solver attempts optimization
    Then memory usage should not exceed 500MB
    And solver should gracefully degrade if memory limit approached
    And solver should not crash with out-of-memory error

  # =========================================================================
  # Determinism and Reproducibility
  # =========================================================================

  Scenario: Router produces consistent results for same input
    Given a specific graph and routing query
    When the same query is run 5 times
    Then all 5 results should be identical
    And OR-Tools random seed should be fixed for tests
    And caching should not affect determinism
    And results should be reproducible across runs

  Scenario: Router handles random seed for test reproducibility
    Given OR-Tools solver configured with seed=12345
    And a routing problem
    When solver is run multiple times
    Then results should be identical across runs
    And metaheuristic search should be deterministic
    And test failures should be reproducible

  # =========================================================================
  # Cache Management
  # =========================================================================

  Scenario: Router caches tour results for performance
    Given a frequently-queried route from A to B
    When route is queried first time
    Then result should be computed and cached
    When same route is queried again
    Then cached result should be returned instantly
    And computation should not be repeated
    And cache should be indexed by (waypoints, start, ship_data)

  Scenario: Router invalidates cache when graph changes
    Given a cached tour result
    When graph is updated with new edges or waypoints
    Then cache should be invalidated
    And next query should recompute route
    And stale cached routes should not be returned

  Scenario: Router cache respects memory limits
    Given 1000 different tour queries
    When all queries are executed and cached
    Then cache should not exceed 100MB
    And least-recently-used entries should be evicted
    And cache hit rate should be monitored and logged

  # =========================================================================
  # Integration with SmartNavigator
  # =========================================================================

  Scenario: SmartNavigator retries on transient router failures
    Given OR-Tools solver encounters transient error
    When SmartNavigator attempts route planning
    Then SmartNavigator should retry up to 3 times
    And retry should use exponential backoff
    And if all retries fail, fallback to Dijkstra should activate
    And user should receive actionable error message

  Scenario: SmartNavigator validates router output before execution
    Given router returns a route
    When SmartNavigator prepares to execute route
    Then SmartNavigator should validate:
      | check                          |
      | All waypoints in route exist   |
      | Route is fuel-feasible         |
      | Route starts at current location|
      | Route ends at destination      |
      | No duplicate waypoints         |
    And only valid routes should be executed
    And invalid routes should be rejected with clear error

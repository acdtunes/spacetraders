Feature: Visualizer Cache Lookup Consistency
  As a visualizer querying tour cache
  I want my lookup keys to match daemon's cache keys exactly
  So that I don't get crossing edges from unoptimized routes

  Background:
    Given a temporary test database
    And a tour optimization system with WAL mode enabled
    And daemon has cached an optimized tour
    And visualizer receives ship assignments with full market list

  Scenario: Daemon cache key format validation
    Given assigned markets include start waypoint as first market
    When daemon caches the tour
    Then it should remove start from markets list before caching
    And it should provide start_waypoint as non-NULL parameter

  Scenario: Visualizer lookup matches daemon format (after fix)
    Given assigned markets ["X1-JB26-A1", "X1-JB26-A2", "X1-JB26-B7"]
    When visualizer extracts start as first market
    And visualizer removes start from markets for lookup
    Then visualizer lookup key should match daemon cache key

  Scenario: Cache miss with incorrect lookup (before fix)
    Given daemon cached with markets excluding start
    When visualizer looks up with markets including start
    And visualizer uses NULL start_waypoint
    Then cache should miss due to key mismatch
    And visualizer displays unoptimized assignment order with crossing edges

  Scenario: Cache hit with correct lookup (after fix)
    Given daemon cached with markets excluding start
    When visualizer looks up with markets excluding start
    And visualizer uses non-NULL start_waypoint
    Then cache should hit with exact key match
    And visualizer displays optimized tour with no crossing edges

  Scenario: Database roundtrip integration test
    Given daemon saves tour via database.save_tour_cache()
    And tour uses optimized order from OR-Tools
    When visualizer retrieves via database.get_cached_tour()
    And visualizer uses correct cache key format (after fix)
    Then retrieved tour order should match daemon's cached tour
    And no crossing edges should appear in visualization

  Scenario: SQL query includes all 4 cache key components
    When visualizer builds SQL query for cache lookup
    Then query must include system parameter
    And query must include markets (sorted, start removed)
    And query must include algorithm preference (ortools > 2opt)
    And query must include start_waypoint (non-NULL)

Feature: Tour Cache Key Format Consistency
  As a tour caching system
  I want cache keys to match scout assignment format
  So that visualizer can retrieve cached tours without cache misses

  Background:
    Given a simple 4-waypoint graph
    And a standard ship configuration

  @xfail
  Scenario: Tour cache includes start waypoint in markets list
    Given start waypoint "X1-TEST-I63"
    And 3 additional waypoints for the tour
    When I generate and cache the tour
    Then the cached markets list should include the start waypoint
    And the cached markets should match scout assignment format

  @xfail
  Scenario: Visualizer retrieval with scout assignment format
    Given a cached tour with full market list (start + stops)
    When visualizer queries with scout assignment format
    Then the cache should hit
    And the returned tour should have identical distance

  @xfail
  Scenario: Cache key consistency across daemon restarts
    Given a tour generated and cached in first bot run
    When daemon crashes and restarts
    And visualizer queries cache with same scout assignment
    Then cache should hit with consistent key format
    And tour distance should match original

  @xfail
  Scenario: Old cache format causes cache miss (regression test)
    Given a tour cached with old buggy format (stops only, no start)
    When visualizer queries with full market list
    Then cache should miss because keys don't match

  @xfail
  Scenario: Fixed cache format succeeds (fix validation)
    Given a tour cached with fixed format (includes start)
    When visualizer queries with full market list
    Then cache should hit because keys match
    And cached tour order should be returned

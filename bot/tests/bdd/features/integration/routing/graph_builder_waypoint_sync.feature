Feature: Graph Builder Waypoint Synchronization
  When GraphBuilder builds a system graph, it should:
  1. Return structure-only graph data (for navigation with infinite TTL)
  2. Save full waypoint trait data to waypoints table (for queries with 2hr TTL)

  This implements the split-caching strategy where structure and traits are separated.

  Background:
    Given a clean database
    And a mock API client that returns waypoints for system "X1-TEST"

  Scenario: Graph builder returns structure data and saves waypoint traits
    When I build a system graph for "X1-TEST" with player_id 1
    Then the returned graph should contain structure data only
    And the returned graph should have 3 waypoints
    And the returned graph structure should not contain traits or has_fuel
    And the waypoint repository should have been called to save 3 waypoints
    And the saved waypoints should contain trait data
    And saved waypoint "X1-TEST-A1" should have has_fuel true

  Scenario: Returned graph excludes traits, saved waypoints include traits
    When I build a system graph for "X1-TEST" with player_id 1
    Then the returned graph waypoints should contain only x, y, type, systemSymbol, orbitals
    And the returned graph waypoints should not contain traits or has_fuel
    And the saved waypoint objects should have traits attribute
    And the saved waypoint objects should have has_fuel attribute

Feature: Waypoint Repository Lazy-Loading
  As a system component
  I want the WaypointRepository to transparently handle cache misses
  So that all callers automatically benefit from lazy-loading without query wrappers

  Background:
    Given the database is initialized
    And the waypoint repository is initialized with API client factory

  # ============================================================================
  # Repository-Level Lazy-Loading (Transparent to Callers)
  # ============================================================================

  Scenario: Repository with fresh cache returns data without API call
    Given a player with ID 1 exists
    And waypoints exist for system "X1-FRESH":
      | symbol         | type        | x    | y   | traits      | has_fuel |
      | X1-FRESH-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE | true     |
      | X1-FRESH-B2    | MOON        | -5.0 | 8.0 | SHIPYARD    | false    |
    And the waypoints were synced 1 hour ago
    When I call repository find_by_system for "X1-FRESH" with player_id 1
    Then I should receive 2 waypoints
    And the API should not have been called

  Scenario: Repository with empty cache auto-fetches from API
    Given a player with ID 1 exists with valid token
    And no waypoints exist in cache for system "X1-EMPTY"
    And the API will return waypoints for system "X1-EMPTY":
      | symbol         | type        | x    | y   | traits      |
      | X1-EMPTY-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE |
      | X1-EMPTY-B2    | ASTEROID    | 20.0 | 15.0| SHIPYARD    |
    When I call repository find_by_system for "X1-EMPTY" with player_id 1
    Then I should receive 2 waypoints
    And the waypoints should be cached in the database
    And the API should have been called once

  Scenario: Repository with stale cache auto-refreshes from API
    Given a player with ID 1 exists with valid token
    And waypoints exist for system "X1-STALE":
      | symbol         | type        | x    | y   | traits      | has_fuel |
      | X1-STALE-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE | false    |
    And the waypoints were synced 3 hours ago
    And the API will return waypoints for system "X1-STALE":
      | symbol         | type        | x    | y   | traits      |
      | X1-STALE-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE |
      | X1-STALE-B2    | MOON        | -5.0 | 8.0 | FUEL_STATION|
    When I call repository find_by_system for "X1-STALE" with player_id 1
    Then I should receive 2 waypoints
    And the waypoints should be cached in the database
    And the API should have been called once

  Scenario: Repository without player_id operates in cache-only mode
    Given waypoints exist for system "X1-CACHE":
      | symbol         | type        | x    | y   | traits      | has_fuel |
      | X1-CACHE-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE | true     |
    And the waypoints were synced 1 hour ago
    When I call repository find_by_system for "X1-CACHE" without player_id
    Then I should receive 1 waypoint
    And the API should not have been called

  Scenario: Repository with stale cache but no player_id returns stale data
    Given waypoints exist for system "X1-STALE-NO-ID":
      | symbol         | type        | x    | y   | traits      | has_fuel |
      | X1-STALE-NO-ID-A1 | PLANET   | 10.0 | 5.0 | MARKETPLACE | false    |
    And the waypoints were synced 5 hours ago
    When I call repository find_by_system for "X1-STALE-NO-ID" without player_id
    Then I should receive 1 waypoint
    And the API should not have been called

  Scenario: Repository with empty cache and no player_id returns empty list
    Given no waypoints exist in cache for system "X1-EMPTY-NO-ID"
    When I call repository find_by_system for "X1-EMPTY-NO-ID" without player_id
    Then I should receive 0 waypoints
    And the API should not have been called

  # ============================================================================
  # Filter Methods Also Benefit from Lazy-Loading
  # ============================================================================

  Scenario: find_by_trait with empty cache auto-fetches from API
    Given a player with ID 1 exists with valid token
    And no waypoints exist in cache for system "X1-TRAIT"
    And the API will return waypoints for system "X1-TRAIT":
      | symbol         | type        | x    | y   | traits      |
      | X1-TRAIT-A1    | PLANET      | 10.0 | 5.0 | MARKETPLACE |
      | X1-TRAIT-B2    | ASTEROID    | 20.0 | 15.0| SHIPYARD    |
    When I call repository find_by_trait for "X1-TRAIT" with trait "SHIPYARD" and player_id 1
    Then I should receive 1 waypoint
    And the waypoint "X1-TRAIT-B2" should be in the results
    And the API should have been called once

  Scenario: find_by_fuel with stale cache auto-refreshes from API
    Given a player with ID 1 exists with valid token
    And waypoints exist for system "X1-FUEL":
      | symbol         | type        | x    | y   | traits      | has_fuel |
      | X1-FUEL-A1     | PLANET      | 10.0 | 5.0 | MARKETPLACE | false    |
    And the waypoints were synced 3 hours ago
    And the API will return waypoints for system "X1-FUEL":
      | symbol         | type        | x    | y   | traits      |
      | X1-FUEL-A1     | PLANET      | 10.0 | 5.0 | MARKETPLACE |
      | X1-FUEL-B2     | MOON        | -5.0 | 8.0 | MARKETPLACE |
    When I call repository find_by_fuel for "X1-FUEL" with player_id 1
    Then I should receive 2 waypoints
    And the API should have been called once

  # ============================================================================
  # Navigation Handler Integration
  # ============================================================================

  Scenario: NavigateShipHandler never sees empty cache when player_id provided
    Given a player with ID 1 exists with valid token
    And a ship "SHIP-1" exists at waypoint "X1-NAV-A1" for player 1
    And no waypoints exist in cache for system "X1-NAV"
    And the API will return waypoints for system "X1-NAV":
      | symbol         | type        | x    | y   | traits      |
      | X1-NAV-A1      | PLANET      | 0.0  | 0.0 | MARKETPLACE |
      | X1-NAV-B2      | MOON        | 10.0 | 10.0| SHIPYARD    |
    When NavigateShipHandler queries waypoints for system "X1-NAV" with player_id 1
    Then the handler should receive 2 waypoints
    And the API should have been called once transparently
    And navigation should proceed without error

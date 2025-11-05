Feature: Data Mappers
  As a persistence layer
  I want to convert between domain entities and database rows
  So that I can maintain clean separation of concerns

  # PlayerMapper Tests
  Scenario: Convert complete database row to Player
    Given a database row with player data
      | player_id | agent_symbol | token         | created_at          | last_active         | metadata                             |
      | 1         | TEST_AGENT   | Bearer token | 2025-01-01T12:00:00 | 2025-01-02T15:30:00 | {"faction": "COSMIC", "credits": 1000} |
    When I convert the row to a Player entity
    Then the player should have player_id 1
    And the player should have agent_symbol "TEST_AGENT"
    And the player should have token "Bearer token"
    And the player should have metadata with key "faction"

  Scenario: Convert row with NULL last_active
    Given a database row with NULL last_active
      | player_id | agent_symbol | token   | created_at          |
      | 1         | TEST_AGENT   | token123 | 2025-01-01T12:00:00 |
    When I convert the row to a Player entity
    Then the player last_active should equal created_at

  Scenario: Convert row with NULL metadata
    Given a database row with NULL metadata
      | player_id | agent_symbol | token   | created_at          | last_active         |
      | 1         | TEST_AGENT   | token123 | 2025-01-01T12:00:00 | 2025-01-01T12:00:00 |
    When I convert the row to a Player entity
    Then the player metadata should be empty

  Scenario: Convert row with empty metadata JSON
    Given a database row with empty metadata JSON "{}"
    When I convert the row to a Player entity
    Then the player metadata should be empty

  # ShipMapper Tests
  Scenario: Convert Ship to database dict
    Given a Ship entity at waypoint "X1-A1"
    When I convert the Ship to a database dict
    Then the dict should have ship_symbol "SHIP-1"
    And the dict should have current_location_symbol "X1-A1"
    And the dict should have fuel_current 100
    And the dict should have nav_status "IN_ORBIT"

  Scenario: Convert database row to Ship
    Given a ship database row
      | ship_symbol | player_id | current_location_symbol | fuel_current | fuel_capacity | nav_status |
      | SHIP-1      | 1         | X1-A1                   | 100          | 200           | IN_ORBIT   |
    And a waypoint entity for "X1-A1"
    When I convert the row to a Ship entity
    Then the ship should have ship_symbol "SHIP-1"
    And the ship should have fuel current 100
    And the ship should have fuel capacity 200

  Scenario: Ship roundtrip conversion preserves data
    Given a Ship entity at waypoint "X1-A1"
    When I convert Ship to dict then back to Ship
    Then all ship fields should match the original

  # RouteMapper Tests
  Scenario: Convert Route to database dict
    Given a Route with one segment
    When I convert the Route to a database dict
    Then the dict should have route_id "route-123"
    And the dict should have ship_symbol "SHIP-1"
    And the dict should have status "PLANNED"
    And the dict should have segments_json

  Scenario: Convert database row to Route
    Given a route database row with segments JSON
    When I convert the row to a Route entity
    Then the route should have route_id "route-123"
    And the route should have 1 segment
    And the route status should be PLANNED

  Scenario: Route roundtrip conversion preserves data
    Given a Route with one segment
    When I convert Route to dict then back to Route
    Then all route fields should match the original
    And the segment details should be preserved

  # Removed: Scenarios testing private _serialize_segment() and _deserialize_segment()
  # These are implementation details. The "Route roundtrip conversion preserves data"
  # scenario above already verifies that segment serialization works correctly.

  Scenario: Route with multiple segments
    Given a Route with 2 segments
    When I convert Route to dict then back to Route
    Then the route should have 2 segments
    And segment 0 should go from "X1-A1" to "X1-A2"
    And segment 1 should go from "X1-A2" to "X1-A3"

  Scenario: Route state is preserved through serialization
    Given a Route with status EXECUTING
    When I convert Route to dict then back to Route
    Then the route status should be EXECUTING

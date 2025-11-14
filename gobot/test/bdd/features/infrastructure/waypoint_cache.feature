Feature: Waypoint Caching
  As the waypoint repository
  I want to cache waypoint data with TTL and filtering capabilities
  So that I can minimize API calls and support efficient waypoint discovery

  Background:
    Given the database is initialized

  Scenario: Save single waypoint to database
    When I save waypoint "X1-GZ7-A1" for system "X1-GZ7" with:
      | type   | x    | y   | traits          | has_fuel |
      | PLANET | 10.0 | 5.0 | SHIPYARD,MARKET | true     |
    Then the waypoint should be saved in the database
    And waypoint "X1-GZ7-A1" should exist in the database

  Scenario: Save multiple waypoints for a system (batch insert)
    When I save waypoints for system "X1-GZ7" with:
      | symbol     | type    | x    | y    | traits           | has_fuel | orbitals   |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z |
      | X1-GZ7-A1Z | ORBITAL | 10.0 | 5.0  |                  | false    |            |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE      | false    |            |
    Then all waypoints should be saved in the database
    And the database should have 3 waypoints for system "X1-GZ7"

  Scenario: Retrieve cached waypoint by symbol
    Given waypoint "X1-GZ7-A1" exists in database for system "X1-GZ7" with:
      | type   | x    | y   | traits          | has_fuel |
      | PLANET | 10.0 | 5.0 | SHIPYARD,MARKET | true     |
    When I query waypoint "X1-GZ7-A1" from system "X1-GZ7"
    Then I should receive the waypoint
    And the waypoint should have type "PLANET"
    And the waypoint should have coordinates (10.0, 5.0)
    And the waypoint should have traits "SHIPYARD,MARKET"
    And the waypoint should have fuel available

  Scenario: List all waypoints in a system
    Given waypoints exist in database for system "X1-GZ7":
      | symbol     | type    | x    | y    | traits           | has_fuel |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKET  | true     |
      | X1-GZ7-A1Z | ORBITAL | 10.0 | 5.0  |                  | false    |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE      | false    |
    When I list waypoints for system "X1-GZ7"
    Then I should receive 3 waypoints
    And the waypoint list should contain "X1-GZ7-A1"
    And the waypoint list should contain "X1-GZ7-A1Z"
    And the waypoint list should contain "X1-GZ7-B2"

  Scenario: Filter waypoints by trait (SHIPYARD)
    Given waypoints exist in database for system "X1-GZ7":
      | symbol     | type    | x    | y    | traits           | has_fuel |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKET  | true     |
      | X1-GZ7-A1Z | ORBITAL | 10.0 | 5.0  |                  | false    |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE      | false    |
    When I filter waypoints for system "X1-GZ7" by trait "SHIPYARD"
    Then I should receive 1 waypoint
    And the waypoint list should contain "X1-GZ7-A1"

  Scenario: Filter waypoints by trait (MARKETPLACE)
    Given waypoints exist in database for system "X1-GZ7":
      | symbol     | type    | x    | y    | traits                 | has_fuel |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKETPLACE   | true     |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE            | false    |
    When I filter waypoints for system "X1-GZ7" by trait "MARKETPLACE"
    Then I should receive 2 waypoints
    And the waypoint list should contain "X1-GZ7-A1"
    And the waypoint list should contain "X1-GZ7-B2"

  Scenario: Filter waypoints by type (PLANET)
    Given waypoints exist in database for system "X1-GZ7":
      | symbol     | type    | x    | y    | traits           | has_fuel |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKET  | true     |
      | X1-GZ7-A1Z | ORBITAL | 10.0 | 5.0  |                  | false    |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE      | false    |
    When I filter waypoints for system "X1-GZ7" by type "PLANET"
    Then I should receive 1 waypoint
    And the waypoint list should contain "X1-GZ7-A1"

  Scenario: Filter waypoints by has_fuel flag
    Given waypoints exist in database for system "X1-GZ7":
      | symbol     | type    | x    | y    | traits           | has_fuel |
      | X1-GZ7-A1  | PLANET  | 10.0 | 5.0  | SHIPYARD,MARKET  | true     |
      | X1-GZ7-A1Z | ORBITAL | 10.0 | 5.0  |                  | false    |
      | X1-GZ7-B2  | MOON    | -5.0 | 8.0  | MARKETPLACE      | false    |
    When I filter waypoints for system "X1-GZ7" with fuel available
    Then I should receive 1 waypoint
    And the waypoint list should contain "X1-GZ7-A1"

  Scenario: Return empty list when no waypoints match trait filter
    Given waypoints exist in database for system "X1-GZ7":
      | symbol    | type | x    | y   | traits      | has_fuel |
      | X1-GZ7-B2 | MOON | -5.0 | 8.0 | MARKETPLACE | false    |
    When I filter waypoints for system "X1-GZ7" by trait "SHIPYARD"
    Then I should receive 0 waypoints

  Scenario: Return empty list when no waypoints match type filter
    Given waypoints exist in database for system "X1-GZ7":
      | symbol    | type | x    | y   | traits      | has_fuel |
      | X1-GZ7-B2 | MOON | -5.0 | 8.0 | MARKETPLACE | false    |
    When I filter waypoints for system "X1-GZ7" by type "PLANET"
    Then I should receive 0 waypoints

  Scenario: Upsert waypoint (update existing with new data)
    Given waypoint "X1-GZ7-A1" exists in database for system "X1-GZ7" with:
      | type   | x    | y   | traits   | has_fuel |
      | PLANET | 10.0 | 5.0 | SHIPYARD | false    |
    When I save waypoint "X1-GZ7-A1" for system "X1-GZ7" with:
      | type   | x    | y   | traits          | has_fuel |
      | PLANET | 10.0 | 5.0 | SHIPYARD,MARKET | true     |
    Then the waypoint should be saved in the database
    And waypoint "X1-GZ7-A1" should have traits "SHIPYARD,MARKET"
    And waypoint "X1-GZ7-A1" should have fuel available

  Scenario: Preserve waypoint orbitals when saving
    When I save waypoint "X1-GZ7-A1" for system "X1-GZ7" with orbitals:
      | type   | x    | y   | traits  | has_fuel | orbitals              |
      | PLANET | 10.0 | 5.0 | SHIPYARD | true    | X1-GZ7-A1Z,X1-GZ7-A1Y |
    Then waypoint "X1-GZ7-A1" should have orbitals "X1-GZ7-A1Z,X1-GZ7-A1Y"

  Scenario: Preserve waypoint coordinates accurately (no rounding errors)
    When I save waypoint "X1-GZ7-A1" for system "X1-GZ7" with:
      | type   | x       | y        | traits | has_fuel |
      | PLANET | 123.456 | -789.012 | MARKET | false    |
    Then waypoint "X1-GZ7-A1" should have coordinates (123.456, -789.012)

  Scenario: Deduplication by symbol (same symbol overwrites)
    Given waypoint "X1-GZ7-A1" exists in database for system "X1-GZ7" with:
      | type   | x    | y   | traits | has_fuel |
      | PLANET | 10.0 | 5.0 | MARKET | false    |
    When I save waypoints for system "X1-GZ7" with:
      | symbol    | type   | x    | y   | traits   | has_fuel |
      | X1-GZ7-A1 | PLANET | 10.0 | 5.0 | SHIPYARD | true     |
      | X1-GZ7-B2 | MOON   | -5.0 | 8.0 | MARKET   | false    |
    Then the database should have 2 waypoints for system "X1-GZ7"
    And waypoint "X1-GZ7-A1" should have traits "SHIPYARD"

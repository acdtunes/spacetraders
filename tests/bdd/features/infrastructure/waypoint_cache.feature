Feature: Waypoint Caching
  As a system
  I want to cache waypoint data from the SpaceTraders API
  So that I can avoid repeated API calls when discovering shipyards and markets

  Background:
    Given the database is initialized

  Scenario: Save waypoints for a system
    When I save waypoints for system "X1-GZ7" with waypoints:
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z      |
      | X1-GZ7-A1Z     | ORBITAL     | 10.0 | 5.0 |                  | false    |                 |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      | false    |                 |
    Then waypoints should be saved in the database

  Scenario: Find waypoints by system
    Given waypoints exist for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z      |
      | X1-GZ7-A1Z     | ORBITAL     | 10.0 | 5.0 |                  | false    |                 |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      | false    |                 |
    When I query waypoints for system "X1-GZ7"
    Then I should receive 3 waypoints
    And waypoint "X1-GZ7-A1" should have type "PLANET"
    And waypoint "X1-GZ7-A1" should have traits "SHIPYARD,MARKET"

  Scenario: Find waypoints with specific trait
    Given waypoints exist for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z      |
      | X1-GZ7-A1Z     | ORBITAL     | 10.0 | 5.0 |                  | false    |                 |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      | false    |                 |
    When I query waypoints with trait "SHIPYARD" in system "X1-GZ7"
    Then I should receive 1 waypoint
    And waypoint "X1-GZ7-A1" should be in the results

  Scenario: Find waypoints with fuel stations
    Given waypoints exist for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z      |
      | X1-GZ7-A1Z     | ORBITAL     | 10.0 | 5.0 |                  | false    |                 |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      | false    |                 |
    When I query waypoints with fuel in system "X1-GZ7"
    Then I should receive 1 waypoint
    And waypoint "X1-GZ7-A1" should be in the results

  Scenario: Return empty list when no waypoints match trait
    Given waypoints exist for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      | false    |                 |
    When I query waypoints with trait "SHIPYARD" in system "X1-GZ7"
    Then I should receive 0 waypoints

  Scenario: Update existing waypoints when saving again
    Given waypoints exist for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD         | false    |                 |
    When I save waypoints for system "X1-GZ7" with waypoints:
      | symbol         | type        | x    | y   | traits           | has_fuel | orbitals        |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  | true     | X1-GZ7-A1Z      |
    Then waypoints should be saved in the database
    And waypoint "X1-GZ7-A1" should have traits "SHIPYARD,MARKET"
    And waypoint "X1-GZ7-A1" should have fuel available

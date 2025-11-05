Feature: Sync System Waypoints
  As a system
  I want to sync waypoint data from the SpaceTraders API to cache
  So that I can avoid repeated API calls for shipyard discovery

  Background:
    Given a player exists with agent "TEST-AGENT" and player_id 1
    And the database is initialized

  Scenario: Sync waypoints for system (first time)
    Given the API will return waypoints for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      |
    When I sync waypoints for system "X1-GZ7" for player 1
    Then waypoints should be cached in the database
    And the cache should contain 2 waypoints for system "X1-GZ7"

  Scenario: Sync waypoints updates existing cache
    Given waypoints are already cached for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD         |
    And the API will return waypoints for system "X1-GZ7":
      | symbol         | type        | x    | y   | traits           |
      | X1-GZ7-A1      | PLANET      | 10.0 | 5.0 | SHIPYARD,MARKET  |
      | X1-GZ7-B2      | MOON        | -5.0 | 8.0 | MARKETPLACE      |
    When I sync waypoints for system "X1-GZ7" for player 1
    Then the cache should contain 2 waypoints for system "X1-GZ7"
    And waypoint "X1-GZ7-A1" should have traits "SHIPYARD" and "MARKET"

  Scenario: Sync waypoints with pagination
    Given the API will return waypoints for system "X1-GZ7" across 3 pages:
      | page | waypoint_count |
      | 1    | 20             |
      | 2    | 20             |
      | 3    | 10             |
    When I sync waypoints for system "X1-GZ7" for player 1
    Then the cache should contain 50 waypoints for system "X1-GZ7"

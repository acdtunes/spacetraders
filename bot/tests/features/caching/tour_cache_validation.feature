Feature: Tour Cache Validation for Fuel Stations
  As a scout coordinator
  I want the cache validation to reject stale tours containing fuel stations
  So that scouts only visit legitimate markets with trade goods

  Background:
    Given the X1-JV40 system with waypoints:
      | waypoint     | type         | has_marketplace | has_fuel |
      | X1-JV40-A1   | PLANET       | yes             | yes      |
      | X1-JV40-J52  | FUEL_STATION | no              | yes      |
      | X1-JV40-J53  | PLANET       | yes             | yes      |

  Scenario: Cache validation rejects stale cached tour with fuel station for scouts
    Given a scout ship at "X1-JV40-A1"
    And a stale cache entry with tour order: A1 → J53 → J52 → A1
    When I request an optimized tour for markets A1 and J53
    Then the cache should be invalidated due to fuel station presence
    And a fresh tour should be built
    And the tour should visit "X1-JV40-A1"
    And the tour should visit "X1-JV40-J53"

  Scenario: Cache validation rejects stale cached tour with fuel station for probes
    Given a probe ship at "X1-JV40-A1"
    And a stale cache entry with tour order: A1 → J52 → J53 → A1
    When I request an optimized tour for markets A1 and J53
    Then the cache should be invalidated due to fuel station presence
    And a fresh tour should be built

  Scenario: Cache validation allows legitimate cached tours without fuel stations
    Given a scout ship at "X1-JV40-A1"
    And a valid cache entry with tour order: A1 → J53 → A1
    When I request an optimized tour for markets A1 and J53
    Then the cache should be used
    And the tour should visit only "X1-JV40-A1" and "X1-JV40-J53"

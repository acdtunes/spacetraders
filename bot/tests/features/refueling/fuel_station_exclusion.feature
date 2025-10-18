Feature: Fuel Station Exclusion from Scout Tours
  As a scout coordinator
  I want fuel stations excluded from market scouting tours
  So that scouts only visit waypoints with actual trade goods

  Background:
    Given the X1-JV40 system with waypoints:
      | waypoint     | type         | has_marketplace | has_fuel |
      | X1-JV40-A1   | PLANET       | yes             | yes      |
      | X1-JV40-J52  | FUEL_STATION | no              | yes      |
      | X1-JV40-J53  | PLANET       | yes             | yes      |

  Scenario: Coordinator filters fuel stations from market list
    When the coordinator extracts markets from the graph
    Then the markets should include "X1-JV40-A1"
    And the markets should include "X1-JV40-J53"
    And the markets should NOT include "X1-JV40-J52"
    And there should be exactly 2 markets

  Scenario: Tour visits assigned markets only (no fuel stations)
    Given a scout ship "SCOUT-4" at "X1-JV40-A1"
    And assigned markets: X1-JV40-A1, X1-JV40-J53
    When I generate an optimized tour without cache
    Then the tour should visit "X1-JV40-A1"
    And the tour should visit "X1-JV40-J53"
    And the tour should NOT visit "X1-JV40-J52"
    And the tour should visit exactly the assigned markets

  Scenario: Tour cache never stores fuel stations
    Given a scout ship "SCOUT-4" at "X1-JV40-A1"
    And assigned markets: X1-JV40-A1, X1-JV40-J53
    When I generate and cache an optimized tour
    Then the cached markets should NOT include fuel stations
    And all cached markets should have MARKETPLACE trait

  Scenario: Stale cache with fuel station is rejected
    Given a scout ship "SCOUT-4" at "X1-JV40-A1"
    And a stale cache entry with markets: X1-JV40-J53, X1-JV40-J52
    And assigned markets: X1-JV40-A1, X1-JV40-J53
    When I request an optimized tour with cache enabled
    Then the tour should visit "X1-JV40-A1"
    And the tour should visit "X1-JV40-J53"
    And the tour should NOT visit "X1-JV40-J52"

  Scenario: Coordinator market list matches tour input
    When the coordinator extracts markets from the graph
    And the markets are passed to the tour optimizer
    Then no fuel stations should be in the tour input
    And all waypoints should have MARKETPLACE trait

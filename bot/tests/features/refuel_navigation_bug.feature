Feature: Refuel Navigation Bug Fix
  As a bot operator
  I want ships to navigate to refuel waypoints before refueling
  So that they don't try to refuel at the wrong location

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-JB26" has the following waypoints:
      | symbol       | type        | x   | y   | traits      |
      | X1-JB26-A1   | PLANET      | 0   | 0   | MARKETPLACE |
      | X1-JB26-B7   | PLANET      | 100 | 0   | MARKETPLACE |
      | X1-JB26-B8   | ASTEROID    | 150 | 0   |             |
      | X1-JB26-C9   | PLANET      | 400 | 0   | MARKETPLACE |

  Scenario: Ship at different location than refuel waypoint navigates correctly
    Given a ship "VOID_HUNTER-1" at "X1-JB26-B8" with 40 fuel out of 400 capacity
    And the ship is in "IN_ORBIT" state
    When I execute a refuel step for waypoint "X1-JB26-B7"
    Then the ship should navigate to "X1-JB26-B7" first
    And then the ship should dock at "X1-JB26-B7"
    And then the ship should refuel successfully
    And the ship should be at "X1-JB26-B7" after refuel

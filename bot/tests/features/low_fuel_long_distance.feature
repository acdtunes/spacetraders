Feature: Low Fuel Long Distance Navigation Bug
  As a bot operator
  I want SmartNavigator to insert refuel stops for long journeys with low fuel
  So that ships don't fail navigation due to insufficient fuel

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-GZ97" has the following waypoints:
      | symbol       | type        | x   | y   | traits      |
      | X1-GZ97-B17  | PLANET      | 0   | 0   | MARKETPLACE |
      | X1-GZ97-B7   | PLANET      | 622 | 0   | MARKETPLACE |
      | X1-GZ97-H48  | ASTEROID    | 303 | 0   |             |

  Scenario: Ship with 97 fuel needs to travel 303 units should plan refuel stop
    Given a ship "STORMHAVEN-1" at "X1-GZ97-B17" with 97 fuel out of 400 capacity
    And the ship is in "DOCKED" state
    When I plan a route to "X1-GZ97-H48" with cruise preferred
    Then the route should have a refuel stop at "X1-GZ97-B17"
    And the route should have navigation steps

  Scenario: Ship navigates 622 units in DRIFT with only 97 fuel should require refuel
    Given a ship "STORMHAVEN-1" at "X1-GZ97-B17" with 97 fuel out of 400 capacity
    And the ship is in "IN_ORBIT" state
    When I navigate to "X1-GZ97-B7"
    Then the navigation should succeed
    And the ship should be at "X1-GZ97-B7"

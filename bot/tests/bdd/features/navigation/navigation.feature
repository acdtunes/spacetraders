Feature: Ship Navigation
  As a bot operator
  I want ships to navigate intelligently with fuel optimization
  So that they never get stranded and operate efficiently

  Background:
    Given the SpaceTraders API is mocked
    And the system "X1-HU87" has the following waypoints:
      | symbol       | type     | x   | y   | traits      |
      | X1-HU87-A1   | PLANET   | 0   | 0   | MARKETPLACE |
      | X1-HU87-B7   | PLANET   | 100 | 0   | MARKETPLACE |
      | X1-HU87-B9   | ASTEROID | 150 | 0   |             |
      | X1-HU87-C5   | ASTEROID | 300 | 0   |             |

  Scenario: Direct navigation with sufficient fuel
    Given a ship "TEST-1" at "X1-HU87-A1" with 400 fuel
    When I navigate to "X1-HU87-B9"
    Then the navigation should succeed
    And the ship should be at "X1-HU87-B9"
    And the ship should have consumed approximately 150 fuel

  Scenario: Navigation requires automatic refuel stop
    Given a ship "TEST-1" at "X1-HU87-A1" with 50 fuel
    When I navigate to "X1-HU87-C5"
    Then the navigation should succeed
    And the ship should be at "X1-HU87-C5"
    And the route should have included a refuel stop at "X1-HU87-B7"

  Scenario: Route validation prevents impossible navigation
    Given a ship "TEST-1" at "X1-HU87-A1" with 2 fuel
    And waypoint "X1-HU87-Z9" is 150000 units away with no marketplace between
    When I validate the route to "X1-HU87-Z9"
    Then the route should be invalid
    And the reason should contain "insufficient fuel" or "no route found"

  Scenario: High fuel triggers CRUISE mode
    Given a ship "TEST-1" at "X1-HU87-A1" with 350 fuel out of 400 capacity
    When I plan a route to "X1-HU87-B9" with cruise preferred
    Then the route should use "CRUISE" mode

  Scenario: Low fuel triggers DRIFT mode
    Given a ship "TEST-1" at "X1-HU87-A1" with 100 fuel out of 400 capacity
    When I plan a route to "X1-HU87-B9" without cruise preference
    Then the route should use "DRIFT" mode

Feature: Refuel Step Execution in Smart Navigator
  As a smart navigator
  I want to correctly execute refuel steps during route execution
  So that ships have sufficient fuel for CRUISE mode navigation

  Background:
    Given the X1-GH18 system with waypoints:
      | waypoint      | x   | y   | has_fuel | traits      |
      | X1-GH18-A2    | 0   | 0   | no       |             |
      | X1-GH18-B33   | 346 | 0   | yes      | MARKETPLACE |
      | X1-GH18-J62   | 346 | 382 | no       |             |

  @xfail
  Scenario: Use DRIFT for final approach when CRUISE impossible
    Given a ship at "X1-GH18-B33" with 400 fuel (capacity 400)
    And destination "X1-GH18-J62" is 382 units away
    And CRUISE requires 420 fuel with safety margin (exceeds capacity)
    When I plan a route with prefer_cruise=True
    Then the route should exist
    And the route should allow DRIFT to goal
    And the final navigation step should reach J62

  @xfail
  Scenario: Route plan includes refuel step
    Given a ship at "X1-GH18-A2" with 390 fuel (capacity 400)
    And destination "X1-GH18-J62" (500 units direct, or 346+382 via B33)
    When I plan a route with prefer_cruise=True
    Then the route should have exactly 3 steps
    And step 1 should navigate A2 → B33 in CRUISE
    And step 2 should refuel at B33
    And step 3 should navigate B33 → J62
    And the refuel step should add fuel

  @xfail
  Scenario: Smart Navigator executes refuel step
    Given a ship at "X1-GH18-A2" with 390 fuel
    And a mock ship controller
    When I execute the route to "X1-GH18-J62" with prefer_cruise=True
    Then the execution should succeed
    And the operation sequence should include refuel
    And refuel should occur after navigate to B33
    And refuel should occur before navigate to J62
    And the refuel operation should be executed

Feature: Intermediate Refuel Station Routing
  As a route optimizer
  I want to find intermediate refuel stations for long routes
  So that ships can use CRUISE mode instead of slow DRIFT

  Background:
    Given the X1-GH18 system with waypoints:
      | waypoint      | x   | y   | has_fuel | traits      |
      | X1-GH18-B32   | 0   | 0   | yes      | MARKETPLACE |
      | X1-GH18-X     | 200 | 0   | yes      | MARKETPLACE |
      | X1-GH18-C43   | 428 | 0   | no       |             |

  Scenario: Find intermediate refuel station instead of DRIFT
    Given a ship "SILMARETH-1" at "X1-GH18-B32" with 58 fuel (capacity 400)
    And the destination is "X1-GH18-C43" (428 units away)
    And direct CRUISE to C43 requires 471 fuel (impossible with 400 capacity)
    When I plan a route with prefer_cruise=True
    Then the route should use ONLY CRUISE mode
    And the route should visit intermediate station "X1-GH18-X"
    And the route should include at least 2 refuel actions
    And the total time should be less than 3000 seconds

  @xfail
  Scenario: Use DRIFT when no intermediate stations exist
    Given a ship "TEST-SHIP" at "X1-TEST-A" with 50 fuel (capacity 400)
    And the destination is "X1-TEST-B" (500 units away)
    And there are no intermediate fuel stations
    When I plan a route with prefer_cruise=True
    Then the route should exist
    And the route should use DRIFT mode
    And DRIFT is the only option

  Scenario: Refuel at intermediate waypoint for CRUISE route
    Given a ship at "X1-TEST-A2" with 100 fuel (capacity 400)
    And waypoint "X1-TEST-B33" at 300 units with fuel
    And destination "X1-TEST-J62" at 600 units total
    When I plan a route with prefer_cruise=True
    Then the route should navigate A2 → B33 in CRUISE
    And the route should refuel at B33
    And the route should navigate B33 → J62 in CRUISE
    And all navigation should use CRUISE mode

  Scenario: Direct route when fuel sufficient
    Given a ship at "X1-TEST-A2" with 350 fuel (capacity 400)
    And destination "X1-TEST-J62" at 300 units
    When I plan a route with prefer_cruise=True
    Then the route should have 1 direct navigation leg
    And the route should use CRUISE mode
    And there should be 0 refuel stops

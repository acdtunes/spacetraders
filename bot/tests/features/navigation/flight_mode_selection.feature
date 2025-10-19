Feature: Flight Mode Selection (CRUISE vs DRIFT)
  As a smart navigator
  I want to select the optimal flight mode based on fuel and distance
  So that ships travel efficiently without unnecessary DRIFT slowdowns

  Background:
    Given a routing system with flight mode selection
    And ships with varying fuel levels and capacities

  @xfail
  Scenario: Prefer CRUISE when fuel is adequate
    Given a ship at waypoint B7 with 96/400 fuel
    And destination J55 is 671 units away
    And B7 has a marketplace for refueling
    When I plan route with prefer_cruise=True
    Then route should use CRUISE mode for all navigation legs
    And route should include refuel stops as needed
    And no DRIFT legs should exist

  @xfail
  Scenario: Short trip with adequate fuel uses CRUISE
    Given a ship at waypoint B14 with 67/80 fuel (84% capacity)
    And destination B7 is 11.7 units away
    And ship has more than enough fuel for CRUISE
    When I plan the route
    Then route should use CRUISE mode (travel time ~40s)
    And route should NOT use DRIFT mode (travel time ~350s)
    And navigation fuel cost should be ~12 units (CRUISE rate)

  @xfail
  Scenario: Emergency DRIFT when no fuel for CRUISE
    Given a ship at waypoint A1 with only 5 fuel
    And destination B1 is 100 units away
    And B1 has marketplace for refueling
    And not enough fuel for CRUISE (needs ~100 fuel)
    When I plan route with prefer_cruise=True
    Then route should allow emergency DRIFT to reach fuel station
    And this is the only acceptable use of DRIFT

  @xfail
  Scenario: Safety margin enforced for CRUISE selection
    Given a ship with fuel near the threshold
    And destination requires significant fuel
    And safety margin is configured (default 10%)
    When selecting flight mode
    Then CRUISE should only be selected if fuel > required * 1.1
    And DRIFT should be used if fuel is below safety threshold

  @xfail
  Scenario: Route optimization minimizes legs when using CRUISE
    Given a long-distance route (671 units)
    And multiple intermediate fuel stations available
    When I plan route with prefer_cruise=True
    Then route should have 2-4 navigation legs maximum
    And total time should be under 15 minutes
    And all legs should use CRUISE mode

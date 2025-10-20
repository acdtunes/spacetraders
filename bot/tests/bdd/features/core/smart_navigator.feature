Feature: Smart navigation with fuel optimization
  As a fleet commander
  I want intelligent route planning with automatic fuel management
  So that ships can reach destinations safely and efficiently

  Background:
    Given a mock API client
    And a system graph for "X1-TEST"
    And a smart navigator for system "X1-TEST"

  Scenario: Plan direct route with sufficient fuel
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I plan route to "X1-TEST-B2"
    Then route planning should succeed
    And route should have 1 navigation step
    And route should use CRUISE mode
    And route should not require refuel stops

  Scenario: Plan route with automatic refuel stop insertion
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 50/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 200 from "X1-TEST-A1"
    And waypoint "X1-TEST-A5" is a marketplace at distance 80 from "X1-TEST-A1"
    When I plan route to "X1-TEST-B2"
    Then route planning should succeed
    And route should require refuel stops
    And route should include waypoint "X1-TEST-A5" for refueling

  Scenario: Validate route with sufficient fuel
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I validate route to "X1-TEST-B2"
    Then route validation should succeed
    And validation message should be "Route OK"

  Scenario: Validate route fails with insufficient fuel
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 0/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 200 from "X1-TEST-A1"
    And no refuel waypoints are available
    When I validate route to "X1-TEST-B2"
    Then route validation should fail
    And validation message should contain "Insufficient fuel"

  Scenario: Execute route with single navigation step
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I execute route to "X1-TEST-B2"
    Then route execution should succeed
    And the ship should be at "X1-TEST-B2"
    And fuel should be consumed for the journey

  Scenario: Execute route with refuel stop
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 150/400 fuel
    And the ship "TEST-SHIP" is IN_ORBIT at "X1-TEST-A1"
    And waypoint "X1-TEST-B2" exists at distance 200 from "X1-TEST-A1"
    And waypoint "X1-TEST-A5" is a marketplace at distance 80 from "X1-TEST-A1"
    When I execute route to "X1-TEST-B2"
    Then route execution should succeed
    And the ship should visit "X1-TEST-A5" for refueling
    And the ship should end at "X1-TEST-B2"
    And fuel should be replenished at "X1-TEST-A5"

  Scenario: Get fuel estimate for route
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 150 from "X1-TEST-A1"
    When I get fuel estimate for route to "X1-TEST-B2"
    Then fuel estimate should be provided
    And estimate should show total fuel cost
    And estimate should show final fuel level
    And estimate should indicate route feasibility

  Scenario: Flight mode auto-selection prefers CRUISE
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 350/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I plan route to "X1-TEST-B2"
    Then route planning should succeed
    And route should prefer CRUISE mode
    And route should not use DRIFT mode

  Scenario: Flight mode falls back to DRIFT when fuel low
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 70/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I plan route to "X1-TEST-B2"
    Then route planning should succeed
    And route should use DRIFT mode due to low fuel

  Scenario: Handle IN_TRANSIT state by waiting for arrival
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 300/400 fuel
    And the ship "TEST-SHIP" is IN_TRANSIT to "X1-TEST-B2"
    And the ship will arrive in 15 seconds
    And waypoint "X1-TEST-C3" exists at distance 80 from "X1-TEST-B2"
    When I execute route to "X1-TEST-C3"
    Then route execution should wait for arrival at "X1-TEST-B2"
    And then continue to "X1-TEST-C3"
    And route execution should succeed

  Scenario: Validate ship health before navigation
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And the ship has 40% frame integrity
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I execute route to "X1-TEST-B2"
    Then route execution should fail
    And error should indicate critical damage

  Scenario: Routing paused prevents route planning
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And routing is paused due to validation failure
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I plan route to "X1-TEST-B2"
    Then route planning should fail
    And error should indicate routing is paused

  Scenario: Plan multi-waypoint route with fuel optimization
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And waypoint "X1-TEST-B2" exists at distance 120 from "X1-TEST-A1"
    And waypoint "X1-TEST-C3" exists at distance 140 from "X1-TEST-B2"
    And waypoint "X1-TEST-A5" is a marketplace at distance 80 from "X1-TEST-A1"
    When I plan multi-stop route to "X1-TEST-B2" then "X1-TEST-C3"
    Then route planning should succeed
    And route should optimize fuel consumption
    And route should insert refuel stops as needed

  Scenario: Execute route handles state transitions automatically
    Given a ship "TEST-SHIP" at "X1-TEST-A1" with 400/400 fuel
    And the ship "TEST-SHIP" is DOCKED at "X1-TEST-A1"
    And waypoint "X1-TEST-B2" exists at distance 100 from "X1-TEST-A1"
    When I execute route to "X1-TEST-B2"
    Then the ship should orbit before navigating
    And route execution should succeed
    And the ship should be at "X1-TEST-B2"

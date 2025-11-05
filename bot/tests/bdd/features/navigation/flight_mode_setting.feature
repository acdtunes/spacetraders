Feature: Flight Mode Setting Before Navigation
  As a ship operator
  I want the ship to use the planned flight mode before each navigation
  So that fuel consumption matches the routing plan

  Background:
    Given the navigation system is initialized
    And a player with ID 1 exists
    And an API client for player 1

  # Single Segment - DRIFT Mode
  Scenario: Ship uses DRIFT mode for fuel-efficient navigation
    Given a ship "TEST-SHIP-1" at waypoint "X1-A1" with 100 fuel
    And waypoint "X1-B2" is 200 units away
    And the routing engine plans a route with DRIFT mode
    When I navigate the ship to "X1-B2"
    Then the API should set flight mode to "DRIFT" before navigating
    And the ship should navigate to "X1-B2"

  # Single Segment - CRUISE Mode
  Scenario: Ship uses CRUISE mode for balanced navigation
    Given a ship "TEST-SHIP-1" at waypoint "X1-A1" with 300 fuel
    And waypoint "X1-B2" is 200 units away
    And the routing engine plans a route with CRUISE mode
    When I navigate the ship to "X1-B2"
    Then the API should set flight mode to "CRUISE" before navigating
    And the ship should navigate to "X1-B2"

  # Single Segment - BURN Mode
  Scenario: Ship uses BURN mode for high-speed navigation
    Given a ship "TEST-SHIP-1" at waypoint "X1-A1" with 500 fuel
    And waypoint "X1-B2" is 150 units away
    And the routing engine plans a route with BURN mode
    When I navigate the ship to "X1-B2"
    Then the API should set flight mode to "BURN" before navigating
    And the ship should navigate to "X1-B2"


  # Verify Correct Flight Mode Applied
  Scenario: Ship consumes fuel according to DRIFT mode
    Given a ship "TEST-SHIP-1" at waypoint "X1-A1" with 200 fuel
    And waypoint "X1-B2" is 150 units away
    And the routing engine plans a route with DRIFT mode
    When I navigate the ship to "X1-B2"
    Then set_flight_mode should be called before navigate_ship
    And the mode parameter should be "DRIFT"

  # Verify Fuel Consumption for BURN Mode
  Scenario: Ship consumes fuel according to BURN mode
    Given a ship "TEST-SHIP-1" at waypoint "X1-A1" with 500 fuel
    And waypoint "X1-B2" is 100 units away
    And the routing engine plans a route with BURN mode
    When I navigate the ship to "X1-B2"
    Then set_flight_mode should be called before navigate_ship
    And the mode parameter should be "BURN"

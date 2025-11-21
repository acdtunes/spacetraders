Feature: Ship Fuel Service
  As a domain service
  The ShipFuelService provides fuel management calculations and decisions
  To support ship navigation and refueling operations

  Background:
    Given a ship fuel service

  Scenario: Calculate fuel required for trip in CRUISE mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required from "X1-A1" to "X1-B2" in CRUISE mode
    Then the service fuel required should be 100 units

  Scenario: Calculate fuel required for trip in DRIFT mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required from "X1-A1" to "X1-B2" in DRIFT mode
    Then the service fuel required should be 1 units

  Scenario: Calculate fuel required for trip in BURN mode
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (100, 0)
    When I calculate fuel required from "X1-A1" to "X1-B2" in BURN mode
    Then the service fuel required should be 200 units

  Scenario: Check if ship can navigate to destination with sufficient fuel
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (50, 0)
    When I check if ship with 100 units of fuel can navigate from "X1-A1" to "X1-B2"
    Then the service result should be true

  Scenario: Check if ship cannot navigate with insufficient fuel
    Given waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (200, 0)
    When I check if ship with 0 units of fuel can navigate from "X1-A1" to "X1-B2"
    Then the service result should be false

  Scenario: Ship needs refuel for journey beyond fuel capacity
    Given a fuel state with 50 units of fuel and capacity 100
    And waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (200, 0)
    When I check if refuel needed from "X1-A1" to "X1-B2" with safety margin 0.1
    Then the service result should be true

  Scenario: Ship does not need refuel for short journey
    Given a fuel state with 100 units of fuel and capacity 100
    And waypoint "X1-A1" at coordinates (0, 0)
    And waypoint "X1-B2" at coordinates (20, 0)
    When I check if refuel needed from "X1-A1" to "X1-B2" with safety margin 0.1
    Then the service result should be false

  Scenario: Select BURN mode when fuel is abundant
    When I select optimal flight mode with 200 fuel for distance 50.0 with safety margin 4
    Then the selected flight mode should be "BURN"

  Scenario: Select CRUISE mode when fuel is moderate
    When I select optimal flight mode with 120 fuel for distance 100.0 with safety margin 4
    Then the selected flight mode should be "CRUISE"

  Scenario: Select DRIFT mode when fuel is low
    When I select optimal flight mode with 10 fuel for distance 100.0 with safety margin 4
    Then the selected flight mode should be "DRIFT"

  Scenario: Should refuel opportunistically when below threshold at fuel station
    Given a fuel state with 80 units of fuel and capacity 100
    And waypoint "X1-STATION" at coordinates (0, 0) with fuel available
    When I check if should refuel opportunistically at "X1-STATION" with threshold 0.9
    Then the service result should be true

  Scenario: Should not refuel opportunistically when above threshold
    Given a fuel state with 95 units of fuel and capacity 100
    And waypoint "X1-STATION" at coordinates (0, 0) with fuel available
    When I check if should refuel opportunistically at "X1-STATION" with threshold 0.9
    Then the service result should be false

  Scenario: Should not refuel opportunistically at waypoint without fuel
    Given a fuel state with 50 units of fuel and capacity 100
    And waypoint "X1-ASTEROID" at coordinates (0, 0) without fuel
    When I check if should refuel opportunistically at "X1-ASTEROID" with threshold 0.9
    Then the service result should be false

  Scenario: Should not refuel opportunistically for ship with zero fuel capacity
    Given a fuel state with 0 units of fuel and capacity 0
    And waypoint "X1-STATION" at coordinates (0, 0) with fuel available
    When I check if should refuel opportunistically at "X1-STATION" with threshold 0.9
    Then the service result should be false

  Scenario: Calculate fuel needed to fill tank
    When I calculate fuel needed to full with current 60 and capacity 100
    Then the service fuel needed should be 40 units

  Scenario: Calculate fuel needed when tank is full
    When I calculate fuel needed to full with current 100 and capacity 100
    Then the service fuel needed should be 0 units

  Scenario: Calculate fuel needed when current exceeds capacity
    When I calculate fuel needed to full with current 110 and capacity 100
    Then the service fuel needed should be 0 units

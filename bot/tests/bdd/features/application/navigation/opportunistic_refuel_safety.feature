Feature: Navigate Command Opportunistic Refueling Safety Check
  The navigate command should refuel opportunistically when arriving at
  a waypoint with fuel and the ship's fuel is below 90% capacity.

  Background:
    Given a player with ID 1 exists in the database

  Scenario: Navigate command refuels when arriving with fuel below 90%
    Given a ship with 400 fuel capacity
    And the ship has 300 fuel
    And the ship arrives at waypoint "X1-A1" with MARKETPLACE
    When the navigate command executes a segment arrival
    Then the ship should opportunistically refuel at "X1-A1"
    And the ship should have full fuel after refueling

  Scenario: Navigate command does not refuel when fuel above 90%
    Given a ship with 400 fuel capacity
    And the ship has 380 fuel
    And the ship arrives at waypoint "X1-A1" with MARKETPLACE
    When the navigate command executes a segment arrival
    Then the ship should NOT opportunistically refuel
    And the ship fuel should remain unchanged

  Scenario: Navigate command does not refuel at waypoint without fuel
    Given a ship with 400 fuel capacity
    And the ship has 100 fuel
    And the ship arrives at waypoint "X1-B2" ASTEROID without fuel
    When the navigate command executes a segment arrival
    Then the ship should NOT opportunistically refuel
    And the ship fuel should remain unchanged

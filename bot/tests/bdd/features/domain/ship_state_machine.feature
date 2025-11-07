Feature: Ship Navigation State Machine

  As a fleet operator
  I want ships to enforce valid state transitions
  So that ships cannot perform impossible operations while in transit

  Background:
    Given a ship with the following state:
      | field          | value        |
      | ship_symbol    | TEST-SHIP-1  |
      | player_id      | 1            |
      | location       | X1-TEST-A1   |
      | fuel_current   | 400          |
      | fuel_capacity  | 400          |
      | cargo_capacity | 40           |
      | cargo_units    | 0            |
      | engine_speed   | 30           |

  Scenario: Cannot orbit while in transit
    Given the ship is in status "IN_TRANSIT"
    When the ship attempts to orbit
    Then the operation should fail with "Cannot orbit while in transit"

  Scenario: Cannot dock while in transit
    Given the ship is in status "IN_TRANSIT"
    When the ship attempts to dock
    Then the operation should fail with "Cannot dock while in transit"

  Scenario: Valid transition from DOCKED to IN_ORBIT
    Given the ship is in status "DOCKED"
    When the ship departs to orbit
    Then the ship should be in status "IN_ORBIT"

  Scenario: Valid transition from IN_ORBIT to DOCKED
    Given the ship is in status "IN_ORBIT"
    When the ship docks
    Then the ship should be in status "DOCKED"

  Scenario: Valid transition from IN_TRANSIT to IN_ORBIT (arrival)
    Given the ship is in status "IN_TRANSIT"
    When the ship arrives at destination
    Then the ship should be in status "IN_ORBIT"

Feature: Jettison Cargo Command
  As a contract workflow system
  I need to jettison unwanted cargo from ships
  So that ships can carry only required contract goods

  Background:
    Given a player exists with ID 1
    And the player has agent symbol "TEST-AGENT"

  Scenario: Successfully jettison cargo via API
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 10 units of "COPPER" in cargo
    And the ship is docked at waypoint "X1-TEST-A1"
    When I jettison 5 units of "COPPER" from ship "TEST-AGENT-1"
    Then the jettison command should succeed
    And the API should receive jettison request for ship "TEST-AGENT-1" with cargo "COPPER" and 5 units

  Scenario: Jettison all cargo of specific type
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 20 units of "IRON_ORE" in cargo
    And the ship has 10 units of "COPPER" in cargo
    And the ship is in orbit at waypoint "X1-TEST-A1"
    When I jettison 20 units of "IRON_ORE" from ship "TEST-AGENT-1"
    Then the jettison command should succeed
    And the API should receive jettison request for ship "TEST-AGENT-1" with cargo "IRON_ORE" and 20 units

  Scenario: Jettison cargo when ship is in orbit
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 15 units of "ALUMINUM" in cargo
    And the ship is in orbit at waypoint "X1-TEST-B1"
    When I jettison 15 units of "ALUMINUM" from ship "TEST-AGENT-1"
    Then the jettison command should succeed
    And the API should receive jettison request for ship "TEST-AGENT-1" with cargo "ALUMINUM" and 15 units

Feature: Refuel Ship Command
  As a SpaceTraders bot
  I want to refuel ships at fuel stations
  So that I can prepare for journeys and continue operations

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Refuel ship to full capacity at fuel station
    Given a ship "SHIP-1" for player 1 at fuel station "X1-A1" with status "DOCKED" and fuel 50/100
    When I execute RefuelShipCommand for ship "SHIP-1" and player 1 with nil units
    Then the refuel command should succeed
    And the ship should have fuel 100/100
    And 50 units of fuel should have been added

  Scenario: Refuel ship with specific units at fuel station
    Given a ship "SHIP-1" for player 1 at fuel station "X1-A1" with status "DOCKED" and fuel 30/100
    When I execute RefuelShipCommand for ship "SHIP-1" and player 1 with 20 units
    Then the refuel command should succeed
    And the ship should have fuel 50/100
    And 20 units of fuel should have been added

  Scenario: Refuel ship that already has full fuel (idempotent)
    Given a ship "SHIP-1" for player 1 at fuel station "X1-A1" with status "DOCKED" and fuel 100/100
    When I execute RefuelShipCommand for ship "SHIP-1" and player 1 with nil units
    Then the refuel command should succeed
    And the ship should have fuel 100/100
    And 0 units of fuel should have been added

  Scenario: Auto-dock ship before refueling when in orbit
    Given a ship "SHIP-1" for player 1 at fuel station "X1-A1" with status "IN_ORBIT" and fuel 50/100
    When I execute RefuelShipCommand for ship "SHIP-1" and player 1 with nil units
    Then the refuel command should succeed
    And the ship should have fuel 100/100
    And 50 units of fuel should have been added

  Scenario: Cannot refuel ship not at fuel station
    Given a ship "SHIP-1" for player 1 at waypoint "X1-B2" without fuel with status "DOCKED" and fuel 50/100
    When I execute RefuelShipCommand for ship "SHIP-1" and player 1 with nil units
    Then the refuel command should fail with error "waypoint does not have fuel station"

  Scenario: Cannot refuel ship that does not exist
    When I execute RefuelShipCommand for ship "NONEXISTENT" and player 1 with nil units
    Then the refuel command should fail with error "ship not found"

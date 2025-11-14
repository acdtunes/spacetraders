Feature: Dock Ship Command
  As a SpaceTraders bot
  I want to dock ships at waypoints
  So that I can refuel and access station services

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Dock ship when already docked
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    When I execute DockShipCommand for ship "SHIP-1" and player 1
    Then the dock command should succeed with status "already_docked"
    And the ship should still be docked

  Scenario: Dock ship when in orbit
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute DockShipCommand for ship "SHIP-1" and player 1
    Then the dock command should succeed with status "docked"
    And the ship should be docked at "X1-A1"

  Scenario: Cannot dock ship when in transit
    Given a ship "SHIP-1" for player 1 in transit to "X1-B2"
    When I execute DockShipCommand for ship "SHIP-1" and player 1
    Then the dock command should fail with error "cannot dock while in transit"

  Scenario: Cannot dock ship that does not exist
    When I execute DockShipCommand for ship "NONEXISTENT" and player 1
    Then the dock command should fail with error "ship not found"

  Scenario: Cannot dock ship belonging to different player
    Given a ship "SHIP-1" for player 2 at "X1-A1" with status "IN_ORBIT"
    When I execute DockShipCommand for ship "SHIP-1" and player 1
    Then the dock command should fail with error "ship not found"

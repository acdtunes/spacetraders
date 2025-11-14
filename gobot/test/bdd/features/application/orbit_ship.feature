Feature: Orbit Ship Command
  As a SpaceTraders bot
  I want to put ships into orbit
  So that I can navigate to other waypoints

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Orbit ship when already in orbit
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "IN_ORBIT"
    When I execute OrbitShipCommand for ship "SHIP-1" and player 1
    Then the orbit command should succeed with status "already_in_orbit"
    And the ship should still be in orbit

  Scenario: Orbit ship when docked
    Given a ship "SHIP-1" for player 1 at "X1-A1" with status "DOCKED"
    When I execute OrbitShipCommand for ship "SHIP-1" and player 1
    Then the orbit command should succeed with status "in_orbit"
    And the ship should be in orbit at "X1-A1"

  Scenario: Cannot orbit ship when in transit
    Given a ship "SHIP-1" for player 1 in transit to "X1-B2"
    When I execute OrbitShipCommand for ship "SHIP-1" and player 1
    Then the orbit command should fail with error "cannot orbit while in transit"

  Scenario: Cannot orbit ship that does not exist
    When I execute OrbitShipCommand for ship "NONEXISTENT" and player 1
    Then the orbit command should fail with error "ship not found"

  Scenario: Cannot orbit ship belonging to different player
    Given a ship "SHIP-1" for player 2 at "X1-A1" with status "DOCKED"
    When I execute OrbitShipCommand for ship "SHIP-1" and player 1
    Then the orbit command should fail with error "ship not found"

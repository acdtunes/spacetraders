Feature: Jettison Cargo Command
  As a SpaceTraders bot
  I want to jettison cargo from ships
  So that I can free up cargo space when needed

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Jettison cargo successfully from ship in orbit
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1" with cargo:
      | symbol    | units |
      | IRON_ORE  | 50    |
    When I execute JettisonCargoCommand for ship "SHIP-1" jettisoning 30 units of "IRON_ORE"
    Then the jettison command should succeed
    And 30 units should have been jettisoned
    And the ship should have 20 units of "IRON_ORE" remaining

  Scenario: Jettison all cargo of a type
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1" with cargo:
      | symbol    | units |
      | IRON_ORE  | 50    |
    When I execute JettisonCargoCommand for ship "SHIP-1" jettisoning 50 units of "IRON_ORE"
    Then the jettison command should succeed
    And 50 units should have been jettisoned
    And the ship should have 0 units of "IRON_ORE" remaining

  Scenario: Cannot jettison more cargo than ship has
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1" with cargo:
      | symbol    | units |
      | IRON_ORE  | 20    |
    When I execute JettisonCargoCommand for ship "SHIP-1" jettisoning 30 units of "IRON_ORE"
    Then the jettison command should fail with error "insufficient cargo"

  Scenario: Cannot jettison cargo ship doesn't have at all
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1" with cargo:
      | symbol    | units |
    When I execute JettisonCargoCommand for ship "SHIP-1" jettisoning 30 units of "IRON_ORE"
    Then the jettison command should fail with error "insufficient cargo"

  Scenario: Ship not found
    When I execute JettisonCargoCommand for ship "NON-EXISTENT" jettisoning 30 units of "IRON_ORE"
    Then the jettison command should fail with error "ship not found"

  Scenario: Auto-orbit ship before jettisoning when docked
    Given a ship "SHIP-1" for player 1 docked at "X1-A1" with cargo:
      | symbol    | units |
      | IRON_ORE  | 50    |
    When I execute JettisonCargoCommand for ship "SHIP-1" jettisoning 30 units of "IRON_ORE"
    Then the jettison command should succeed
    And 30 units should have been jettisoned
    And the ship should have 20 units of "IRON_ORE" remaining

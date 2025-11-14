Feature: Purchase Cargo Command
  As a SpaceTraders bot
  I want to purchase cargo for my ships
  So that I can fulfill contracts and trade goods

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Purchase cargo successfully
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1"
    And the ship has 50 cargo space available
    When I execute purchase cargo command for 30 units of "IRON_ORE" on ship "SHIP-1"
    Then the purchase cargo command should succeed
    And 30 units should be added to cargo
    And the total cost should be greater than 0

  Scenario: Cannot purchase when ship not docked
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1"
    When I attempt to execute purchase cargo command for 30 units of "IRON_ORE" on ship "SHIP-1"
    Then the purchase cargo command should fail with error "ship must be docked to purchase cargo"

  Scenario: Cannot purchase more than cargo space
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1"
    And the ship has 20 cargo space available
    When I attempt to execute purchase cargo command for 30 units of "IRON_ORE" on ship "SHIP-1"
    Then the purchase cargo command should fail with error "insufficient cargo space: need 30, have 20"

  Scenario: Ship not found
    When I attempt to execute purchase cargo command for 30 units of "IRON_ORE" on ship "NON-EXISTENT"
    Then the purchase cargo command should fail with error "ship not found"

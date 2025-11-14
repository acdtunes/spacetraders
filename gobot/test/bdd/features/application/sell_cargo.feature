Feature: Sell Cargo Command
  As a SpaceTraders bot
  I want to sell cargo from my ships at marketplaces
  So that I can earn credits and free up cargo space

  Background:
    Given a player exists with agent "TEST-AGENT" and token "test-token-123"
    And the player has player_id 1

  Scenario: Sell cargo successfully
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1" with cargo
    And the ship contains 30 units of "IRON_ORE"
    When I execute sell cargo command for 20 units of "IRON_ORE" from ship "SHIP-1"
    Then the sell cargo command should succeed
    And 20 units should be sold from cargo
    And the total revenue should be greater than 0

  Scenario: Sell all cargo units
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1" with cargo
    And the ship contains 50 units of "IRON_ORE"
    When I execute sell cargo command for 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the sell cargo command should succeed
    And 50 units should be sold from cargo

  Scenario: Cannot sell when ship not docked
    Given a ship "SHIP-1" for player 1 in orbit at "X1-A1" with cargo
    And the ship contains 30 units of "IRON_ORE"
    When I attempt to execute sell cargo command for 20 units of "IRON_ORE" from ship "SHIP-1"
    Then the sell cargo command should fail with error "ship must be docked to sell cargo"

  Scenario: Cannot sell more than ship has
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1" with cargo
    And the ship contains 10 units of "IRON_ORE"
    When I attempt to execute sell cargo command for 20 units of "IRON_ORE" from ship "SHIP-1"
    Then the sell cargo command should fail with error "insufficient cargo: need 20, have 10"

  Scenario: Cannot sell cargo ship doesn't have
    Given a ship "SHIP-1" for player 1 docked at marketplace "X1-A1" with cargo
    And the ship contains 30 units of "IRON_ORE"
    When I attempt to execute sell cargo command for 10 units of "COPPER_ORE" from ship "SHIP-1"
    Then the sell cargo command should fail with error "insufficient cargo: need 10, have 0"

  Scenario: Ship not found
    When I attempt to execute sell cargo command for 20 units of "IRON_ORE" from ship "NON-EXISTENT"
    Then the sell cargo command should fail with error "ship not found"

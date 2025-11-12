Feature: Trading CLI
  As a fleet operator
  I want to sell cargo from ships via CLI
  So that I can convert goods to credits

  Background:
    Given a registered player with agent "TEST_AGENT" and player ID 1
    And a ship "TEST_AGENT-1" exists for player 1 at "X1-TEST-M1"
    And the ship is docked at "X1-TEST-M1"
    And the ship has 10 units of "IRON_ORE" in cargo

  Scenario: Sell cargo successfully via CLI
    Given the market at "X1-TEST-M1" buys "IRON_ORE" for 50 credits per unit
    When I run the CLI command "sell --ship TEST_AGENT-1 --good IRON_ORE --units 5 --agent TEST_AGENT"
    Then the command should succeed
    And the ship should have 5 units of "IRON_ORE" in cargo
    And the player should gain 250 credits

  Scenario: Sell cargo with explicit player ID
    Given the market at "X1-TEST-M1" buys "IRON_ORE" for 50 credits per unit
    When I run the CLI command "sell --ship TEST_AGENT-1 --good IRON_ORE --units 5 --player-id 1"
    Then the command should succeed

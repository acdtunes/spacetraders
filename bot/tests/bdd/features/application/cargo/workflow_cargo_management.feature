Feature: Contract Workflow Cargo Management
  As a contract workflow system
  I need to manage ship cargo intelligently
  So that required contract cargo is never jettisoned

  Background:
    Given a player exists with ID 1
    And the player has agent symbol "TEST-AGENT"

  Scenario: Ship has exact required cargo - no jettison, no purchase
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 30 units of "IRON_ORE" in cargo
    And the ship has 5 units of "COPPER" in cargo
    And a contract requires delivering 30 units of "IRON_ORE"
    When the workflow processes cargo for delivery
    Then the workflow should jettison 5 units of "COPPER"
    And the workflow should NOT jettison any "IRON_ORE"
    And the workflow should determine 0 units to purchase
    And the workflow should proceed directly to delivery

  Scenario: Ship has more than required cargo - no jettison of excess required cargo
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 40 units of "IRON_ORE" in cargo
    And the ship has 10 units of "COPPER" in cargo
    And a contract requires delivering 30 units of "IRON_ORE"
    When the workflow processes cargo for delivery
    Then the workflow should jettison 10 units of "COPPER"
    And the workflow should NOT jettison any "IRON_ORE"
    And the workflow should determine 0 units to purchase
    And the workflow should proceed directly to delivery with 40 units

  Scenario: Ship has partial required cargo with wrong cargo
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 15 units of "IRON_ORE" in cargo
    And the ship has 20 units of "COPPER" in cargo
    And a contract requires delivering 30 units of "IRON_ORE"
    When the workflow processes cargo for delivery
    Then the workflow should jettison 20 units of "COPPER"
    And the workflow should NOT jettison any "IRON_ORE"
    And the workflow should determine 15 units to purchase after jettison
    And the workflow should proceed to purchase and delivery

  Scenario: Ship has only wrong cargo
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 30 units of "COPPER" in cargo
    And the ship has 10 units of "ALUMINUM" in cargo
    And a contract requires delivering 30 units of "IRON_ORE"
    When the workflow processes cargo for delivery
    Then the workflow should jettison 30 units of "COPPER"
    And the workflow should jettison 10 units of "ALUMINUM"
    And the workflow should NOT jettison any "IRON_ORE"
    And the workflow should determine 30 units to purchase after jettison
    And the workflow should proceed to purchase and delivery

  Scenario: Ship has only required cargo - no jettison, no purchase
    Given a ship "TEST-AGENT-1" exists for player 1
    And the ship has 30 units of "IRON_ORE" in cargo
    And a contract requires delivering 30 units of "IRON_ORE"
    When the workflow processes cargo for delivery
    Then the workflow should NOT jettison any cargo
    And the workflow should determine 0 units to purchase
    And the workflow should proceed directly to delivery

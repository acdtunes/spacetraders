Feature: Purchase Cargo
  As a fleet operator
  I want to purchase goods from markets
  So that I can fulfill contract delivery requirements

  Background:
    Given a registered player with agent "TEST_AGENT"
    And a ship "TEST_AGENT-1" exists in the database
    And the ship is docked at waypoint "X1-TEST-M1"
    And the ship has 50 units of cargo space available
    And the player has 10000 credits

  Scenario: Purchase cargo from market successfully
    Given the market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    When I purchase 10 units of "IRON_ORE" for ship "TEST_AGENT-1"
    Then the purchase should succeed
    And the ship should have 10 units of "IRON_ORE" in cargo
    And 1000 credits should be deducted from player balance

  Scenario: Purchase cargo fills available cargo space
    Given the market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the ship has 30 units of cargo space available
    When I purchase 30 units of "IRON_ORE" for ship "TEST_AGENT-1"
    Then the purchase should succeed
    And the ship cargo should be full

  Scenario: Fail to purchase when ship is not docked
    Given the ship is in orbit at waypoint "X1-TEST-M1"
    And the market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    When I purchase 10 units of "IRON_ORE" for ship "TEST_AGENT-1"
    Then the purchase should fail with "Ship must be docked to purchase cargo"

  Scenario: Fail to purchase when insufficient credits
    Given the market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the player has 500 credits
    When I purchase 10 units of "IRON_ORE" for ship "TEST_AGENT-1"
    Then the purchase should fail with "Insufficient credits"

  Scenario: Fail to purchase when insufficient cargo space
    Given the market at "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And the ship has 5 units of cargo space available
    When I purchase 10 units of "IRON_ORE" for ship "TEST_AGENT-1"
    Then the purchase should fail with "Insufficient cargo space"

Feature: Find Cheapest Market
  As a fleet operator
  I want to find the cheapest market selling specific goods
  So that I can minimize costs when purchasing resources

  Background:
    Given a player with agent "TEST_AGENT"

  Scenario: Find cheapest market in system
    Given market "X1-TEST-M1" sells "IRON_ORE" for 100 credits per unit
    And market "X1-TEST-M2" sells "IRON_ORE" for 80 credits per unit
    And market "X1-TEST-M3" sells "IRON_ORE" for 120 credits per unit
    When I search for cheapest market selling "IRON_ORE" in system "X1-TEST"
    Then the cheapest market should be "X1-TEST-M2"
    And the price should be 80 credits per unit

  Scenario: No market sells the requested good
    Given market "X1-TEST-M1" sells "COPPER_ORE" for 100 credits per unit
    When I search for cheapest market selling "IRON_ORE" in system "X1-TEST"
    Then no market should be found

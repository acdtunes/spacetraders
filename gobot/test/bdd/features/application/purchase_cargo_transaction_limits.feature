Feature: Purchase Cargo with Transaction Limit Splitting

  Background:
    Given a player with ID 1

  Scenario: Purchase within single transaction limit
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 100 units of cargo space
    And market "X1-A1" sells "IRON_ORE" with transaction limit 100
    When I purchase 50 units of "IRON_ORE" for ship "SHIP-1"
    Then the purchase should succeed
    And 1 transaction should have been executed
    And 50 units should have been purchased

  Scenario: Purchase exceeds market transaction limit - requires splitting
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 100 units of cargo space
    And market "X1-A1" sells "IRON_ORE" with transaction limit 20
    When I purchase 50 units of "IRON_ORE" for ship "SHIP-1"
    Then the purchase should succeed
    And 3 transactions should have been executed
    And 50 units should have been purchased

  Scenario: Market data unavailable - single transaction fallback
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 100 units of cargo space
    And market data is not available for "X1-A1"
    When I purchase 30 units of "IRON_ORE" for ship "SHIP-1"
    Then the purchase should succeed
    And 1 transaction should have been executed
    And 30 units should have been purchased

  Scenario: Partial failure during multi-transaction purchase
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 100 units of cargo space
    And market "X1-A1" sells "IRON_ORE" with transaction limit 20
    And API will fail on transaction 3 with "insufficient credits"
    When I purchase 50 units of "IRON_ORE" for ship "SHIP-1"
    Then the purchase should return partial success
    And 2 transactions should have been executed
    And 40 units should have been purchased
    And the transaction error should mention "partial failure"

  Scenario: Good not available at market - use fallback limit
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 100 units of cargo space
    And market "X1-A1" exists but doesn't sell "RARE_METAL"
    When I purchase 30 units of "RARE_METAL" for ship "SHIP-1"
    Then the purchase should succeed
    And 1 transaction should have been executed

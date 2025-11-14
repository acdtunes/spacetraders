Feature: Sell Cargo with Transaction Limit Splitting

  Background:
    Given a player with ID 1

  Scenario: Sell within single transaction limit
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 50 units of "IRON_ORE" in cargo
    And market "X1-A1" buys "IRON_ORE" with transaction limit 100
    When I sell 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the sale should succeed
    And 1 transaction should have been executed
    And 50 units should have been sold

  Scenario: Sell exceeds market transaction limit - requires splitting
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 50 units of "IRON_ORE" in cargo
    And market "X1-A1" buys "IRON_ORE" with transaction limit 20
    When I sell 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the sale should succeed
    And 3 transactions should have been executed
    And 50 units should have been sold

  Scenario: Market data unavailable - single transaction fallback
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 30 units of "IRON_ORE" in cargo
    And market data is not available for "X1-A1"
    When I sell 30 units of "IRON_ORE" from ship "SHIP-1"
    Then the sale should succeed
    And 1 transaction should have been executed
    And 30 units should have been sold

  Scenario: Partial failure during multi-transaction sale
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 50 units of "IRON_ORE" in cargo
    And market "X1-A1" buys "IRON_ORE" with transaction limit 20
    And API will fail on transaction 3 with "market oversaturated"
    When I sell 50 units of "IRON_ORE" from ship "SHIP-1"
    Then the sale should return partial success
    And 2 transactions should have been executed
    And 40 units should have been sold
    And the transaction error should mention "partial failure"

  Scenario: Good not available at market - use fallback limit
    Given a ship "SHIP-1" docked at waypoint "X1-A1"
    And the ship has 30 units of "RARE_METAL" in cargo
    And market "X1-A1" exists but doesn't buy "RARE_METAL"
    When I sell 30 units of "RARE_METAL" from ship "SHIP-1"
    Then the sale should succeed
    And 1 transaction should have been executed

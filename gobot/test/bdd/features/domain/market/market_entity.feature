Feature: Market Entity
  As a SpaceTraders bot
  I want to manage market entities as immutable snapshots
  So that I can safely access market data without mutation

  # ============================================================================
  # TradeGood Creation and Validation
  # ============================================================================

  Scenario: Create valid trade good with all fields
    Given a trade good with:
      | symbol | supply   | activity  | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG    | 200            | 250        | 100          |
    When I create the trade good
    Then the trade good should be valid
    And the trade good symbol should be "IRON_ORE"
    And the trade good supply should be "MODERATE"
    And the trade good activity should be "STRONG"
    And the trade good purchase price should be 200
    And the trade good sell price should be 250
    And the trade good trade volume should be 100

  Scenario: Create trade good with nil supply and activity
    Given a trade good with:
      | symbol     | supply | activity | purchase_price | sell_price | trade_volume |
      | COPPER_ORE |        |          | 150            | 180        | 50           |
    When I create the trade good
    Then the trade good should be valid
    And the trade good supply should be nil
    And the trade good activity should be nil

  Scenario: Create trade good with zero prices
    Given a trade good with:
      | symbol | supply | activity | purchase_price | sell_price | trade_volume |
      | FUEL   | HIGH   | WEAK     | 0              | 0          | 200          |
    When I create the trade good
    Then the trade good should be valid
    And the trade good purchase price should be 0
    And the trade good sell price should be 0

  Scenario: Reject trade good with empty symbol
    Given a trade good with:
      | symbol | supply   | activity | purchase_price | sell_price | trade_volume |
      |        | MODERATE | STRONG   | 200            | 250        | 100          |
    When I attempt to create the trade good
    Then trade good creation should fail with error "symbol cannot be empty"

  Scenario: Reject trade good with negative purchase price
    Given a trade good with:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | -100           | 250        | 100          |
    When I attempt to create the trade good
    Then trade good creation should fail with error "purchase price must be non-negative"

  Scenario: Reject trade good with negative sell price
    Given a trade good with:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 200            | -50        | 100          |
    When I attempt to create the trade good
    Then trade good creation should fail with error "sell price must be non-negative"

  Scenario: Reject trade good with negative trade volume
    Given a trade good with:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 200            | 250        | -10          |
    When I attempt to create the trade good
    Then trade good creation should fail with error "trade volume must be non-negative"

  Scenario Outline: Validate supply values
    Given a trade good with supply "<supply>"
    When I attempt to create the trade good
    Then <result>

    Examples:
      | supply    | result                                                            |
      | SCARCE    | the trade good should be valid                                    |
      | LIMITED   | the trade good should be valid                                    |
      | MODERATE  | the trade good should be valid                                    |
      | HIGH      | the trade good should be valid                                    |
      | ABUNDANT  | the trade good should be valid                                    |
      | INVALID   | trade good creation should fail with error "invalid supply value: INVALID" |

  Scenario Outline: Validate activity values
    Given a trade good with activity "<activity>"
    When I attempt to create the trade good
    Then <result>

    Examples:
      | activity   | result                                                              |
      | WEAK       | the trade good should be valid                                      |
      | GROWING    | the trade good should be valid                                      |
      | STRONG     | the trade good should be valid                                      |
      | RESTRICTED | the trade good should be valid                                      |
      | INVALID    | trade good creation should fail with error "invalid activity value: INVALID" |

  # ============================================================================
  # Market Creation and Validation
  # ============================================================================

  Scenario: Create valid market with multiple trade goods
    Given trade goods for market:
      | symbol     | supply   | activity  | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG    | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK      | 150            | 180        | 50           |
      | ALUMINUM   | LIMITED  | GROWING   | 300            | 350        | 75           |
    And a market at waypoint "X1-MARKET" updated at "2025-01-15T10:00:00Z"
    When I create the market
    Then the market should be valid
    And the market waypoint symbol should be "X1-MARKET"
    And the market should have 3 trade goods
    And the market last updated should be "2025-01-15T10:00:00Z"

  Scenario: Create market with single trade good
    Given trade goods for market:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 200            | 250        | 100          |
    And a market at waypoint "X1-STATION" updated at "2025-01-15T12:30:00Z"
    When I create the market
    Then the market should be valid
    And the market should have 1 trade goods

  Scenario: Create market with empty trade goods list
    Given no trade goods for market
    And a market at waypoint "X1-EMPTY" updated at "2025-01-15T15:00:00Z"
    When I create the market
    Then the market should be valid
    And the market should have 0 trade goods

  Scenario: Reject market with empty waypoint symbol
    Given trade goods for market:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 200            | 250        | 100          |
    And a market at waypoint "" updated at "2025-01-15T10:00:00Z"
    When I attempt to create the market
    Then market creation should fail with error "waypoint symbol cannot be empty"

  Scenario: Reject market with zero timestamp
    Given trade goods for market:
      | symbol   | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE | MODERATE | STRONG   | 200            | 250        | 100          |
    And a market at waypoint "X1-MARKET" updated at "0001-01-01T00:00:00Z"
    When I attempt to create the market
    Then market creation should fail with error "timestamp cannot be empty"

  # ============================================================================
  # Market FindGood and HasGood
  # ============================================================================

  Scenario: FindGood returns trade good when found
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I find good "IRON_ORE"
    Then the found good should exist
    And the found good symbol should be "IRON_ORE"
    And the found good purchase price should be 200

  Scenario: FindGood returns nil when not found
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I find good "GOLD_ORE"
    Then the found good should not exist

  Scenario: HasGood returns true when good exists
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I check if market has good "COPPER_ORE"
    Then the market should have the good

  Scenario: HasGood returns false when good does not exist
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I check if market has good "PLATINUM"
    Then the market should not have the good

  # ============================================================================
  # Market GoodsCount
  # ============================================================================

  Scenario: GoodsCount returns correct count
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
      | ALUMINUM   | LIMITED  | GROWING  | 300            | 350        | 75           |
    When I get the goods count
    Then the goods count should be 3

  Scenario: GoodsCount returns zero for empty market
    Given a valid market with no trade goods
    When I get the goods count
    Then the goods count should be 0

  # ============================================================================
  # Market GetTransactionLimit
  # ============================================================================

  Scenario: GetTransactionLimit returns trade volume for existing good
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I get transaction limit for "IRON_ORE"
    Then the transaction limit should be 100

  Scenario: GetTransactionLimit returns zero for non-existent good
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I get transaction limit for "GOLD_ORE"
    Then the transaction limit should be 0

  # ============================================================================
  # Market Immutability
  # ============================================================================

  Scenario: TradeGoods returns defensive copy
    Given a valid market with trade goods:
      | symbol     | supply   | activity | purchase_price | sell_price | trade_volume |
      | IRON_ORE   | MODERATE | STRONG   | 200            | 250        | 100          |
      | COPPER_ORE | HIGH     | WEAK     | 150            | 180        | 50           |
    When I get the trade goods
    And I modify the returned trade goods array
    Then the original market trade goods should be unchanged

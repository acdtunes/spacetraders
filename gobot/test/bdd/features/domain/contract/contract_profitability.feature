Feature: Contract Profitability Evaluation
  As a SpaceTraders bot
  I want to evaluate contract profitability based on market conditions
  So that I can make informed decisions about which contracts to accept

  # ============================================================================
  # Profitable Contracts
  # ============================================================================

  Scenario: Highly profitable single-delivery contract
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 200        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be 39000
    And total payment should be 60000
    And purchase cost should be 20000
    And fuel cost should be 1000
    And trips required should be 1
    And profitability reason should be "Profitable"

  Scenario: Profitable multi-delivery contract with multiple trips
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 20000       | 80000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
      | COPPER_ORE   | X1-MARKET   | 150            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 2000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 150        |
      | COPPER_ORE   | 100        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be 64000
    And total payment should be 100000
    And purchase cost should be 30000
    And fuel cost should be 6000
    And trips required should be 3

  # ============================================================================
  # Acceptable Small Loss Contracts
  # ============================================================================

  Scenario: Acceptable small loss within threshold
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 5000        | 15000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 220        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be -2000
    And profitability reason should be "Acceptable small loss (avoids opportunity cost)"

  Scenario: Exactly at minimum profit threshold
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 5000        | 10000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 210        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be -5000
    And profitability reason should be "Acceptable small loss (avoids opportunity cost)"

  # ============================================================================
  # Unprofitable Contracts
  # ============================================================================

  Scenario: Loss exceeds acceptable threshold
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 5000        | 10000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 215        |
    When I evaluate profitability
    Then the contract should not be profitable
    And net profit should be -5500
    And profitability reason should be "Loss exceeds acceptable threshold"

  Scenario: Highly unprofitable contract
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 1000        | 5000         |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | PRECIOUS_STONES | X1-MARKET | 50            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 50             | 2000               | X1-SOURCE       |
    And market prices:
      | trade_symbol    | sell_price |
      | PRECIOUS_STONES | 1000       |
    When I evaluate profitability
    Then the contract should not be profitable
    And net profit should be -48000
    And profitability reason should be "Loss exceeds acceptable threshold"

  # ============================================================================
  # Partially Fulfilled Deliveries
  # ============================================================================

  Scenario: Profitability calculation accounts for partially fulfilled deliveries
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 40000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 60              |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 200        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be 41000
    And purchase cost should be 8000
    And trips required should be 1

  Scenario: All deliveries already fulfilled
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 40000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 100             |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 200        |
    When I evaluate profitability
    Then the contract should be profitable
    And net profit should be 50000
    And purchase cost should be 0
    And fuel cost should be 0
    And trips required should be 0

  # ============================================================================
  # Trip Calculations
  # ============================================================================

  Scenario: Calculate trips required with ceiling division
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 250            | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1500               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 100        |
    When I evaluate profitability
    Then trips required should be 3
    And fuel cost should be 4500
    And net profit should be 30500

  Scenario: Zero trips when no units needed
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 40000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 0              | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 200        |
    When I evaluate profitability
    Then trips required should be 0
    And fuel cost should be 0

  # ============================================================================
  # Error Cases
  # ============================================================================

  Scenario: Missing market price for required trade good
    Given a contract with payment:
      | on_accepted | on_fulfilled |
      | 10000       | 50000        |
    And contract deliveries:
      | trade_symbol | destination | units_required | units_fulfilled |
      | IRON_ORE     | X1-MARKET   | 100            | 0               |
      | COPPER_ORE   | X1-MARKET   | 50             | 0               |
    And profitability context:
      | cargo_capacity | fuel_cost_per_trip | cheapest_market |
      | 100            | 1000               | X1-SOURCE       |
    And market prices:
      | trade_symbol | sell_price |
      | IRON_ORE     | 200        |
    When I attempt to evaluate profitability
    Then the profitability evaluation should fail with error "missing market price for COPPER_ORE"

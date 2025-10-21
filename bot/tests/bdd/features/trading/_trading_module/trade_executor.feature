Feature: Trade Executor - Buy and sell action execution

  As a trading system
  I need to execute buy and sell actions reliably
  So that trades are completed and database is updated

  Background:
    Given a mock ship controller for "TRADER-1"
    And a mock API client
    And a mock database
    And a trade executor in system "X1-TEST"

  # Buy Action Execution

  Scenario: Execute small buy action (single transaction)
    Given ship has 40 cargo capacity
    And ship has empty cargo
    And a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 3 units at 100 credits per unit
    When executing buy action
    Then purchase should execute as single transaction
    And ship should buy 3 units of "COPPER"
    And total cost should be 300 credits
    And database should be updated with purchase price
    And operation should succeed

  Scenario: Execute large buy action (batch purchasing)
    Given ship has 40 cargo capacity
    And ship has empty cargo
    And a BUY action for "IRON" at waypoint "X1-TEST-B7"
    And buy quantity is 20 units at 150 credits per unit
    And batch size is 5 units
    When executing buy action with batching
    Then 4 batches should be executed
    And each batch should purchase 5 units
    And total cost should be 3000 credits
    And database should be updated after each batch
    And operation should succeed

  Scenario: Buy action fails due to insufficient cargo space
    Given ship has 40 cargo capacity
    And ship has 38 units of existing cargo
    And a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 10 units at 100 credits per unit
    When executing buy action
    Then purchase should fail with cargo space error
    And only 2 units should be purchased
    And total cost should be 200 credits
    And operation should return partial success

  Scenario: Buy action with profitability validation failure
    Given ship has 40 cargo capacity
    And ship has empty cargo
    And a BUY action for "GOLD" at waypoint "X1-TEST-B7"
    And buy quantity is 3 units at 1500 credits per unit
    And profitability validator rejects purchase
    When executing buy action
    Then purchase should be blocked
    And no units should be purchased
    And total cost should be 0 credits
    And operation should fail

  Scenario: Batch purchasing stops when cargo fills mid-batch
    Given ship has 40 cargo capacity
    And ship has 33 units of existing cargo
    And a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 20 units at 100 credits per unit
    And batch size is 5 units
    When executing buy action with batching
    Then batch 1 should complete with 5 units
    And batch 2 should complete with 2 units
    And remaining batches should be skipped
    And total units purchased should be 7
    And total cost should be 700 credits

  # Sell Action Execution

  Scenario: Execute sell action successfully
    Given ship has cargo with 15 units of "COPPER"
    And a SELL action for "COPPER" at waypoint "X1-TEST-D42"
    And sell quantity is 15 units at 500 credits per unit
    When executing sell action
    Then ship should sell 15 units of "COPPER"
    And total revenue should be 7500 credits
    And database should be updated with sell price
    And cargo should be empty after sale
    And operation should succeed

  Scenario: Sell action with actual price different from planned
    Given ship has cargo with 20 units of "IRON"
    And a SELL action for "IRON" at waypoint "X1-TEST-D42"
    And sell quantity is 20 units at planned price 300 credits per unit
    And actual market price is 280 credits per unit
    When executing sell action
    Then ship should sell 20 units of "IRON"
    And total revenue should be 5600 credits (actual price)
    And a price difference warning should be logged
    And price difference should be -6.7 percent
    And database should be updated with actual price 280

  Scenario: Sell action fails - no cargo
    Given ship has empty cargo
    And a SELL action for "COPPER" at waypoint "X1-TEST-D42"
    And sell quantity is 10 units at 500 credits per unit
    When executing sell action
    Then sale should fail
    And total revenue should be 0 credits
    And operation should fail

  Scenario: Partial sell - selling less than planned
    Given ship has cargo with 8 units of "GOLD"
    And a SELL action for "GOLD" at waypoint "X1-TEST-D42"
    And sell quantity is 8 units at 2000 credits per unit
    When executing sell action
    Then ship should sell 8 units of "GOLD"
    And total revenue should be 16000 credits
    And database should be updated with sell price
    And operation should succeed

  # Database Updates

  Scenario: Purchase updates database with actual transaction price
    Given a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 10 units at planned price 100 credits per unit
    And actual transaction price is 105 credits per unit
    When executing buy action
    Then database should be updated with PURCHASE transaction
    And database sell_price should be 105 credits
    And database purchase_price should remain unchanged
    And last_updated should be current timestamp

  Scenario: Sale updates database with actual transaction price
    Given ship has cargo with 10 units of "IRON"
    And a SELL action for "IRON" at waypoint "X1-TEST-D42"
    And sell quantity is 10 units at planned price 300 credits per unit
    And actual transaction price is 295 credits per unit
    When executing sell action
    Then database should be updated with SELL transaction
    And database purchase_price should be 295 credits
    And database sell_price should remain unchanged
    And last_updated should be current timestamp

  # Batch Size Logging

  Scenario: High-value good logs minimal risk strategy
    Given a BUY action for "CLOTHING" at waypoint "X1-TEST-B7"
    And buy quantity is 10 units at 2892 credits per unit
    When executing buy action
    Then batch size should be 2 units
    And log should contain "minimal risk strategy"
    And log should contain "high-value good ≥2000 cr/unit"

  Scenario: Standard good logs default batching
    Given a BUY action for "IRON_ORE" at waypoint "X1-TEST-B7"
    And buy quantity is 25 units at 150 credits per unit
    When executing buy action
    Then batch size should be 5 units
    And log should contain "default batching"
    And log should contain "standard good ≥50 cr/unit"

  Scenario: Bulk good logs efficiency mode
    Given a BUY action for "ICE_WATER" at waypoint "X1-TEST-B7"
    And buy quantity is 30 units at 30 credits per unit
    When executing buy action
    Then batch size should be 10 units
    And log should contain "efficiency mode"
    And log should contain "bulk good <50 cr/unit"

  # Edge Cases

  Scenario: Zero quantity buy action
    Given a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 0 units
    When executing buy action
    Then no purchase should be executed
    And total cost should be 0 credits
    And operation should succeed trivially

  Scenario: Buy action when ship controller returns None
    Given a BUY action for "COPPER" at waypoint "X1-TEST-B7"
    And buy quantity is 10 units at 100 credits per unit
    And ship controller buy() returns None
    When executing buy action
    Then purchase should fail
    And total cost should be 0 credits
    And operation should fail

  Scenario: Sell action when ship controller returns None
    Given ship has cargo with 10 units of "COPPER"
    And a SELL action for "COPPER" at waypoint "X1-TEST-D42"
    And sell quantity is 10 units at 500 credits per unit
    And ship controller sell() returns None
    When executing sell action
    Then sale should fail
    And total revenue should be 0 credits
    And operation should fail

Feature: Batch purchasing with inter-batch price validation prevents market price spikes

  Background:
    Given a ship "TRADER-1" docked at market "X1-TEST-B7"
    And the ship has 40 cargo capacity
    And the ship has empty cargo
    And agent has 100000 credits
    And the batch size is 5 units per batch

  Scenario: Successful batch purchase with stable prices
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 1000 credits per unit
    And the planned buy quantity is 20 units
    And the spike threshold is 30 percent
    When batch 1 shows price 1000 credits per unit
    And batch 2 shows price 1000 credits per unit
    And batch 3 shows price 1000 credits per unit
    And batch 4 shows price 1000 credits per unit
    Then all 4 batches should complete successfully
    And the ship cargo should contain 20 units of "COPPER"
    And 20000 credits should be spent (20 × 1000)
    And the operation should complete successfully

  Scenario: Price spike detected mid-purchase - abort remaining batches
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 1000 credits per unit
    And the planned buy quantity is 20 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 1000 credits per unit (5 units purchased)
    And batch 2 completes with price 1050 credits per unit (5 units purchased)
    And batch 3 shows price 1400 credits per unit (40% spike detected)
    Then the circuit breaker should trigger BEFORE batch 3
    And only 2 batches should complete (10 units purchased)
    And the ship cargo should contain 10 units of "COPPER"
    And 10250 credits should be spent (batch 1: 5000, batch 2: 5250)
    And remaining 10 units should not be purchased
    And the operation should salvage partial cargo and continue route

  Scenario: Partial success - some batches complete before price spike
    Given a planned buy action for "IRON" at "X1-TEST-B7"
    And the planned buy price is 800 credits per unit
    And the planned buy quantity is 15 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 800 credits per unit (5 units purchased)
    And batch 2 shows price 1100 credits per unit (37.5% spike detected)
    Then the circuit breaker should trigger BEFORE batch 2
    And only 1 batch should complete (5 units purchased)
    And the ship cargo should contain 5 units of "IRON"
    And 4000 credits should be spent (5 × 800)
    And remaining 10 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Circuit breaker activation on actual batch price (post-batch validation)
    Given a planned buy action for "GOLD" at "X1-TEST-B7"
    And the planned buy price is 1500 credits per unit
    And the planned buy quantity is 10 units
    And the spike threshold is 30 percent
    When batch 1 pre-check shows price 1500 credits per unit
    But batch 1 actual transaction price is 2000 credits per unit (33% spike)
    Then the post-batch circuit breaker should trigger AFTER batch 1
    And only 1 batch should complete (3 units purchased)
    And the ship cargo should contain 3 units of "GOLD"
    And 6000 credits should be spent (3 × 2000 actual)
    And remaining 7 units should not be purchased
    And the operation should salvage cargo at bad price

  Scenario: Batch purchase with incremental pricing simulation
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 1255 credits per unit
    And the planned buy quantity is 20 units
    And the spike threshold is 30 percent
    And the market has incremental pricing enabled
    When batch 1 shows price 1255 credits per unit (initial supply)
    And batch 2 shows price 1320 credits per unit (supply decreasing)
    And batch 3 shows price 1400 credits per unit (supply low)
    And batch 4 shows price 1650 credits per unit (31.5% spike from batch 1)
    Then the circuit breaker should trigger BEFORE batch 4
    And only 3 batches should complete (15 units purchased)
    And the ship cargo should contain 15 units of "COPPER"
    And cumulative cost should be 19875 credits (batch 1: 6275, batch 2: 6600, batch 3: 7000)
    And remaining 5 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Small purchase fits in single batch - no batching needed
    Given a planned buy action for "ALUMINUM" at "X1-TEST-B7"
    And the planned buy price is 500 credits per unit
    And the planned buy quantity is 3 units
    And the spike threshold is 30 percent
    When the purchase quantity is less than batch size
    Then the purchase should execute as single transaction
    And no batch logic should be applied
    And the ship cargo should contain 3 units of "ALUMINUM"
    And 1500 credits should be spent (3 × 500)

  Scenario: Batch size configuration changes behavior
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 1000 credits per unit
    And the planned buy quantity is 20 units
    And the spike threshold is 30 percent
    When the batch size is set to 10 units per batch
    And batch 1 completes with price 1000 credits per unit
    And batch 2 shows price 1400 credits per unit (40% spike)
    Then the circuit breaker should trigger BEFORE batch 2
    And only 1 batch should complete (10 units purchased)
    And the ship cargo should contain 10 units of "COPPER"
    And 10000 credits should be spent (10 × 1000)
    And remaining 10 units should not be purchased

  Scenario: Backward compatibility - existing routes work without batching
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 1000 credits per unit
    And the planned buy quantity is 20 units
    And the spike threshold is 30 percent
    And batch purchasing is disabled (default behavior)
    When the purchase executes
    Then the purchase should complete as single bulk transaction
    And pre-purchase validation should check initial price
    And post-purchase validation should check average price
    And the ship cargo should contain 20 units of "COPPER"

  # Dynamic Batch Sizing Tests

  Scenario: High-value good (≥2000 cr/unit) uses 2-unit batches for minimal risk
    Given a planned buy action for "CLOTHING" at "X1-TEST-B7"
    And the planned buy price is 2892 credits per unit
    And the planned buy quantity is 10 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 2892 credits per unit (2 units purchased)
    And batch 2 completes with price 2900 credits per unit (2 units purchased)
    And batch 3 shows price 3800 credits per unit (31% spike detected)
    Then the circuit breaker should trigger BEFORE batch 3
    And only 2 batches should complete (4 units purchased)
    And the ship cargo should contain 4 units of "CLOTHING"
    And 11584 credits should be spent (batch 1: 5784, batch 2: 5800)
    And remaining 6 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Medium-high value good (≥1500 cr/unit) uses 3-unit batches for cautious approach
    Given a planned buy action for "GOLD" at "X1-TEST-B7"
    And the planned buy price is 1500 credits per unit
    And the planned buy quantity is 15 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 1500 credits per unit (3 units purchased)
    And batch 2 completes with price 1550 credits per unit (3 units purchased)
    And batch 3 completes with price 1600 credits per unit (3 units purchased)
    And batch 4 shows price 2000 credits per unit (33% spike detected)
    Then the circuit breaker should trigger BEFORE batch 4
    And only 3 batches should complete (9 units purchased)
    And the ship cargo should contain 9 units of "GOLD"
    And 13950 credits should be spent (batch 1: 4500, batch 2: 4650, batch 3: 4800)
    And remaining 6 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Standard good (≥50 cr/unit) uses 5-unit batches for default efficiency
    Given a planned buy action for "IRON_ORE" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 25 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 150 credits per unit (5 units purchased)
    And batch 2 completes with price 160 credits per unit (5 units purchased)
    And batch 3 shows price 200 credits per unit (33% spike detected)
    Then the circuit breaker should trigger BEFORE batch 3
    And only 2 batches should complete (10 units purchased)
    And the ship cargo should contain 10 units of "IRON_ORE"
    And 1550 credits should be spent (batch 1: 750, batch 2: 800)
    And remaining 15 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Very low-value good (<50 cr/unit) uses 10-unit batches for maximum efficiency
    Given a planned buy action for "ICE_WATER" at "X1-TEST-B7"
    And the planned buy price is 30 credits per unit
    And the planned buy quantity is 30 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 30 credits per unit (10 units purchased)
    And batch 2 completes with price 35 credits per unit (10 units purchased)
    And batch 3 shows price 45 credits per unit (50% spike detected)
    Then the circuit breaker should trigger BEFORE batch 3
    And only 2 batches should complete (20 units purchased)
    And the ship cargo should contain 20 units of "ICE_WATER"
    And 650 credits should be spent (batch 1: 300, batch 2: 350)
    And remaining 10 units should not be purchased
    And the operation should salvage partial cargo

  Scenario: Dynamic batch sizing reduces loss on high-value spike - real-world example
    Given a planned buy action for "CLOTHING" at "X1-TEST-B7"
    And the planned buy price is 2892 credits per unit
    And the planned buy quantity is 10 units
    And the spike threshold is 30 percent
    When batch 1 completes with price 2892 credits per unit (2 units purchased)
    And batch 2 shows price 3771 credits per unit (30.4% spike detected)
    Then the circuit breaker should trigger BEFORE batch 2
    And only 1 batch should complete (2 units purchased)
    And the ship cargo should contain 2 units of "CLOTHING"
    And 5784 credits should be spent (2 × 2892)
    And remaining 8 units should not be purchased
    And the operation should salvage partial cargo

Feature: Circuit breaker validates buy price BEFORE spending credits

  Background:
    Given a ship "TRADER-1" docked at market "X1-TEST-B7"
    And the ship has 40 cargo capacity
    And the ship has empty cargo
    And agent has 100000 credits

  Scenario: Circuit breaker prevents purchase when live market shows price spike
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 40 units
    And the spike threshold is 30 percent
    When the live market shows "COPPER" sell price at 220 credits per unit
    Then the circuit breaker should trigger BEFORE purchase
    And no credits should be spent
    And the ship cargo should remain empty
    And the operation should abort with "price spike detected"

  Scenario: Circuit breaker allows purchase when live market shows acceptable price
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 40 units
    And the spike threshold is 30 percent
    When the live market shows "COPPER" sell price at 160 credits per unit
    Then the circuit breaker should NOT trigger
    And the purchase should proceed
    And 6400 credits should be spent (40 × 160)
    And the ship cargo should contain 40 units of "COPPER"

  Scenario: Circuit breaker handles live market API failure gracefully
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 40 units
    And the spike threshold is 30 percent
    When the live market API call fails with network error
    Then the circuit breaker should log warning about live check failure
    And the purchase should proceed with caution
    And post-purchase validation should still apply

  Scenario: Post-purchase circuit breaker catches actual transaction price spike
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 40 units
    And the spike threshold is 30 percent
    When the live market check passes with price 160
    But the actual transaction price is 220 credits per unit
    Then the post-purchase circuit breaker should trigger
    And 8800 credits will have been spent (already lost)
    And auto-recovery should be initiated
    And the ship should navigate to planned sell destination
    And the ship should sell all cargo to recover credits

  Scenario: Dynamic spike threshold adjusts based on market data freshness
    Given a planned buy action for "COPPER" at "X1-TEST-B7"
    And the planned buy price is 150 credits per unit
    And the planned buy quantity is 40 units
    When the buy market data is 2 minutes old
    Then the spike threshold should be 100 percent (2.0x multiplier)
    When the buy market data is 8 minutes old
    Then the spike threshold should be 150 percent (2.5x multiplier)
    When the buy market data is 25 minutes old
    Then the spike threshold should be 200 percent (3.0x multiplier)
    When the buy market data is 35 minutes old
    Then the operation should abort with "market data too stale"

  Scenario: Recovery flow sells cargo at planned destination after spike
    Given a planned route from "X1-TEST-B7" (buy) to "X1-TEST-C5" (sell)
    And a planned buy action for "COPPER" at "X1-TEST-B7" for 150 credits
    And the ship purchases 40 units at unexpected price of 220 credits (8800 spent)
    And post-purchase circuit breaker triggers
    When auto-recovery is initiated
    Then the ship should navigate to "X1-TEST-C5"
    And the ship should dock at "X1-TEST-C5"
    And the ship should sell all 40 units of "COPPER"
    And recovery should log total revenue and net loss
    And the operation should exit cleanly with status code 1

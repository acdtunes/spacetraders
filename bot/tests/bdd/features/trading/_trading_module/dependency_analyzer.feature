Feature: Dependency Analyzer - Route segment dependency analysis

  As a trading system
  I need to analyze dependencies between route segments
  So that I can make smart skip decisions when failures occur

  Background:
    Given a multi-leg trading route

  # Basic Dependency Detection

  Scenario: Independent segments have no dependencies
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7
    And segment 2: BUY 20 GOLD at C5
    When analyzing route dependencies
    Then segment 0 should have dependency type "NONE"
    And segment 1 should have dependency type "NONE"
    And segment 2 should have dependency type "NONE"
    And all segments should have can_skip=True

  Scenario: Simple buy-sell dependency
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 10 COPPER at B7
    When analyzing route dependencies
    Then segment 0 should have dependency type "NONE"
    And segment 1 should have dependency type "CARGO"
    And segment 1 should depend on segment 0
    And segment 1 should require 10 COPPER from prior segments
    And segment 1 should have can_skip=False

  Scenario: Multiple goods with mixed dependencies
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7, SELL 10 COPPER at B7
    And segment 2: SELL 15 IRON at C5
    When analyzing route dependencies
    Then segment 1 should depend on segment 0 for COPPER
    And segment 2 should depend on segment 1 for IRON
    And segment 0 should have no dependencies
    And segment 1 should have dependency type "CARGO"
    And segment 2 should have dependency type "CARGO"

  # Cargo Flow Tracking

  Scenario: Partial sell creates carry-through cargo
    Given segment 0: BUY 20 COPPER at A1
    And segment 1: SELL 10 COPPER at B7
    And segment 2: SELL 10 COPPER at C5
    When analyzing route dependencies
    Then segment 1 should depend on segment 0
    And segment 2 should depend on segment 0
    And segment 2 should NOT depend on segment 1
    And segment 2 should require 10 COPPER from segment 0

  Scenario: Multiple sources for same good (FIFO consumption)
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 COPPER at B7
    And segment 2: SELL 25 COPPER at C5
    When analyzing route dependencies
    Then segment 2 should depend on both segment 0 and segment 1
    And segment 2 required_cargo should be 25 COPPER
    And segment 2 should consume from segment 0 first (FIFO)
    And segment 2 should consume remaining from segment 1

  Scenario: Selling more than bought creates negative dependency
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 15 COPPER at B7
    When analyzing route dependencies
    Then segment 1 should depend on segment 0
    And segment 1 should require 10 COPPER from segment 0
    And segment 1 should have unfulfilled requirement of 5 COPPER

  # Smart Skip Decision Logic

  Scenario: Failed independent segment can be skipped
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5
    And segment 3: SELL 15 IRON at D42
    And segment 1 fails due to unprofitable purchase
    And route total has 2 independent segments remaining
    And remaining profit is 8000 credits
    When evaluating if segment 1 should be skipped
    Then skip decision should be TRUE
    And reason should contain "independent segments remain"
    And segments 0, 2 can still execute
    And segment 3 should also be skipped (depends on failed segment 1)

  Scenario: Failed segment with dependent segments must abort
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 10 COPPER at B7
    And segment 0 fails due to circuit breaker
    And segment 1 depends on segment 0
    When evaluating if segment 0 should be skipped
    Then skip decision should be FALSE
    And reason should contain "All remaining segments depend on failed segment"
    And route execution should abort

  Scenario: Transitive dependency - cascade skip detection
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: SELL 10 COPPER at B7, BUY 15 IRON at B7
    And segment 2: SELL 15 IRON at C5
    And segment 0 fails
    When evaluating affected segments
    Then segment 1 should be affected (depends on segment 0)
    And segment 2 should be affected (depends on segment 1)
    And skip decision should be FALSE
    And reason should contain "All remaining segments depend on failed segment"

  Scenario: Mixed independent and dependent segments - partial skip
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5 (depends on 0)
    And segment 3: SELL 15 IRON at D42 (depends on 1)
    And segment 0 fails
    When evaluating affected segments
    Then segment 2 should be affected
    And segment 1 should NOT be affected
    And segment 3 should NOT be affected
    And skip decision should be TRUE
    And segments 1 and 3 can continue

  # Profitability Threshold

  Scenario: Remaining profit too low - abort instead of skip
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5
    And segment 3: SELL 15 IRON at D42
    And segment 0 fails
    And remaining independent segments profit is 3000 credits
    And minimum profit threshold is 5000 credits
    When evaluating if segment 0 should be skipped
    Then skip decision should be FALSE
    And reason should contain "Remaining profit too low"
    And reason should contain "3,000 credits < 5,000 minimum"

  Scenario: Remaining profit sufficient - skip allowed
    Given segment 0: BUY 10 COPPER at A1
    And segment 1: BUY 15 IRON at B7 (independent)
    And segment 2: SELL 10 COPPER at C5
    And segment 3: SELL 15 IRON at D42
    And segment 0 fails
    And remaining independent segments profit is 12000 credits
    And minimum profit threshold is 5000 credits
    When evaluating if segment 0 should be skipped
    Then skip decision should be TRUE
    And reason should contain "independent segments remain with 12,000 profit"

  # Cargo Blocking

  Scenario: Current cargo blocks future segment buy actions
    Given segment 0: BUY 10 COPPER at A1 (completed)
    And segment 1: BUY 30 IRON at B7 (planned)
    And ship has 40 cargo capacity
    And ship currently has 10 COPPER in cargo
    And remaining cargo space is 30 units
    When checking if cargo blocks future segments
    Then cargo should NOT block segment 1
    And segment 1 requires exactly 30 units

  Scenario: Stranded cargo blocks future purchases
    Given segment 0: BUY 10 COPPER at A1 (completed)
    And segment 1: BUY 35 IRON at B7 (planned - failed to sell COPPER)
    And ship has 40 cargo capacity
    And ship currently has 10 COPPER in cargo (stranded)
    And remaining cargo space is 30 units
    When checking if cargo blocks future segments
    Then cargo SHOULD block segment 1
    And segment 1 requires 35 units but only 30 available

  Scenario: Empty cargo does not block
    Given segment 0: SELL 10 COPPER at A1 (completed)
    And segment 1: BUY 40 IRON at B7 (planned)
    And ship has 40 cargo capacity
    And ship has empty cargo
    When checking if cargo blocks future segments
    Then cargo should NOT block any segment
    And all 40 units available for purchase

  # Credit Dependencies

  Scenario: Segment requires revenue from prior segment
    Given segment 0: SELL 10 COPPER at A1 (revenue: 5000 credits)
    And segment 1: BUY 15 IRON at B7 (cost: 4500 credits)
    And agent starts with 100 credits
    When analyzing route dependencies
    Then segment 1 should require 4500 credits
    And segment 1 implicitly depends on segment 0 revenue
    And segment 1 cannot execute if segment 0 fails

  Scenario: Segment has sufficient starting credits - no credit dependency
    Given segment 0: BUY 10 COPPER at A1 (cost: 1000 credits)
    And segment 1: BUY 15 IRON at B7 (cost: 1500 credits)
    And agent starts with 10000 credits
    When analyzing route dependencies
    Then segments should be credit-independent
    And both can execute without revenue dependency

  # Complex Multi-Good Scenarios

  Scenario: Real-world multi-leg route with mixed dependencies
    Given segment 0: BUY 18 SHIP_PLATING at E45 (cost: 36k)
    And segment 1: SELL 18 SHIP_PLATING at D42, BUY 21 ASSAULT_RIFLES at D42 (revenue: 144k, cost: 63k)
    And segment 2: SELL 21 ASSAULT_RIFLES at A1, BUY 20 ADVANCED_CIRCUITRY at A1 (revenue: 126k, cost: 80k)
    And segment 3: SELL 20 ADVANCED_CIRCUITRY at E45 (revenue: 160k)
    When analyzing route dependencies
    Then segment 1 depends on segment 0 (SHIP_PLATING cargo)
    And segment 2 depends on segment 1 (ASSAULT_RIFLES cargo)
    And segment 3 depends on segment 2 (ADVANCED_CIRCUITRY cargo)
    And if segment 0 fails, all segments affected
    And if segment 1 fails, segments 2 and 3 affected
    And if segment 2 fails, only segment 3 affected

  Scenario: Segment with multiple buy and sell actions
    Given segment 0: BUY 10 COPPER at A1, BUY 15 IRON at A1
    And segment 1: SELL 10 COPPER at B7, SELL 15 IRON at B7, BUY 20 GOLD at B7
    And segment 2: SELL 20 GOLD at C5
    When analyzing route dependencies
    Then segment 1 depends on segment 0 for both COPPER and IRON
    And segment 1 required_cargo should be {COPPER: 10, IRON: 15}
    And segment 2 depends on segment 1 for GOLD
    And segment 2 required_cargo should be {GOLD: 20}

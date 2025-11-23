Feature: Arbitrage Opportunity Value Object
  As a domain model
  I want to represent arbitrage opportunities as immutable value objects
  So that I can ensure data integrity and enable pure functional scoring

  # ============================================================================
  # Opportunity Creation & Validation
  # ============================================================================

  Scenario: Create valid arbitrage opportunity
    Given good symbol "IRON_ORE"
    And buy market "X1-A1" at coordinates (0, 0)
    And sell market "X1-B1" at coordinates (100, 0)
    And buy price 100 credits
    And sell price 150 credits
    And cargo capacity 40 units
    And buy supply "ABUNDANT"
    And sell activity "STRONG"
    And minimum margin 10.0%
    When I create an arbitrage opportunity
    Then the opportunity should be valid
    And profit per unit should be 50 credits
    And profit margin should be 50.0%
    And estimated profit should be 2000 credits
    And distance should be 100.0 units
    And opportunity should be viable

  Scenario: Reject opportunity with empty good symbol
    Given good symbol ""
    And valid buy and sell markets
    When I create an arbitrage opportunity
    Then creation should fail with error "good symbol required"

  Scenario: Reject opportunity with nil buy market
    Given good symbol "IRON_ORE"
    And buy market is nil
    And valid sell market
    When I create an arbitrage opportunity
    Then creation should fail with error "buy market required"

  Scenario: Reject opportunity with non-positive buy price
    Given good symbol "IRON_ORE"
    And valid markets
    And buy price 0 credits
    When I create an arbitrage opportunity
    Then creation should fail with error "buy price must be positive"

  Scenario: Reject opportunity with sell price <= buy price
    Given good symbol "IRON_ORE"
    And valid markets
    And buy price 100 credits
    And sell price 100 credits
    When I create an arbitrage opportunity
    Then creation should fail with error "sell price (100) must exceed buy price (100)"

  Scenario: Reject opportunity with invalid supply value
    Given good symbol "IRON_ORE"
    And valid markets and prices
    And buy supply "INVALID_VALUE"
    When I create an arbitrage opportunity
    Then creation should fail with error "invalid supply value"

  # ============================================================================
  # Viability Calculation
  # ============================================================================

  Scenario: Mark opportunity as viable when margin meets threshold
    Given buy price 100 credits and sell price 120 credits
    And minimum margin threshold 10.0%
    When I create an arbitrage opportunity
    Then profit margin should be 20.0%
    And opportunity should be viable

  Scenario: Mark opportunity as non-viable when margin below threshold
    Given buy price 100 credits and sell price 105 credits
    And minimum margin threshold 10.0%
    When I create an arbitrage opportunity
    Then profit margin should be 5.0%
    And opportunity should not be viable

  # ============================================================================
  # Distance Calculation
  # ============================================================================

  Scenario: Calculate Euclidean distance between markets
    Given buy market at coordinates (0, 0)
    And sell market at coordinates (300, 400)
    When I create an arbitrage opportunity
    Then distance should be 500.0 units
    # sqrt(300² + 400²) = sqrt(250000) = 500

  Scenario: Zero distance for same waypoint coordinates
    Given buy market at coordinates (100, 100)
    And sell market at coordinates (100, 100)
    When I create an arbitrage opportunity
    Then distance should be 0.0 units

  # ============================================================================
  # Estimated Net Profit
  # ============================================================================

  Scenario: Calculate net profit after fuel costs
    Given estimated profit 2000 credits
    And fuel cost 300 credits
    When I calculate estimated net profit
    Then net profit should be 1700 credits

  Scenario: Negative net profit when fuel exceeds gross profit
    Given estimated profit 500 credits
    And fuel cost 800 credits
    When I calculate estimated net profit
    Then net profit should be -300 credits

  # ============================================================================
  # Immutability
  # ============================================================================

  Scenario: Opportunity fields are read-only
    Given a valid arbitrage opportunity
    When I access opportunity fields
    Then all fields should be readable via getters
    And no setter methods should exist
    # Ensures value object immutability

  Scenario: Score can be set after creation
    Given a valid arbitrage opportunity with score 0.0
    When I set score to 850.5
    Then score should be 850.5
    # Score is calculated by analyzer after construction

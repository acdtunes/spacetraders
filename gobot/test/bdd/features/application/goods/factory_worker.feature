Feature: Goods Factory Worker
  As a SpaceTraders bot
  I want to execute production for a single good
  So that I can acquire goods through automated manufacturing

  # ============================================================================
  # Simple Buy Operations
  # ============================================================================

  Scenario: Worker buys a raw material from export market
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    And market "X1-A1" sells "IRON_ORE" with price 10 and supply 100
    And ship "SHIP-1" is docked at "X1-A1" with 50 cargo capacity and 0 cargo units
    When the worker executes production for "IRON_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And the production result quantity should be 50
    And the production result cost should be 500
    And the production result waypoint should be "X1-A1"
    And ship "SHIP-1" should have 50 cargo units

  Scenario: Worker buys good when multiple markets available
    Given a supply chain node for good "COPPER_ORE" with acquisition method "BUY"
    And market "X1-B1" sells "COPPER_ORE" with price 15 and supply 200
    And market "X1-B2" sells "COPPER_ORE" with price 12 and supply 150
    And ship "SHIP-1" is at waypoint "X1-B3" with 100 cargo capacity
    When the worker executes production for "COPPER_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And the worker should navigate to the cheapest market "X1-B2"
    And the production result quantity should be 100

  Scenario: Worker handles partial cargo capacity for purchase
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    And market "X1-C1" sells "IRON_ORE" with price 8 and supply 1000
    And ship "SHIP-1" is docked at "X1-C1" with 30 cargo capacity and 20 cargo units
    When the worker executes production for "IRON_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And the production result quantity should be 10
    And ship "SHIP-1" should have 30 cargo units

  # ============================================================================
  # Fabrication Operations
  # ============================================================================

  Scenario: Worker fabricates good with single input
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-D1" sells "IRON_ORE" with price 10 and supply 100
    And waypoint "X1-D2" imports "IRON_ORE" and produces "IRON"
    And ship "SHIP-1" is at waypoint "X1-D1" with 50 cargo capacity
    When the worker executes production for "IRON" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And ship "SHIP-1" should acquire "IRON_ORE" from "X1-D1"
    And ship "SHIP-1" should deliver "IRON_ORE" to "X1-D2"
    And ship "SHIP-1" should sell all cargo at "X1-D2"
    And ship "SHIP-1" should wait for "IRON" to appear in exports
    And ship "SHIP-1" should purchase "IRON" from "X1-D2"

  Scenario: Worker fabricates good with multiple inputs
    Given a supply chain map with "ELECTRONICS" requiring "SILICON_CRYSTALS,COPPER"
    And a supply chain map with "COPPER" requiring "COPPER_ORE"
    And market "X1-E1" sells "SILICON_CRYSTALS" with price 20 and supply 100
    And market "X1-E2" sells "COPPER_ORE" with price 10 and supply 100
    And waypoint "X1-E3" imports "COPPER_ORE" and produces "COPPER"
    And waypoint "X1-E4" imports "SILICON_CRYSTALS,COPPER" and produces "ELECTRONICS"
    And ship "SHIP-1" is at waypoint "X1-E1" with 100 cargo capacity
    When the worker executes production for "ELECTRONICS" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And the worker should recursively produce inputs before parent
    And ship "SHIP-1" should acquire "COPPER_ORE" first
    And ship "SHIP-1" should fabricate "COPPER" at "X1-E3"
    And ship "SHIP-1" should acquire "SILICON_CRYSTALS"
    And ship "SHIP-1" should deliver both inputs to "X1-E4"
    And ship "SHIP-1" should fabricate "ELECTRONICS" at "X1-E4"

  Scenario: Worker polls for production completion
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And waypoint "X1-F1" imports "IRON_ORE" and produces "IRON"
    And market data for "X1-F1" is updated every 30 seconds by scouts
    And ship "SHIP-1" has delivered "IRON_ORE" to "X1-F1"
    When the worker waits for "IRON" to appear in "X1-F1" exports
    Then the worker should poll market data at 30 second intervals
    And the worker should check database for updated market data
    And the worker should wait indefinitely until "IRON" appears
    And when "IRON" appears the worker should purchase immediately

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Worker fails when no market sells required good
    Given a supply chain node for good "RARE_ITEM" with acquisition method "BUY"
    And no markets in system "X1" sell "RARE_ITEM"
    And ship "SHIP-1" is at waypoint "X1-G1"
    When the worker executes production for "RARE_ITEM" using ship "SHIP-1" in system "X1"
    Then the worker should fail
    And the error should contain "no market found selling RARE_ITEM"

  Scenario: Worker fails when ship has no cargo capacity
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    And market "X1-H1" sells "IRON_ORE" with price 10 and supply 100
    And ship "SHIP-1" is docked at "X1-H1" with 50 cargo capacity and 50 cargo units
    When the worker executes production for "IRON_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should fail
    And the error should contain "no cargo space available"

  Scenario: Worker handles API errors gracefully
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    And market "X1-I1" sells "IRON_ORE" with price 10 and supply 100
    And ship "SHIP-1" is at waypoint "X1-I2"
    And the navigation API returns error "rate limit exceeded"
    When the worker executes production for "IRON_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should fail
    And the error should contain "failed to navigate"
    And the error should contain "rate limit exceeded"

  # ============================================================================
  # Market-Driven Behavior
  # ============================================================================

  Scenario: Worker acquires whatever quantity is available (market-driven)
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    And market "X1-J1" sells "IRON_ORE" with price 10 and limited supply 25
    And ship "SHIP-1" is docked at "X1-J1" with 100 cargo capacity
    When the worker executes production for "IRON_ORE" using ship "SHIP-1" in system "X1"
    Then the worker should succeed
    And the production result quantity should be 25
    # NOTE: Worker doesn't calculate exact amounts - it takes what's available

  Scenario: Worker doesn't use fixed conversion ratios for fabrication
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And waypoint "X1-K1" imports "IRON_ORE" and produces "IRON"
    And ship "SHIP-1" delivers 50 units of "IRON_ORE" to "X1-K1"
    When the worker polls for "IRON" production at "X1-K1"
    # The worker doesn't calculate "50 IRON_ORE → X units of IRON"
    # It simply waits for ANY amount of IRON to appear in exports
    Then the worker should purchase whatever quantity of "IRON" is available
    And the production result quantity could be any positive number
    # Because production is time-based and market-driven, not ratio-based

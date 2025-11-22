Feature: Supply Chain Resolver
  As a SpaceTraders bot
  I want to build dependency trees for goods production
  So that I can plan automated manufacturing workflows

  # ============================================================================
  # Simple Dependency Resolution
  # ============================================================================

  Scenario: Resolve raw material as BUY
    Given a supply chain map
    And market "X1-A1" sells "IRON_ORE" with activity "STRONG" and supply "ABUNDANT"
    When I build dependency tree for "IRON_ORE" in system "X1"
    Then the tree should have root "IRON_ORE" with acquisition method "BUY"
    And the root should have 0 children
    And the root market activity should be "STRONG"
    And the root supply level should be "ABUNDANT"

  Scenario: Resolve simple fabrication dependency
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-B1" sells "IRON_ORE" with activity "GROWING" and supply "HIGH"
    And "IRON" is not available in any market
    When I build dependency tree for "IRON" in system "X1"
    Then the tree should have root "IRON" with acquisition method "FABRICATE"
    And the root should have 1 children
    And child 0 should be "IRON_ORE" with acquisition method "BUY"

  # ============================================================================
  # Multi-Level Dependency Resolution
  # ============================================================================

  Scenario: Resolve two-level dependency tree
    Given a supply chain map with:
      | Output     | Inputs   |
      | ELECTRONICS| SILICON_CRYSTALS, COPPER |
      | COPPER     | COPPER_ORE |
    And market "X1-C1" sells "SILICON_CRYSTALS" with activity "STRONG" and supply "ABUNDANT"
    And market "X1-C2" sells "COPPER_ORE" with activity "GROWING" and supply "HIGH"
    And "ELECTRONICS" is not available in any market
    And "COPPER" is not available in any market
    When I build dependency tree for "ELECTRONICS" in system "X1"
    Then the tree should have root "ELECTRONICS" with acquisition method "FABRICATE"
    And the root should have 2 children
    And the tree should contain node "SILICON_CRYSTALS" with acquisition method "BUY"
    And the tree should contain node "COPPER" with acquisition method "FABRICATE"
    And the tree should contain node "COPPER_ORE" with acquisition method "BUY"
    And the tree depth should be 3

  Scenario: Resolve complex multi-level tree
    Given a supply chain map with:
      | Output              | Inputs                     |
      | ADVANCED_CIRCUITRY  | ELECTRONICS, MICROPROCESSORS |
      | ELECTRONICS         | SILICON_CRYSTALS, COPPER   |
      | MICROPROCESSORS     | SILICON_CRYSTALS, COPPER   |
      | COPPER              | COPPER_ORE                 |
    And market "X1-D1" sells "SILICON_CRYSTALS" with activity "STRONG" and supply "ABUNDANT"
    And market "X1-D2" sells "COPPER_ORE" with activity "GROWING" and supply "MODERATE"
    And "ADVANCED_CIRCUITRY" is not available in any market
    And "ELECTRONICS" is not available in any market
    And "MICROPROCESSORS" is not available in any market
    And "COPPER" is not available in any market
    When I build dependency tree for "ADVANCED_CIRCUITRY" in system "X1"
    Then the tree should have root "ADVANCED_CIRCUITRY" with acquisition method "FABRICATE"
    And the tree depth should be 4
    And the tree should have 6 total nodes
    And the tree should contain 2 BUY nodes
    And the tree should contain 4 FABRICATE nodes

  # ============================================================================
  # Buy vs Fabricate Strategy
  # ============================================================================

  Scenario: Prefer buying when available in market
    Given a supply chain map with "MACHINERY" requiring "IRON"
    And market "X1-E1" sells "MACHINERY" with activity "STRONG" and supply "MODERATE"
    When I build dependency tree for "MACHINERY" in system "X1"
    Then the tree should have root "MACHINERY" with acquisition method "BUY"
    And the root should have 0 children
    # Because the good is available for purchase we skip fabrication

  Scenario: Fabricate intermediate good if not available
    Given a supply chain map with:
      | Output   | Inputs   |
      | MACHINERY| IRON     |
      | IRON     | IRON_ORE |
    And market "X1-F1" sells "IRON_ORE" with activity "GROWING" and supply "HIGH"
    And "MACHINERY" is not available in any market
    And "IRON" is not available in any market
    When I build dependency tree for "MACHINERY" in system "X1"
    Then the tree should have root "MACHINERY" with acquisition method "FABRICATE"
    And the tree should contain node "IRON" with acquisition method "FABRICATE"
    And the tree should contain node "IRON_ORE" with acquisition method "BUY"

  Scenario: Mix buy and fabricate in same tree
    Given a supply chain map with:
      | Output     | Inputs             |
      | EQUIPMENT  | IRON, ELECTRONICS  |
      | ELECTRONICS| SILICON_CRYSTALS, COPPER |
      | COPPER     | COPPER_ORE         |
    And market "X1-G1" sells "IRON" with activity "STRONG" and supply "ABUNDANT"
    And market "X1-G2" sells "SILICON_CRYSTALS" with activity "GROWING" and supply "HIGH"
    And market "X1-G3" sells "COPPER_ORE" with activity "GROWING" and supply "MODERATE"
    And "EQUIPMENT" is not available in any market
    And "ELECTRONICS" is not available in any market
    And "COPPER" is not available in any market
    When I build dependency tree for "EQUIPMENT" in system "X1"
    Then the tree should contain node "IRON" with acquisition method "BUY"
    And the tree should contain node "ELECTRONICS" with acquisition method "FABRICATE"
    And the tree should contain node "SILICON_CRYSTALS" with acquisition method "BUY"
    And the tree should contain node "COPPER" with acquisition method "FABRICATE"
    And the tree should contain node "COPPER_ORE" with acquisition method "BUY"

  # ============================================================================
  # Cycle Detection
  # ============================================================================

  Scenario: Detect direct circular dependency
    Given a supply chain map with:
      | Output | Inputs |
      | A      | B      |
      | B      | A      |
    When I attempt to build dependency tree for "A" in system "X1"
    Then tree building should fail with circular dependency error
    And the error should mention goods "A" and "B"

  Scenario: Detect indirect circular dependency
    Given a supply chain map with:
      | Output | Inputs |
      | A      | B      |
      | B      | C      |
      | C      | A      |
    When I attempt to build dependency tree for "A" in system "X1"
    Then tree building should fail with circular dependency error
    And the cycle path should be "A -> B -> C -> A"

  # ============================================================================
  # Error Handling
  # ============================================================================

  Scenario: Unknown good cannot be purchased or fabricated
    Given a supply chain map
    And "UNOBTAINIUM" is not available in any market
    And "UNOBTAINIUM" is not in the supply chain map
    When I attempt to build dependency tree for "UNOBTAINIUM" in system "X1"
    Then tree building should fail with unknown good error
    And the error should mention "UNOBTAINIUM"

  Scenario: Valid supply chain with all raw materials
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    When I validate the supply chain for "IRON"
    Then validation should succeed

  Scenario: Invalid supply chain with missing inputs
    Given an empty supply chain map
    When I validate the supply chain for "MYSTERY_GOOD"
    Then validation should succeed
    # Because raw materials dont need to be in the map

  # ============================================================================
  # Market Data Population
  # ============================================================================

  Scenario: Populate market data for BUY nodes
    Given a supply chain map with "MACHINERY" requiring "IRON"
    And market "X1-H1" at waypoint "X1-H1-STATION" sells "IRON" with activity "STRONG" and supply "ABUNDANT" at price 500
    And "MACHINERY" is not available in any market
    When I build dependency tree for "MACHINERY" in system "X1"
    Then the tree should contain node "IRON" with acquisition method "BUY"
    And node "IRON" should have waypoint symbol "X1-H1-STATION"
    And node "IRON" should have market activity "STRONG"
    And node "IRON" should have supply level "ABUNDANT"

  Scenario: FABRICATE nodes have no market data
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-I1" sells "IRON_ORE" with activity "GROWING" and supply "HIGH"
    And "IRON" is not available in any market
    When I build dependency tree for "IRON" in system "X1"
    Then node "IRON" should have empty market activity
    And node "IRON" should have empty supply level
    # Because FABRICATE nodes will find markets during execution

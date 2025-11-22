Feature: Supply Chain Node Value Object
  As a SpaceTraders bot
  I want to represent goods dependencies as a tree structure
  So that I can plan recursive production workflows

  # ============================================================================
  # SupplyChainNode Creation
  # ============================================================================

  Scenario: Create leaf node for raw material
    Given a supply chain node for good "IRON_ORE" with acquisition method "BUY"
    When I create the supply chain node
    Then the node should be valid
    And the node good should be "IRON_ORE"
    And the node acquisition method should be "BUY"
    And the node should be a leaf

  Scenario: Create fabrication node with children
    Given a supply chain node for good "MACHINERY" with acquisition method "FABRICATE"
    And the node has child "IRON" with acquisition method "BUY"
    When I create the supply chain node
    Then the node should be valid
    And the node good should be "MACHINERY"
    And the node acquisition method should be "FABRICATE"
    And the node should not be a leaf
    And the node should have 1 children

  Scenario: Create node with market data
    Given a supply chain node for good "COPPER_ORE" with acquisition method "BUY"
    And the node has market activity "STRONG"
    And the node has supply level "ABUNDANT"
    When I create the supply chain node
    Then the node market activity should be "STRONG"
    And the node supply level should be "ABUNDANT"

  # ============================================================================
  # Tree Depth Calculation
  # ============================================================================

  Scenario: Leaf node has depth 1
    Given a leaf node for good "IRON_ORE"
    When I calculate the tree depth
    Then the depth should be 1

  Scenario: Two-level tree has depth 2
    Given a supply chain tree:
      | good      | children |
      | MACHINERY | IRON     |
      | IRON      |          |
    When I calculate the tree depth
    Then the depth should be 2

  Scenario: Three-level tree has depth 3
    Given a supply chain tree:
      | good        | children      |
      | ELECTRONICS | SILICON,COPPER|
      | COPPER      | COPPER_ORE    |
      | COPPER_ORE  |               |
      | SILICON     |               |
    When I calculate the tree depth
    Then the depth should be 3

  # ============================================================================
  # Tree Flattening
  # ============================================================================

  Scenario: Flatten single node tree
    Given a leaf node for good "IRON_ORE"
    When I flatten the tree to a list
    Then the list should contain 1 nodes
    And the list should contain "IRON_ORE"

  Scenario: Flatten multi-level tree
    Given a supply chain tree:
      | good              | children                  |
      | ADVANCED_CIRCUITRY| ELECTRONICS,MICROPROCESSORS|
      | ELECTRONICS       | SILICON,COPPER            |
      | MICROPROCESSORS   | SILICON,COPPER            |
      | SILICON           |                           |
      | COPPER            |                           |
    When I flatten the tree to a list
    Then the list should contain 5 nodes
    And the list should contain "ADVANCED_CIRCUITRY"
    And the list should contain "ELECTRONICS"
    And the list should contain "MICROPROCESSORS"
    And the list should contain "SILICON"
    And the list should contain "COPPER"

  # ============================================================================
  # Raw Materials Extraction
  # ============================================================================

  Scenario: Extract raw materials from leaf node
    Given a leaf node for good "IRON_ORE"
    When I get required raw materials
    Then the raw materials should contain "IRON_ORE"

  Scenario: Extract raw materials from complex tree
    Given a supply chain tree:
      | good        | children      |
      | ELECTRONICS | SILICON,COPPER|
      | COPPER      | COPPER_ORE    |
      | COPPER_ORE  |               |
      | SILICON     |               |
    When I get required raw materials
    Then the raw materials should contain "COPPER_ORE"
    And the raw materials should contain "SILICON"
    And the raw materials should not contain "ELECTRONICS"
    And the raw materials should not contain "COPPER"

  # ============================================================================
  # Node Counting
  # ============================================================================

  Scenario: Count nodes in tree
    Given a supply chain tree:
      | good      | children |
      | MACHINERY | IRON     |
      | IRON      |          |
    When I count the nodes
    Then the node count should be 2

  Scenario: Count acquisition methods
    Given a supply chain tree:
      | good        | method    | children      |
      | ELECTRONICS | FABRICATE | SILICON,COPPER|
      | COPPER      | FABRICATE | COPPER_ORE    |
      | COPPER_ORE  | BUY       |               |
      | SILICON     | BUY       |               |
    When I count by acquisition method
    Then the BUY count should be 2
    And the FABRICATE count should be 2

  # ============================================================================
  # Completion Tracking
  # ============================================================================

  Scenario: Mark node as completed
    Given a leaf node for good "IRON_ORE"
    When I mark the node completed with quantity 100
    Then the node should be completed
    And the node quantity acquired should be 100

  Scenario: Check all children completed
    Given a supply chain tree:
      | good      | children |
      | MACHINERY | IRON     |
      | IRON      |          |
    And node "IRON" is marked completed
    When I check if all children of "MACHINERY" are completed
    Then all children should be completed

  Scenario: Not all children completed
    Given a supply chain tree:
      | good        | children      |
      | ELECTRONICS | SILICON,COPPER|
      | SILICON     |               |
      | COPPER      |               |
    And node "SILICON" is marked completed
    When I check if all children of "ELECTRONICS" are completed
    Then all children should not be completed

  # ============================================================================
  # Production Time Estimation
  # ============================================================================

  Scenario: Estimate production time for STRONG market
    Given a supply chain tree with depth 3
    And the root node has market activity "STRONG"
    When I estimate production time
    Then the estimated time should be approximately 6 minutes

  Scenario: Estimate production time for WEAK market
    Given a supply chain tree with depth 2
    And the root node has market activity "WEAK"
    When I estimate production time
    Then the estimated time should be approximately 12 minutes

  Scenario: Estimate production time for GROWING market
    Given a supply chain tree with depth 2
    And the root node has market activity "GROWING"
    When I estimate production time
    Then the estimated time should be approximately 6 minutes

Feature: Goods Factory Entity
  As a SpaceTraders bot
  I want to manage goods factory entities with lifecycle state
  So that I can track automated production operations

  # ============================================================================
  # GoodsFactory Creation
  # ============================================================================

  Scenario: Create valid goods factory
    Given a goods factory for player 1 producing "MACHINERY" in system "X1-TEST"
    And a dependency tree with root "MACHINERY"
    When I create the goods factory
    Then the goods factory should be valid
    And the factory player ID should be 1
    And the factory target good should be "MACHINERY"
    And the factory system symbol should be "X1-TEST"
    And the factory status should be "PENDING"

  Scenario: Create goods factory with metadata
    Given a goods factory for player 2 producing "ELECTRONICS" in system "X1-ABC"
    And a dependency tree with root "ELECTRONICS"
    And factory metadata:
      | key        | value    |
      | worker_id  | worker-1 |
      | ship_count | 3        |
    When I create the goods factory
    Then the goods factory should be valid
    And the factory metadata should contain "worker_id" with value "worker-1"
    And the factory metadata should contain "ship_count" with value "3"

  # ============================================================================
  # State Transitions
  # ============================================================================

  Scenario: Start factory from PENDING state
    Given a goods factory in PENDING state
    When I start the factory
    Then the factory status should be "ACTIVE"
    And the factory started_at timestamp should be set
    And the factory should be active

  Scenario: Cannot start factory from ACTIVE state
    Given a goods factory in ACTIVE state
    When I attempt to start the factory
    Then the factory start should fail with error "cannot start factory in ACTIVE state"

  Scenario: Complete factory from ACTIVE state
    Given a goods factory in ACTIVE state
    When I complete the factory
    Then the factory status should be "COMPLETED"
    And the factory stopped_at timestamp should be set
    And the factory should be finished

  Scenario: Cannot complete factory from PENDING state
    Given a goods factory in PENDING state
    When I attempt to complete the factory
    Then the factory complete should fail with error "cannot complete factory in PENDING state"

  Scenario: Fail factory from ACTIVE state
    Given a goods factory in ACTIVE state
    When I fail the factory with error "network timeout"
    Then the factory status should be "FAILED"
    And the factory stopped_at timestamp should be set
    And the factory last error should be "network timeout"
    And the factory should be finished

  Scenario: Cannot fail factory from COMPLETED state
    Given a goods factory in COMPLETED state
    When I attempt to fail the factory
    Then the factory fail should fail with error "cannot fail factory in COMPLETED state"

  Scenario: Stop factory from ACTIVE state
    Given a goods factory in ACTIVE state
    When I stop the factory
    Then the factory status should be "STOPPED"
    And the factory stopped_at timestamp should be set
    And the factory should be finished

  Scenario: Cannot stop factory from COMPLETED state
    Given a goods factory in COMPLETED state
    When I attempt to stop the factory
    Then the factory stop should fail with error "cannot stop factory in COMPLETED state"

  # ============================================================================
  # State Guards
  # ============================================================================

  Scenario: CanStart validation
    Given a goods factory in PENDING state
    Then the factory can be started
    When I transition factory to ACTIVE
    Then the factory cannot be started

  Scenario: CanComplete validation
    Given a goods factory in PENDING state
    Then the factory cannot be completed
    When I transition factory to ACTIVE
    Then the factory can be completed

  Scenario: CanFail validation
    Given a goods factory in PENDING state
    Then the factory can be failed
    When I transition factory to COMPLETED
    Then the factory cannot be failed

  # ============================================================================
  # Metrics Tracking
  # ============================================================================

  Scenario: Track quantity acquired
    Given a goods factory in ACTIVE state
    When I set quantity acquired to 50
    Then the factory quantity acquired should be 50

  Scenario: Track total cost
    Given a goods factory in ACTIVE state
    When I add cost of 1000 credits
    And I add cost of 500 credits
    Then the factory total cost should be 1500

  # ============================================================================
  # Metadata Management
  # ============================================================================

  Scenario: Update factory metadata
    Given a goods factory in ACTIVE state
    And factory metadata:
      | key       | value      |
      | status    | processing |
    When I update metadata with:
      | key      | value     |
      | status   | completed |
      | progress | 100       |
    Then the factory metadata should contain "status" with value "completed"
    And the factory metadata should contain "progress" with value "100"

  # ============================================================================
  # Progress Calculation
  # ============================================================================

  Scenario: Calculate progress with no completed nodes
    Given a goods factory with dependency tree of 10 nodes
    And 0 nodes are completed
    When I get the factory progress
    Then the progress should be 0 percent

  Scenario: Calculate progress with half completed
    Given a goods factory with dependency tree of 10 nodes
    And 5 nodes are completed
    When I get the factory progress
    Then the progress should be 50 percent

  Scenario: Calculate progress with all completed
    Given a goods factory with dependency tree of 8 nodes
    And 8 nodes are completed
    When I get the factory progress
    Then the progress should be 100 percent

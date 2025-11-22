Feature: Goods Factory Coordinator
  As a SpaceTraders bot
  I want to coordinate multi-ship production for complex goods
  So that I can automate complete supply chains

  # ============================================================================
  # Sequential Production (MVP)
  # ============================================================================

  Scenario: Coordinator executes sequential production with single ship
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-A1" sells "IRON_ORE" with price 10 and supply 100
    And waypoint "X1-A2" imports "IRON_ORE" and produces "IRON"
    And player 1 has idle ship "SHIP-1" with 50 cargo capacity
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should discover idle ships
    And the coordinator should build dependency tree for "IRON"
    And the coordinator should execute production sequentially
    And the coordinator should use ship "SHIP-1" for all nodes
    And the coordinator should complete successfully
    And the production response should contain factory ID
    And the production response should show quantity acquired > 0

  Scenario: Coordinator handles multi-level dependencies sequentially
    Given a supply chain map with "ELECTRONICS" requiring "SILICON_CRYSTALS,COPPER"
    And a supply chain map with "COPPER" requiring "COPPER_ORE"
    And market "X1-B1" sells "SILICON_CRYSTALS" with price 20 and supply 100
    And market "X1-B2" sells "COPPER_ORE" with price 10 and supply 100
    And waypoint "X1-B3" imports "COPPER_ORE" and produces "COPPER"
    And waypoint "X1-B4" imports "SILICON_CRYSTALS,COPPER" and produces "ELECTRONICS"
    And player 1 has idle ship "SHIP-1" with 100 cargo capacity
    When I start factory coordinator for player 1 producing "ELECTRONICS" in system "X1"
    Then the coordinator should build dependency tree with 4 nodes
    And the coordinator should execute nodes in depth-first order
    # Process COPPER_ORE (leaf) → COPPER (intermediate) → SILICON_CRYSTALS (leaf) → ELECTRONICS (root)
    And ship "SHIP-1" should complete all 4 nodes sequentially
    And the coordinator should complete successfully

  Scenario: Coordinator processes complex dependency tree
    Given a supply chain map with:
      | Output              | Inputs                           |
      | ADVANCED_CIRCUITRY  | ELECTRONICS,MICROPROCESSORS      |
      | ELECTRONICS         | SILICON_CRYSTALS,COPPER          |
      | MICROPROCESSORS     | SILICON_CRYSTALS,COPPER          |
      | COPPER              | COPPER_ORE                       |
    And raw materials are available in markets
    And manufacturing waypoints exist for all outputs
    And player 1 has idle ship "SHIP-1" with 150 cargo capacity
    When I start factory coordinator for player 1 producing "ADVANCED_CIRCUITRY" in system "X1"
    Then the coordinator should build dependency tree with 6 nodes
    # 6 nodes: ADVANCED_CIRCUITRY, ELECTRONICS, MICROPROCESSORS, SILICON_CRYSTALS, COPPER (x2), COPPER_ORE (x2)
    # Note: Tree deduplicates shared dependencies (COPPER, COPPER_ORE, SILICON_CRYSTALS appear once)
    And the coordinator should process all nodes sequentially
    And the coordinator should complete successfully

  # ============================================================================
  # Fleet Discovery and Ship Management
  # ============================================================================

  Scenario: Coordinator discovers idle hauler ships dynamically
    Given player 1 has ships:
      | Symbol  | Type        | Status | Cargo Capacity |
      | SHIP-1  | HAULER      | IDLE   | 100            |
      | SHIP-2  | PROBE       | ACTIVE | 10             |
      | SHIP-3  | HAULER      | ACTIVE | 100            |
      | SHIP-4  | HAULER      | IDLE   | 80             |
    When I start factory coordinator for player 1
    Then the coordinator should discover ships using FindIdleLightHaulers
    And the coordinator should find 2 idle haulers: "SHIP-1", "SHIP-4"
    # MVP: Uses only first idle ship for sequential production
    And the coordinator should use ship "SHIP-1" for production

  Scenario: Coordinator fails when no idle ships available
    Given player 1 has ships:
      | Symbol  | Type    | Status | Cargo Capacity |
      | SHIP-1  | PROBE   | IDLE   | 10             |
      | SHIP-2  | HAULER  | ACTIVE | 100            |
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should fail
    And the error should contain "no idle hauler ships available"

  Scenario: Coordinator releases ship assignment on completion
    Given player 1 has idle ship "SHIP-1"
    And ship "SHIP-1" is assigned to factory "factory-123"
    When the coordinator completes production
    Then ship "SHIP-1" should be released from assignment
    And ship "SHIP-1" should be available for other operations

  # ============================================================================
  # Logging and Metrics
  # ============================================================================

  Scenario: Coordinator logs comprehensive execution details
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And player 1 has idle ship "SHIP-1"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should log:
      | Event                      | Details                                    |
      | Dependency tree built      | 2 nodes (1 BUY, 1 FABRICATE)               |
      | Ships discovered           | 1 idle hauler found                        |
      | Production started         | Processing 2 nodes sequentially            |
      | Node execution started     | Node: IRON_ORE, Method: BUY                |
      | Node execution completed   | Acquired X units at Y credits              |
      | Node execution started     | Node: IRON, Method: FABRICATE              |
      | Node execution completed   | Acquired X units at Y credits              |
      | Production completed       | Total: X units, Cost: Y credits            |

  Scenario: Coordinator tracks production metrics
    Given a supply chain map with "ELECTRONICS" requiring "SILICON_CRYSTALS,COPPER"
    And player 1 has idle ship "SHIP-1"
    When I start factory coordinator for player 1 producing "ELECTRONICS" in system "X1"
    And production completes successfully
    Then the coordinator response should include:
      | Metric              | Description                      |
      | TargetGood          | ELECTRONICS                      |
      | QuantityAcquired    | Total units produced             |
      | TotalCost           | Sum of all purchase costs        |
      | NodesCompleted      | Number of nodes processed        |
      | NodesTotal          | Total nodes in dependency tree   |
      | ShipsUsed           | Number of ships utilized (MVP:1) |
      | Completed           | true/false                       |

  # ============================================================================
  # System Symbol Handling
  # ============================================================================

  Scenario: Coordinator produces in specified system
    Given player 1 has idle ship "SHIP-1" in system "X1"
    And markets and manufacturing exist in system "X1"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should only use markets in system "X1"
    And the coordinator should only use manufacturing waypoints in system "X1"
    And ship "SHIP-1" should not leave system "X1"

  Scenario: Coordinator defaults to ship's current system
    Given player 1 has idle ship "SHIP-1" in system "X2"
    When I start factory coordinator without specifying system
    Then the coordinator should detect ship location
    And the coordinator should use system "X2" for production
    # NOTE: Current implementation requires system symbol parameter

  # ============================================================================
  # Error Recovery and Edge Cases
  # ============================================================================

  Scenario: Coordinator handles worker failure gracefully
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-D1" sells "IRON_ORE"
    And player 1 has idle ship "SHIP-1"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    And the worker fails with error "API rate limit exceeded"
    Then the coordinator should propagate the error
    And the coordinator response should have Completed = false
    And the coordinator response error should contain "API rate limit exceeded"

  Scenario: Coordinator handles missing manufacturing waypoint
    Given a supply chain map with "IRON" requiring "IRON_ORE"
    And market "X1-E1" sells "IRON_ORE"
    And no waypoint in system "X1" imports "IRON_ORE"
    And player 1 has idle ship "SHIP-1"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should fail during node execution
    And the error should contain "no waypoint found importing IRON_ORE"

  Scenario: Coordinator respects player isolation
    Given player 1 has idle ship "SHIP-1"
    And player 2 has idle ship "SHIP-2"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then the coordinator should only discover player 1 ships
    And the coordinator should not use "SHIP-2"
    And the coordinator should only use "SHIP-1"

  # ============================================================================
  # Integration with Persistence
  # ============================================================================

  Scenario: Coordinator creates factory entity in database
    Given player 1 has idle ship "SHIP-1"
    When I start factory coordinator for player 1 producing "IRON" in system "X1"
    Then a GoodsFactory entity should be created with status PENDING
    And the factory should transition to ACTIVE when production starts
    And the factory should have dependency tree stored
    And the factory should track quantity and cost metrics

  Scenario: Coordinator updates factory status on completion
    Given player 1 has idle ship "SHIP-1"
    And factory coordinator is running for "IRON"
    When production completes successfully acquiring 50 units at 500 credits
    Then the factory status should be COMPLETED
    And the factory quantity_acquired should be 50
    And the factory total_cost should be 500
    And the factory completed_at timestamp should be set

  Scenario: Coordinator updates factory status on failure
    Given player 1 has idle ship "SHIP-1"
    And factory coordinator is running for "IRON"
    When production fails with error "insufficient funds"
    Then the factory status should be FAILED
    And the factory last_error should contain "insufficient funds"
    And the factory stopped_at timestamp should be set

  # ============================================================================
  # Future: Parallel Execution (Not MVP)
  # ============================================================================

  @future @parallel
  Scenario: Coordinator assigns independent nodes to different ships (parallel)
    # NOTE: This is a future enhancement - MVP uses sequential execution
    Given a supply chain map with "ELECTRONICS" requiring "SILICON_CRYSTALS,COPPER"
    And player 1 has idle ships: "SHIP-1", "SHIP-2"
    When I start factory coordinator with parallel execution enabled
    Then the coordinator should identify independent nodes
    # SILICON_CRYSTALS and COPPER can be produced in parallel (no dependencies)
    And "SHIP-1" should process SILICON_CRYSTALS in parallel
    And "SHIP-2" should process COPPER in parallel
    And both ships should complete before ELECTRONICS fabrication starts

  @future @parallel
  Scenario: Coordinator waits for all parallel workers before parent node
    Given a complex dependency tree with multiple parallel paths
    And player 1 has 5 idle ships
    When I start factory coordinator with parallel execution enabled
    Then the coordinator should execute leaf nodes in parallel
    And the coordinator should wait for all children to complete
    And parent nodes should only start after all children done
    And the coordinator should maximize parallelism while respecting dependencies

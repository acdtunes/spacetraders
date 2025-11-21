Feature: Fleet Assignment and Ship Selection
  As a contract management system
  I want to optimally assign ships to targets and select ships for tasks
  So that fleet operations are efficient and balanced

  # Fleet Assigner - Rebalancing Logic
  Scenario: Detect clustering when ships exceed limit at single waypoint
    Given 3 ships at waypoint X1-A1
    And 2 target waypoints at X1-B2 and X1-C3
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should be needed
    And clustering should be detected at X1-A1

  Scenario: No clustering with ships distributed across waypoints
    Given 2 ships at waypoint X1-A1
    And 1 ship at waypoint X1-B2
    And 3 target waypoints at X1-C3, X1-D4, and X1-E5
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should not be needed due to clustering

  Scenario: Rebalancing needed when average distance exceeds threshold
    Given 2 ships at waypoint X1-A1 at coordinates (0, 0)
    And 2 target waypoints at X1-FAR1 (500, 500) and X1-FAR2 (600, 600)
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should be needed
    And average distance should be greater than 100

  Scenario: No rebalancing when ships close to targets
    Given 2 ships at waypoint X1-A1 at coordinates (0, 0)
    And 2 target waypoints at X1-NEAR1 (10, 10) and X1-NEAR2 (15, 15)
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should not be needed
    And average distance should be less than 100

  Scenario: Handle empty fleet gracefully
    Given 0 ships in the fleet
    And 2 target waypoints at X1-A1 and X1-B2
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should not be needed

  Scenario: Handle no targets gracefully
    Given 2 ships at waypoint X1-A1
    And 0 target waypoints
    When I check if rebalancing is needed with distance threshold 100
    Then rebalancing should not be needed

  # Fleet Assigner - Ship Distribution
  Scenario: Distribute 5 ships evenly across 3 targets
    Given 5 ships at various waypoints
    And 3 target waypoints for assignment
    When I assign ships to targets
    Then all 5 ships should be assigned
    And no target should have more than 2 ships
    And ships should be assigned to nearest available targets

  Scenario: Respect max ships per market limit
    Given 10 ships at waypoint X1-START
    And 2 target waypoints at X1-T1 and X1-T2
    When I assign ships to targets
    Then target X1-T1 should have exactly 2 ships
    And target X1-T2 should have exactly 2 ships
    And 6 ships should remain unassigned

  Scenario: Assign ships to nearest targets with balanced distribution
    Given ship "SHIP-A" at waypoint X1-A1 (0, 0)
    And ship "SHIP-B" at waypoint X1-B2 (100, 0)
    And ship "SHIP-C" at waypoint X1-C3 (200, 0)
    And target waypoint X1-TARGET1 at (50, 0)
    And target waypoint X1-TARGET2 at (150, 0)
    When I assign ships to targets
    Then "SHIP-A" should be assigned to X1-TARGET1
    And "SHIP-B" should be assigned to X1-TARGET1
    And "SHIP-C" should be assigned to X1-TARGET2

  Scenario: Handle more targets than ships
    Given 2 ships at waypoint X1-A1
    And 5 target waypoints for assignment
    When I assign ships to targets
    Then exactly 2 ships should be assigned
    And each target should have at most 1 ship

  Scenario: Handle empty fleet assignment
    Given 0 ships in the fleet
    And 3 target waypoints for assignment
    When I assign ships to targets
    Then 0 ships should be assigned

  Scenario: Handle no targets assignment
    Given 3 ships at waypoint X1-A1
    And 0 target waypoints
    When I assign ships to targets
    Then 0 ships should be assigned

  # Fleet Assigner - Distribution Quality
  Scenario: Calculate distribution quality for well-distributed fleet
    Given ship "SHIP-A" at waypoint X1-A1 (0, 0)
    And ship "SHIP-B" at waypoint X1-B2 (10, 10)
    And target waypoint X1-T1 at (5, 5)
    And target waypoint X1-T2 at (15, 15)
    When I calculate distribution quality
    Then quality score should be approximately 7.1

  Scenario: Calculate distribution quality for poorly distributed fleet
    Given ship "SHIP-A" at waypoint X1-A1 (0, 0)
    And ship "SHIP-B" at waypoint X1-B2 (0, 0)
    And target waypoint X1-FAR at (1000, 1000)
    When I calculate distribution quality
    Then quality score should be approximately 1414.2

  Scenario: Distribution quality calculation requires ships and targets
    Given 0 ships in the fleet
    And 2 target waypoints for assignment
    When I calculate distribution quality
    Then distribution quality calculation should fail

  # Ship Selector - Cargo Priority Selection
  Scenario: Select ship with required cargo over closer ships
    Given ship "SHIP-A" at X1-A1 (0, 0) with 100 units of IRON_ORE
    And ship "SHIP-B" at X1-B2 (5, 5) with no cargo
    And ship "SHIP-C" at X1-C3 (3, 3) with 50 units of COPPER
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship for target requiring IRON_ORE
    Then "SHIP-A" should be selected
    And selection reason should be "has IRON_ORE in cargo (priority)"

  Scenario: Select closest ship when no cargo requirement
    Given ship "SHIP-A" at X1-A1 (0, 0)
    And ship "SHIP-B" at X1-B2 (100, 100)
    And ship "SHIP-C" at X1-C3 (5, 5)
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship without cargo requirement
    Then "SHIP-C" should be selected
    And selection distance should be approximately 7.1

  Scenario: Select ship with cargo even if in transit
    Given ship "SHIP-A" docked at X1-A1 (0, 0) with no cargo
    And ship "SHIP-B" in transit at X1-B2 (5, 5) with 100 units of IRON_ORE
    And ship "SHIP-C" in orbit at X1-C3 (3, 3) with no cargo
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship for target requiring IRON_ORE
    Then "SHIP-B" should be selected

  Scenario: Exclude in-transit ships without required cargo
    Given ship "SHIP-A" in orbit at X1-A1 (0, 0)
    And ship "SHIP-B" in transit at X1-B2 (5, 5) with no cargo
    And ship "SHIP-C" in orbit at X1-C3 (3, 3)
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship without cargo requirement
    Then "SHIP-C" should be selected

  Scenario: No available ships when all in transit
    Given ship "SHIP-A" in transit at X1-A1
    And ship "SHIP-B" in transit at X1-B2
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship without cargo requirement
    Then ship selection should fail with "no available ships found (all are in transit)"

  Scenario: Select ship by distance only (simple rebalancing)
    Given ship "SHIP-A" at X1-A1 (0, 0)
    And ship "SHIP-B" at X1-B2 (100, 100)
    And ship "SHIP-C" at X1-C3 (20, 20)
    And target waypoint X1-TARGET at (25, 25)
    When I select closest ship by distance excluding in-transit
    Then "SHIP-C" should be selected
    And selection distance should be approximately 7.1

  Scenario: Select by distance with in-transit allowed
    Given ship "SHIP-A" docked at X1-A1 (100, 100)
    And ship "SHIP-B" in transit at X1-B2 (5, 5)
    And target waypoint X1-TARGET at (10, 10)
    When I select closest ship by distance including in-transit
    Then "SHIP-B" should be selected

  # Error Handling
  Scenario: Ship selection requires at least one ship
    Given 0 ships in the fleet
    And target waypoint X1-TARGET at (10, 10)
    When I select optimal ship without cargo requirement
    Then ship selection should fail with "no ships available for selection"

  Scenario: Ship selection requires target waypoint
    Given ship "SHIP-A" at X1-A1 (0, 0)
    When I select optimal ship with nil target
    Then ship selection should fail with "target waypoint cannot be nil"

  Scenario: Distance selection requires at least one ship
    Given 0 ships in the fleet
    And target waypoint X1-TARGET at (10, 10)
    When I select closest ship by distance excluding in-transit
    Then ship selection should fail with "no ships available for selection"

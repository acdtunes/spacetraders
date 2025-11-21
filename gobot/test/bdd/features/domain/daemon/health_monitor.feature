Feature: Daemon Health Monitor
  As a SpaceTraders daemon
  I want to monitor container and ship health
  So that I can detect stuck operations and attempt recovery

  Background:
    Given a mock clock at time "2025-01-15T10:00:00Z"

  # Constructor and Configuration
  Scenario: Create health monitor with default settings
    When I create a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    Then the health monitor check interval should be 5 minutes
    And the health monitor recovery timeout should be 30 minutes
    And the health monitor metrics should show 0 successful recoveries
    And the health monitor metrics should show 0 failed recoveries
    And the health monitor metrics should show 0 abandoned ships

  Scenario: Configure max recovery attempts
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    When I set max recovery attempts to 3
    Then max recovery attempts should be 3

  # Cooldown Logic
  Scenario: Skip health check when called too soon (cooldown active)
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And the health monitor last check time is "2025-01-15T10:00:00Z"
    And current time is "2025-01-15T10:02:00Z"
    When I run health check with empty assignments, containers, and ships
    Then health check should be skipped due to cooldown

  Scenario: Execute health check after cooldown expires
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And the health monitor last check time is "2025-01-15T10:00:00Z"
    And current time is "2025-01-15T10:06:00Z"
    When I run health check with empty assignments, containers, and ships
    Then health check should be executed
    And last check time should be "2025-01-15T10:06:00Z"

  Scenario: Execute health check on first run (no previous check)
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And current time is "2025-01-15T10:00:00Z"
    When I run health check with empty assignments, containers, and ships
    Then health check should be executed
    And last check time should be "2025-01-15T10:00:00Z"

  # Stale Assignment Cleanup
  Scenario: Clean stale assignments for non-existent containers
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And an active ship assignment for "SHIP-A" to container "MINING-1"
    And an active ship assignment for "SHIP-B" to container "MINING-2"
    And an active ship assignment for "SHIP-C" to container "MINING-3"
    And only container "MINING-1" exists
    When I clean stale assignments
    Then 2 stale assignments should be cleaned
    And assignment for "SHIP-A" should still be active
    And assignment for "SHIP-B" should be released
    And assignment for "SHIP-C" should be released

  Scenario: No cleanup when all assignments are valid
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And an active ship assignment for "SHIP-A" to container "MINING-1"
    And an active ship assignment for "SHIP-B" to container "MINING-2"
    And containers "MINING-1" and "MINING-2" exist
    When I clean stale assignments
    Then 0 stale assignments should be cleaned
    And assignment for "SHIP-A" should still be active
    And assignment for "SHIP-B" should still be active

  Scenario: Skip inactive assignments during cleanup
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And an active ship assignment for "SHIP-A" to container "MINING-1"
    And an inactive ship assignment for "SHIP-B" to container "MINING-2"
    And only container "MINING-1" exists
    When I clean stale assignments
    Then 0 stale assignments should be cleaned

  # Infinite Loop Detection
  Scenario: Detect container with suspiciously fast iterations
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a running container "MINING-1" with infinite iterations
    And container "MINING-1" has completed 120 iterations
    And container "MINING-1" has runtime metadata of 300 seconds
    When I detect infinite loops
    Then container "MINING-1" should be flagged as suspicious

  Scenario: Normal iteration speed not flagged
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a running container "MINING-1" with infinite iterations
    And container "MINING-1" has completed 10 iterations
    And container "MINING-1" has runtime metadata of 100 seconds
    When I detect infinite loops
    Then no containers should be flagged as suspicious

  Scenario: Skip containers with finite iteration limits
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a running container "MINING-1" with max iterations 10
    And container "MINING-1" has completed 120 iterations in 300 seconds
    When I detect infinite loops
    Then no containers should be flagged as suspicious

  Scenario: Skip containers without runtime metadata
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a running container "MINING-1" with infinite iterations
    And container "MINING-1" has completed 120 iterations
    When I detect infinite loops
    Then no containers should be flagged as suspicious

  Scenario: Skip containers that have not started iterating
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a running container "MINING-1" with infinite iterations
    And container "MINING-1" has completed 0 iterations
    When I detect infinite loops
    Then no containers should be flagged as suspicious

  Scenario: Skip non-running containers
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a pending container "MINING-1" with infinite iterations
    When I detect infinite loops
    Then no containers should be flagged as suspicious

  # Recovery Attempts
  Scenario: Record successful recovery attempt
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    When I record recovery attempt for "SHIP-A" with result "success"
    Then recovery attempt count for "SHIP-A" should be 1
    And successful recoveries metric should be 1

  Scenario: Record failed recovery attempt
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    When I record recovery attempt for "SHIP-A" with result "failure"
    Then recovery attempt count for "SHIP-A" should be 1
    And failed recoveries metric should be 1

  Scenario: Multiple recovery attempts tracked per ship
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    When I record recovery attempt for "SHIP-A" with result "failure"
    And I record recovery attempt for "SHIP-A" with result "failure"
    And I record recovery attempt for "SHIP-A" with result "success"
    Then recovery attempt count for "SHIP-A" should be 3
    And successful recoveries metric should be 1
    And failed recoveries metric should be 2

  Scenario: Abandon ship after max recovery attempts
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And I set max recovery attempts to 3
    And recovery attempt count for "SHIP-A" is 3
    And a ship "SHIP-A" in transit at waypoint X1-A1
    When I attempt recovery for "SHIP-A"
    Then abandoned ships metric should be 1
    And recovery attempt count for "SHIP-A" should be 3

  Scenario: Successful recovery before max attempts
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And I set max recovery attempts to 5
    And recovery attempt count for "SHIP-A" is 2
    And a ship "SHIP-A" in transit at waypoint X1-A1
    When I attempt recovery for "SHIP-A"
    Then recovery attempt count for "SHIP-A" should be 3
    And successful recoveries metric should be 1
    And abandoned ships metric should be 0

  # Watch List Management
  Scenario: Add ship to watch list
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And current time is "2025-01-15T10:00:00Z"
    When I add "SHIP-A" to watch list
    Then "SHIP-A" should be in watch list

  Scenario: Remove ship from watch list clears recovery attempts
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And "SHIP-A" is in watch list
    And recovery attempt count for "SHIP-A" is 3
    When I remove "SHIP-A" from watch list
    Then "SHIP-A" should not be in watch list
    And recovery attempt count for "SHIP-A" should be 0

  # Detect Stuck Ships (Note: Current implementation has stub that returns false)
  Scenario: Detect ships in transit state
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And a ship "SHIP-A" in transit at waypoint X1-A1
    And a ship "SHIP-B" docked at waypoint X1-B2
    And a ship "SHIP-C" in orbit at waypoint X1-C3
    When I detect stuck ships
    Then no ships should be detected as stuck

  # Full Health Check Integration
  Scenario: Full health check cleans stale assignments
    Given a health monitor with check interval 5 minutes and recovery timeout 30 minutes
    And current time is "2025-01-15T10:00:00Z"
    And an active ship assignment for "SHIP-A" to container "MINING-1"
    And an active ship assignment for "SHIP-B" to container "MINING-DELETED"
    And only container "MINING-1" exists
    When I run full health check
    Then health check should be executed
    And assignment for "SHIP-B" should be released

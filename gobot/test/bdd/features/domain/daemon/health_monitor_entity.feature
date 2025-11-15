Feature: Health Monitor Entity - Domain Logic
  As an autonomous bot operator
  I want health monitor business rules enforced
  So that container and ship health is managed correctly

  # ============================================================================
  # Health Monitor Initialization Tests
  # ============================================================================

  Scenario: Create health monitor with valid configuration
    When I create a health monitor with check interval 60 seconds and recovery timeout 300 seconds
    Then the health monitor should have check interval 60 seconds
    And the health monitor should have recovery timeout 300 seconds
    And the health monitor should have no last check time
    And the health monitor should have 0 successful recoveries
    And the health monitor should have 0 failed recoveries
    And the health monitor should have 0 abandoned ships

  Scenario: Create health monitor with different intervals
    When I create a health monitor with check interval 120 seconds and recovery timeout 600 seconds
    Then the health monitor should have check interval 120 seconds
    And the health monitor should have recovery timeout 600 seconds

  # ============================================================================
  # Check Interval Cooldown Tests
  # ============================================================================

  Scenario: First health check always runs
    Given a health monitor with check interval 60 seconds
    When I run a health check
    Then the check should execute
    And the last check time should be updated

  Scenario: Health check skipped during cooldown
    Given a health monitor with check interval 60 seconds
    And the last check ran 30 seconds ago
    When I run a health check
    Then the check should be skipped
    And the last check time should not change

  Scenario: Health check runs after cooldown expires
    Given a health monitor with check interval 60 seconds
    And the last check ran 65 seconds ago
    When I run a health check
    Then the check should execute
    And the last check time should be updated

  Scenario: Health check at exact cooldown boundary executes
    Given a health monitor with check interval 60 seconds
    And the last check ran exactly 60 seconds ago
    When I run a health check
    Then the check should execute

  Scenario: Health check one second after cooldown runs
    Given a health monitor with check interval 60 seconds
    And the last check ran 61 seconds ago
    When I run a health check
    Then the check should execute

  # ============================================================================
  # Clean Stale Assignments Tests
  # ============================================================================

  Scenario: Clean stale assignments releases orphaned containers
    Given a health monitor with assignments:
      | ship   | container     | created_minutes_ago |
      | SHIP-1 | container-123 | 10                  |
      | SHIP-2 | container-456 | 20                  |
    And only container "container-123" exists
    When the health monitor cleans stale assignments
    Then 1 assignment should be released
    And ship "SHIP-2" assignment should have release reason "stale_cleanup"
    And ship "SHIP-1" assignment should remain active

  Scenario: Clean stale assignments with all containers existing
    Given a health monitor with assignments:
      | ship   | container     | created_minutes_ago |
      | SHIP-1 | container-123 | 10                  |
      | SHIP-2 | container-456 | 20                  |
    And containers "container-123,container-456" exist
    When the health monitor cleans stale assignments
    Then 0 assignments should be released

  Scenario: Clean stale assignments skips released assignments
    Given a health monitor with assignments:
      | ship   | container     | created_minutes_ago | status   |
      | SHIP-1 | container-123 | 10                  | active   |
      | SHIP-2 | container-456 | 20                  | released |
    And no containers exist
    When the health monitor cleans stale assignments
    Then 1 assignment should be released

  # ============================================================================
  # Detect Stuck Ships Tests
  # ============================================================================

  Scenario: Detect ship stuck in transit beyond timeout
    Given a health monitor with recovery timeout 300 seconds
    And a ship "SHIP-1" in transit for 400 seconds
    When the health monitor detects stuck ships
    Then "SHIP-1" should be detected as stuck

  Scenario: Ship in transit within timeout is not stuck
    Given a health monitor with recovery timeout 300 seconds
    And a ship "SHIP-1" in transit for 200 seconds
    When the health monitor detects stuck ships
    Then "SHIP-1" should not be detected as stuck

  Scenario: Ship not in transit is not checked for stuck
    Given a health monitor with recovery timeout 300 seconds
    And a ship "SHIP-1" docked for 500 seconds
    When the health monitor detects stuck ships
    Then "SHIP-1" should not be detected as stuck

  Scenario: Multiple stuck ships detected
    Given a health monitor with recovery timeout 300 seconds
    And a ship "SHIP-1" in transit for 400 seconds
    And a ship "SHIP-2" in transit for 500 seconds
    And a ship "SHIP-3" in transit for 200 seconds
    When the health monitor detects stuck ships
    Then 2 ships should be detected as stuck
    And "SHIP-1" should be in stuck ships list
    And "SHIP-2" should be in stuck ships list
    And "SHIP-3" should not be in stuck ships list

  # ============================================================================
  # Detect Infinite Loops Tests
  # ============================================================================

  Scenario: Detect infinite loop with rapid iterations
    Given a health monitor exists
    And a container "container-123" with max_iterations -1
    And the container completed 100 iterations in 200 seconds
    When the health monitor detects infinite loops
    Then "container-123" should be flagged as suspicious

  Scenario: Container with normal iteration rate is not suspicious
    Given a health monitor exists
    And a container "container-123" with max_iterations -1
    And the container completed 10 iterations in 100 seconds
    When the health monitor detects infinite loops
    Then "container-123" should not be flagged as suspicious

  Scenario: Finite loop container is not checked
    Given a health monitor exists
    And a container "container-123" with max_iterations 100
    And the container completed 50 iterations in 100 seconds
    When the health monitor detects infinite loops
    Then "container-123" should not be flagged as suspicious

  Scenario: Container not running is not checked
    Given a health monitor exists
    And a stopped container "container-123" with max_iterations -1
    When the health monitor detects infinite loops
    Then "container-123" should not be flagged as suspicious

  Scenario: Suspicious iteration threshold is less than 5 seconds average
    Given a health monitor exists
    And a container "container-123" with max_iterations -1
    And the container completed 50 iterations in 240 seconds
    When the health monitor detects infinite loops
    Then "container-123" should be flagged as suspicious

  Scenario: Exactly 5 second average is not suspicious
    Given a health monitor exists
    And a container "container-123" with max_iterations -1
    And the container completed 50 iterations in 250 seconds
    When the health monitor detects infinite loops
    Then "container-123" should not be flagged as suspicious

  Scenario: Zero iterations is not checked
    Given a health monitor exists
    And a container "container-123" with max_iterations -1
    And the container completed 0 iterations in 100 seconds
    When the health monitor detects infinite loops
    Then "container-123" should not be flagged as suspicious

  # ============================================================================
  # Attempt Recovery Tests
  # ============================================================================

  Scenario: First recovery attempt succeeds
    Given a health monitor with max recovery attempts 5
    And a ship "SHIP-1" is stuck in transit
    When the health monitor attempts recovery for "SHIP-1"
    Then the recovery should succeed
    And the recovery attempt count for "SHIP-1" should be 1
    And successful recoveries metric should be 1

  Scenario: Recovery attempt fails
    Given a health monitor with max recovery attempts 5
    And a ship "SHIP-1" is stuck in transit
    When the health monitor attempts failed recovery for "SHIP-1"
    Then the recovery attempt count for "SHIP-1" should be 1
    And failed recoveries metric should be 1

  Scenario: Ship abandoned after max recovery attempts
    Given a health monitor with max recovery attempts 5
    And a ship "SHIP-1" has failed recovery 5 times
    When the health monitor attempts recovery for "SHIP-1"
    Then the ship "SHIP-1" should be abandoned
    And the recovery attempt count for "SHIP-1" should remain 5
    And abandoned ships metric should be 1

  Scenario: Recovery attempts are tracked per ship
    Given a health monitor with max recovery attempts 5
    And a ship "SHIP-1" has failed recovery 2 times
    And a ship "SHIP-2" has failed recovery 1 time
    When I check recovery attempt counts
    Then "SHIP-1" should have 2 recovery attempts
    And "SHIP-2" should have 1 recovery attempt

  Scenario: Max recovery attempts boundary
    Given a health monitor with max recovery attempts 3
    And a ship "SHIP-1" has failed recovery 2 times
    When the health monitor attempts recovery for "SHIP-1"
    Then the recovery should succeed
    And the recovery attempt count for "SHIP-1" should be 3

  Scenario: Ship abandoned at exact max attempts
    Given a health monitor with max recovery attempts 3
    And a ship "SHIP-1" has failed recovery 3 times
    When the health monitor attempts recovery for "SHIP-1"
    Then the ship "SHIP-1" should be abandoned

  # ============================================================================
  # Recovery Metrics Tests
  # ============================================================================

  Scenario: Track successful recovery metrics
    Given a health monitor exists
    When the health monitor records 5 successful recoveries
    Then successful recoveries metric should be 5

  Scenario: Track failed recovery metrics
    Given a health monitor exists
    When the health monitor records 3 failed recoveries
    Then failed recoveries metric should be 3

  Scenario: Track abandoned ships metric
    Given a health monitor with max recovery attempts 2
    When the health monitor abandons 2 ships
    Then abandoned ships metric should be 2

  Scenario: Metrics accumulate over time
    Given a health monitor exists
    When the health monitor records 10 successful recoveries
    And the health monitor records 5 failed recoveries
    And the health monitor abandons 2 ships
    Then successful recoveries metric should be 10
    And failed recoveries metric should be 5
    And abandoned ships metric should be 2

  # ============================================================================
  # Watch List Tests
  # ============================================================================

  Scenario: Add ship to watch list
    Given a health monitor exists
    When I add ship "SHIP-1" to the watch list
    Then ship "SHIP-1" should be on the watch list

  Scenario: Remove ship from watch list
    Given a health monitor exists
    And ship "SHIP-1" is on the watch list
    When I remove ship "SHIP-1" from the watch list
    Then ship "SHIP-1" should not be on the watch list

  Scenario: Remove ship from watch list resets recovery attempts
    Given a health monitor exists
    And ship "SHIP-1" has failed recovery 3 times
    And ship "SHIP-1" is on the watch list
    When I remove ship "SHIP-1" from the watch list
    Then the recovery attempt count for "SHIP-1" should be 0

  Scenario: Watch list tracks multiple ships
    Given a health monitor exists
    When I add ship "SHIP-1" to the watch list
    And I add ship "SHIP-2" to the watch list
    And I add ship "SHIP-3" to the watch list
    Then all 3 ships should be on the watch list

  # ============================================================================
  # Max Recovery Attempts Configuration Tests
  # ============================================================================

  Scenario: Set max recovery attempts to custom value
    Given a health monitor with max recovery attempts 5
    When I set max recovery attempts to 10
    Then the health monitor should have max recovery attempts 10

  Scenario: Set max recovery attempts to 1
    Given a health monitor with max recovery attempts 5
    When I set max recovery attempts to 1
    Then the health monitor should have max recovery attempts 1

  Scenario: Set max recovery attempts to very high value
    Given a health monitor with max recovery attempts 5
    When I set max recovery attempts to 1000
    Then the health monitor should have max recovery attempts 1000

  # ============================================================================
  # Edge Cases for Coverage
  # ============================================================================

  Scenario: Health check with zero interval
    Given a health monitor with check interval 0 seconds and recovery timeout 300 seconds
    And the last check ran 1 second ago
    When I run a health check
    Then the check should be skipped

  Scenario: Recovery timeout of zero
    Given a health monitor with check interval 60 seconds and recovery timeout 0 seconds
    And a ship "SHIP-1" in transit for 1 second
    When the health monitor detects stuck ships
    Then "SHIP-1" should be detected as stuck

  Scenario: Get recovery attempt count for never-attempted ship
    Given a health monitor exists
    When I check recovery attempt count for "SHIP-NEVER-SEEN"
    Then the recovery attempt count should be 0

  Scenario: Multiple recovery attempts for same ship
    Given a health monitor with max recovery attempts 10
    And a ship "SHIP-1" is stuck in transit
    When the health monitor attempts recovery for "SHIP-1" 5 times
    Then the recovery attempt count for "SHIP-1" should be 5

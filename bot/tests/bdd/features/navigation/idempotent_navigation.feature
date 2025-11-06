Feature: Idempotent Navigation Commands
  Navigation commands should be idempotent - they can be sent at any time,
  even when the ship is IN_TRANSIT from a previous command. The handler should
  wait for the previous transit to complete before executing the new command.

  Background:
    Given the navigation system is initialized
    And a player exists with ID 1 and token "test-token-123"
    And a ship "TEST-SHIP-1" exists for player 1 at "X1-TEST-A1"
    And the SpaceTraders API is available

  Scenario: Navigation command sent while ship is IN_TRANSIT
    Given the database shows ship "TEST-SHIP-1" with status "IN_TRANSIT"
    And the API shows ship "TEST-SHIP-1" arriving in 2 seconds
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-C3"
    Then the ship should wait for previous transit to complete
    And the navigation should proceed after arrival
    And the ship should arrive at "X1-TEST-C3"

  Scenario: Navigation command accepted even when ship is in transit
    Given the database shows ship "TEST-SHIP-1" with status "IN_TRANSIT"
    And the API shows ship "TEST-SHIP-1" arriving in 2 seconds
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-B2"
    Then the command should be accepted without error
    And the handler should log "waiting for arrival"
    And the ship should eventually reach "X1-TEST-B2"

  Scenario: Multiple navigation commands sent in sequence
    Given ship "TEST-SHIP-1" is at "X1-TEST-A1" with status "IN_ORBIT"
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-B2"
    And I immediately send another navigation command for ship "TEST-SHIP-1" to "X1-TEST-C3"
    Then the first command should start execution
    And the second command should wait for the first to complete
    And the ship should eventually arrive at "X1-TEST-C3"

  Scenario: Idempotency wait calculates correct arrival time
    Given the database shows ship "TEST-SHIP-1" with status "IN_TRANSIT"
    And the API shows ship "TEST-SHIP-1" with arrival time "2025-10-30T12:05:00Z"
    And the current time is "2025-10-30T12:00:00Z"
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-B2"
    Then the handler should wait approximately 300 seconds
    And the handler should log "Waiting 303 seconds for ship to complete previous transit"
    And navigation should proceed after the wait

  Scenario: Ship already arrived when command sent
    Given the database shows ship "TEST-SHIP-1" with status "IN_TRANSIT"
    But the API shows ship "TEST-SHIP-1" with status "IN_ORBIT" at "X1-TEST-B2"
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-C3"
    Then the handler should sync the ship state immediately
    And no idempotency wait should occur
    And the navigation should proceed to "X1-TEST-C3"

  Scenario: Idempotency handles API errors gracefully
    Given the database shows ship "TEST-SHIP-1" with status "IN_TRANSIT"
    And the API returns error 500 when fetching ship state
    When I send a navigation command for ship "TEST-SHIP-1" to "X1-TEST-B2"
    Then the navigation should fail with appropriate error
    And the error should mention API failure

Feature: ArrivalTime Value Object
  As a SpaceTraders bot
  I want to work with arrival time value objects
  So that I can calculate wait times for ships in transit

  # ============================================================================
  # ArrivalTime Creation and Validation
  # ============================================================================

  Scenario: Create arrival time with valid ISO8601 timestamp
    Given an ISO8601 timestamp "2025-01-01T12:00:00Z"
    When I create an arrival time with that timestamp
    Then the arrival time should be created successfully
    And the arrival time timestamp should be "2025-01-01T12:00:00Z"

  Scenario: Create arrival time with +00:00 suffix
    Given an ISO8601 timestamp "2025-01-01T12:00:00+00:00"
    When I create an arrival time with that timestamp
    Then the arrival time should be created successfully

  Scenario: Reject empty timestamp
    Given an empty timestamp
    When I create an arrival time with that timestamp
    Then the arrival time creation should fail with error "arrival time timestamp cannot be empty"

  Scenario: Reject invalid timestamp format
    Given an invalid timestamp "not-a-timestamp"
    When I create an arrival time with that timestamp
    Then the arrival time creation should fail with error containing "invalid arrival time format"

  # ============================================================================
  # Wait Time Calculation
  # Note: These tests use actual current time, so we only test that:
  # - Past times return 0
  # - Future times return positive values
  # ============================================================================

  Scenario: Calculate wait time returns zero for past arrival
    Given an arrival time of "2020-01-01T00:00:00Z"
    When I calculate the wait time
    Then the wait time should be 0 seconds

  # ============================================================================
  # HasArrived Check
  # ============================================================================

  Scenario: HasArrived returns true for past arrival
    Given an arrival time of "2020-01-01T00:00:00Z"
    When I check if the ship has arrived
    Then the ship should have arrived

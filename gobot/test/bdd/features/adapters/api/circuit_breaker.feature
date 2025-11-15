Feature: Circuit Breaker Pattern
  As a SpaceTraders API client
  I want circuit breaker protection
  So that I don't overwhelm a failing API and allow time for recovery

  Background:
    Given a circuit breaker with max failures 5 and timeout 60 seconds

  # State Transitions
  Scenario: Circuit starts in closed state
    Then the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  Scenario: Circuit opens after max consecutive failures
    Given the circuit breaker is in "CLOSED" state
    When I execute 5 failing operations through the circuit breaker
    Then the circuit breaker state should be "OPEN"
    And the circuit breaker failure count should be 5

  Scenario: Circuit remains open within timeout period
    Given the circuit breaker is "OPEN" with last failure 30 seconds ago
    When I attempt to execute an operation through the circuit breaker
    Then the operation should fail immediately with "circuit breaker is open"
    And the circuit breaker state should still be "OPEN"

  Scenario: Circuit transitions to half-open after timeout expires
    Given the circuit breaker is "OPEN" with last failure 61 seconds ago
    When I execute a successful operation through the circuit breaker
    Then the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  Scenario: Failure in half-open state reopens circuit
    Given the circuit breaker is "OPEN" with last failure 61 seconds ago
    When I execute a failing operation through the circuit breaker
    Then the circuit breaker state should be "OPEN"
    And the circuit breaker failure count should be 1

  Scenario: Success in half-open state closes circuit
    Given the circuit breaker is "OPEN" with last failure 61 seconds ago
    When I execute a successful operation through the circuit breaker
    Then the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  # Edge Cases
  Scenario: Circuit does not open with non-consecutive failures
    Given the circuit breaker is in "CLOSED" state
    When I execute 3 failing operations through the circuit breaker
    And I execute 1 successful operation through the circuit breaker
    And I execute 3 more failing operations through the circuit breaker
    Then the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 3

  Scenario: Manual reset closes circuit and clears failures
    Given the circuit breaker is "OPEN" with 5 failures
    When I manually reset the circuit breaker
    Then the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  Scenario: Circuit breaker tracks state transitions correctly
    Given the circuit breaker is in "CLOSED" state
    When I execute 5 failing operations through the circuit breaker
    Then the circuit breaker state should be "OPEN"
    When I wait 61 seconds
    And I execute 1 successful operation through the circuit breaker
    Then the circuit breaker state should be "CLOSED"

  Scenario: Circuit breaker prevents cascading failures
    Given the circuit breaker is in "CLOSED" state
    When I execute 5 failing operations through the circuit breaker
    Then the circuit breaker state should be "OPEN"
    When I attempt to execute 100 operations through the circuit breaker
    Then all 100 operations should fail immediately
    And the circuit breaker failure count should be 5

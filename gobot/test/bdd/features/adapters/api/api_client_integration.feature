Feature: API Client Integration with Circuit Breaker and Retries
  As a SpaceTraders API client
  I want circuit breaker, retry logic, and rate limiting to work together
  So that I have comprehensive error resilience

  Background:
    Given an API client with the following configuration:
      | max_retries           | 3   |
      | backoff_base          | 1s  |
      | circuit_threshold     | 5   |
      | circuit_timeout       | 60s |
      | rate_limit            | 2/s |

  # Circuit Breaker with Retries
  Scenario: Circuit breaker wraps entire retry sequence
    Given the mock API always responds with status 503
    When I call GetShip with symbol "SHIP-1"
    Then the request should fail after max retries
    And exactly 4 HTTP attempts should have been made
    And the circuit breaker failure count should be 1
    And the circuit breaker state should be "CLOSED"

  Scenario: Circuit opens after multiple failed retry sequences
    Given the mock API always responds with status 503
    When I make 5 consecutive API calls to GetShip
    Then all 5 requests should fail
    And the circuit breaker state should be "OPEN"
    And 20 total HTTP attempts should have been made

  Scenario: Open circuit prevents retry attempts
    Given the circuit breaker is "OPEN"
    When I call GetShip with symbol "SHIP-1"
    Then the request should fail immediately with "circuit breaker is open"
    And no HTTP requests should be made
    And no retry attempts should occur

  Scenario: Successful requests reset circuit breaker failure count
    Given the mock API responds with status 503 for 2 attempts then 200
    When I make 10 consecutive API calls to GetShip
    Then all 10 requests should succeed
    And the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  # Rate Limiting with Retries
  Scenario: Rate limiting applies to retry attempts
    Given the mock API is configured to:
      | attempt | status |
      | 1       | 503    |
      | 2       | 503    |
      | 3       | 200    |
    When I call GetShip with symbol "SHIP-1"
    Then the request should succeed
    And all retry attempts should respect rate limiting
    And the total time should account for both backoff and rate limiting

  Scenario: Multiple concurrent requests with retries respect rate limit
    Given the mock API responds with status 503 for first attempt then 200
    When I make 5 concurrent API calls to GetShip
    Then all requests should succeed after 1 retry
    And the requests should be throttled to 2 req/sec
    And the circuit breaker should remain "CLOSED"

  # Circuit Breaker with Rate Limiting
  Scenario: Open circuit prevents rate limiter from being used
    Given the circuit breaker is "OPEN"
    When I make 100 rapid API calls to GetShip
    Then all 100 requests should fail immediately
    And the rate limiter should not be invoked
    And no HTTP requests should be made

  Scenario: Circuit breaker state transitions don't affect rate limiting
    Given the mock API always responds with status 503
    When I make 5 API calls to open the circuit
    Then the circuit breaker state should be "OPEN"
    When I wait 61 seconds for circuit timeout
    And I make 2 rapid API calls
    Then the calls should be rate limited
    And the circuit breaker should attempt half-open state

  # All Three Together (Holy Trinity of Resilience)
  Scenario: Circuit breaker, retries, and rate limiting work harmoniously
    Given the mock API is configured with intermittent failures:
      | request | attempt_1 | attempt_2 | attempt_3 |
      | 1       | 503       | 200       |           |
      | 2       | 200       |           |           |
      | 3       | 503       | 503       | 200       |
      | 4       | 200       |           |           |
      | 5       | 503       | 200       |           |
    When I make 5 concurrent API calls to GetShip
    Then all 5 requests should eventually succeed
    And the circuit breaker should remain "CLOSED"
    And rate limiting should be applied throughout
    And retry backoff should be applied for failed attempts

  # Edge Cases
  Scenario: Non-retryable error does not count toward circuit breaker threshold
    Given the mock API responds with status 404
    When I make 10 consecutive API calls to GetShip
    Then all 10 requests should fail immediately
    And no retry attempts should occur
    And the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  Scenario: Context cancellation bypasses all resilience patterns
    Given the mock API has 5 second delay
    When I call GetShip with a context that times out after 1 second
    Then the request should fail with context timeout
    And the circuit breaker failure count should be 0
    And no retries should occur

  Scenario: Circuit recovery after timeout allows new requests
    Given the circuit breaker is "OPEN" from previous failures
    When I wait 61 seconds for circuit timeout
    And the mock API is now responding with 200
    And I call GetShip with symbol "SHIP-1"
    Then the request should succeed
    And the circuit breaker state should be "CLOSED"
    And the circuit breaker failure count should be 0

  # Real-World Scenarios
  Scenario: Handling SpaceTraders API maintenance window
    Given the mock API responds with 503 for 2 minutes
    When I continuously attempt API calls during this period
    Then the circuit breaker should open quickly
    And subsequent calls should fail fast
    When the API recovers after 2 minutes
    And the circuit timeout expires
    Then requests should succeed again
    And the circuit should close

  Scenario: Handling temporary network issues
    Given the mock API has intermittent connection failures
    When I make 20 API calls over 10 seconds
    Then most requests should succeed after retries
    And the circuit breaker should not open
    And rate limiting should prevent overwhelming the API

  Scenario: Handling rate limit errors with backoff
    Given the mock API responds with 429 and Retry-After 5 seconds
    When I call GetShip with symbol "SHIP-1"
    Then the request should wait 5 seconds
    And retry the request
    And eventually succeed
    And the circuit breaker should remain "CLOSED"

  # Performance and Efficiency
  Scenario: Fast path for successful requests
    Given the mock API responds immediately with 200
    When I make 100 sequential API calls
    Then all requests should succeed
    And average latency should be under 10ms (excluding network)
    And the circuit breaker should add minimal overhead
    And the rate limiter should throttle appropriately

  Scenario: Metrics and observability
    Given I have made various API calls with mixed results
    When I query the API client metrics
    Then I should see:
      | metric                    | value |
      | total_requests            | >0    |
      | successful_requests       | >0    |
      | failed_requests           | >=0   |
      | circuit_breaker_state     | -     |
      | circuit_breaker_failures  | -     |
      | retry_attempts            | >=0   |
      | rate_limited_wait_time    | >=0   |

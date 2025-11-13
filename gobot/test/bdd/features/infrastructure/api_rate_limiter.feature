Feature: API Rate Limiter
  As an API client
  I want to enforce rate limits on API requests
  So that I respect the SpaceTraders 2 req/sec limit using a token bucket algorithm

  Background:
    Given a mock clock starting at "2025-01-01T00:00:00Z"

  # Initialization
  Scenario: Rate limiter initializes with correct parameters
    When I create a rate limiter with 2.0 requests per second and burst 2
    Then the rate limiter should be created successfully
    And the rate limiter should allow 2 immediate requests

  Scenario: Rate limiter starts with full token bucket
    When I create a rate limiter with 5.0 requests per second and burst 5
    Then the rate limiter should allow 5 immediate requests
    And the 6th request should block

  # Token Bucket Algorithm
  Scenario: Rate limiter allows requests within limit
    Given a rate limiter with 2.0 requests per second and burst 2
    When I make 2 requests immediately
    Then all 2 requests should succeed without blocking
    And the total time should be less than 100 milliseconds

  Scenario: Rate limiter blocks requests exceeding limit
    Given a rate limiter with 2.0 requests per second and burst 2
    When I make 2 requests immediately
    And I attempt a 3rd request
    Then the 3rd request should block
    And I should wait approximately 500 milliseconds

  Scenario: Rate limiter refills tokens over time
    Given a rate limiter with 2.0 requests per second and burst 2
    When I consume 2 tokens
    And I advance the clock by 500 milliseconds
    Then 1 token should have refilled
    And I should be able to make 1 more request immediately

  # Token Bucket Capacity
  Scenario: Rate limiter tokens are capped at burst capacity
    Given a rate limiter with 2.0 requests per second and burst 2
    When I wait 1 seconds without making requests
    Then I should be able to make exactly 2 immediate requests
    And the 3rd request should still block

  Scenario: Rate limiter handles burst traffic correctly
    Given a rate limiter with 5.0 requests per second and burst 5
    When I make 5 requests immediately
    Then all 5 requests should succeed
    And the 6th request should block for approximately 200 milliseconds

  # Concurrent Request Handling
  Scenario: Rate limiter handles concurrent requests safely
    Given a rate limiter with 2.0 requests per second and burst 2
    When I make 10 concurrent requests
    Then all 10 requests should eventually complete
    And requests should be throttled to respect the rate limit
    And no race conditions should occur

  Scenario: Rate limiter per-context isolation
    Given a rate limiter with 2.0 requests per second and burst 2
    When I make 2 requests with context A
    And I make 2 requests with context B
    Then context A requests should share the same rate limit
    And context B requests should share the same rate limit
    And both contexts should respect the global 2 req/sec limit

  # SpaceTraders Specific Requirements
  Scenario: Rate limiter respects SpaceTraders 2 req/sec limit
    Given a rate limiter configured for SpaceTraders API
    When I measure the rate over 10 requests
    Then the average rate should not exceed 2.1 requests per second
    And the burst should not exceed 2 requests

  # Token Refill Accuracy
  Scenario: Rate limiter token refill rate is accurate
    Given a rate limiter with 2.0 requests per second and burst 2
    When I consume all tokens
    And I advance the clock by 1000 milliseconds
    Then exactly 2 tokens should have refilled
    And I should be able to make 2 immediate requests

  Scenario: Rate limiter refills fractional tokens correctly
    Given a rate limiter with 10.0 requests per second and burst 10
    When I consume 5 tokens
    And I advance the clock by 250 milliseconds
    Then approximately 2.5 tokens should have refilled
    And I should be able to make 2-3 more immediate requests

  # Zero Token Scenario
  Scenario: Rate limiter zero token scenario blocks correctly
    Given a rate limiter with 1.0 requests per second and burst 1
    When I consume the initial token
    Then the token bucket should be empty
    And the next request should block for approximately 1000 milliseconds

  # Error Handling
  Scenario: Rate limiter handles context cancellation
    Given a rate limiter with 1.0 requests per second and burst 1
    When I consume the initial token
    And I attempt a request with a cancelled context
    Then the request should fail with context cancelled error
    And the rate limiter should remain functional

  # Integration with API Client
  Scenario: API client uses rate limiter for all requests
    Given an API client with rate limiting enabled
    When I make 5 API requests in quick succession
    Then the first 2 requests should complete immediately
    And the remaining 3 requests should be throttled
    And all requests should eventually complete successfully

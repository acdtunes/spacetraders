Feature: Rate Limiter
  As a system component
  I want to limit the rate of API requests
  So that I don't exceed API rate limits using a token bucket algorithm

  # Initialization
  Scenario: Create rate limiter with valid parameters
    When I create a rate limiter with 5 max_requests and 2.0 time_window
    Then the rate limiter should have max_requests 5
    And the rate limiter should have time_window 2.0
    And the rate limiter should have 5 tokens initially

  Scenario: Rate limiter starts with full token bucket
    When I create a rate limiter with 10 max_requests and 1.0 time_window
    Then the rate limiter should have 10 tokens initially

  # Token Acquisition
  Scenario: Acquire consumes one token when available
    Given a rate limiter with 2 max_requests and 1.0 time_window
    And the initial token count is recorded
    When I acquire a token
    Then the token count should decrease by 1

  Scenario: Acquire multiple times consumes multiple tokens
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I acquire a token
    And I acquire a token
    Then the token count should be close to 0

  Scenario: Acquire blocks when no tokens available
    Given a rate limiter with 1 max_requests and 1.0 time_window
    When I acquire the initial token
    And I measure the time to acquire another token
    Then the acquire should have blocked for approximately 1.0 seconds

  Scenario: Acquire is thread-safe with concurrent calls
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I acquire tokens from 4 concurrent threads
    Then all 4 threads should eventually acquire tokens
    And no race conditions should occur

  # Token Replenishment
  Scenario: Tokens replenish over time
    Given a rate limiter with 1 max_requests and 1.0 time_window
    When I consume the initial token
    And I wait for 0.5 seconds
    Then tokens should have replenished
    And I should be able to acquire a token

  Scenario: Tokens are capped at max capacity
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I consume one token
    And I wait for 3.0 seconds
    And I rapidly acquire 2 tokens
    Then the third acquire should block
    And tokens were capped at max_requests

  Scenario: Replenishment rate matches time window
    Given a rate limiter with 10 max_requests and 1.0 time_window
    When I consume all 10 tokens
    And I wait for 0.1 seconds
    Then approximately 1 token should have replenished
    And the next acquire should not block significantly

  # Wait Time Calculation
  Scenario: Wait appropriate time when no tokens available
    Given a rate limiter with 1 max_requests and 1.0 time_window
    When I consume the initial token
    And I measure the time to acquire another token
    Then the wait time should be between 0.9 and 1.2 seconds

  Scenario: Wait time proportional to token deficit
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I consume both tokens
    And I measure the time to acquire another token
    Then the wait time should be at least 0.4 seconds

  # Burst Capacity
  Scenario: Allow burst up to max requests
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I measure the time to acquire 2 tokens
    Then both acquires should be immediate
    And the total time should be less than 0.1 seconds

  Scenario: Block after burst capacity exhausted
    Given a rate limiter with 2 max_requests and 1.0 time_window
    When I consume both tokens immediately
    And I measure the time to acquire another token
    Then the third acquire should have blocked for at least 0.4 seconds

  # Edge Cases
  Scenario: Handle zero tokens gracefully
    Given a rate limiter with 1 max_requests and 1.0 time_window
    When I consume the initial token
    Then the token count should be 0
    And I should still be able to acquire tokens by waiting

  Scenario: Handle fractional tokens during replenishment
    Given a rate limiter with 10 max_requests and 1.0 time_window
    When I consume 5 tokens
    And I wait for 0.25 seconds
    Then approximately 2.5 tokens should have replenished
    And I should be able to acquire 2-3 more tokens

  Scenario: Handle high request rate correctly
    Given a rate limiter with 10 max_requests and 1.0 time_window
    When I acquire 20 tokens rapidly
    Then all acquires should succeed
    And rate limiting should have throttled the requests

  Scenario: Work correctly across multiple time windows
    Given a rate limiter with 2 max_requests and 0.5 time_window
    When I consume both tokens
    And I wait for 0.6 seconds
    And I measure the time to acquire 2 tokens
    Then both acquires should be immediate

  # Thread Safety
  Scenario: Maintain rate limit with concurrent requests
    Given a rate limiter with 10 max_requests and 1.0 time_window
    When 5 threads each make 10 requests concurrently
    Then all 50 requests should complete
    And no deadlocks should occur
    And the rate limit should be enforced

  Scenario: Lock prevents race conditions on tokens
    Given a rate limiter with 100 max_requests and 1.0 time_window
    When 10 threads each acquire 10 tokens rapidly
    Then all 100 tokens should be consumed correctly
    And no over-consumption should occur

  # Last Update Tracking
  Scenario: Update last_update time on acquire
    Given a rate limiter with 2 max_requests and 1.0 time_window
    And the initial last_update time is recorded
    When I wait 0.1 seconds
    And I acquire a token
    Then the last_update time should have advanced

  Scenario: Use last_update for replenishment calculation
    Given a rate limiter with 10 max_requests and 1.0 time_window
    When I consume all 10 tokens
    And I wait 0.5 seconds
    And I acquire a token
    Then the acquire should succeed without blocking
    And elapsed time was used for replenishment
